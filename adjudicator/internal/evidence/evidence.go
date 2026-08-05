// Package evidence builds the merkle commitment DepositDispute verifies against.
//
// THIS IS THE OTHER HALF OF A CONTRACT, WRITTEN IN A DIFFERENT LANGUAGE. The
// Solidity side folds a proof; this side lays out the tree and reads siblings off
// it. If the two ever disagree the adjudicator produces verdicts the chain
// refuses, so the agreement is pinned by a literal that both languages assert —
// see TestTheTreePinMatchesTheSolidityFixture here and its counterpart in
// test/DepositDispute.t.sol.
package evidence

import (
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/sha3"

	"github.com/pigfox/deposit-dispute/adjudicator/internal/config"
)

// Sentinel errors.
var (
	// ErrIndexOutOfRange means an item index was not below config.ItemCount.
	ErrIndexOutOfRange = errors.New("evidence: item index out of range")
	// ErrBadHash means a supplied hex string was not a 32-byte value.
	ErrBadHash = errors.New("evidence: not a 0x-prefixed 32-byte hash")
)

// Hash is a 32-byte value.
type Hash [32]byte

// Hex renders a hash as the 0x-prefixed lower-case string `cast` wants.
//
// LOWER CASE, ALWAYS. A hash is not an address: EIP-55 casing carries a checksum
// for addresses and means nothing here, and mixing the two conventions is how a
// 32-byte value ends up being read as an address that fails a checksum test.
func (h Hash) Hex() string { return "0x" + hex.EncodeToString(h[:]) }

// ParseHash reads a 0x-prefixed 32-byte hex string.
func ParseHash(s string) (Hash, error) {
	var h Hash
	t := strings.TrimSpace(s)
	if !strings.HasPrefix(t, "0x") {
		return h, fmt.Errorf("%w: %q", ErrBadHash, s)
	}
	raw, err := hex.DecodeString(t[2:])
	if err != nil || len(raw) != len(h) {
		return h, fmt.Errorf("%w: %q", ErrBadHash, s)
	}
	copy(h[:], raw)
	return h, nil
}

// Bundle is the five per-item evidence hashes of one claim, in item order.
type Bundle [config.ItemCount]Hash

// Keccak hashes its inputs with the EVM's keccak256.
//
// Exported because the prompt hash and the narrative hash a verdict publishes are
// the same function over different bytes, and a second implementation of a hash
// is a second thing that can be wrong.
//
// NOT sha3.Sum256. Go's crypto/sha3 implements the FIPS-202 SHA3-256, which is a
// DIFFERENT function from the Keccak-256 the EVM uses — they differ in the
// padding byte, so the two produce different digests for every input and nothing
// would fail until a proof was rejected on chain.
func Keccak(parts ...[]byte) Hash { return keccak256(parts...) }

// keccak256 hashes its inputs with the EVM's keccak256.
func keccak256(parts ...[]byte) Hash {
	d := sha3.NewLegacyKeccak256()
	for _, p := range parts {
		_, _ = d.Write(p)
	}
	var out Hash
	copy(out[:], d.Sum(nil))
	return out
}

// word renders a small integer as a 32-byte big-endian word, which is how
// abi.encode lays out a uint256.
func word(n uint64) []byte {
	var b [32]byte
	for i := 0; n > 0; i++ {
		b[31-i] = byte(n)
		n >>= 8
	}
	return b[:]
}

// Leaf builds the leaf DepositDispute.leafFor builds for the same inputs:
//
//	keccak256(abi.encode(uint256 index, bytes32 itemEvidenceHash))
//
// THE INDEX IS INSIDE THE HASH, and that is the entire point of the scheme.
// Evidence gathered for the carpet produces a different leaf when it is offered
// against the wall, so it proves membership at the wrong place and the fold
// lands somewhere other than the committed root.
func Leaf(index int, itemEvidenceHash Hash) (Hash, error) {
	if index < 0 || index >= config.ItemCount {
		return Hash{}, fmt.Errorf("%w: %d", ErrIndexOutOfRange, index)
	}
	return leafAt(index, itemEvidenceHash), nil
}

// leafAt is Leaf without the bounds check, for the callers that hold an index
// they derived from the fixed schedule and therefore cannot get wrong.
//
// THE BOUNDS CHECK LIVES IN EXACTLY ONE PLACE PER ENTRY POINT. Writing it again
// on a path where the index came from `for i := range Bundle` produces a branch
// no test can reach, and an unreachable branch is not a safety net — it is a
// claim about a risk that does not exist there.
func leafAt(index int, itemEvidenceHash Hash) Hash {
	return keccak256(word(uint64(index)), itemEvidenceHash[:])
}

// hashPair is the order-independent pair hash the contract folds with. Because
// it is commutative, a proof carries no direction bits.
func hashPair(a, b Hash) Hash {
	if lessThan(a, b) {
		return keccak256(a[:], b[:])
	}
	return keccak256(b[:], a[:])
}

