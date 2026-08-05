// SPDX-License-Identifier: MIT
pragma solidity 0.8.28;

/// @title MerkleBuilder — the off-chain side of the evidence commitment, in Solidity.
/// @notice Builds the five-leaf tree {DepositDispute} verifies against and produces the
///         proof for any one item. Used by the unit suite and by the property harness, and
///         written in plain Solidity with no dependencies so the engine-pure harness can
///         call it.
///
/// @dev    WHY IT BUILDS AND THE CONTRACT ONLY FOLDS. The contract under test never
///         constructs a tree; it takes a leaf and a path and folds. This library does the
///         opposite: it lays out the rows and reads siblings off them. They are different
///         algorithms, so a mistake in one does not generally cancel a mistake in the other
///         — which is the same reason the property harness records what it expects rather
///         than asking the contract. That is a real but PARTIAL independence: both were
///         written by the same hand in the same session, so a shared misconception about
///         the tree's SHAPE would survive both. What catches that is the cross-index
///         rejection property, which does not depend on the shape being the intended one,
///         only on it distinguishing indices.
///
/// @dev    THE SHAPE, fixed forever because {DepositDispute.ITEM_COUNT} is:
///
///           row 0   L0  L1  L2  L3  L4
///           row 1   h(L0,L1)  h(L2,L3)  L4        <- L4 promoted unpaired
///           row 2   h(row1_0,row1_1)    L4        <- promoted again
///           row 3   root = h(row2_0, L4)
///
///         Pair hashing is order-independent, so a proof carries no direction bits.
library MerkleBuilder {
    /// @notice How many line items a dispute has. Mirrors {DepositDispute.ITEM_COUNT}.
    uint256 internal constant ITEM_COUNT = 5;
    /// @notice Proof length for items 0..3, which sit three rows below the root.
    uint256 internal constant PROOF_LENGTH_PAIRED = 3;
    /// @notice Proof length for item 4, promoted twice and sitting one row below it.
    uint256 internal constant PROOF_LENGTH_PROMOTED = 1;

    /// @notice The leaf for one item's evidence. Mirrors {DepositDispute.leafFor}.
    /// @dev The index is bound INTO the hash. That is what makes evidence gathered for one
    ///      line item unusable against another.
    /// @param index The line item.
    /// @param itemEvidenceHash The evidence bundle hash for that item.
    /// @return The leaf.
    function leafFor(uint256 index, bytes32 itemEvidenceHash) internal pure returns (bytes32) {
        return keccak256(abi.encode(index, itemEvidenceHash));
    }

    /// @notice The five leaves for a full evidence set, in item order.
    /// @param evidenceHashes The per-item evidence bundle hashes.
    /// @return The leaves.
    function leaves(bytes32[ITEM_COUNT] memory evidenceHashes)
        internal
        pure
        returns (bytes32[ITEM_COUNT] memory)
    {
        bytes32[ITEM_COUNT] memory out;
        for (uint256 i = 0; i < ITEM_COUNT; i++) {
            out[i] = leafFor(i, evidenceHashes[i]);
        }
        return out;
    }

    /// @notice The merkle root the landlord commits.
    /// @param evidenceHashes The per-item evidence bundle hashes.
    /// @return The root.
    function rootOf(bytes32[ITEM_COUNT] memory evidenceHashes) internal pure returns (bytes32) {
        bytes32[ITEM_COUNT] memory l = leaves(evidenceHashes);
        bytes32 row2Left = _hashPair(_hashPair(l[0], l[1]), _hashPair(l[2], l[3]));
        return _hashPair(row2Left, l[4]);
    }

    /// @notice The proof for one line item.
    /// @dev Length is 3 for items 0..3 and 1 for item 4, which is exactly what
    ///      {DepositDispute} pins per index. A caller that produced a path of any other
    ///      length would be refused before the fold ran.
    /// @param evidenceHashes The per-item evidence bundle hashes.
    /// @param index The item to prove.
    /// @return The sibling path, bottom row first.
    function proofFor(bytes32[ITEM_COUNT] memory evidenceHashes, uint256 index)
        internal
        pure
        returns (bytes32[] memory)
    {
        bytes32[ITEM_COUNT] memory l = leaves(evidenceHashes);
        bytes32 row1Left = _hashPair(l[0], l[1]);
        bytes32 row1Right = _hashPair(l[2], l[3]);
        bytes32 row2Left = _hashPair(row1Left, row1Right);

        if (index == ITEM_COUNT - 1) {
            bytes32[] memory promoted = new bytes32[](PROOF_LENGTH_PROMOTED);
            promoted[0] = row2Left;
            return promoted;
        }

        bytes32[] memory proof = new bytes32[](PROOF_LENGTH_PAIRED);
        // Bottom-row sibling: the other member of this leaf's pair.
        proof[0] = index % 2 == 0 ? l[index + 1] : l[index - 1];
        // Row-1 sibling: the other pair's hash.
        proof[1] = index < 2 ? row1Right : row1Left;
        // Row-2 sibling: the promoted leaf, which never moved off row 0.
        proof[2] = l[4];
        return proof;
    }

    /// @dev Order-independent hash of two nodes. Mirrors {DepositDispute}'s private one.
    function _hashPair(bytes32 a, bytes32 b) private pure returns (bytes32) {
        return a < b ? keccak256(abi.encode(a, b)) : keccak256(abi.encode(b, a));
    }
}
