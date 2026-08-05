package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"syscall"
	"testing"
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
