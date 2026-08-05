// SPDX-License-Identifier: MIT
pragma solidity 0.8.28;

import {PigfoxProperties} from "pipeline/PigfoxProperties.sol";

import {DepositDispute} from "../src/DepositDispute.sol";
import {Actor} from "./Actor.sol";
import {MerkleBuilder} from "./MerkleBuilder.sol";

/// @title Properties — the deposit-dispute system's invariants, in one place.
/// @notice A single property harness driven by all three engines: Foundry's invariant runner
///         (via `InvariantsTest`), Echidna and Medusa. It opens disputes, drives them
///         through filing, adjudication, settlement and withdrawal, and exposes the
///         `echidna_*` predicates all three evaluate.
///
/// @dev    ENGINE-PURE. No forge-std, no cheatcodes, no `vm`. A harness that reached for a
///         cheatcode would work under Foundry and be undrivable by the fuzzers, and the
///         whole value of one property file is that the same predicates answer to all three.
///
/// @dev    THE BASE IS CONSUMED, NOT COPIED. {PigfoxProperties} comes from
///         lib/solidity-pipeline, vendored as a submodule at a commit this repo pins, so the
///         declaration contract cannot drift from the gate scripts that read it. This repo
///         briefly carried its own re-implementation of that file; it was DELETED when the
///         pipeline was adopted rather than reconciled, because two copies of a contract
///         whose entire job is to be a single source of truth is the failure it exists to
///         prevent.
///
/// @dev    WHY THERE ARE ACTOR CONTRACTS. `fileClaim` accepts only the landlord,
///         `submitVerdict` only a registered adjudicator, and `withdraw` pays only whoever
///         calls it. Neither fuzzer has cheatcodes, so without contracts at those addresses
///         a campaign could file no claim, cast no verdict and take no payout, and every
///         property about adjudication and custody would hold vacuously over a system
///         nothing had driven.
///
/// @dev    WHY ONE DISPUTE IS ONE DEPLOYMENT. {DepositDispute} fixes its parties, deposit,
///         schedule and adjudicators at construction and has no owner and no upgrade path,
///         so a second dispute is a second contract. The harness therefore holds a growing
///         list of them and rotates. That is not incidental to the properties: `conservation`
///         and `solvency` are per-dispute statements, and a harness that only ever drove one
///         instance would never see a later dispute corrupt an earlier one's record.
///
/// @dev    WHY THE CHECKS ARE EAGER. A predicate that walks every dispute is quadratic over
///         a campaign — the fuzzer evaluates predicates after every single call — and it
///         spends the budget re-reading old state instead of reaching new state. So each
///         entry point verifies its own effect IMMEDIATELY against what the harness
///         independently expected, trips a sticky flag on disagreement, and {_afterCall}
///         additionally re-verifies ONE older dispute round-robin. Predicates then read the
///         flags in O(1), and the comparison happens at the moment of divergence, on the
///         input that caused it.
///
/// @dev    WHY {settleWithAPartialSplit} AND {settleAtTheCap} EXIST. PF-S134 shipped an
///         invariant that stayed green under mutation precisely because its campaign never
///         reached the state under test, and the lesson taken from it is that a property
///         over an unreached state is not a weaker proof, it is no proof.
///
///         The showpiece state here is a PARTIAL SPLIT: both parties credited a non-zero
///         amount out of one deposit. Reaching it by chance needs a sequence to open a
///         dispute, file it, land ten verdicts across five items in agreeing pairs, and have
///         the established subset sum to strictly between zero and the deposit — all before
///         the sequence ends. Most sequences never do. The second state, the CAP, needs the
///         established subset to sum above the deposit instead. Both are driven end to end
///         by an entry point, and {ghostPartialSplitsView} and {ghostCapHitsView} are the
///         canaries that say how often each was actually reached. A run that never produced
///         a partial split is a failed run, not a green one, and `InvariantsTest` fails on
///         it rather than reporting green.
contract Properties is PigfoxProperties {
    /*//////////////////////////////////////////////////////////////
                                CONSTANTS
    //////////////////////////////////////////////////////////////*/

    /// @dev Mirrors {DepositDispute.ITEM_COUNT}. A compile-time constant here because it
    ///      sizes arrays; asserted equal to the contract's own in the Foundry suite.
    uint256 internal constant ITEM_COUNT = 5;
    /// @dev How many interchangeable party actors the harness rotates through. Four, so the
    ///      same address is the landlord of one dispute and the tenant of another — a fixed
    ///      pair can never catch a bug that only shows up when roles overlap across disputes.
    uint256 internal constant NUM_PARTIES = 4;
    /// @dev How many adjudicator actors. Three, matching the contract's fixed slot count.
    uint256 internal constant NUM_ADJUDICATORS = 3;

    /// @dev The threshold this harness INDEPENDENTLY expects.
    ///
    ///      A LITERAL, not `DISPUTE.QUORUM()`, for exactly the same reason
    ///      {pigfoxPropertyCount} is a literal. Reading the threshold from the contract would
    ///      make this harness agree with any threshold that contract happened to hold — a
    ///      1-of-3 rule and a 3-of-3 rule would both look correct, and every property about
    ///      the threshold would be unfalsifiable. Change the contract's QUORUM and this
    ///      number together, in one commit, or the harness fails.
    uint256 internal constant EXPECTED_QUORUM = 2;

    /// @dev The smallest deposit the harness will open a dispute with. Above ten wei because
    ///      {settleWithAPartialSplit} divides the deposit by ten to build a schedule whose
    ///      items are individually non-zero.
    uint256 internal constant MIN_DEPOSIT = 1 gwei;
    /// @dev The span above {MIN_DEPOSIT} a seed may reach.
    uint256 internal constant DEPOSIT_SPAN = 1 ether;
    /// @dev Divisor building the partial-split driver's schedule: five items of a tenth of
    ///      the deposit each sum to half of it, so any non-empty proper subset lands strictly
    ///      inside the split band.
    uint256 internal constant SPLIT_ITEM_DIVISOR = 10;
    /// @dev How many items the partial-split driver establishes. Two tenths of the deposit
    ///      to the landlord, eight to the tenant: both non-zero, which is the whole point.
    uint256 internal constant SPLIT_ESTABLISHED_ITEMS = 2;

    /// @dev A well-formed prompt hash seed. The contract only rejects zero.
    bytes32 internal constant PROMPT_SEED = keccak256("harness-prompt");
    /// @dev A well-formed narrative hash seed.
    bytes32 internal constant NARRATIVE_SEED = keccak256("harness-narrative");
    /// @dev A reason inside {DepositDispute.MAX_REASON_BYTES}.
    string internal constant REASON = "bounded reason";

    /*//////////////////////////////////////////////////////////////
                                  STATE
    //////////////////////////////////////////////////////////////*/

    Actor[NUM_PARTIES] internal partyActors;
    Actor[NUM_ADJUDICATORS] internal adjActors;

    /// @notice Every dispute this harness has opened, in order.
    DepositDispute[] public disputes;

    // --- what the harness independently believes -----------------------------
    // Recorded at the moment of the write, and compared against the contracts' own records
    // later. Asking a contract what it should say would let a bug agree with itself.

    mapping(uint256 disputeId => address landlord) internal expectedLandlord;
    mapping(uint256 disputeId => address tenant) internal expectedTenant;
    mapping(uint256 disputeId => uint256 deposit) internal expectedDeposit;
    mapping(uint256 disputeId => uint256[ITEM_COUNT] amounts) internal expectedAmounts;
    mapping(uint256 disputeId => bytes32[ITEM_COUNT] evidence) internal expectedEvidence;
    mapping(uint256 disputeId => bool filed) internal expectedFiled;
    mapping(uint256 disputeId => bool settled) internal expectedSettled;
    mapping(uint256 disputeId => uint256 award) internal expectedLandlordAward;
    mapping(uint256 disputeId => uint256 award) internal expectedTenantAward;

    mapping(uint256 disputeId => mapping(uint256 item => mapping(DepositDispute.ItemFinding => uint256)))
        internal expectedTally;
    mapping(uint256 disputeId => mapping(uint256 item => bool frozen)) internal expectedFrozen;
    mapping(uint256 disputeId => mapping(uint256 item => DepositDispute.ItemFinding)) internal
        expectedFinding;

    // --- ghost counters ------------------------------------------------------
    // These prove a run was not inert. A campaign that never settled anything satisfies most
    // of the predicates below trivially and reports the same green as one that reached every
    // state.

    uint256 internal ghostDisputes;
    uint256 internal ghostFilings;
    uint256 internal ghostVerdicts;
    uint256 internal ghostItemsFrozen;
    uint256 internal ghostSettlements;
    uint256 internal ghostPartialSplits;
    uint256 internal ghostCapHits;
    uint256 internal ghostWithdrawals;
    uint256 internal ghostRejectedCrossItem;
    uint256 internal ghostRejectedDoubleVotes;

    uint256 internal disputeCursor;

    // --- sticky flags --------------------------------------------------------

    /// @notice Set if a settled dispute's two awards ever failed to sum to its deposit.
    bool public conservationViolated;
    /// @notice Set if a landlord was ever credited more than the deposit.
    bool public capViolated;
    /// @notice Set if an item the adjudicators did not establish ever contributed to what
    ///         the landlord was owed.
    bool public burdenViolated;
    /// @notice Set if any address outside a dispute's own two parties ever held a credit.
    bool public thirdPartyCredited;
    /// @notice Set if the same adjudicator ever recorded two verdicts on one item.
    bool public doubleVoteAccepted;
    /// @notice Set if evidence proving membership at one index was ever accepted against
    ///         another index.
    bool public crossItemEvidenceAccepted;
    /// @notice Set if a dispute's balance ever fell below what it owed.
    bool public insolvent;

    /// @dev PAYABLE, and it has to be. Echidna's `balanceContract` endows the harness by
    ///      sending value WITH the creation transaction, so a non-payable constructor makes
    ///      the deployment revert and the whole campaign dies with "Deploying the contract
    ///      failed" — which reads like a harness bug rather than a config mismatch. Foundry
    ///      and Medusa both deploy with zero value and are unaffected.
    constructor() payable {
        for (uint256 i = 0; i < NUM_PARTIES; i++) {
            partyActors[i] = new Actor();
        }
        for (uint256 i = 0; i < NUM_ADJUDICATORS; i++) {
            adjActors[i] = new Actor();
        }
    }

    /// @dev Nothing in this system pays the harness — every payout goes to a party actor —
    ///      but a campaign that could not receive would fail loudly on an unrelated path.
    receive() external payable {}

    /*//////////////////////////////////////////////////////////////
                              DECLARATION
    //////////////////////////////////////////////////////////////*/

    /// @inheritdoc PigfoxProperties
    function pigfoxPropertyCount() public pure override returns (uint256) {
        return 7;
    }

    /// @inheritdoc PigfoxProperties
    function pigfoxHarnessDescription() public pure override returns (string memory) {
        return "the deposit is conserved and never over-credited, an unestablished item never pays, "
            "only the two parties hold credit, one vote per adjudicator per item, evidence binds to "
            "the item it was filed against, and every dispute stays solvent";
    }

    /*//////////////////////////////////////////////////////////////
                                ACTIONS
    //////////////////////////////////////////////////////////////*/

    /// @notice Open a dispute between two of the party actors, funded from the harness.
    /// @dev The schedule is drawn from the seed and may or may not exceed the deposit, so
    ///      both the split band and the cap are reachable without a driver — the drivers
    ///      exist to make them reliable, not to make them possible.
    function openDispute(uint256 landlordSeed, uint256 tenantSeed, uint256 depositSeed, uint256 amountSeed)
        public
    {
        uint256 deposit = MIN_DEPOSIT + (depositSeed % DEPOSIT_SPAN);
        if (address(this).balance < deposit) return;

        (address landlord, address tenant) = _twoDistinctParties(landlordSeed, tenantSeed);

        uint256[ITEM_COUNT] memory amounts;
        for (uint256 i = 0; i < ITEM_COUNT; i++) {
            amounts[i] = 1 + (uint256(keccak256(abi.encode(amountSeed, i))) % deposit);
        }

        _open(landlord, tenant, deposit, amounts);
    }

    /// @notice The landlord of an unfiled dispute commits its evidence tree.
    function fileClaim(uint256 disputeSeed, uint256 evidenceSeed) public {
        uint256 id = _findDispute(disputeSeed, Phase.Unfiled);
        if (id == type(uint256).max) return;

        _fileFor(id, evidenceSeed);
        _afterCall();
    }

    /// @notice A registered adjudicator submits a well-formed verdict on one item.
    function submitVerdict(uint256 disputeSeed, uint256 itemSeed, uint256 adjSeed, uint256 findingSeed)
        public
    {
        uint256 id = _findDispute(disputeSeed, Phase.Filed);
        if (id == type(uint256).max) return;

        _voteFor(
            id,
            itemSeed % ITEM_COUNT,
            adjSeed % NUM_ADJUDICATORS,
            findingSeed % 2 == 0
                ? DepositDispute.ItemFinding.NotEstablished
                : DepositDispute.ItemFinding.Established
        );
        _afterCall();
    }

    /// @notice An adjudicator offers item `source`'s evidence and proof against item
    ///         `target`. The contract must refuse it every single time.
    /// @dev    THE EVIDENCE-BINDING PROPERTY STATED AS AN ACTION rather than as a claim. If
    ///         the call ever succeeds, `crossItemEvidenceAccepted` trips and the
    ///         counterexample names the call that did it. A predicate that only asserted a
    ///         flag stayed false would prove nothing unless the attempt was actually made,
    ///         so the attempts are counted too.
    function submitVerdictForWrongItem(uint256 disputeSeed, uint256 itemSeed, uint256 adjSeed) public {
        uint256 id = _findDispute(disputeSeed, Phase.Filed);
        if (id == type(uint256).max) return;

        uint256 target = itemSeed % ITEM_COUNT;
        uint256 source = (target + 1 + (adjSeed % (ITEM_COUNT - 1))) % ITEM_COUNT;

        bytes32[ITEM_COUNT] memory ev = expectedEvidence[id];
        uint256 slot = adjSeed % NUM_ADJUDICATORS;

        (bool ok,) = adjActors[slot]
        .call(
            address(disputes[id]),
            abi.encodeCall(
                DepositDispute.submitVerdict,
                (
                    target,
                    DepositDispute.ItemFinding.Established,
                    ev[source],
                    MerkleBuilder.proofFor(ev, source),
                    PROMPT_SEED,
                    NARRATIVE_SEED,
                    REASON
                )
            )
        );

        if (ok) crossItemEvidenceAccepted = true;
        else ghostRejectedCrossItem++;

        _afterCall();
    }

    /// @notice An adjudicator that has already voted on an item votes on it again. The
    ///         contract must refuse it every single time.
    /// @dev    The one-vote property stated as an action, for the same reason as above.
    function voteTwice(uint256 disputeSeed, uint256 itemSeed, uint256 adjSeed) public {
        uint256 id = _findDispute(disputeSeed, Phase.Filed);
        if (id == type(uint256).max) return;

        uint256 item = itemSeed % ITEM_COUNT;
        uint256 slot = adjSeed % NUM_ADJUDICATORS;
        DepositDispute d = disputes[id];

        // Make sure there is a first vote to duplicate, then try to duplicate it.
        if (!d.hasVoted(item, address(adjActors[slot]))) {
            _voteFor(id, item, slot, DepositDispute.ItemFinding.Established);
        }
        if (!d.hasVoted(item, address(adjActors[slot]))) return;

        uint256 countBefore = d.verdictCount(item);
        bytes32[ITEM_COUNT] memory ev = expectedEvidence[id];

        (bool ok,) = adjActors[slot]
        .call(
            address(d),
            abi.encodeCall(
                DepositDispute.submitVerdict,
                (
                    item,
                    DepositDispute.ItemFinding.NotEstablished,
                    ev[item],
                    MerkleBuilder.proofFor(ev, item),
                    PROMPT_SEED,
                    NARRATIVE_SEED,
                    REASON
                )
            )
        );

        if (ok || d.verdictCount(item) != countBefore) doubleVoteAccepted = true;
        else ghostRejectedDoubleVotes++;

        _afterCall();
    }

    /// @notice Attempt to settle a dispute. Succeeds only once every item has frozen.
    function settle(uint256 disputeSeed) public {
        uint256 id = _findDispute(disputeSeed, Phase.Filed);
        if (id == type(uint256).max) return;

        _attemptSettle(id);
        _afterCall();
    }

    /// @notice A party takes whatever the settlement credited it.
    function withdraw(uint256 disputeSeed, uint256 whoSeed) public {
        uint256 id = _findDispute(disputeSeed, Phase.Settled);
        if (id == type(uint256).max) return;

        DepositDispute d = disputes[id];
        address who = whoSeed % 2 == 0 ? expectedLandlord[id] : expectedTenant[id];
        uint256 owedBefore = d.pendingWithdrawals(who);
        if (owedBefore == 0) return;

        uint256 index = _partyIndex(who);
        uint256 balanceBefore = who.balance;

        (bool ok,) = partyActors[index].call(address(d), abi.encodeCall(DepositDispute.withdraw, ()));

        if (!ok) {
            // Every party actor accepts ether and the dispute is solvent by invariant, so
            // nothing about this call could legitimately fail.
            conservationViolated = true;
        } else {
            if (who.balance != balanceBefore + owedBefore) conservationViolated = true;
            if (d.pendingWithdrawals(who) != 0) conservationViolated = true;
            ghostWithdrawals++;
        }

        _afterCall();
    }

    /*//////////////////////////////////////////////////////////////
                                DRIVERS
    //////////////////////////////////////////////////////////////*/

    /// @notice Drive a dispute all the way to A PARTIAL SPLIT — both parties credited a
    ///         non-zero amount out of one deposit.
    /// @dev    THE SHOWPIECE, driven rather than hoped for. The schedule is five items of a
    ///         tenth of the deposit each, and exactly two are established, so the landlord is
    ///         owed two tenths and the tenant keeps eight. Both non-zero by construction, for
    ///         every deposit the harness will open with.
    ///
    ///         Every write here is accounted for in the ghost totals exactly as a
    ///         fuzzer-driven one would be, so nothing is hidden from a predicate.
    function settleWithAPartialSplit(uint256 seed) public {
        uint256 deposit = MIN_DEPOSIT + (seed % DEPOSIT_SPAN);
        if (address(this).balance < deposit) return;

        uint256[ITEM_COUNT] memory amounts;
        for (uint256 i = 0; i < ITEM_COUNT; i++) {
            amounts[i] = deposit / SPLIT_ITEM_DIVISOR;
        }

        (address landlord, address tenant) = _twoDistinctParties(seed, seed >> 8);
        uint256 id = _open(landlord, tenant, deposit, amounts);
        if (id == type(uint256).max) return;

        _fileFor(id, seed);
        if (!expectedFiled[id]) return;

        for (uint256 i = 0; i < ITEM_COUNT; i++) {
            DepositDispute.ItemFinding finding = i < SPLIT_ESTABLISHED_ITEMS
                ? DepositDispute.ItemFinding.Established
                : DepositDispute.ItemFinding.NotEstablished;
            _voteFor(id, i, 0, finding);
            _voteFor(id, i, 1, finding);
        }

        uint256 settledBefore = ghostSettlements;
        _attemptSettle(id);

        if (ghostSettlements > settledBefore) {
            DepositDispute d = disputes[id];
            if (d.landlordAward() > 0 && d.tenantAward() > 0) {
                ghostPartialSplits++;
            } else {
                // The driver's whole job is to reach a two-sided credit. If it did not, the
                // schedule arithmetic it relies on has changed underneath it, and the
                // canary must not report a state it did not reach.
                conservationViolated = true;
            }
        }

        _afterCall();
    }

    /// @notice Drive a dispute all the way to THE CAP — established items summing to more
    ///         than the deposit, the landlord credited the deposit and nothing more.
    /// @dev    The second canary. The schedule is five items of one deposit each, and all
    ///         five are established, so the claim is five times the deposit and the cap must
    ///         bite. The excess is not recorded as a debt anywhere; see the contract notice.
    function settleAtTheCap(uint256 seed) public {
        uint256 deposit = MIN_DEPOSIT + (seed % DEPOSIT_SPAN);
        if (address(this).balance < deposit) return;

        uint256[ITEM_COUNT] memory amounts;
        for (uint256 i = 0; i < ITEM_COUNT; i++) {
            amounts[i] = deposit;
        }

        (address landlord, address tenant) = _twoDistinctParties(seed >> 16, seed >> 24);
        uint256 id = _open(landlord, tenant, deposit, amounts);
        if (id == type(uint256).max) return;

        _fileFor(id, seed);
        if (!expectedFiled[id]) return;

        for (uint256 i = 0; i < ITEM_COUNT; i++) {
            _voteFor(id, i, 0, DepositDispute.ItemFinding.Established);
            _voteFor(id, i, 1, DepositDispute.ItemFinding.Established);
        }

        uint256 settledBefore = ghostSettlements;
        _attemptSettle(id);

        if (ghostSettlements > settledBefore) {
            DepositDispute d = disputes[id];
            if (d.landlordAward() == deposit && d.tenantAward() == 0) {
                ghostCapHits++;
            } else {
                capViolated = true;
            }
        }

        _afterCall();
    }

    /*//////////////////////////////////////////////////////////////
                              PROPERTIES
    //////////////////////////////////////////////////////////////*/

    /// @notice (a) In any terminal state the two awards sum to exactly the deposit. Not a
    ///         wei is created and not a wei is lost.
    function echidna_deposit_is_conserved() public view returns (bool) {
        return !conservationViolated;
    }

    /// @notice (b) The landlord is never credited more than the deposit, whatever the
    ///         schedule claimed.
    function echidna_landlord_credit_never_exceeds_the_deposit() public view returns (bool) {
        return !capViolated;
    }

    /// @notice (c) An item fewer than two adjudicators found Established never contributes
    ///         to what the landlord is owed. The burden sits on the landlord.
    function echidna_unestablished_items_never_pay() public view returns (bool) {
        return !burdenViolated;
    }

    /// @notice (d) No address outside a dispute's own landlord and tenant ever holds a
    ///         credit against it.
    function echidna_only_the_parties_hold_credit() public view returns (bool) {
        return !thirdPartyCredited;
    }

    /// @notice (e) No adjudicator ever records two verdicts on the same item.
    function echidna_one_vote_per_adjudicator_per_item() public view returns (bool) {
        return !doubleVoteAccepted;
    }

    /// @notice (f) Evidence proving membership at one index is never accepted against
    ///         another. The carpet's photographs cannot be spent on the wall.
    function echidna_evidence_binds_to_its_own_item() public view returns (bool) {
        return !crossItemEvidenceAccepted;
    }

    /// @notice (g) Every dispute always holds at least what it owes.
    /// @dev Stated as `>=` rather than `==` because a chain can force ether into a contract
    ///      by means no contract can refuse. An equality here would report a forced send as
    ///      a violation, which is a false alarm rather than a finding.
    function echidna_every_dispute_is_solvent() public view returns (bool) {
        return !insolvent;
    }

    /*//////////////////////////////////////////////////////////////
                          NON-INERTNESS VIEWS
    //////////////////////////////////////////////////////////////*/

    /// @notice How many disputes the run opened.
    function ghostDisputesView() external view returns (uint256) {
        return ghostDisputes;
    }

    /// @notice How many claims the run filed.
    function ghostFilingsView() external view returns (uint256) {
        return ghostFilings;
    }

    /// @notice How many verdicts the run recorded.
    function ghostVerdictsView() external view returns (uint256) {
        return ghostVerdicts;
    }

    /// @notice How many line items the run froze on 2-of-3 agreement.
    function ghostItemsFrozenView() external view returns (uint256) {
        return ghostItemsFrozen;
    }

    /// @notice How many disputes the run settled.
    function ghostSettlementsView() external view returns (uint256) {
        return ghostSettlements;
    }

    /// @notice How many of those settlements were A PARTIAL SPLIT.
    /// @dev THE CANARY. This is the state the conservation and cap properties are most about,
    ///      and a campaign that never reached it would satisfy them over all-or-nothing
    ///      outcomes alone — which is exactly what the estate's two existing contracts
    ///      already do, and therefore exactly what this repo would be proving nothing new
    ///      about. A zero here means the run proved nothing about splitting, and
    ///      `InvariantsTest` fails on it rather than reporting green.
    function ghostPartialSplitsView() external view returns (uint256) {
        return ghostPartialSplits;
    }

    /// @notice How many settlements hit THE CAP, with the claim exceeding the deposit.
    /// @dev The second canary. The cap property is unfalsifiable over a run in which no
    ///      schedule ever exceeded its deposit.
    function ghostCapHitsView() external view returns (uint256) {
        return ghostCapHits;
    }

    /// @notice How many credited awards were actually withdrawn.
    function ghostWithdrawalsView() external view returns (uint256) {
        return ghostWithdrawals;
    }

    /// @notice How many cross-item evidence verdicts were offered and refused.
    /// @dev The other half of the binding property. `crossItemEvidenceAccepted` staying false
    ///      proves nothing if no such verdict was ever attempted.
    function ghostRejectedCrossItemView() external view returns (uint256) {
        return ghostRejectedCrossItem;
    }

    /// @notice How many second votes were offered and refused.
    /// @dev The other half of the one-vote property, for the same reason.
    function ghostRejectedDoubleVotesView() external view returns (uint256) {
        return ghostRejectedDoubleVotes;
    }

    /// @notice How many disputes the harness is holding.
    function disputeCount() external view returns (uint256) {
        return disputes.length;
    }

    /*//////////////////////////////////////////////////////////////
                                HELPERS
    //////////////////////////////////////////////////////////////*/

    /// @dev Which stage of its life a dispute is in, for {_findDispute}.
    enum Phase {
        Unfiled,
        Filed,
        Settled
    }

    /// @dev Deploys a dispute, records what the harness believes about it, and checks the
    ///      contract agrees on the parts it can be asked about immediately.
    function _open(address landlord, address tenant, uint256 deposit, uint256[ITEM_COUNT] memory amounts)
        internal
        returns (uint256)
    {
        bytes32[ITEM_COUNT] memory descHashes;
        for (uint256 i = 0; i < ITEM_COUNT; i++) {
            if (amounts[i] == 0) return type(uint256).max;
            descHashes[i] = keccak256(abi.encode("item", disputes.length, i));
        }

        address[NUM_ADJUDICATORS] memory signers;
        for (uint256 i = 0; i < NUM_ADJUDICATORS; i++) {
            signers[i] = address(adjActors[i]);
        }

        DepositDispute d = new DepositDispute{
            value: deposit
        }(
            landlord,
            tenant,
            descHashes,
            amounts,
            signers,
            ["harness-model-alpha", "harness-model-beta", "harness-model-gamma"]
        );

        uint256 id = disputes.length;
        disputes.push(d);

        expectedLandlord[id] = landlord;
        expectedTenant[id] = tenant;
        expectedDeposit[id] = deposit;
        expectedAmounts[id] = amounts;
        ghostDisputes++;

        if (d.DEPOSIT_WEI() != deposit) conservationViolated = true;
        if (address(d).balance != deposit) insolvent = true;

        _afterCall();
        return id;
    }

    /// @dev Files as the dispute's own landlord and records the commitment.
    function _fileFor(uint256 id, uint256 evidenceSeed) internal {
        bytes32[ITEM_COUNT] memory ev;
        for (uint256 i = 0; i < ITEM_COUNT; i++) {
            ev[i] = keccak256(abi.encode("evidence", id, i, evidenceSeed));
        }

        uint256 index = _partyIndex(expectedLandlord[id]);
        (bool ok,) = partyActors[index]
        .call(address(disputes[id]), abi.encodeCall(DepositDispute.fileClaim, (MerkleBuilder.rootOf(ev))));
        if (!ok) return;

        expectedEvidence[id] = ev;
        expectedFiled[id] = true;
        ghostFilings++;

        if (disputes[id].evidenceRoot() != MerkleBuilder.rootOf(ev)) crossItemEvidenceAccepted = true;
    }

    /// @dev Casts a well-formed verdict and checks what the contract recorded against what
    ///      the harness independently expected.
    function _voteFor(uint256 id, uint256 item, uint256 slot, DepositDispute.ItemFinding finding) internal {
        DepositDispute d = disputes[id];
        bytes32[ITEM_COUNT] memory ev = expectedEvidence[id];
        uint256 countBefore = d.verdictCount(item);

        (bool ok,) = adjActors[slot]
        .call(
            address(d),
            abi.encodeCall(
                DepositDispute.submitVerdict,
                (
                    item,
                    finding,
                    ev[item],
                    MerkleBuilder.proofFor(ev, item),
                    PROMPT_SEED,
                    NARRATIVE_SEED,
                    REASON
                )
            )
        );
        if (!ok) return;

        ghostVerdicts++;

        // The harness's own tally, kept independently of the contract's.
        uint256 agreeing = expectedTally[id][item][finding] + 1;
        expectedTally[id][item][finding] = agreeing;
        if (!expectedFrozen[id][item] && agreeing >= EXPECTED_QUORUM) {
            expectedFrozen[id][item] = true;
            expectedFinding[id][item] = finding;
            ghostItemsFrozen++;
        }

        if (d.verdictCount(item) != countBefore + 1) doubleVoteAccepted = true;

        DepositDispute.Verdict memory v = d.verdictAt(item, countBefore);
        if (v.signer != address(adjActors[slot])) doubleVoteAccepted = true;
        if (v.finding != finding) burdenViolated = true;
        if (v.itemEvidenceHash != ev[item]) crossItemEvidenceAccepted = true;
        if (v.modelIdHash != d.modelIdHashAt(slot)) burdenViolated = true;

        (bool frozen,,,) = d.itemStatus(item);
        if (frozen != expectedFrozen[id][item]) burdenViolated = true;
    }

    /// @dev Attempts a settlement and compares the split against the one the harness computed
    ///      for itself, from its own tally and its own copy of the schedule. Deliberately NOT
    ///      read back from the contract — asking the contract under test what it should have
    ///      concluded would let broken arithmetic agree with itself.
    function _attemptSettle(uint256 id) internal {
        DepositDispute d = disputes[id];

        uint256 expectedOwed = 0;
        uint256 frozenCount = 0;
        uint256[ITEM_COUNT] memory amounts = expectedAmounts[id];
        for (uint256 i = 0; i < ITEM_COUNT; i++) {
            if (!expectedFrozen[id][i]) continue;
            frozenCount++;
            if (expectedFinding[id][i] == DepositDispute.ItemFinding.Established) {
                expectedOwed += amounts[i];
            }
        }

        (bool ok,) = partyActors[0].call(address(d), abi.encodeCall(DepositDispute.settle, ()));

        if (!ok) {
            // A refusal is correct only when some item has not yet frozen.
            if (frozenCount == ITEM_COUNT && !expectedSettled[id]) burdenViolated = true;
            return;
        }
        if (frozenCount != ITEM_COUNT) {
            // It settled with an item still open, which is the threshold not being enforced.
            burdenViolated = true;
            return;
        }

        uint256 deposit = expectedDeposit[id];
        uint256 expectedToLandlord = expectedOwed < deposit ? expectedOwed : deposit;
        uint256 expectedToTenant = deposit - expectedToLandlord;

        expectedSettled[id] = true;
        expectedLandlordAward[id] = expectedToLandlord;
        expectedTenantAward[id] = expectedToTenant;
        ghostSettlements++;

        if (d.landlordAward() != expectedToLandlord) burdenViolated = true;
        if (d.tenantAward() != expectedToTenant) conservationViolated = true;

        // THE BURDEN, checked directly rather than inferred: every item the contract says was
        // Established must have had at least EXPECTED_QUORUM adjudicators say so, counted by
        // the harness.
        for (uint256 i = 0; i < ITEM_COUNT; i++) {
            if (d.findingOf(i) != DepositDispute.ItemFinding.Established) continue;
            if (expectedTally[id][i][DepositDispute.ItemFinding.Established] < EXPECTED_QUORUM) {
                burdenViolated = true;
            }
        }
    }

    /// @dev Two distinct party actors. The landlord of one dispute is freely the tenant of
    ///      another; only the two roles within a single dispute must differ.
    function _twoDistinctParties(uint256 aSeed, uint256 bSeed) internal view returns (address, address) {
        uint256 a = aSeed % NUM_PARTIES;
        uint256 b = bSeed % NUM_PARTIES;
        if (a == b) b = (b + 1) % NUM_PARTIES;
        return (address(partyActors[a]), address(partyActors[b]));
    }

    /// @dev Index of `who` among the party actors. Only ever called with an address the
    ///      harness itself recorded as a party, so the fallback is unreachable in practice
    ///      and returns zero rather than reverting — a revert here would abort a fuzzer
    ///      transaction and discard an interesting state.
    function _partyIndex(address who) internal view returns (uint256) {
        for (uint256 i = 0; i < NUM_PARTIES; i++) {
            if (address(partyActors[i]) == who) return i;
        }
        return 0;
    }

    /// @dev First dispute at or after `seed` in the requested phase, scanning a bounded
    ///      window. Bounded rather than exhaustive so the cost per call stays flat as a
    ///      campaign grows; over a sequence the window moves across the whole set.
    function _findDispute(uint256 seed, Phase phase) internal view returns (uint256) {
        uint256 count = disputes.length;
        if (count == 0) return type(uint256).max;

        uint256 start = seed % count;
        uint256 window = count < 8 ? count : 8;
        for (uint256 i = 0; i < window; i++) {
            uint256 id = (start + i) % count;
            if (_inPhase(id, phase)) return id;
        }
        return type(uint256).max;
    }

    function _inPhase(uint256 id, Phase phase) internal view returns (bool) {
        DepositDispute d = disputes[id];
        if (phase == Phase.Unfiled) return d.evidenceRoot() == bytes32(0);
        if (phase == Phase.Filed) return d.evidenceRoot() != bytes32(0) && !d.settled();
        return d.settled();
    }

    /// @dev Runs after every action: re-verify ONE older dispute round-robin. O(1) per call,
    ///      and over a campaign it re-reads every dispute many times interleaved with writes
    ///      — which is what would catch a later call corrupting an earlier record. Walking
    ///      the whole set inside a predicate would do the same job quadratically.
    function _afterCall() internal {
        uint256 count = disputes.length;
        if (count == 0) return;
        _verifyDispute(disputeCursor % count);
        disputeCursor++;
    }

    /// @dev Compares one dispute's on-chain record against what the harness recorded, and
    ///      checks the three standing structural facts: solvency, the cap, and that nobody
    ///      outside the two parties holds a credit.
    function _verifyDispute(uint256 id) internal {
        DepositDispute d = disputes[id];
        uint256 deposit = expectedDeposit[id];

        if (address(d).balance < d.totalPending()) insolvent = true;
        if (d.landlordAward() > deposit) capViolated = true;

        if (d.settled()) {
            if (d.landlordAward() + d.tenantAward() != deposit) conservationViolated = true;
            if (d.landlordAward() != expectedLandlordAward[id]) conservationViolated = true;
            if (d.tenantAward() != expectedTenantAward[id]) conservationViolated = true;
        }

        _checkNoThirdPartyCredit(d, expectedLandlord[id], expectedTenant[id]);
    }

    /// @dev THE THIRD-PARTY PROPERTY, over the whole address set this harness controls: the
    ///      four party actors, the three adjudicators, the harness itself and the zero
    ///      address. That set is not every address in the world, and the property is
    ///      structurally true for a stronger reason — `_credit` is private and is called from
    ///      exactly one place with exactly two immutable arguments. What this check adds is
    ///      that the two arguments really are THIS dispute's two parties: a party actor that
    ///      is the landlord of dispute 3 must hold nothing against dispute 4, and the
    ///      adjudicators, who touch the decision path, must hold nothing anywhere.
    function _checkNoThirdPartyCredit(DepositDispute d, address landlord, address tenant) internal {
        for (uint256 i = 0; i < NUM_PARTIES; i++) {
            address who = address(partyActors[i]);
            if (who == landlord || who == tenant) continue;
            if (d.pendingWithdrawals(who) != 0) thirdPartyCredited = true;
        }
        for (uint256 i = 0; i < NUM_ADJUDICATORS; i++) {
            if (d.pendingWithdrawals(address(adjActors[i])) != 0) thirdPartyCredited = true;
        }
        if (d.pendingWithdrawals(address(this)) != 0) thirdPartyCredited = true;
        if (d.pendingWithdrawals(address(0)) != 0) thirdPartyCredited = true;
    }
}
