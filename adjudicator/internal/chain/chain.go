// Package chain reads the deployed DepositDispute back and refuses to run when
// the running configuration and the deployed one disagree.
package chain

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"

	"github.com/pigfox/deposit-dispute/adjudicator/internal/config"
	"github.com/pigfox/deposit-dispute/adjudicator/internal/evidence"
	"github.com/pigfox/deposit-dispute/adjudicator/internal/panel"
)

// Sentinel errors.
var (
	// ErrCallFailed means the read did not complete.
	ErrCallFailed = errors.New("chain: call failed")
	// ErrShortRead means the call returned fewer values than the signature says.
	ErrShortRead = errors.New("chain: call returned too few values")
	// ErrModelMismatch means a slot's configured model is not the one the
	// contract was constructed with.
	ErrModelMismatch = errors.New("chain: configured model does not match the deployed slot")
	// ErrWrongChain means the endpoint is not Base Sepolia.
	ErrWrongChain = errors.New("chain: endpoint is not the expected chain")
	// ErrBadChainID means the endpoint's chain id did not parse.
	ErrBadChainID = errors.New("chain: chain id did not parse")
)

// Runner executes an external command and returns its combined output. It is an
// interface so every test drives the chain package without a node, a network or
// a foundry installation.
type Runner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

// ExecRunner runs commands for real.
type ExecRunner struct{}