// lessThan compares two hashes as the EVM compares two bytes32 values: as
// big-endian unsigned integers, which for a fixed-width array is byte order.
func lessThan(a, b Hash) bool {
	for i := range a {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return false
}

// tree is the three interior values of the fixed five-leaf shape.
//
//	row 0   L0  L1  L2  L3  L4
//	row 1   h(L0,L1)  h(L2,L3)  L4        <- L4 promoted unpaired
//	row 2   h(row1_0,row1_1)    L4        <- promoted again
//	row 3   root = h(row2_0, L4)
type tree struct {
	leaves   [config.ItemCount]Hash
	row1Left Hash
	row1Rite Hash
	row2Left Hash
	root     Hash
}

// build lays out the whole tree once, so Root and Proof cannot disagree about
// its shape. Total: a Bundle is a fixed-size array, so every index it walks is
// in range by construction.
func build(b Bundle) tree {
	var t tree
	for i := 0; i < config.ItemCount; i++ {
		t.leaves[i] = leafAt(i, b[i])
	}
	t.row1Left = hashPair(t.leaves[0], t.leaves[1])
	t.row1Rite = hashPair(t.leaves[2], t.leaves[3])
	t.row2Left = hashPair(t.row1Left, t.row1Rite)
	t.root = hashPair(t.row2Left, t.leaves[4])
	return t
}

// Root is the commitment the landlord files.
func Root(b Bundle) Hash { return build(b).root }

// ProofLength is the ONLY proof length the contract accepts for an index.
//
// PINNED, NOT BOUNDED, and the contract pins the same numbers. Items 0 through 3
// sit three rows below the root; item 4 is promoted unpaired twice and sits one
// row below it. Accepting any other length would let item 4's short path be
// offered for item 0, or a path be padded while a caller searched for a fold that
// happened to land on the root.
func ProofLength(index int) (int, error) {
	if index < 0 || index >= config.ItemCount {
		return 0, fmt.Errorf("%w: %d", ErrIndexOutOfRange, index)
	}
	if index == config.ItemCount-1 {
		return proofLengthPromoted, nil
	}
	return proofLengthPaired, nil
}

const (
	// proofLengthPaired is the path length for the four items that pair up in
	// the bottom row.
	proofLengthPaired = 3
	// proofLengthPromoted is the path length for the one item promoted unpaired.
	proofLengthPromoted = 1
)

// Proof is the sibling path from one item's leaf to the committed root, bottom
// row first.
func Proof(b Bundle, index int) ([]Hash, error) {
	length, err := ProofLength(index)
	if err != nil {
		return nil, err
	}
	t := build(b)

	if index == config.ItemCount-1 {
		return []Hash{t.row2Left}, nil
	}

	proof := make([]Hash, 0, length)
	// Bottom-row sibling: the other member of this leaf's pair.
	if index%2 == 0 {
		proof = append(proof, t.leaves[index+1])
	} else {
		proof = append(proof, t.leaves[index-1])
	}
	// Row-1 sibling: the other pair's hash.
	if index < 2 {
		proof = append(proof, t.row1Rite)
	} else {
		proof = append(proof, t.row1Left)
	}
	// Row-2 sibling: the promoted leaf, which never moved off row 0.
	proof = append(proof, t.leaves[config.ItemCount-1])
	return proof, nil
}

// Verify folds a proof exactly as the contract does and reports whether it
// reproduces the root.
//
// THE AGENT CHECKS ITS OWN WORK BEFORE SPENDING A TRANSACTION ON IT. This is the
// contract's algorithm, not this package's: build the leaf from the index being
// claimed, fold, compare. A proof that fails here would have reverted on chain,
// and finding that out locally costs nothing.
func Verify(root Hash, index int, itemEvidenceHash Hash, proof []Hash) bool {
	want, err := ProofLength(index)
	if err != nil || len(proof) != want {
		return false
	}
	computed := leafAt(index, itemEvidenceHash)
	for _, sibling := range proof {
		computed = hashPair(computed, sibling)
	}
	return computed == root
}

// Proofs returns the proof for every line item, in schedule order.
//
// TOTAL, BY CONSTRUCTION. A Bundle has exactly config.ItemCount entries, so every
// index this walks is in range and there is nothing here that can fail. That
// matters at the call site: the adjudicator builds all five proofs before it
// spends a single vendor call, and a per-item error return there would be a
// branch no test could reach.
func Proofs(b Bundle) [config.ItemCount][]Hash {
	var out [config.ItemCount][]Hash
	for i := range out {
		// The error is impossible for an index below config.ItemCount, which is
		// the whole range of this loop.
		proof, _ := Proof(b, i)
		out[i] = proof
	}
	return out
}

// HexProof renders a proof as the array literal `cast` wants for a bytes32[].
func HexProof(proof []Hash) string {
	parts := make([]string, 0, len(proof))
	for _, h := range proof {
		parts = append(parts, h.Hex())
	}
	return "[" + strings.Join(parts, ",") + "]"
}
