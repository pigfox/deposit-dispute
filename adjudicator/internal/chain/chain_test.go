package chain_test

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/pigfox/deposit-dispute/adjudicator/internal/chain"
	"github.com/pigfox/deposit-dispute/adjudicator/internal/config"
	"github.com/pigfox/deposit-dispute/adjudicator/internal/evidence"
	"github.com/pigfox/deposit-dispute/adjudicator/internal/model"
	"github.com/pigfox/deposit-dispute/adjudicator/internal/panel"
)

// fakeRunner answers `cast` invocations from a script. NO TEST HERE NEEDS A
// NODE, A NETWORK OR A FOUNDRY INSTALLATION.
type fakeRunner struct {
	// byArg maps a substring of the argv to the output it produces.
	byArg map[string]string
	// failOn makes any invocation containing this substring fail.
	failOn string
	// argv records every invocation, so a test can assert what was actually run.
	argv [][]string
}

func (f *fakeRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	f.argv = append(f.argv, append([]string{name}, args...))
	joined := strings.Join(args, " ")
	if f.failOn != "" && strings.Contains(joined, f.failOn) {
		return []byte("cast: execution reverted"), errors.New("exit status 1")
	}
	for key, out := range f.byArg {
		if strings.Contains(joined, key) {
			return []byte(out), nil
		}
	}
	return nil, fmt.Errorf("fakeRunner: nothing scripted for %q", joined)
}

const disputeAddress = "0xDD00000000000000000000000000000000000DD0"

// slotModels are the identifiers this fixture's contract was constructed with.
var slotModels = [config.SlotCount]string{"model-alpha", "model-beta", "model-gamma"}

// healthyRunner answers every read the way a correctly-deployed dispute would.
func healthyRunner() *fakeRunner {
	byArg := map[string]string{
		"chain-id":             strconv.FormatUint(config.ExpectedChainID, 10),
		config.SigEvidenceRoot: evidence.Keccak([]byte("the filed commitment")).Hex(),
	}
	for i, id := range slotModels {
		byArg[config.SigModelIDHashAt+" "+strconv.Itoa(i)] = evidence.Keccak([]byte(id)).Hex()
		byArg[config.SigAdjudicatorAt+" "+strconv.Itoa(i)] =
			fmt.Sprintf("0x000000000000000000000000000000000000000%d\n%q", i+1, id)
	}
	return &fakeRunner{byArg: byArg}
}

func newClient(r chain.Runner) *chain.Client {
	return &chain.Client{Runner: r, RPCURL: "https://example.invalid/rpc", Address: disputeAddress}
}

// matchingConfig is a run configured for the deployed slots.
func matchingConfig() config.Config {
	var cfg config.Config
	cfg.DisputeAddress = disputeAddress
	for i, id := range slotModels {
		cfg.Slots[i] = config.Slot{Provider: config.ProviderAnthropic, ModelID: id, APIKey: "k"}
	}
	return cfg
}

func TestVerifyChainAcceptsBaseSepoliaOnly(t *testing.T) {
	if err := newClient(healthyRunner()).VerifyChain(context.Background()); err != nil {
		t.Fatalf("VerifyChain: %v", err)
	}

	wrong := healthyRunner()
	wrong.byArg["chain-id"] = "1"
	err := newClient(wrong).VerifyChain(context.Background())
	if !errors.Is(err, chain.ErrWrongChain) {
		t.Fatalf("err = %v, want ErrWrongChain", err)
	}
	if !strings.Contains(err.Error(), strconv.FormatUint(config.ExpectedChainID, 10)) {
		t.Errorf("the refusal should name the expected chain: %v", err)
	}

	// An endpoint that cannot even be asked is a refusal too, not a pass.
	unreachable := healthyRunner()
	unreachable.failOn = "chain-id"
	if err := newClient(unreachable).VerifyChain(context.Background()); !errors.Is(err, chain.ErrCallFailed) {
		t.Fatalf("err = %v, want ErrCallFailed", err)
	}
}

