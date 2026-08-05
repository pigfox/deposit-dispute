package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"strings"
	"syscall"
	"testing"

	"github.com/pigfox/deposit-dispute/adjudicator/internal/evidence"
)

// TestMainWiresRunToExit exercises main() itself without exiting the test binary
// or making a real call: every seam is replaced, and the test asserts that the
// exit code main passes on is exactly the one run returned.
func TestMainWiresRunToExit(t *testing.T) {
	oldExit, oldRun, oldStdout, oldArgs := exit, run, stdout, args
	t.Cleanup(func() { exit, run, stdout, args = oldExit, oldRun, oldStdout, oldArgs })

	var buf bytes.Buffer
	var gotCode int
	var gotArgs []string
	var liveDuringRun bool

	exit = func(code int) { gotCode = code }
	run = func(ctx context.Context, out io.Writer, argv []string) int {
		// Read the context WHILE run is executing. main defers stop(), so by the
		// time main returns the context is cancelled — which is correct, and is
		// why a check afterwards would be checking the wrong moment.
		liveDuringRun = ctx != nil && ctx.Err() == nil
		gotArgs = argv
		_, _ = out.Write([]byte("ran"))
		return 3
	}
	stdout = &buf
	args = []string{"adjudicator", "--bundle", "evidence/example.json"}

	main()

	if gotCode != 3 {
		t.Fatalf("exit code = %d, want 3", gotCode)
	}
	if buf.String() != "ran" {
		t.Fatalf("stdout was not passed through: %q", buf.String())
	}
	if len(gotArgs) != 3 || gotArgs[0] != "adjudicator" {
		t.Fatalf("argv was not passed through: %v", gotArgs)
	}
	if !liveDuringRun {
		t.Fatal("main did not supply a live context to run")
	}
}

// bundlePaths maps a published evidence bundle to the deploy-script constants
// that describe the same five deductions on chain.
var bundlePaths = map[string]string{
	"evidence/example.json":     "SPLIT_DESC_",
	"evidence/cap-example.json": "CAP_DESC_",
}

// deployScriptPath is the script that writes the schedule on chain.
const deployScriptPath = "../script/Deploy.s.sol"

// TestTheDeployedScheduleMatchesThePublishedBundles holds the two halves of the
// system together across the language boundary.
//
// Solidity cannot see the JSON, so the deploy script carries each deduction's
// description as a string literal and the contract stores its keccak256. The
// adjudicator shows the JSON's description to the models. If the two ever
// disagree, the panel is answering about a deduction the chain does not describe
// — and the chain would still accept the verdict, because the contract commits to
// the description it was given, not to the one the model saw. Nothing on chain
// could detect that. This is the only place it can be caught.
func TestTheDeployedScheduleMatchesThePublishedBundles(t *testing.T) {
	script, err := os.ReadFile(deployScriptPath)
	if err != nil {
		t.Fatalf("reading %s: %v", deployScriptPath, err)
	}

	for path, prefix := range bundlePaths {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading %s: %v", path, err)
		}
		var doc struct {
			Items []struct {
				Description string `json:"description"`
			} `json:"items"`
		}
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatalf("decoding %s: %v", path, err)
		}
		if len(doc.Items) == 0 {
			t.Fatalf("%s carries no items; the guard cannot see what it is guarding", path)
		}

		// The guard must be looking at real constants, not passing because it
		// found none to check.
		if !strings.Contains(string(script), prefix) {
			t.Fatalf("%s declares no %s* constants; the guard has gone blind", deployScriptPath, prefix)
		}

		for i, item := range doc.Items {
			quoted := `"` + item.Description + `"`
			if !strings.Contains(string(script), quoted) {
				t.Errorf("%s item %d describes %q, which %s does not declare.\n"+
					"The chain would commit to a description the models never saw.",
					path, i, item.Description, deployScriptPath)
			}
		}
	}
}

// TestChangingTheDisputeFieldDoesNotMoveTheCommitment pins a property the live
// run depends on: the bundle is published before the address is known, so the
// `dispute` field is filled in after deployment and before filing. That must not
// move the root, or the commitment would answer to a document nobody could
// reproduce.
func TestChangingTheDisputeFieldDoesNotMoveTheCommitment(t *testing.T) {
	for path := range bundlePaths {
		f, err := os.Open(path)
		if err != nil {
			t.Fatalf("opening %s: %v", path, err)
		}
		doc, bundle, err := evidence.Load(f)
		_ = f.Close()
		if err != nil {
			t.Fatalf("%s would be refused: %v", path, err)
		}

		before := evidence.Root(bundle)

		doc.Dispute = "0x1111111111111111111111111111111111111111"
		after, err := doc.Bundle()
		if err != nil {
			t.Fatalf("re-hashing %s: %v", path, err)
		}
		if evidence.Root(after) != before {
			t.Fatalf("%s: naming a different dispute moved the commitment", path)
		}
	}
}

// TestBothPublishedBundlesAreUsable guards the shipped documents against rotting
// into something the service would refuse, and against two items sharing a
// commitment.
func TestBothPublishedBundlesAreUsable(t *testing.T) {
	for path := range bundlePaths {
		f, err := os.Open(path)
		if err != nil {
			t.Fatalf("opening %s: %v", path, err)
		}
		_, bundle, err := evidence.Load(f)
		_ = f.Close()
		if err != nil {
			t.Fatalf("%s would be refused: %v", path, err)
		}

		root := evidence.Root(bundle)
		for index, proof := range evidence.Proofs(bundle) {
			if !evidence.Verify(root, index, bundle[index], proof) {
				t.Fatalf("%s item %d would be refused on chain", path, index)
			}
		}

		seen := map[evidence.Hash]bool{}
		for _, h := range bundle {
			if seen[h] {
				t.Fatalf("%s: two items share an evidence hash", path)
			}
			seen[h] = true
		}
	}
}

// TestTheDefaultSeamsAreTheRealThings keeps the production wiring honest: a seam
// pointing somewhere else would make the test above vacuous.
func TestTheDefaultSeamsAreTheRealThings(t *testing.T) {
	if stdout != io.Writer(os.Stdout) {
		t.Error("the default stdout seam is not os.Stdout")
	}
	if len(args) == 0 {
		t.Error("the default args seam is empty")
	}
	want := map[os.Signal]bool{syscall.SIGINT: true, syscall.SIGTERM: true}
	if len(signals) != len(want) {
		t.Fatalf("watching %d signals, want %d", len(signals), len(want))
	}
	for _, sig := range signals {
		if !want[sig] {
			t.Errorf("watching an unexpected signal %v", sig)
		}
	}
}
