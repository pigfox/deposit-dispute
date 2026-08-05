// Package app wires configuration, the evidence bundle, the model clients, the
// panel and the chain reads into one run.
package app

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/pigfox/deposit-dispute/adjudicator/internal/chain"
	"github.com/pigfox/deposit-dispute/adjudicator/internal/config"
	"github.com/pigfox/deposit-dispute/adjudicator/internal/evidence"
	"github.com/pigfox/deposit-dispute/adjudicator/internal/model"
	"github.com/pigfox/deposit-dispute/adjudicator/internal/panel"
)

// Exit codes. Distinct so a caller can tell a configuration problem from a
// refusal to run from a failed adjudication without parsing the log.
const (
	// ExitOK means the run completed.
	ExitOK = 0
	// ExitUsage means the arguments could not be parsed.
	ExitUsage = 1
	// ExitConfig means the environment or the bundle was unusable.
	ExitConfig = 2
	// ExitChainMismatch means the deployed contract and this run disagree.
	ExitChainMismatch = 3
	// ExitAdjudication means a line item could not be put to the panel at all.
	ExitAdjudication = 4
)

// httpTimeout bounds one vendor round trip.
const httpTimeout = 120 * time.Second

// Deps are the seams a test replaces so a run touches no environment, no
// network and no node.
type Deps struct {
	// Getenv reads configuration.
	Getenv config.Getenv
	// Doer performs vendor round trips.
	Doer model.Doer
	// Runner executes chain reads.
	Runner chain.Runner
	// Open reads the evidence bundle.
	Open func(name string) (io.ReadCloser, error)
}

// DefaultDeps is the production wiring.
func DefaultDeps() Deps {
	return Deps{
		Getenv: os.Getenv,
		Doer:   &http.Client{Timeout: httpTimeout},
		Runner: chain.ExecRunner{},
		Open:   func(name string) (io.ReadCloser, error) { return os.Open(name) },
	}
}

// Run is the production entry point.
func Run(ctx context.Context, out io.Writer, argv []string) int {
	return RunWith(ctx, out, argv, DefaultDeps())
}

// options are the parsed command line.
type options struct {
	bundlePath string
	printRoot  bool
}

// parseArgs reads the command line into options.
func parseArgs(out io.Writer, argv []string) (options, error) {
	var opts options
	fs := flag.NewFlagSet(argv[0], flag.ContinueOnError)
	fs.SetOutput(out)
	fs.StringVar(&opts.bundlePath, "bundle", "", "path to the published evidence bundle (required)")
	fs.BoolVar(&opts.printRoot, "print-root", false,
		"print the bundle's merkle root and exit; makes no vendor call and reads no chain")
	if err := fs.Parse(argv[1:]); err != nil {
		return options{}, err
	}
	if opts.bundlePath == "" {
		return options{}, fmt.Errorf("%w: --bundle", config.ErrMissing)
	}
	return opts, nil
}

// Submission is one verdict rendered as the call that would put it on chain.
//
// EMITTED AS JSON, one per line, because the reason string is model-written and
// can carry spaces — a whitespace-delimited log line would be ambiguous exactly
// where it matters most. The slot is included because a rendered submission that
// does not say WHO must sign it is incomplete: only that slot's registered signer
// can submit it.
type Submission struct {
	Slot int      `json:"slot"`
	Item int      `json:"item"`
	Args []string `json:"args"`
}

// submitPrefix marks a rendered submission line.
const submitPrefix = "SUBMIT "

// jsonLine encodes a value for the log, reporting rather than hiding a failure.
func jsonLine(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return `{"error":` + strconv.Quote(err.Error()) + `}`
	}
	return string(b)
}

