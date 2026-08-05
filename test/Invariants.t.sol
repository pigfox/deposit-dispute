// SPDX-License-Identifier: MIT
pragma solidity 0.8.28;

import {Test} from "forge-std/Test.sol";

import {DepositDispute} from "../src/DepositDispute.sol";
import {Properties} from "./Properties.sol";

/// @title InvariantsTest — the third engine on the same property file.
/// @notice Echidna and Medusa will both drive `test/Properties.sol`. So does Foundry's
///         invariant runner, here. Three engines, ONE set of predicates: a property cannot
///         hold under one and quietly rot under another, and a plain `forge test` catches a
///         broken invariant before anyone reaches for a fuzzer.
///
/// @dev    REACHABILITY IS PROVED BEFORE THE INVARIANTS ARE BELIEVED, and that is the whole
///         reason this file is longer than the seven assertions it exists to make. PF-S134
///         shipped a property that survived deleting the guard it existed to protect,
///         because the campaign never reached the state under test. A property over an
///         unreached state is not a weaker proof — it is no proof, and it reports the same
///         green.
///
///         So the guarding is split by how reliably a single sequence gets somewhere.
///         {afterInvariant} asserts only what is effectively certain, because it runs after
///         EVERY sequence and anything probabilistic there is a coin flip dressed up as a
///         gate. Everything else — the partial split, the cap, the refused cross-item
///         evidence, the refused second vote, the payout — is asserted DETERMINISTICALLY
///         below, which is a stronger check than a probabilistic one anyway.
contract InvariantsTest is Test {
    /// @dev What the harness is endowed with. Deposits are at most a little over one ether
    ///      and a sequence is 64 calls, so this cannot run dry mid-sequence and turn a
    ///      reachability assertion into a funding accident.
    uint256 internal constant HARNESS_ENDOWMENT = 1_000 ether;

    /// @dev The number of `echidna_*` predicates this file drives. Bump it, the harness's
    ///      {Properties.pigfoxPropertyCount} and the predicate together, in one commit.
    uint256 internal constant EXPECTED_PROPERTY_COUNT = 7;

    /// @dev How many state-changing entry points the harness exposes, which is the set
    ///      Foundry's invariant runner selects from. Kept here so
    ///      {test_aRandomSequenceReachesBothShowpieceStates} draws from the same distribution
    ///      the campaign does rather than from a hand-picked subset.
    uint256 internal constant ENTRY_POINTS = 9;
    /// @dev Sequence length for the reachability measurement. Half the campaign's `depth`,
    ///      so the measurement is a harder test than the campaign it stands in for.
    uint256 internal constant CALLS_PER_SEQUENCE = 32;
    /// @dev How many independent sequences the measurement walks.
    uint256 internal constant SEQUENCES_MEASURED = 4;

    Properties internal props;

    function setUp() public {
        props = new Properties();
        // The harness funds each dispute from its own balance. Under the fuzzers that
        // balance arrives at construction (echidna `balanceContract`, medusa
        // `targetContractsBalances`); under Foundry it arrives here.
        vm.deal(address(props), HARNESS_ENDOWMENT);
        targetContract(address(props));
    }

    // --- the seven predicates, verbatim --------------------------------------

    function invariant_DepositIsConserved() public view {
        assertTrue(props.echidna_deposit_is_conserved(), "the two awards sum to the deposit");
    }

    function invariant_LandlordCreditNeverExceedsTheDeposit() public view {
        assertTrue(props.echidna_landlord_credit_never_exceeds_the_deposit(), "the landlord is capped");
    }

    function invariant_UnestablishedItemsNeverPay() public view {
        assertTrue(props.echidna_unestablished_items_never_pay(), "the burden sits on the landlord");
    }

    function invariant_OnlyThePartiesHoldCredit() public view {
        assertTrue(props.echidna_only_the_parties_hold_credit(), "no third party holds a credit");
    }

    function invariant_OneVotePerAdjudicatorPerItem() public view {
        assertTrue(props.echidna_one_vote_per_adjudicator_per_item(), "one vote each, per item");
    }

    function invariant_EvidenceBindsToItsOwnItem() public view {
        assertTrue(props.echidna_evidence_binds_to_its_own_item(), "evidence cannot cross items");
    }

    function invariant_EveryDisputeIsSolvent() public view {
        assertTrue(props.echidna_every_dispute_is_solvent(), "every dispute holds what it owes");
    }

    // --- the harness's own declaration ---------------------------------------

    /// @dev The count a static gate checks against the source, checked again here at runtime
    ///      against the predicates this file actually asserts. The pair is what makes "seven
    ///      properties" mean seven: the static gate catches a miscount, this catches a
    ///      predicate nobody wired up.
    function test_declaredPropertyCountMatchesThisFile() public view {
        assertEq(props.pigfoxPropertyCount(), EXPECTED_PROPERTY_COUNT, "declared property count");
        assertEq(_predicatesDrivenHere(), props.pigfoxPropertyCount(), "predicates asserted by this file");
    }

    function test_harnessSaysWhatItProves() public view {
        assertEq(
            props.pigfoxHarnessDescription(),
            "the deposit is conserved and never over-credited, an unestablished item never pays, "
            "only the two parties hold credit, one vote per adjudicator per item, evidence binds to "
            "the item it was filed against, and every dispute stays solvent"
        );
    }

    /// @dev Counted by calling every predicate this file asserts. Deliberately a literal list
    ///      rather than a number: adding an `invariant_` above without adding it here leaves
    ///      the two disagreeing, which is the point.
    function _predicatesDrivenHere() internal view returns (uint256 n) {
        props.echidna_deposit_is_conserved();
        n++;
        props.echidna_landlord_credit_never_exceeds_the_deposit();
        n++;
        props.echidna_unestablished_items_never_pay();
        n++;
        props.echidna_only_the_parties_hold_credit();
        n++;
        props.echidna_one_vote_per_adjudicator_per_item();
        n++;
        props.echidna_evidence_binds_to_its_own_item();
        n++;
        props.echidna_every_dispute_is_solvent();
        n++;
    }

    /// @dev Non-inertness guard. `afterInvariant` runs after EVERY sequence, so anything
    ///      asserted here must be something a single sequence reaches with effective
    ///      certainty.
    ///
    ///      Opening a dispute and filing a claim qualify: three of the nine state-changing
    ///      entry points open a dispute and three file a claim, so a 64-call sequence
    ///      reaching neither is a one-in-ten-billion event. THE PARTIAL SPLIT DOES NOT
    ///      QUALIFY and is deliberately absent — it needs one particular entry point in nine,
    ///      so a sequence missing it entirely turns up often enough to be a flake rather than
    ///      a gate. It is asserted deterministically instead.
    function afterInvariant() public view {
        assertGt(props.ghostDisputesView(), 0, "harness never opened a dispute - invariants vacuous");
        assertGt(props.ghostFilingsView(), 0, "harness never filed a claim - adjudication vacuous");
    }

    // --- reachability, driven deterministically ------------------------------

    /// @dev THE CANARY, asserted rather than hoped for. This drives the exact state the
    ///      conservation and cap properties are most about — one deposit, both parties
    ///      credited something — and fails if the harness did not actually get there.
    ///      Without it, those predicates could report green over a run in which every
    ///      settlement was all-or-nothing, which is precisely what the estate's two existing
    ///      contracts already do and therefore what this repo would be adding nothing to.
    function test_partialSplitIsReachableAndCounted() public {
        Properties p = _freshHarness();

        p.settleWithAPartialSplit(0.37 ether);

        assertEq(p.ghostDisputesView(), 1, "one dispute opened");
        assertEq(p.ghostFilingsView(), 1, "one claim filed");
        assertEq(p.ghostVerdictsView(), 10, "two adjudicators on each of five items");
        assertEq(p.ghostItemsFrozenView(), 5, "every item froze");
        assertEq(p.ghostSettlementsView(), 1, "the dispute settled");
        assertEq(p.ghostPartialSplitsView(), 1, "and it settled as A PARTIAL SPLIT");

        DepositDispute d = p.disputes(0);
        assertGt(d.landlordAward(), 0, "the landlord was credited something");
        assertGt(d.tenantAward(), 0, "and so was the tenant");
        assertEq(d.landlordAward() + d.tenantAward(), d.DEPOSIT_WEI(), "out of exactly one deposit");
        assertEq(d.totalPending(), d.DEPOSIT_WEI(), "and the contract owes all of it");

        _assertAllPredicatesHold(p);
    }

    /// @dev The split state must be reached MANY times in a sequence, not once by luck. This
    ///      asserts the harness can sustain it rather than working only from a clean slate —
    ///      the difference between "reachable" and "reachable once", which is what a campaign
    ///      actually needs.
    function test_partialSplitIsSustainedAcrossASequence() public {
        Properties p = _freshHarness();

        for (uint256 i = 0; i < 12; i++) {
            p.settleWithAPartialSplit(0.1 ether + i);
        }

        assertEq(p.ghostPartialSplitsView(), 12, "every drive reached the split state");
        assertEq(p.ghostSettlementsView(), 12, "and every one settled");
        assertEq(p.ghostVerdictsView(), 120, "ten verdicts each");
        assertEq(p.disputeCount(), 12, "twelve independent disputes");

        _assertAllPredicatesHold(p);
    }

    /// @dev THE SECOND CANARY. The cap property is unfalsifiable over a run in which no
    ///      schedule ever exceeded its deposit, so the state is driven directly: five items
    ///      of one deposit each, all established, a claim of five times the deposit.
    function test_theCapIsReachableAndCounted() public {
        Properties p = _freshHarness();

        p.settleAtTheCap(0.21 ether);

        assertEq(p.ghostSettlementsView(), 1, "the dispute settled");
        assertEq(p.ghostCapHitsView(), 1, "and it settled AT THE CAP");

        DepositDispute d = p.disputes(0);
        uint256 deposit = d.DEPOSIT_WEI();
        uint256 claimed;
        for (uint256 i = 0; i < d.ITEM_COUNT(); i++) {
            claimed += d.scheduleAt(i).amountWei;
        }

        assertGt(claimed, deposit, "the claim really did exceed the deposit");
        assertEq(d.landlordAward(), deposit, "the landlord got the deposit and no more");
        assertEq(d.tenantAward(), 0, "and the tenant nothing");
        assertEq(address(d).balance, deposit, "no more than the deposit was ever held");

        _assertAllPredicatesHold(p);
    }

    function test_theCapIsSustainedAcrossASequence() public {
        Properties p = _freshHarness();

        for (uint256 i = 0; i < 8; i++) {
            p.settleAtTheCap(0.05 ether + i);
        }

        assertEq(p.ghostCapHitsView(), 8, "every drive hit the cap");
        _assertAllPredicatesHold(p);
    }

    /// @dev THE BURDEN, reached concretely. Every item is put to the adjudicators and none is
    ///      established, so the landlord is owed nothing at all and the tenant keeps the whole
    ///      deposit. A run that never reached this would satisfy the burden property over
    ///      outcomes in which the landlord happened to win everything.
    function test_theBurdenSitsOnTheLandlord() public {
        Properties p = _freshHarness();

        p.openDispute(0, 1, 0.42 ether, 7);
        p.fileClaim(0, 11);
        for (uint256 item = 0; item < 5; item++) {
            p.submitVerdict(0, item, 0, 0);
            p.submitVerdict(0, item, 1, 0);
        }
        p.settle(0);

        assertEq(p.ghostSettlementsView(), 1, "it settled");

        DepositDispute d = p.disputes(0);
        assertEq(d.landlordAward(), 0, "nothing was established, so nothing is owed");
        assertEq(d.tenantAward(), d.DEPOSIT_WEI(), "the tenant keeps the deposit");

        _assertAllPredicatesHold(p);
    }

    /// @dev The other half of the evidence-binding property. `crossItemEvidenceAccepted`
    ///      staying false proves nothing if no cross-item verdict was ever OFFERED, so this
    ///      proves the offer was made and refused.
    function test_crossItemEvidenceIsOfferedAndRefused() public {
        Properties p = _freshHarness();

        p.openDispute(0, 1, 0.33 ether, 5);
        p.fileClaim(0, 13);
        assertEq(p.ghostFilingsView(), 1, "a claim is on file to attack");

        p.submitVerdictForWrongItem(0, 0, 0);
        p.submitVerdictForWrongItem(0, 2, 1);
        p.submitVerdictForWrongItem(0, 4, 2);

        assertEq(p.ghostRejectedCrossItemView(), 3, "all three cross-item verdicts were refused");
        assertEq(p.ghostVerdictsView(), 0, "and none of them was recorded");
        assertTrue(p.echidna_evidence_binds_to_its_own_item(), "the binding held");
    }

    /// @dev The other half of the one-vote property, for the same reason.
    function test_aSecondVoteIsOfferedAndRefused() public {
        Properties p = _freshHarness();

        p.openDispute(0, 1, 0.29 ether, 9);
        p.fileClaim(0, 17);

        p.voteTwice(0, 0, 0);
        p.voteTwice(0, 3, 1);

        assertEq(p.ghostRejectedDoubleVotesView(), 2, "both second votes were refused");
        assertEq(p.ghostVerdictsView(), 2, "and only the two first votes were recorded");
        assertTrue(p.echidna_one_vote_per_adjudicator_per_item(), "one vote each");
    }

    /// @dev The payout path, which the conservation and solvency properties are about. A run
    ///      that never paid anything out satisfies both of them trivially.
    function test_thePayoutPathIsReachable() public {
        Properties p = _freshHarness();

        p.settleWithAPartialSplit(0.61 ether);
        assertEq(p.ghostSettlementsView(), 1, "settled");

        DepositDispute d = p.disputes(0);
        uint256 owedToLandlord = d.landlordAward();
        uint256 owedToTenant = d.tenantAward();

        p.withdraw(0, 0);
        p.withdraw(0, 1);

        assertEq(p.ghostWithdrawalsView(), 2, "both parties actually took the money");
        assertEq(d.pendingWithdrawals(d.LANDLORD()), 0, "the landlord's credit is spent");
        assertEq(d.pendingWithdrawals(d.TENANT()), 0, "and so is the tenant's");
        assertEq(d.totalPending(), 0, "nothing is owed");
        assertEq(address(d).balance, 0, "and nothing is held");
        assertEq(d.LANDLORD().balance, owedToLandlord, "the landlord holds its award");
        assertEq(d.TENANT().balance, owedToTenant, "and the tenant holds its own");

        _assertAllPredicatesHold(p);
    }

    /// @dev A dispute with one item still open must not settle, and the refusal must NOT be
    ///      recorded as a burden violation — the two look identical from outside and mean
    ///      opposite things.
    function test_aPartlyAdjudicatedDisputeDoesNotSettle() public {
        Properties p = _freshHarness();

        p.openDispute(0, 1, 0.19 ether, 21);
        p.fileClaim(0, 23);
        for (uint256 item = 0; item < 4; item++) {
            p.submitVerdict(0, item, 0, 1);
            p.submitVerdict(0, item, 1, 1);
        }
        p.settle(0);

        assertEq(p.ghostItemsFrozenView(), 4, "four items froze");
        assertEq(p.ghostSettlementsView(), 0, "so nothing settled");
        assertFalse(p.disputes(0).settled(), "and the contract agrees");
        assertTrue(
            p.echidna_unestablished_items_never_pay(),
            "a refusal with an item still open is correct, not a violation"
        );

        _assertAllPredicatesHold(p);
    }

    /// @dev Every dispute a run opens is independent: settling one must not touch another's
    ///      custody or credit. The harness rotates its four party actors, so the same address
    ///      is a landlord here and a tenant there, which is where a shared-state bug would
    ///      show.
    function test_disputesDoNotBleedIntoEachOther() public {
        Properties p = _freshHarness();

        p.settleWithAPartialSplit(0.13 ether);
        p.settleAtTheCap(0.17 ether);
        p.settleWithAPartialSplit(0.23 ether);

        assertEq(p.disputeCount(), 3, "three disputes");
        assertEq(p.ghostPartialSplitsView(), 2, "two splits");
        assertEq(p.ghostCapHitsView(), 1, "one cap");

        for (uint256 i = 0; i < 3; i++) {
            DepositDispute d = p.disputes(i);
            assertEq(d.landlordAward() + d.tenantAward(), d.DEPOSIT_WEI(), "each conserves its own");
            assertEq(address(d).balance, d.DEPOSIT_WEI(), "and holds its own");
        }

        _assertAllPredicatesHold(p);
    }

    /// @dev WHAT A CAMPAIGN ACTUALLY REACHES, measured rather than assumed. The tests above
    ///      call a driver directly, so of course the driver's state is reached; that proves
    ///      the state is REACHABLE but says nothing about how often a randomly-ordered
    ///      sequence gets there, which is what the invariant campaign is made of.
    ///
    ///      So this walks pseudo-random sequences over the same nine state-changing entry
    ///      points the fuzzer picks from, with seeds derived by keccak rather than by any
    ///      cheatcode. It is deterministic, so it is a gate and not a sample: if a change
    ///      makes a showpiece state harder to reach, this reddens instead of silently
    ///      thinning the campaign.
    ///
    ///      THE GATE IS THE AGGREGATE, NOT EACH SEQUENCE, and the difference is a real
    ///      measurement rather than a convenience. The first run of this test found a
    ///      sequence that reached zero cross-item attacks, and the reason is structural:
    ///      {Properties.submitVerdictForWrongItem} and {Properties.voteTwice} need a dispute
    ///      that is FILED AND NOT YET SETTLED, and the two drivers open, file, adjudicate
    ///      and settle in a single call — so the disputes they create never sit in that
    ///      phase. Only the `openDispute` then `fileClaim` path leaves one there. A campaign
    ///      is 256 sequences, so the aggregate is what it actually experiences; both attack
    ///      paths are additionally asserted deterministically, one per dedicated test above,
    ///      which is a stronger check than any frequency claim.
    ///
    ///      Per-sequence numbers are logged rather than asserted, so a human reading `-vv`
    ///      sees the distribution instead of a single pass/fail.
    function test_aRandomSequenceReachesBothShowpieceStates() public {
        uint256 totalSplits;
        uint256 totalCaps;
        uint256 totalCrossItem;
        uint256 totalDoubleVotes;
        uint256 sequencesWithASplit;

        for (uint256 s = 0; s < SEQUENCES_MEASURED; s++) {
            Properties p = _freshHarness();
            _driveSequence(p, uint256(keccak256(abi.encode("sequence", s))), CALLS_PER_SEQUENCE);

            emit log_named_uint("sequence", s);
            emit log_named_uint("  disputes opened", p.ghostDisputesView());
            emit log_named_uint("  settlements", p.ghostSettlementsView());
            emit log_named_uint("  PARTIAL SPLITS", p.ghostPartialSplitsView());
            emit log_named_uint("  CAP HITS", p.ghostCapHitsView());
            emit log_named_uint("  cross-item refused", p.ghostRejectedCrossItemView());
            emit log_named_uint("  second votes refused", p.ghostRejectedDoubleVotesView());
            emit log_named_uint("  withdrawals", p.ghostWithdrawalsView());

            totalSplits += p.ghostPartialSplitsView();
            totalCaps += p.ghostCapHitsView();
            totalCrossItem += p.ghostRejectedCrossItemView();
            totalDoubleVotes += p.ghostRejectedDoubleVotesView();
            if (p.ghostPartialSplitsView() > 0) sequencesWithASplit++;

            _assertAllPredicatesHold(p);
        }

        emit log_named_uint("TOTAL partial splits", totalSplits);
        emit log_named_uint("TOTAL cap hits", totalCaps);
        emit log_named_uint("TOTAL cross-item refused", totalCrossItem);
        emit log_named_uint("TOTAL second votes refused", totalDoubleVotes);
        emit log_named_uint("sequences reaching a split", sequencesWithASplit);

        assertGt(totalSplits, 0, "no sequence reached a partial split");
        assertGt(totalCaps, 0, "no sequence reached the cap");
        assertGt(totalCrossItem, 0, "no sequence attacked the evidence binding");
        assertGt(totalDoubleVotes, 0, "no sequence attempted a second vote");
        assertEq(sequencesWithASplit, SEQUENCES_MEASURED, "the split must be reached by every sequence");
    }

    /// @dev Dispatches `calls` pseudo-random calls across the nine state-changing entry
    ///      points, which is the same set Foundry's invariant runner selects from.
    function _driveSequence(Properties p, uint256 seed, uint256 calls) internal {
        for (uint256 i = 0; i < calls; i++) {
            uint256 r = uint256(keccak256(abi.encode(seed, i)));
            uint256 a = uint256(keccak256(abi.encode(r, "a")));
            uint256 b = uint256(keccak256(abi.encode(r, "b")));
            uint256 c = uint256(keccak256(abi.encode(r, "c")));
            uint256 which = r % ENTRY_POINTS;

            if (which == 0) p.openDispute(a, b, c % 1 ether, r);
            else if (which == 1) p.fileClaim(a, b);
            else if (which == 2) p.submitVerdict(a, b, c, r);
            else if (which == 3) p.submitVerdictForWrongItem(a, b, c);
            else if (which == 4) p.voteTwice(a, b, c);
            else if (which == 5) p.settle(a);
            else if (which == 6) p.withdraw(a, b);
            else if (which == 7) p.settleWithAPartialSplit(a % 1 ether);
            else p.settleAtTheCap(a % 1 ether);
        }
    }

    // --- helpers -------------------------------------------------------------

    function _freshHarness() internal returns (Properties p) {
        p = new Properties();
        vm.deal(address(p), HARNESS_ENDOWMENT);
    }

    function _assertAllPredicatesHold(Properties p) internal view {
        assertTrue(p.echidna_deposit_is_conserved(), "conservation");
        assertTrue(p.echidna_landlord_credit_never_exceeds_the_deposit(), "cap");
        assertTrue(p.echidna_unestablished_items_never_pay(), "burden");
        assertTrue(p.echidna_only_the_parties_hold_credit(), "no third party");
        assertTrue(p.echidna_one_vote_per_adjudicator_per_item(), "one vote");
        assertTrue(p.echidna_evidence_binds_to_its_own_item(), "evidence binding");
        assertTrue(p.echidna_every_dispute_is_solvent(), "solvency");
    }
}