func TestChainIDReportsFailures(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(r *fakeRunner)
		want   error
	}{
		{"the call failed", func(r *fakeRunner) { r.failOn = "chain-id" }, chain.ErrCallFailed},
		{"nothing came back", func(r *fakeRunner) { r.byArg["chain-id"] = "  \n " }, chain.ErrShortRead},
		{"not a number", func(r *fakeRunner) { r.byArg["chain-id"] = "base-sepolia" }, chain.ErrBadChainID},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := healthyRunner()
			tc.mutate(r)
			if _, err := newClient(r).ChainID(context.Background()); !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
}

// TestVerifySlotsAcceptsTheDeployedSet is the happy path of the check that stops
// the agent answering for a model the contract never registered.
func TestVerifySlotsAcceptsTheDeployedSet(t *testing.T) {
	if err := newClient(healthyRunner()).VerifySlots(context.Background(), matchingConfig()); err != nil {
		t.Fatalf("VerifySlots: %v", err)
	}
}

// TestVerifySlotsRefusesAModelTheContractDidNotRegister is the reason the
// package exists.
func TestVerifySlotsRefusesAModelTheContractDidNotRegister(t *testing.T) {
	cfg := matchingConfig()
	cfg.Slots[1].ModelID = "a-model-nobody-registered"

	err := newClient(healthyRunner()).VerifySlots(context.Background(), cfg)
	if !errors.Is(err, chain.ErrModelMismatch) {
		t.Fatalf("err = %v, want ErrModelMismatch", err)
	}
	if !strings.Contains(err.Error(), "a-model-nobody-registered") {
		t.Errorf("the refusal should name the configured model: %v", err)
	}
	if !strings.Contains(err.Error(), slotModels[1]) {
		t.Errorf("the refusal should name the registered model: %v", err)
	}
	if !strings.Contains(err.Error(), "slot 1") {
		t.Errorf("the refusal should name the slot: %v", err)
	}
}

// TestTheComparisonIsOnTheHashNotTheRenderedString catches what a readable
// string cannot: an identifier that renders identically but differs in a byte.
func TestTheComparisonIsOnTheHashNotTheRenderedString(t *testing.T) {
	cfg := matchingConfig()
	cfg.Slots[0].ModelID = slotModels[0] + "\u200b" // zero-width space

	if err := newClient(healthyRunner()).VerifySlots(context.Background(), cfg); !errors.Is(err, chain.ErrModelMismatch) {
		t.Fatalf("an invisible difference was not caught: %v", err)
	}
}

func TestVerifySlotsReportsReadFailures(t *testing.T) {
	r := healthyRunner()
	r.failOn = config.SigModelIDHashAt
	if err := newClient(r).VerifySlots(context.Background(), matchingConfig()); !errors.Is(err, chain.ErrCallFailed) {
		t.Fatalf("err = %v, want ErrCallFailed", err)
	}

	// A mismatch triggers a second read to name the registered model. When THAT
	// read fails the error must be the read failure, not a misleading mismatch.
	cfg := matchingConfig()
	cfg.Slots[0].ModelID = "wrong"
	r2 := healthyRunner()
	r2.failOn = config.SigAdjudicatorAt
	if err := newClient(r2).VerifySlots(context.Background(), cfg); !errors.Is(err, chain.ErrCallFailed) {
		t.Fatalf("err = %v, want ErrCallFailed", err)
	}
}

func TestAdjudicatorAtUnwrapsTheEnvelopeAndNeverRepairsTheContent(t *testing.T) {
	signer, modelID, err := newClient(healthyRunner()).AdjudicatorAt(context.Background(), 2)
	if err != nil {
		t.Fatalf("AdjudicatorAt: %v", err)
	}
	if signer != "0x0000000000000000000000000000000000000003" {
		t.Errorf("signer = %q", signer)
	}
	// `cast` renders a returned string quoted; the quotes come off and nothing
	// else does.
	if modelID != slotModels[2] {
		t.Errorf("modelID = %q, want %q", modelID, slotModels[2])
	}
}