// RunWith performs one adjudication against injected dependencies.
//
// NOTHING IS SIGNED AND NOTHING IS BROADCAST. Every verdict is rendered as the
// `cast send` argument list that would submit it, and written out. Sending is a
// separate, deliberate act performed by somebody holding a key, and this program
// holds none.
func RunWith(ctx context.Context, out io.Writer, argv []string, deps Deps) int {
	logger := log.New(out, "", 0)

	opts, err := parseArgs(out, argv)
	if err != nil {
		logger.Printf("usage: %v", err)
		return ExitUsage
	}

	doc, bundle, err := loadBundle(deps, opts.bundlePath)
	if err != nil {
		logger.Printf("bundle: %v", err)
		return ExitConfig
	}

	// The landlord needs the root BEFORE the contract has one, to file it. This
	// mode reads no chain, calls no model and needs no configuration beyond the
	// bundle itself.
	if opts.printRoot {
		logger.Print(evidence.Root(bundle).Hex())
		return ExitOK
	}

	cfg, err := config.Load(deps.Getenv)
	if err != nil {
		logger.Printf("config: %v", err)
		return ExitConfig
	}

	clients := make([]model.Client, 0, config.SlotCount)
	for _, slot := range cfg.Slots {
		client, err := model.New(slot, deps.Doer)
		if err != nil {
			logger.Printf("model: %v", err)
			return ExitConfig
		}
		clients = append(clients, client)
	}

	board, err := panel.New(clients, logger)
	if err != nil {
		logger.Printf("panel: %v", err)
		return ExitConfig
	}
	for _, line := range board.SpendSurface() {
		logger.Print(line)
	}

	reader := &chain.Client{Runner: deps.Runner, RPCURL: cfg.RPCURL, Address: cfg.DisputeAddress}
	if err := reader.VerifyChain(ctx); err != nil {
		logger.Printf("chain: %v", err)
		return ExitChainMismatch
	}
	if err := reader.VerifySlots(ctx, cfg); err != nil {
		logger.Printf("chain: %v", err)
		return ExitChainMismatch
	}
	logger.Printf(config.LogChainVerified, config.SlotCount)

	// The commitment is read back and the locally-built root compared against it.
	// A bundle that does not reproduce the filed root is a bundle that answers to
	// a different claim, and every proof built from it would be refused on chain.
	onChainRoot, err := reader.EvidenceRoot(ctx)
	if err != nil {
		logger.Printf("chain: %v", err)
		return ExitChainMismatch
	}
	localRoot := evidence.Root(bundle)
	if onChainRoot != localRoot {
		logger.Printf("chain: the filed commitment is %s but this bundle roots to %s",
			onChainRoot.Hex(), localRoot.Hex())
		return ExitChainMismatch
	}

	adjudicateAll(ctx, logger, board, cfg, doc, bundle)
	return ExitOK
}

// adjudicateAll puts every line item to the panel, ONE ITEM AT A TIME.
//
// The loop is the design. Items are never batched into a single call — see
// panel.Adjudicate for why — so this walks the schedule and makes one round of
// independent calls per item.
//
// EVERY PROOF IS BUILT BEFORE THE FIRST VENDOR CALL. A proof that could not be
// built is a run that could submit nothing, and finding that out after fifteen
// metered calls would be paying to learn it.
func adjudicateAll(
	ctx context.Context,
	logger *log.Logger,
	board *panel.Panel,
	cfg config.Config,
	doc evidence.Document,
	bundle evidence.Bundle,
) {
	proofs := evidence.Proofs(bundle)

	for index := 0; index < config.ItemCount; index++ {
		verdicts := board.Adjudicate(ctx, panel.Item{
			Index:        index,
			Description:  doc.Items[index].Description,
			AmountWei:    doc.Items[index].AmountWei,
			Evidence:     doc.Items[index].Evidence,
			EvidenceHash: bundle[index],
		})

		for _, v := range verdicts {
			if v.Refused() {
				// Already logged by the panel with its reason. Nothing is
				// submitted for a refused slot: the item simply has one fewer
				// voice, which can only make establishing the deduction harder.
				continue
			}
			logger.Print(submitPrefix + jsonLine(Submission{
				Slot: v.Slot,
				Item: index,
				Args: chain.SubmitArgs(cfg.DisputeAddress, v, bundle[index], proofs[index]),
			}))
		}
	}
}

// loadBundle opens and parses the published evidence bundle.
func loadBundle(deps Deps, path string) (evidence.Document, evidence.Bundle, error) {
	f, err := deps.Open(path)
	if err != nil {
		return evidence.Document{}, evidence.Bundle{}, fmt.Errorf("%w: %w", evidence.ErrReadDocument, err)
	}
	defer func() { _ = f.Close() }()
	return evidence.Load(f)
}
