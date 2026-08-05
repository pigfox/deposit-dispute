//go:build live

// Package main's LIVE reachability probe.
//
// BUILD-TAGGED AND OPT-IN. This is the only test in the repository that spends
// money, so it is excluded from every ordinary run: `go test ./...` does not
// compile this file, CI does not run it, and the coverage gate never sees it.
// Run it deliberately:
//
//	./scripts/with-env.sh 'go test -tags live -run TestLiveSlotsAreReachable -v ./...'
//
// WHAT IT IS FOR. The three model identifiers in .env are the strings the
// contracts get CONSTRUCTED with — keccak256 of each is stored on chain, and the
// adjudicator refuses to run if what it is configured with does not hash to what
// the contract holds. A typo, a retired identifier or a key without access to a
// model is therefore not an edit after the fact: it is a redeployment. So each
// slot is proved callable BEFORE anything is deployed, at a cost of three
// trivial calls.
//
// IT ALSO REPORTS WHAT THE VENDOR SAYS IT RAN, which is not always what was
// asked for. An alias resolving to a dated snapshot is exactly the divergence
// worth knowing about before the requested string is hashed into a constructor.
package main

import (
	"context"
	"net/http"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/pigfox/deposit-dispute/adjudicator/internal/config"
	"github.com/pigfox/deposit-dispute/adjudicator/internal/evidence"
	"github.com/pigfox/deposit-dispute/adjudicator/internal/model"
)

// probeTimeout bounds one probe call.
const probeTimeout = 60 * time.Second

// probePrompt asks for the smallest useful answer. The point is reachability,
// not content.
const (
	probeSystem = "Reply with exactly one word."
	probeUser   = "Reply with the single word: OK"
)

// envProbeSlot narrows the probe to a single slot index.
const envProbeSlot = "DD_PROBE_SLOT"

// probePlaceholderDispute stands in for the one value config.Load requires that
// a reachability probe genuinely cannot have.
//
// The dispute address is not known until the contracts are deployed, and the
// whole point of this probe is to run BEFORE that. It is supplied here, in
// process, rather than written into .env — a placeholder in the file would
// outlive this test and would later read as a real target.
const probePlaceholderDispute = "0xDD00000000000000000000000000000000000DD0"

// TestLiveSlotsAreReachable makes ONE call per slot and reports what came back.
func TestLiveSlotsAreReachable(t *testing.T) {
	getenv := func(key string) string {
		if key == config.EnvDisputeAddress {
			if v := os.Getenv(key); v != "" {
				return v
			}
			return probePlaceholderDispute
		}
		return os.Getenv(key)
	}

	cfg, err := config.Load(getenv)
	if err != nil {
		t.Fatalf("config: %v", err)
	}

	// EnvProbeSlot narrows the probe to one slot, so re-checking a single
	// identifier after a change costs one metered call rather than three. Absent,
	// every slot is probed.
	only := -1
	if raw := os.Getenv(envProbeSlot); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 || n >= config.SlotCount {
			t.Fatalf("%s=%q is not a slot index below %d", envProbeSlot, raw, config.SlotCount)
		}
		only = n
		t.Logf("probing slot %d only (%s is set)", only, envProbeSlot)
	}

	doer := &http.Client{Timeout: probeTimeout}

	for slot, slotCfg := range cfg.Slots {
		if only >= 0 && slot != only {
			continue
		}
		client, err := model.New(slotCfg, doer)
		if err != nil {
			t.Fatalf("slot %d: %v", slot, err)
		}

		ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
		reply, err := client.Complete(ctx, probeSystem, probeUser)
		cancel()

		t.Logf("slot %d  provider=%s  requested=%q  temperature=%v",
			slot, slotCfg.Provider, client.ModelID(), client.SendsTemperature())

		if err != nil {
			t.Errorf("slot %d UNREACHABLE: %v", slot, err)
			continue
		}

		t.Logf("slot %d  HTTP 200  resolved=%q  reply=%q", slot, reply.ResolvedModel, reply.Text)

		if reply.ResolvedModel != "" && reply.ResolvedModel != client.ModelID() {
			// NOT a failure, and NOT silently accepted. The requested string is
			// what gets hashed into the constructor, so a divergence is a fact to
			// be reported and decided on, not reconciled by rewriting .env.
			t.Logf("slot %d  DIVERGENCE: requested %q, vendor answered as %q",
				slot, client.ModelID(), reply.ResolvedModel)
		}
	}
}

// TestLiveConfigHashesTheSlotsDistinctly checks the values that will be frozen
// into the constructors. It makes no vendor call; it is tagged `live` only
// because it reads the real .env.
//
// THESE HASHES ARE THE COMMITMENT. keccak256 of each identifier is what the
// contract stores and what a verdict carries, and the adjudicator refuses to run
// when the running configuration does not reproduce them. Computing them here,
// with the SAME function the chain comparison uses, is the last check before a
// deployment makes them permanent.
func TestLiveConfigHashesTheSlotsDistinctly(t *testing.T) {
	getenv := func(key string) string {
		if key == config.EnvDisputeAddress {
			if v := os.Getenv(key); v != "" {
				return v
			}
			return probePlaceholderDispute
		}
		return os.Getenv(key)
	}

	// Load succeeding is itself the proof that the single-vendor refusal did not
	// trigger: config.Load returns ErrSingleVendor when every slot names one
	// provider, so a configuration that loads has more than one.
	cfg, err := config.Load(getenv)
	if err != nil {
		t.Fatalf("config: %v", err)
	}

	providers := map[string]int{}
	ids := map[string]int{}
	hashes := map[evidence.Hash]int{}

	for slot, s := range cfg.Slots {
		h := evidence.Keccak([]byte(s.ModelID))
		t.Logf("slot %d  provider=%-9s  modelId=%-16q  keccak256=%s",
			slot, s.Provider, s.ModelID, h.Hex())

		if first, dup := ids[s.ModelID]; dup {
			t.Errorf("slots %d and %d name the same model %q", first, slot, s.ModelID)
		}
		if first, dup := hashes[h]; dup {
			t.Errorf("slots %d and %d hash to the same commitment", first, slot)
		}
		ids[s.ModelID] = slot
		hashes[h] = slot
		providers[s.Provider]++
	}

	if len(ids) != config.SlotCount {
		t.Errorf("%d distinct identifiers, want %d", len(ids), config.SlotCount)
	}
	if len(hashes) != config.SlotCount {
		t.Errorf("%d distinct hashes, want %d", len(hashes), config.SlotCount)
	}
	if len(providers) < 2 {
		t.Errorf("all slots speak to one vendor: %v", providers)
	}
	t.Logf("providers: %v", providers)
}