func TestAdjudicatorAtReportsFailures(t *testing.T) {
	r := healthyRunner()
	r.failOn = config.SigAdjudicatorAt
	if _, _, err := newClient(r).AdjudicatorAt(context.Background(), 0); !errors.Is(err, chain.ErrCallFailed) {
		t.Fatalf("err = %v, want ErrCallFailed", err)
	}

	short := healthyRunner()
	short.byArg[config.SigAdjudicatorAt+" 0"] = "0x0000000000000000000000000000000000000001"
	if _, _, err := newClient(short).AdjudicatorAt(context.Background(), 0); !errors.Is(err, chain.ErrShortRead) {
		t.Fatalf("err = %v, want ErrShortRead", err)
	}
}

func TestModelIDHashAtReportsFailures(t *testing.T) {
	r := healthyRunner()
	r.failOn = config.SigModelIDHashAt
	if _, err := newClient(r).ModelIDHashAt(context.Background(), 0); !errors.Is(err, chain.ErrCallFailed) {
		t.Fatalf("err = %v, want ErrCallFailed", err)
	}

	empty := healthyRunner()
	empty.byArg[config.SigModelIDHashAt+" 0"] = "   "
	if _, err := newClient(empty).ModelIDHashAt(context.Background(), 0); !errors.Is(err, chain.ErrShortRead) {
		t.Fatalf("err = %v, want ErrShortRead", err)
	}

	junk := healthyRunner()
	junk.byArg[config.SigModelIDHashAt+" 0"] = "not-a-hash"
	if _, err := newClient(junk).ModelIDHashAt(context.Background(), 0); !errors.Is(err, evidence.ErrBadHash) {
		t.Fatalf("err = %v, want ErrBadHash", err)
	}
}

func TestEvidenceRootReadsTheFiledCommitment(t *testing.T) {
	got, err := newClient(healthyRunner()).EvidenceRoot(context.Background())
	if err != nil {
		t.Fatalf("EvidenceRoot: %v", err)
	}
	if got != evidence.Keccak([]byte("the filed commitment")) {
		t.Errorf("EvidenceRoot = %s", got.Hex())
	}

	r := healthyRunner()
	r.failOn = config.SigEvidenceRoot
	if _, err := newClient(r).EvidenceRoot(context.Background()); !errors.Is(err, chain.ErrCallFailed) {
		t.Fatalf("err = %v, want ErrCallFailed", err)
	}

	empty := healthyRunner()
	empty.byArg[config.SigEvidenceRoot] = ""
	if _, err := newClient(empty).EvidenceRoot(context.Background()); !errors.Is(err, chain.ErrShortRead) {
		t.Fatalf("err = %v, want ErrShortRead", err)
	}
}

// TestNoErrorEverCarriesTheEndpoint keeps a project key out of the log. An RPC
// URL often carries one in its path.
func TestNoErrorEverCarriesTheEndpoint(t *testing.T) {
	const secretURL = "https://rpc.example.invalid/v2/PROJECT-KEY-MUST-NOT-LEAK"
	r := healthyRunner()
	r.failOn = config.SigEvidenceRoot
	c := &chain.Client{Runner: r, RPCURL: secretURL, Address: disputeAddress}

	_, err := c.EvidenceRoot(context.Background())
	if err == nil {
		t.Fatal("expected a failure")
	}
	if strings.Contains(err.Error(), "PROJECT-KEY-MUST-NOT-LEAK") {
		t.Fatalf("the error leaked the endpoint: %v", err)
	}
	if !strings.Contains(err.Error(), disputeAddress) {
		t.Errorf("the error should still name the address: %v", err)
	}
}

