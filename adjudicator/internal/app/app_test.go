package app_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/pigfox/deposit-dispute/adjudicator/internal/app"
	"github.com/pigfox/deposit-dispute/adjudicator/internal/chain"
	"github.com/pigfox/deposit-dispute/adjudicator/internal/config"
	"github.com/pigfox/deposit-dispute/adjudicator/internal/evidence"
)

const disputeAddress = "0xDD00000000000000000000000000000000000DD0"

var slotModels = [config.SlotCount]string{"model-alpha", "model-beta", "model-gamma"}

// --- seams -------------------------------------------------------------------

type doerFunc func(*http.Request) (*http.Response, error)

func (f doerFunc) Do(r *http.Request) (*http.Response, error) { return f(r) }

type runnerFunc func(ctx context.Context, name string, args ...string) ([]byte, error)

func (f runnerFunc) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return f(ctx, name, args...)
}

// nopCloser makes a string readable and closable.
type nopCloser struct{ io.Reader }

func (nopCloser) Close() error { return nil }

const bundleJSON = `{
  "dispute": "0xDD00000000000000000000000000000000000DD0",
  "items": [
    {"description": "carpet", "amountWei": "1", "evidence": "carpet evidence"},
    {"description": "wall",   "amountWei": "2", "evidence": "wall evidence"},
    {"description": "window", "amountWei": "3", "evidence": "window evidence"},
    {"description": "door",   "amountWei": "4", "evidence": "door evidence"},
    {"description": "clean",  "amountWei": "5", "evidence": "cleaning evidence"}
  ]
}`

// bundleRoot is the commitment the fixture bundle produces.
func bundleRoot(t *testing.T) evidence.Hash {
	t.Helper()
	doc, _, err := evidence.Load(strings.NewReader(bundleJSON))
	if err != nil {
		t.Fatalf("loading the fixture bundle: %v", err)
	}
	b, err := doc.Bundle()
	if err != nil {
		t.Fatalf("hashing the fixture bundle: %v", err)
	}
	return evidence.Root(b)
}

func goodEnv() map[string]string {
	m := map[string]string{
		config.EnvRPCURL:         "https://example.invalid/rpc",
		config.EnvDisputeAddress: disputeAddress,
		config.EnvAnthropicKey:   "anthropic-key",
		config.EnvOpenAIKey:      "openai-key",
	}
	providers := []string{config.ProviderAnthropic, config.ProviderOpenAI, config.ProviderAnthropic}
	for i := 0; i < config.SlotCount; i++ {
		m[fmt.Sprintf(config.EnvSlotProviderFmt, i)] = providers[i]
		m[fmt.Sprintf(config.EnvSlotModelIDFmt, i)] = slotModels[i]
	}
	return m
}

// healthyChain answers every read as a correctly-deployed dispute would.
func healthyChain(t *testing.T) runnerFunc {
	t.Helper()
	root := bundleRoot(t)
	return func(_ context.Context, _ string, args ...string) ([]byte, error) {
		joined := strings.Join(args, " ")
		switch {
		case strings.Contains(joined, "chain-id"):
			return []byte(strconv.FormatUint(config.ExpectedChainID, 10)), nil
		case strings.Contains(joined, config.SigModelIDHashAt):
			slot, _ := strconv.Atoi(args[len(args)-3])
			return []byte(evidence.Keccak([]byte(slotModels[slot])).Hex()), nil
		case strings.Contains(joined, config.SigAdjudicatorAt):
			return []byte("0x0000000000000000000000000000000000000001\n\"model\""), nil
		case strings.Contains(joined, config.SigEvidenceRoot):
			return []byte(root.Hex()), nil
		}
		return nil, fmt.Errorf("nothing scripted for %q", joined)
	}
}

// answering returns a Doer that gives every slot the same constrained reply.
func answering(finding string) doerFunc {
	body := fmt.Sprintf(
		`{"model":"resolved-snapshot","content":[{"text":%q}],"choices":[{"message":{"content":%q}}]}`,
		fmt.Sprintf(`{"finding":%q,"reason":"the evidence carries it","narrative":"full"}`, finding),
		fmt.Sprintf(`{"finding":%q,"reason":"the evidence carries it","narrative":"full"}`, finding),
	)
	return func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: nopCloser{strings.NewReader(body)}}, nil
	}
}

