// SPDX-License-Identifier: MIT
pragma solidity 0.8.28;

import {Test} from "forge-std/Test.sol";

import {DepositDispute} from "../src/DepositDispute.sol";
import {MerkleBuilder} from "./MerkleBuilder.sol";

/// @title DisputeTestBase — one fixture, wired once.
/// @notice Every number the unit suite leans on is named here rather than written at a call
///         site, so a scenario reads as what it is testing instead of as arithmetic. The
///         default schedule is chosen so that all three settlement shapes are reachable
///         from it without a second fixture:
///
///           every item established   0.1+0.2+0.3+0.4+0.9 = 1.9 ether  > deposit  -> THE CAP
///           items 0 and 1 only              0.1+0.2     = 0.3 ether  < deposit  -> A SPLIT
///           items 0 and 4 only              0.1+0.9     = 1.0 ether  = deposit  -> THE EDGE
///           nothing established                           0    ether           -> ALL TENANT
///
///         The exact-equality row is the one that would be missed by a fixture picked for
///         convenience, and it is the boundary `owed < deposit` is written on.
abstract contract DisputeTestBase is Test {
    /// @dev Mirrors {DepositDispute.ITEM_COUNT}. Needed as a compile-time constant because
    ///      it sizes the fixture arrays; the contract's own public constant is asserted
    ///      equal to it in the unit suite so the two cannot drift silently.
    uint256 internal constant ITEM_COUNT = 5;
    /// @dev Mirrors {DepositDispute.ADJUDICATOR_COUNT}, for the same reason.
    uint256 internal constant ADJUDICATOR_COUNT = 3;

    /// @dev The deposit every default fixture funds.
    uint256 internal constant DEPOSIT = 1 ether;

    uint256 internal constant AMOUNT_CARPET = 0.1 ether;
    uint256 internal constant AMOUNT_WALL = 0.2 ether;
    uint256 internal constant AMOUNT_WINDOW = 0.3 ether;
    uint256 internal constant AMOUNT_DOOR = 0.4 ether;
    uint256 internal constant AMOUNT_CLEANING = 0.9 ether;

    /// @dev Item indices, named. `ITEM_CLEANING` is also the promoted leaf, so it is the
    ///      one index whose proof length differs — several tests turn on that.
    uint256 internal constant ITEM_CARPET = 0;
    uint256 internal constant ITEM_WALL = 1;
    uint256 internal constant ITEM_WINDOW = 2;
    uint256 internal constant ITEM_DOOR = 3;
    uint256 internal constant ITEM_CLEANING = 4;

    /// @dev Adjudicator slots, named.
    uint256 internal constant ADJ_ALPHA = 0;
    uint256 internal constant ADJ_BETA = 1;
    uint256 internal constant ADJ_GAMMA = 2;

    /// @dev A well-formed prompt hash. Any non-zero value; the contract only rejects zero.
    bytes32 internal constant PROMPT_HASH = keccak256("seeded-structured-prompt-v1");
    /// @dev A well-formed narrative hash.
    bytes32 internal constant NARRATIVE_HASH = keccak256("model-narrative-v1");
    /// @dev A reason inside {DepositDispute.MAX_REASON_BYTES}.
    string internal constant REASON = "photographs show the damage described";

    /// @dev How much ether the test contract holds, so it can fund many disputes.
    uint256 internal constant TEST_ENDOWMENT = 100 ether;

    address internal landlord;
    address internal tenant;
    address internal adjAlpha;
    address internal adjBeta;
    address internal adjGamma;
    address internal stranger;

    DepositDispute internal dispute;

    function setUp() public virtual {
        landlord = makeAddr("landlord");
        tenant = makeAddr("tenant");
        adjAlpha = makeAddr("adjudicator-alpha");
        adjBeta = makeAddr("adjudicator-beta");
        adjGamma = makeAddr("adjudicator-gamma");
        stranger = makeAddr("stranger");

        vm.deal(address(this), TEST_ENDOWMENT);
        dispute = _deployDefault();
    }

    /*//////////////////////////////////////////////////////////////
                                FIXTURES
    //////////////////////////////////////////////////////////////*/

    function _descHashes() internal pure returns (bytes32[ITEM_COUNT] memory d) {
        d[ITEM_CARPET] = keccak256("carpet: staining beyond fair wear");
        d[ITEM_WALL] = keccak256("wall: unfilled fixings");
        d[ITEM_WINDOW] = keccak256("window: cracked pane, second bedroom");
        d[ITEM_DOOR] = keccak256("door: replacement front door lock");
        d[ITEM_CLEANING] = keccak256("cleaning: end-of-tenancy clean");
    }

    function _amounts() internal pure returns (uint256[ITEM_COUNT] memory a) {
        a[ITEM_CARPET] = AMOUNT_CARPET;
        a[ITEM_WALL] = AMOUNT_WALL;
        a[ITEM_WINDOW] = AMOUNT_WINDOW;
        a[ITEM_DOOR] = AMOUNT_DOOR;
        a[ITEM_CLEANING] = AMOUNT_CLEANING;
    }

    function _evidence() internal pure returns (bytes32[ITEM_COUNT] memory e) {
        e[ITEM_CARPET] = keccak256("evidence-bundle:carpet");
        e[ITEM_WALL] = keccak256("evidence-bundle:wall");
        e[ITEM_WINDOW] = keccak256("evidence-bundle:window");
        e[ITEM_DOOR] = keccak256("evidence-bundle:door");
        e[ITEM_CLEANING] = keccak256("evidence-bundle:cleaning");
    }

    function _signers() internal view returns (address[ADJUDICATOR_COUNT] memory s) {
        s[ADJ_ALPHA] = adjAlpha;
        s[ADJ_BETA] = adjBeta;
        s[ADJ_GAMMA] = adjGamma;
    }

    function _modelIds() internal pure returns (string[ADJUDICATOR_COUNT] memory m) {
        m[ADJ_ALPHA] = "model-alpha-v1";
        m[ADJ_BETA] = "model-beta-v1";
        m[ADJ_GAMMA] = "model-gamma-v1";
    }

    function _signerAt(uint256 slot) internal view returns (address) {
        return _signers()[slot];
    }

    /*//////////////////////////////////////////////////////////////
                                 DRIVING
    //////////////////////////////////////////////////////////////*/

    function _deployDefault() internal returns (DepositDispute) {
        return _deploy(landlord, tenant, DEPOSIT, _amounts());
    }

    function _deploy(address ll, address tt, uint256 depositWei, uint256[ITEM_COUNT] memory amounts)
        internal
        returns (DepositDispute)
    {
        return new DepositDispute{value: depositWei}(ll, tt, _descHashes(), amounts, _signers(), _modelIds());
    }

    function _file(DepositDispute d) internal {
        vm.prank(d.LANDLORD());
        d.fileClaim(MerkleBuilder.rootOf(_evidence()));
    }

    /// @dev One well-formed verdict, with the proof this index actually requires.
    function _vote(DepositDispute d, uint256 slot, uint256 index, DepositDispute.ItemFinding finding)
        internal
    {
        bytes32[ITEM_COUNT] memory ev = _evidence();
        vm.prank(_signerAt(slot));
        d.submitVerdict(
            index, finding, ev[index], MerkleBuilder.proofFor(ev, index), PROMPT_HASH, NARRATIVE_HASH, REASON
        );
    }

    /// @dev Two agreeing verdicts, which is what freezes an item.
    function _agree(DepositDispute d, uint256 index, DepositDispute.ItemFinding finding) internal {
        _vote(d, ADJ_ALPHA, index, finding);
        _vote(d, ADJ_BETA, index, finding);
    }

    /// @dev Freezes every item on `finding` except those named in `established`, which are
    ///      frozen on `Established`. Used to reach a chosen settlement shape in one line.
    function _adjudicateAll(DepositDispute d, bool[ITEM_COUNT] memory established) internal {
        for (uint256 i = 0; i < ITEM_COUNT; i++) {
            _agree(
                d,
                i,
                established[i]
                    ? DepositDispute.ItemFinding.Established
                    : DepositDispute.ItemFinding.NotEstablished
            );
        }
    }

    function _noneEstablished() internal pure returns (bool[ITEM_COUNT] memory e) {
        return e;
    }

    function _allEstablished() internal pure returns (bool[ITEM_COUNT] memory e) {
        for (uint256 i = 0; i < ITEM_COUNT; i++) {
            e[i] = true;
        }
    }

    /// @dev The split shape: carpet and wall only, 0.3 ether of a 1 ether deposit.
    function _splitEstablished() internal pure returns (bool[ITEM_COUNT] memory e) {
        e[ITEM_CARPET] = true;
        e[ITEM_WALL] = true;
    }

    /// @dev The exact-equality shape: carpet and cleaning, 1.0 ether of a 1 ether deposit.
    function _exactlyDepositEstablished() internal pure returns (bool[ITEM_COUNT] memory e) {
        e[ITEM_CARPET] = true;
        e[ITEM_CLEANING] = true;
    }

    /// @dev Files, adjudicates every item, and settles.
    function _runToSettlement(DepositDispute d, bool[ITEM_COUNT] memory established) internal {
        _file(d);
        _adjudicateAll(d, established);
        d.settle();
    }

    /// @dev Accepts ether so this contract can hold the endowment it funds disputes from.
    receive() external payable {}
}