func TestTheReadsGoThroughCastWithTheRightArguments(t *testing.T) {
	r := healthyRunner()
	if _, err := newClient(r).ModelIDHashAt(context.Background(), 1); err != nil {
		t.Fatalf("ModelIDHashAt: %v", err)
	}

	if len(r.argv) != 1 {
		t.Fatalf("made %d invocations, want 1", len(r.argv))
	}
	got := strings.Join(r.argv[0], " ")
	for _, want := range []string{"cast", "call", disputeAddress, config.SigModelIDHashAt, "--rpc-url"} {
		if !strings.Contains(got, want) {
			t.Errorf("the invocation is missing %q: %s", want, got)
		}
	}
}

// TestSubmitArgsCarriesTheProofWithTheEvidenceHash is the shape the contract
// requires: one without the other could not be accepted.
func TestSubmitArgsCarriesTheProofWithTheEvidenceHash(t *testing.T) {
	var bundle evidence.Bundle
	for i := range bundle {
		bundle[i] = evidence.Keccak([]byte(fmt.Sprintf("item-%d", i)))
	}
	proof, err := evidence.Proof(bundle, 2)
	if err != nil {
		t.Fatalf("Proof: %v", err)
	}

	v := panel.Verdict{
		Slot:          0,
		ItemIndex:     2,
		Finding:       model.Established,
		Reason:        "the invoice matches the damage",
		PromptHash:    evidence.Keccak([]byte("prompt")),
		NarrativeHash: evidence.Keccak([]byte("narrative")),
	}

	args := chain.SubmitArgs(disputeAddress, v, bundle[2], proof)
	joined := strings.Join(args, " ")

	for _, want := range []string{
		"send", disputeAddress, config.SigSubmitVerdict,
		"2", strconv.Itoa(int(model.Established)),
		bundle[2].Hex(), v.PromptHash.Hex(), v.NarrativeHash.Hex(), v.Reason,
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("the submission is missing %q:\n%s", want, joined)
		}
	}
	if !strings.Contains(joined, evidence.HexProof(proof)) {
		t.Errorf("the submission does not carry the proof:\n%s", joined)
	}
	// The proof must travel WITH this item's hash, never another's.
	if strings.Contains(joined, bundle[3].Hex()) {
		t.Error("the submission carries a different item's evidence hash")
	}
}

// TestSubmitArgsRendersTheFindingAsItsEnumOrdinal keeps the Go and Solidity
// enums in step: NotEstablished is zero in both.
func TestSubmitArgsRendersTheFindingAsItsEnumOrdinal(t *testing.T) {
	var bundle evidence.Bundle
	proof, err := evidence.Proof(bundle, 4)
	if err != nil {
		t.Fatalf("Proof: %v", err)
	}

	notEstablished := chain.SubmitArgs(disputeAddress,
		panel.Verdict{ItemIndex: 4, Finding: model.NotEstablished}, bundle[4], proof)
	if notEstablished[4] != "0" {
		t.Errorf("NotEstablished rendered as %q, want 0", notEstablished[4])
	}

	established := chain.SubmitArgs(disputeAddress,
		panel.Verdict{ItemIndex: 4, Finding: model.Established}, bundle[4], proof)
	if established[4] != "1" {
		t.Errorf("Established rendered as %q, want 1", established[4])
	}
}

// TestExecRunnerRunsTheRealThing covers the production seam. It runs a harmless
// command rather than `cast`, because the point is that the seam executes at all.
func TestExecRunnerRunsTheRealThing(t *testing.T) {
	runner := chain.ExecRunner{}

	out, err := runner.Run(context.Background(), "echo", "hello")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if strings.TrimSpace(string(out)) != "hello" {
		t.Fatalf("output = %q", out)
	}

	if _, err := runner.Run(context.Background(), "a-binary-that-does-not-exist"); err == nil {
		t.Fatal("a missing binary should report an error")
	}
}

// TestNewWiresTheRealRunner keeps the production constructor honest.
func TestNewWiresTheRealRunner(t *testing.T) {
	c := chain.New("https://example.invalid", disputeAddress)
	if _, ok := c.Runner.(chain.ExecRunner); !ok {
		t.Fatalf("New did not wire the real runner: %T", c.Runner)
	}
	if c.Address != disputeAddress {
		t.Errorf("Address = %q", c.Address)
	}
}