func deps(t *testing.T, env map[string]string) app.Deps {
	t.Helper()
	return app.Deps{
		Getenv: func(k string) string { return env[k] },
		Doer:   answering(config.FindingEstablished),
		Runner: healthyChain(t),
		Open:   func(string) (io.ReadCloser, error) { return nopCloser{strings.NewReader(bundleJSON)}, nil },
	}
}

func argv(extra ...string) []string {
	return append([]string{"adjudicator", "--bundle", "bundle.json"}, extra...)
}

// --- tests -------------------------------------------------------------------

func TestAFullRunAdjudicatesEveryItemAndSubmitsNothing(t *testing.T) {
	var out bytes.Buffer
	code := app.RunWith(context.Background(), &out, argv(), deps(t, goodEnv()))
	if code != app.ExitOK {
		t.Fatalf("exit = %d, want %d\n%s", code, app.ExitOK, out.String())
	}

	log := out.String()
	for i := 0; i < config.ItemCount; i++ {
		if !strings.Contains(log, fmt.Sprintf("item %d:", i)) {
			t.Errorf("item %d was never adjudicated:\n%s", i, log)
		}
	}
	// Fifteen rendered submissions: five items times three slots, none refused.
	if n := strings.Count(log, "SUBMIT "); n != config.ItemCount*config.SlotCount {
		t.Errorf("rendered %d submissions, want %d", n, config.ItemCount*config.SlotCount)
	}
	// NOTHING IS BROADCAST. The run renders `send` argument lists and never
	// executes one.
	if strings.Contains(log, "broadcast") || strings.Contains(log, "transaction hash") {
		t.Error("the run appears to have sent something")
	}
	if !strings.Contains(log, config.LogSpendSurfaceHeader) {
		t.Error("the spend surface must be printed before any metered call")
	}
}

// TestEveryRenderedSubmissionCarriesAUsableProof checks the agent's output
// against the contract's own rule, without a chain.
func TestEveryRenderedSubmissionCarriesAUsableProof(t *testing.T) {
	doc, _, err := evidence.Load(strings.NewReader(bundleJSON))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	bundle, err := doc.Bundle()
	if err != nil {
		t.Fatalf("Bundle: %v", err)
	}
	root := evidence.Root(bundle)

	for index, proof := range evidence.Proofs(bundle) {
		if !evidence.Verify(root, index, bundle[index], proof) {
			t.Fatalf("item %d's rendered proof would be refused on chain", index)
		}
	}
}