// Run shells out and returns the command's combined output.
func (ExecRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

// castBinary is the tool the reads go through. `cast` rather than a JSON-RPC
// client of our own: it is already pinned by the pipeline, it does the ABI
// decoding, and it is the same implementation every script in the estate uses.
const castBinary = "cast"

// Client reads one deployed dispute.
type Client struct {
	// Runner executes `cast`.
	Runner Runner
	// RPCURL is the endpoint. Never logged: an endpoint often carries a key in
	// its path.
	RPCURL string
	// Address is the DepositDispute being read.
	Address string
}

// New builds a Client that shells out to the real `cast`.
func New(rpcURL, address string) *Client {
	return &Client{Runner: ExecRunner{}, RPCURL: rpcURL, Address: address}
}

// call runs one `cast call` and returns its output lines.
func (c *Client) call(ctx context.Context, sig string, args ...string) ([]string, error) {
	argv := append([]string{"call", c.Address, sig}, args...)
	argv = append(argv, "--rpc-url", c.RPCURL)

	out, err := c.Runner.Run(ctx, castBinary, argv...)
	if err != nil {
		// The signature and the address are named; the endpoint is not, because
		// it may carry a credential.
		return nil, fmt.Errorf("%w: %s on %s: %w: %s",
			ErrCallFailed, sig, c.Address, err, strings.TrimSpace(string(out)))
	}
	return splitLines(string(out)), nil
}

// splitLines returns the non-empty trimmed lines of s.
func splitLines(s string) []string {
	raw := strings.Split(s, "\n")
	lines := make([]string, 0, len(raw))
	for _, l := range raw {
		if t := strings.TrimSpace(l); t != "" {
			lines = append(lines, t)
		}
	}
	return lines
}

// ChainID reads the endpoint's chain id.
func (c *Client) ChainID(ctx context.Context) (uint64, error) {
	out, err := c.Runner.Run(ctx, castBinary, "chain-id", "--rpc-url", c.RPCURL)
	if err != nil {
		return 0, fmt.Errorf("%w: chain-id: %w: %s", ErrCallFailed, err, strings.TrimSpace(string(out)))
	}
	lines := splitLines(string(out))
	if len(lines) == 0 {
		return 0, fmt.Errorf("%w: chain-id returned nothing", ErrShortRead)
	}
	id, err := strconv.ParseUint(lines[0], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%w: %q", ErrBadChainID, lines[0])
	}
	return id, nil
}

// AdjudicatorAt reads a slot's registered signer and pinned model identifier.
func (c *Client) AdjudicatorAt(ctx context.Context, slot int) (signer, modelID string, err error) {
	lines, err := c.call(ctx, config.SigAdjudicatorAt, strconv.Itoa(slot))
	if err != nil {
		return "", "", err
	}
	const wantLines = 2
	if len(lines) < wantLines {
		return "", "", fmt.Errorf("%w: %s returned %d of %d",
			ErrShortRead, config.SigAdjudicatorAt, len(lines), wantLines)
	}
	// `cast` renders a returned string quoted. Unwrap the envelope; never repair
	// the content.
	return lines[0], strings.Trim(lines[1], `"`), nil
}

// ModelIDHashAt reads the model identifier hash the contract holds for a slot.
func (c *Client) ModelIDHashAt(ctx context.Context, slot int) (evidence.Hash, error) {
	lines, err := c.call(ctx, config.SigModelIDHashAt, strconv.Itoa(slot))
	if err != nil {
		return evidence.Hash{}, err
	}
	if len(lines) == 0 {
		return evidence.Hash{}, fmt.Errorf("%w: %s returned nothing",
			ErrShortRead, config.SigModelIDHashAt)
	}
	return evidence.ParseHash(lines[0])
}

// EvidenceRoot reads the commitment the landlord filed. Zero until a claim is on
// file.
func (c *Client) EvidenceRoot(ctx context.Context) (evidence.Hash, error) {
	lines, err := c.call(ctx, config.SigEvidenceRoot)
	if err != nil {
		return evidence.Hash{}, err
	}
	if len(lines) == 0 {
		return evidence.Hash{}, fmt.Errorf("%w: %s returned nothing",
			ErrShortRead, config.SigEvidenceRoot)
	}
	return evidence.ParseHash(lines[0])
}

// VerifyChain refuses any endpoint that is not Base Sepolia.
//
// DIRECT-CHAIN ONLY, checked rather than assumed. An adjudicator pointed at some
// other endpoint would read an address that either does not exist or holds a
// contract nobody in this estate deployed, and would then publish verdicts
// against it. Reading the id back costs one call and removes the whole class.
func (c *Client) VerifyChain(ctx context.Context) error {
	id, err := c.ChainID(ctx)
	if err != nil {
		return err
	}
	if id != config.ExpectedChainID {
		return fmt.Errorf("%w: endpoint reports %d, expected %d",
			ErrWrongChain, id, config.ExpectedChainID)
	}
	return nil
}

// VerifySlots checks that every configured model is the one the contract was
// CONSTRUCTED with, and refuses to run otherwise.
//
// THE COMPARISON IS ON THE HASH, NOT THE STRING. The contract stores
// keccak256(modelId) and that is what a verdict carries, so hashing the
// configured identifier here compares exactly what the chain will compare. It
// also catches the case the readable string cannot: a slot whose stored
// identifier renders identically but differs in a byte the terminal does not
// show.
//
// REFUSING IS THE WHOLE POINT. An adjudicator running a model the contract did
// not register produces verdicts that either revert or, worse, are accepted under
// a slot that claims a different model than the one that actually answered. The
// published modelIdHash would then be a lie that a third party re-running the
// adjudication could not detect, because they would re-run the model the chain
// named. So a mismatch stops the program before any key is used.
func (c *Client) VerifySlots(ctx context.Context, cfg config.Config) error {
	for slot := 0; slot < config.SlotCount; slot++ {
		onChain, err := c.ModelIDHashAt(ctx, slot)
		if err != nil {
			return err
		}
		configured := evidence.Keccak([]byte(cfg.Slots[slot].ModelID))
		if configured != onChain {
			signer, modelID, readErr := c.AdjudicatorAt(ctx, slot)
			if readErr != nil {
				return readErr
			}
			return fmt.Errorf(
				"%w: slot %d is registered to %q (signer %s) but this run is configured for %q",
				ErrModelMismatch, slot, modelID, signer, cfg.Slots[slot].ModelID)
		}
	}
	return nil
}

// SubmitArgs renders the `cast send` argument list for one verdict.
//
// BUILT, LOGGED, AND NOT BROADCAST BY THIS PACKAGE. Nothing here signs anything
// and nothing here holds a key. Rendering the call is what makes it reviewable
// before it is sent and re-runnable by a third party afterwards; sending it is a
// separate, deliberate act.
//
// The proof travels WITH the evidence hash, because the contract will rebuild the
// leaf from the index it is given and fold this proof against the committed root.
// An argument list that carried one without the other could not be accepted.
func SubmitArgs(address string, v panel.Verdict, itemEvidenceHash evidence.Hash, proof []evidence.Hash) []string {
	return []string{
		"send", address, config.SigSubmitVerdict,
		strconv.Itoa(v.ItemIndex),
		strconv.Itoa(int(v.Finding)),
		itemEvidenceHash.Hex(),
		evidence.HexProof(proof),
		v.PromptHash.Hex(),
		v.NarrativeHash.Hex(),
		v.Reason,
	}
}
