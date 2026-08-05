package evidence_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/pigfox/deposit-dispute/adjudicator/internal/config"
	"github.com/pigfox/deposit-dispute/adjudicator/internal/evidence"
)

// pinText is the fixture BOTH LANGUAGES hash. The Solidity counterpart builds
// the same five strings and asserts the same root — see
// test/DepositDispute.t.sol, TestTheMerkleRootPinMatchesTheGoAgent.
func pinText(i int) string { return fmt.Sprintf("deposit-dispute:pin:item%d", i) }

// pinBundle is the five per-item hashes of that fixture.
func pinBundle() evidence.Bundle {
	var b evidence.Bundle
	for i := range b {
		b[i] = evidence.Keccak([]byte(pinText(i)))
	}
	return b
}

// pinnedRoot is the merkle root over pinBundle.
//
// THE CROSS-LANGUAGE PIN. This exact literal appears in the Solidity suite too.
// The Go side lays the tree out and reads siblings off it; the Solidity side
// folds a proof. They are different algorithms over the same shape, and if they
// ever stop agreeing the adjudicator would build proofs the chain refuses — which
// would otherwise surface as a reverted transaction rather than as a failing
// test.
const pinnedRoot = "0x93f43ae0bfa8187d6b710fd892e12ed6c8bc663d4473bea5ee4fa5872a4e3113"

func TestTheTreePinMatchesTheSolidityFixture(t *testing.T) {
	got := evidence.Root(pinBundle())
	if got.Hex() != pinnedRoot {
		t.Fatalf("the Go merkle root is now %s, not the pinned %s.\n"+
			"If this change was intended, the SAME literal must change in "+
			"test/DepositDispute.t.sol in the same commit — otherwise the agent "+
			"builds proofs the contract refuses.", got.Hex(), pinnedRoot)
	}
}

// TestKeccakIsTheEVMHashNotSHA3 catches the single most expensive confusion in
// this package. FIPS-202 SHA3-256 and the EVM's Keccak-256 differ only in a
// padding byte, so a mix-up produces a plausible 32-byte value for every input
// and nothing fails until a proof is rejected on chain.
func TestKeccakIsTheEVMHashNotSHA3(t *testing.T) {
	// keccak256("") — the well-known empty-input digest every EVM tool agrees on.
	const emptyKeccak = "0xc5d2460186f7233c927e7db2dcc703c0e500b653ca82273b7bfad8045d85a470"
	if got := evidence.Keccak(nil).Hex(); got != emptyKeccak {
		t.Fatalf("keccak256(\"\") = %s, want %s — this is SHA3, not Keccak", got, emptyKeccak)
	}
}

func TestLeafBindsTheIndex(t *testing.T) {
	h := evidence.Keccak([]byte("one bundle"))

	a, err := evidence.Leaf(0, h)
	if err != nil {
		t.Fatalf("Leaf(0): %v", err)
	}
	b, err := evidence.Leaf(1, h)
	if err != nil {
		t.Fatalf("Leaf(1): %v", err)
	}
	if a == b {
		t.Fatal("the same evidence at two indices produced the same leaf")
	}
}

func TestLeafRejectsAnIndexOffTheSchedule(t *testing.T) {
	for _, index := range []int{-1, config.ItemCount} {
		if _, err := evidence.Leaf(index, evidence.Hash{}); !errors.Is(err, evidence.ErrIndexOutOfRange) {
			t.Errorf("Leaf(%d) err = %v, want ErrIndexOutOfRange", index, err)
		}
	}
}

// TestProofLengthIsPinnedPerIndex is the rule that stops item 4's short path
// being offered for item 0.
func TestProofLengthIsPinnedPerIndex(t *testing.T) {
	for index := 0; index < config.ItemCount-1; index++ {
		got, err := evidence.ProofLength(index)
		if err != nil {
			t.Fatalf("ProofLength(%d): %v", index, err)
		}
		if got != 3 {
			t.Errorf("ProofLength(%d) = %d, want 3", index, got)
		}
	}
	got, err := evidence.ProofLength(config.ItemCount - 1)
	if err != nil {
		t.Fatalf("ProofLength(promoted): %v", err)
	}
	if got != 1 {
		t.Errorf("ProofLength(promoted) = %d, want 1", got)
	}

	for _, index := range []int{-1, config.ItemCount} {
		if _, err := evidence.ProofLength(index); !errors.Is(err, evidence.ErrIndexOutOfRange) {
			t.Errorf("ProofLength(%d) err = %v, want ErrIndexOutOfRange", index, err)
		}
	}
}

func TestEveryItemProvesMembershipAtItsOwnIndex(t *testing.T) {
	bundle := pinBundle()
	root := evidence.Root(bundle)

	for index := 0; index < config.ItemCount; index++ {
		proof, err := evidence.Proof(bundle, index)
		if err != nil {
			t.Fatalf("Proof(%d): %v", index, err)
		}
		wantLen, _ := evidence.ProofLength(index)
		if len(proof) != wantLen {
			t.Fatalf("Proof(%d) has length %d, want %d", index, len(proof), wantLen)
		}
		if !evidence.Verify(root, index, bundle[index], proof) {
			t.Fatalf("item %d does not prove membership at its own index", index)
		}
	}
}

