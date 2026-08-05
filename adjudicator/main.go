// Command adjudicator runs one deposit-dispute adjudication: it checks the
// published evidence bundle against the claim's on-chain commitment, verifies
// that the three configured models are the ones the contract was constructed
// with, and asks each of them — separately, one call per line item — whether the
// landlord established that deduction.
//
// IT SIGNS NOTHING AND BROADCASTS NOTHING. Every verdict is rendered as the
// argument list that would submit it. That is the mode a third party follows to
// re-run an adjudication and check the published hashes, and it needs no private
// key because this program holds none.
//
// All behavior lives in internal packages; this file only wires stdio and signal
// handling to app.Run.
package main

import (
	"context"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/pigfox/deposit-dispute/adjudicator/internal/app"
)

// Injection seams so main() itself is exercised by a test without exiting the
// test binary or making a real call.
var (
	exit              = os.Exit
	run               = app.Run
	stdout  io.Writer = os.Stdout
	args              = os.Args
	signals           = []os.Signal{syscall.SIGINT, syscall.SIGTERM}
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), signals...)
	defer stop()
	exit(run(ctx, stdout, args))
}