func TestRunRefuses(t *testing.T) {
	cases := []struct {
		name string
		deps func(t *testing.T) app.Deps
		argv []string
		want int
	}{
		{
			"an unparseable flag",
			func(t *testing.T) app.Deps { return deps(t, goodEnv()) },
			[]string{"adjudicator", "--not-a-flag"},
			app.ExitUsage,
		},
		{
			"no bundle named",
			func(t *testing.T) app.Deps { return deps(t, goodEnv()) },
			[]string{"adjudicator"},
			app.ExitUsage,
		},
		{
			"a broken environment",
			func(t *testing.T) app.Deps {
				env := goodEnv()
				delete(env, config.EnvRPCURL)
				return deps(t, env)
			},
			argv(),
			app.ExitConfig,
		},
		{
			"an unopenable bundle",
			func(t *testing.T) app.Deps {
				d := deps(t, goodEnv())
				d.Open = func(string) (io.ReadCloser, error) { return nil, errors.New("no such file") }
				return d
			},
			argv(),
			app.ExitConfig,
		},
		{
			"an invalid bundle",
			func(t *testing.T) app.Deps {
				d := deps(t, goodEnv())
				d.Open = func(string) (io.ReadCloser, error) {
					return nopCloser{strings.NewReader(`{"items":[]}`)}, nil
				}
				return d
			},
			argv(),
			app.ExitConfig,
		},
		{
			"the wrong chain",
			func(t *testing.T) app.Deps {
				d := deps(t, goodEnv())
				d.Runner = runnerFunc(func(context.Context, string, ...string) ([]byte, error) {
					return []byte("1"), nil
				})
				return d
			},
			argv(),
			app.ExitChainMismatch,
		},
		{
			"a model the contract did not register",
			func(t *testing.T) app.Deps {
				env := goodEnv()
				env[fmt.Sprintf(config.EnvSlotModelIDFmt, 1)] = "a-model-nobody-registered"
				return deps(t, env)
			},
			argv(),
			app.ExitChainMismatch,
		},
		{
			"a bundle that does not reproduce the filed commitment",
			func(t *testing.T) app.Deps {
				d := deps(t, goodEnv())
				other := strings.Replace(bundleJSON, "carpet evidence", "a different story", 1)
				d.Open = func(string) (io.ReadCloser, error) {
					return nopCloser{strings.NewReader(other)}, nil
				}
				return d
			},
			argv(),
			app.ExitChainMismatch,
		},
		{
			"an unreadable commitment",
			func(t *testing.T) app.Deps {
				d := deps(t, goodEnv())
				healthy := healthyChain(t)
				d.Runner = runnerFunc(func(ctx context.Context, name string, args ...string) ([]byte, error) {
					if strings.Contains(strings.Join(args, " "), config.SigEvidenceRoot) {
						return nil, errors.New("reverted")
					}
					return healthy(ctx, name, args...)
				})
				return d
			},
			argv(),
			app.ExitChainMismatch,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			if code := app.RunWith(context.Background(), &out, tc.argv, tc.deps(t)); code != tc.want {
				t.Fatalf("exit = %d, want %d\n%s", code, tc.want, out.String())
			}
		})
	}
}

// TestAnUnknownProviderIsRefusedBeforeAnyCall drives the refusal through the
// layer that owns it: config carries the name through and model.New says no.
func TestAnUnknownProviderIsRefusedBeforeAnyCall(t *testing.T) {
	env := goodEnv()
	env[fmt.Sprintf(config.EnvSlotProviderFmt, 0)] = "a-vendor-we-cannot-speak-to"

	d := deps(t, env)
	d.Doer = doerFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("no vendor should have been called")
	})

	var out bytes.Buffer
	if code := app.RunWith(context.Background(), &out, argv(), d); code != app.ExitConfig {
		t.Fatalf("exit = %d, want %d\n%s", code, app.ExitConfig, out.String())
	}
	if !strings.Contains(out.String(), "unknown provider") {
		t.Errorf("the refusal should name the problem:\n%s", out.String())
	}
}

// TestTwoSlotsNamingOneModelIsRefusedByThePanel drives the duplicate-model rule
// through the layer that owns it. Three copies of one model agree trivially, so
// a 2-of-3 threshold over them measures nothing.
func TestTwoSlotsNamingOneModelIsRefusedByThePanel(t *testing.T) {
	env := goodEnv()
	env[fmt.Sprintf(config.EnvSlotModelIDFmt, 2)] = slotModels[0]

	var out bytes.Buffer
	if code := app.RunWith(context.Background(), &out, argv(), deps(t, env)); code != app.ExitConfig {
		t.Fatalf("exit = %d, want %d\n%s", code, app.ExitConfig, out.String())
	}
	if !strings.Contains(out.String(), "declare the same model") {
		t.Errorf("the refusal should name the problem:\n%s", out.String())
	}
}