// TestEvidenceForOneItemCannotBeSpentOnAnother is the binding property, driven
// over every ordered pair — the same assertion the Solidity suite makes.
func TestEvidenceForOneItemCannotBeSpentOnAnother(t *testing.T) {
	bundle := pinBundle()
	root := evidence.Root(bundle)

	for target := 0; target < config.ItemCount; target++ {
		for source := 0; source < config.ItemCount; source++ {
			if source == target {
				continue
			}
			proof, err := evidence.Proof(bundle, source)
			if err != nil {
				t.Fatalf("Proof(%d): %v", source, err)
			}
			if evidence.Verify(root, target, bundle[source], proof) {
				t.Fatalf("item %d's evidence was accepted against item %d", source, target)
			}
		}
	}
}

// TestProofsIsTotalOverTheSchedule covers the call-site shape: all five proofs,
// built before any vendor call, with nothing that can fail.
func TestProofsIsTotalOverTheSchedule(t *testing.T) {
	bundle := pinBundle()
	root := evidence.Root(bundle)

	all := evidence.Proofs(bundle)
	if len(all) != config.ItemCount {
		t.Fatalf("got %d proofs, want %d", len(all), config.ItemCount)
	}
	for index, proof := range all {
		if !evidence.Verify(root, index, bundle[index], proof) {
			t.Fatalf("Proofs()[%d] does not verify", index)
		}
		one, err := evidence.Proof(bundle, index)
		if err != nil {
			t.Fatalf("Proof(%d): %v", index, err)
		}
		if len(one) != len(proof) {
			t.Fatalf("Proofs()[%d] disagrees with Proof(%d)", index, index)
		}
	}
}

// TestVerifyRefusesANodePairedWithItself covers the equal-hash branch of the
// pair comparison, which a well-formed proof never reaches.
func TestVerifyRefusesANodePairedWithItself(t *testing.T) {
	bundle := pinBundle()
	root := evidence.Root(bundle)

	leaf, err := evidence.Leaf(0, bundle[0])
	if err != nil {
		t.Fatalf("Leaf: %v", err)
	}
	// A proof whose first sibling IS the leaf it is folded with.
	selfPaired := []evidence.Hash{leaf, leaf, leaf}
	if evidence.Verify(root, 0, bundle[0], selfPaired) {
		t.Fatal("a proof pairing a node with itself was accepted")
	}
}

func TestVerifyRefusesAMalformedProof(t *testing.T) {
	bundle := pinBundle()
	root := evidence.Root(bundle)
	proof, err := evidence.Proof(bundle, 0)
	if err != nil {
		t.Fatalf("Proof: %v", err)
	}

	if evidence.Verify(root, 0, bundle[0], proof[:1]) {
		t.Error("a short proof was accepted")
	}
	if evidence.Verify(root, 0, bundle[0], append(proof, evidence.Hash{})) {
		t.Error("a padded proof was accepted")
	}
	if evidence.Verify(root, config.ItemCount, bundle[0], proof) {
		t.Error("an index off the schedule was accepted")
	}
	if evidence.Verify(evidence.Hash{}, 0, bundle[0], proof) {
		t.Error("a proof was accepted against the wrong root")
	}
}

func TestProofRejectsAnIndexOffTheSchedule(t *testing.T) {
	if _, err := evidence.Proof(pinBundle(), config.ItemCount); !errors.Is(err, evidence.ErrIndexOutOfRange) {
		t.Fatalf("err = %v, want ErrIndexOutOfRange", err)
	}
}

func TestHashHexIsLowerCaseAndRoundTrips(t *testing.T) {
	h := evidence.Keccak([]byte("round trip"))
	s := h.Hex()

	if s != strings.ToLower(s) {
		t.Errorf("Hex() is not lower case: %s", s)
	}
	if !strings.HasPrefix(s, "0x") || len(s) != 66 {
		t.Errorf("Hex() is not a 0x-prefixed 32-byte string: %s", s)
	}

	back, err := evidence.ParseHash(s)
	if err != nil {
		t.Fatalf("ParseHash: %v", err)
	}
	if back != h {
		t.Error("a hash did not survive Hex then ParseHash")
	}
}

func TestParseHashRejects(t *testing.T) {
	cases := []string{
		"",
		"deadbeef",
		"0xnothex" + strings.Repeat("0", 58),
		"0x" + strings.Repeat("a", 62),
		"0x" + strings.Repeat("a", 66),
	}
	for _, s := range cases {
		if _, err := evidence.ParseHash(s); !errors.Is(err, evidence.ErrBadHash) {
			t.Errorf("ParseHash(%q) err = %v, want ErrBadHash", s, err)
		}
	}
}

func TestParseHashToleratesSurroundingSpace(t *testing.T) {
	h := evidence.Keccak([]byte("spacey"))
	got, err := evidence.ParseHash("  " + h.Hex() + "\n")
	if err != nil {
		t.Fatalf("ParseHash: %v", err)
	}
	if got != h {
		t.Error("trimming changed the value")
	}
}

func TestHexProofRendersACastArrayLiteral(t *testing.T) {
	bundle := pinBundle()
	proof, err := evidence.Proof(bundle, 0)
	if err != nil {
		t.Fatalf("Proof: %v", err)
	}

	got := evidence.HexProof(proof)
	if !strings.HasPrefix(got, "[") || !strings.HasSuffix(got, "]") {
		t.Fatalf("HexProof is not bracketed: %s", got)
	}
	if n := strings.Count(got, "0x"); n != len(proof) {
		t.Fatalf("HexProof carries %d hashes, want %d", n, len(proof))
	}
	if strings.Contains(got, " ") {
		t.Errorf("HexProof must not contain spaces, cast would split it: %s", got)
	}
	if got := evidence.HexProof(nil); got != "[]" {
		t.Errorf("HexProof(nil) = %q, want []", got)
	}
}