// TestARefusingSlotIsSkippedAndTheOthersStillSubmit is the safety direction of
// the whole design, at the level of a whole run.
func TestARefusingSlotIsSkippedAndTheOthersStillSubmit(t *testing.T) {
	calls := 0
	d := deps(t, goodEnv())
	good := answering(config.FindingEstablished)
	d.Doer = doerFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		// Every third call is the third slot; make it answer unusably.
		if calls%config.SlotCount == 0 {
			return &http.Response{
				StatusCode: 200,
				Body:       nopCloser{strings.NewReader(`{"content":[{"text":"I cannot help with that."}]}`)},
			}, nil
		}
		return good(r)
	})

	var out bytes.Buffer
	if code := app.RunWith(context.Background(), &out, argv(), d); code != app.ExitOK {
		t.Fatalf("exit = %d\n%s", code, out.String())
	}

	log := out.String()
	if !strings.Contains(log, "REFUSED") {
		t.Fatalf("the refusal was not logged:\n%s", log)
	}
	want := config.ItemCount * (config.SlotCount - 1)
	if n := strings.Count(log, "SUBMIT "); n != want {
		t.Fatalf("rendered %d submissions, want %d — a refused slot must submit nothing", n, want)
	}
}

// TestPrintRootNeedsNothingButTheBundle covers the mode the landlord uses to
// file: it must work before the contract exists, so it reads no chain, calls no
// model and needs no configuration at all.
func TestPrintRootNeedsNothingButTheBundle(t *testing.T) {
	d := deps(t, map[string]string{}) // deliberately empty environment
	d.Doer = doerFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("no vendor may be called")
	})
	d.Runner = runnerFunc(func(context.Context, string, ...string) ([]byte, error) {
		return nil, errors.New("no chain may be read")
	})

	var out bytes.Buffer
	code := app.RunWith(context.Background(), &out, argv("-print-root"), d)
	if code != app.ExitOK {
		t.Fatalf("exit = %d, want %d\n%s", code, app.ExitOK, out.String())
	}
	if got := strings.TrimSpace(out.String()); got != bundleRoot(t).Hex() {
		t.Fatalf("printed %q, want %s", got, bundleRoot(t).Hex())
	}
}

func TestRunUsesTheRealSeams(t *testing.T) {
	// Run() is the production wrapper. Pointing it at an environment with nothing
	// in it proves it reaches config.Load through the real os.Getenv rather than
	// through a seam a test left behind.
	t.Setenv(config.EnvRPCURL, "")
	t.Setenv(config.EnvDisputeAddress, "")

	var out bytes.Buffer
	if code := app.Run(context.Background(), &out, argv()); code != app.ExitConfig {
		t.Fatalf("exit = %d, want %d\n%s", code, app.ExitConfig, out.String())
	}
}

func TestDefaultDepsAreTheRealThings(t *testing.T) {
	d := app.DefaultDeps()
	if d.Getenv == nil || d.Doer == nil || d.Runner == nil || d.Open == nil {
		t.Fatal("a production seam is nil")
	}
	if _, ok := d.Runner.(chain.ExecRunner); !ok {
		t.Errorf("the runner seam is not the real one: %T", d.Runner)
	}
	if _, err := d.Open("a-file-that-does-not-exist-anywhere"); err == nil {
		t.Error("the open seam should report a missing file")
	}
}

// TestTheShippedExampleBundleIsUsableAndPinned guards the published document
// against rotting into something the service would refuse. It is a document, not
// a fixture: the root below is what a claim filed against it commits to.
func TestTheShippedExampleBundleIsUsableAndPinned(t *testing.T) {
	f, err := app.DefaultDeps().Open("../../evidence/example.json")
	if err != nil {
		t.Fatalf("opening the published bundle: %v", err)
	}
	defer func() { _ = f.Close() }()

	doc, _, err := evidence.Load(f)
	if err != nil {
		t.Fatalf("the published bundle would be refused: %v", err)
	}
	bundle, err := doc.Bundle()
	if err != nil {
		t.Fatalf("hashing the published bundle: %v", err)
	}
	root := evidence.Root(bundle)

	// Every item must produce a proof the contract would accept.
	for index, proof := range evidence.Proofs(bundle) {
		if !evidence.Verify(root, index, bundle[index], proof) {
			t.Fatalf("item %d of the published bundle would be refused on chain", index)
		}
	}

	// And every item's evidence must be distinct, or two deductions would share
	// a commitment and evidence could be moved between them.
	seen := map[evidence.Hash]bool{}
	for _, h := range bundle {
		if seen[h] {
			t.Fatal("two items of the published bundle share an evidence hash")
		}
		seen[h] = true
	}
}
