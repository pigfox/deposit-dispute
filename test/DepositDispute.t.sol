// SPDX-License-Identifier: MIT
pragma solidity 0.8.28;

import {DepositDispute} from "../src/DepositDispute.sol";
import {DisputeTestBase} from "./Base.t.sol";
import {MerkleBuilder} from "./MerkleBuilder.sol";
import {RejectingReceiver, ReenteringParty} from "./Receivers.sol";

/// @title DepositDisputeTest — the unit suite.
/// @notice Every branch in `src/` answers to something here. The suite is grouped by the
///         surface it drives rather than by happy/unhappy path, because the interesting
///         assertions are usually a pair: the call that works and the call that differs from
///         it in exactly one respect and must not.
contract DepositDisputeTest is DisputeTestBase {
    /*//////////////////////////////////////////////////////////////
                              CONSTRUCTION
    //////////////////////////////////////////////////////////////*/

    function test_constantsAreWhatTheFixtureAssumes() public view {
        assertEq(dispute.ITEM_COUNT(), ITEM_COUNT, "item count");
        assertEq(dispute.ADJUDICATOR_COUNT(), ADJUDICATOR_COUNT, "adjudicator count");
        assertEq(dispute.QUORUM(), 2, "quorum");
        assertEq(dispute.MAX_REASON_BYTES(), 128, "reason bound");
    }

    function test_construction_fixesThePartiesAndTakesCustodyOfTheDeposit() public view {
        assertEq(dispute.LANDLORD(), landlord, "landlord");
        assertEq(dispute.TENANT(), tenant, "tenant");
        assertEq(dispute.DEPOSIT_WEI(), DEPOSIT, "recorded deposit");
        assertEq(address(dispute).balance, DEPOSIT, "the deposit actually arrived");
        assertEq(dispute.evidenceRoot(), bytes32(0), "nothing committed yet");
        assertFalse(dispute.settled(), "not settled");
        assertEq(dispute.settledItemCount(), 0, "no item frozen");
        assertEq(dispute.totalPending(), 0, "nothing owed");
    }

    function test_construction_fixesTheScheduleBeforeAnyEvidenceExists() public view {
        bytes32[ITEM_COUNT] memory descs = _descHashes();
        uint256[ITEM_COUNT] memory amounts = _amounts();

        for (uint256 i = 0; i < ITEM_COUNT; i++) {
            DepositDispute.Item memory item = dispute.scheduleAt(i);
            assertEq(item.descHash, descs[i], "description hash");
            assertEq(item.amountWei, amounts[i], "claimed amount");
        }
    }

    function test_construction_theScheduleMayExceedTheDeposit() public view {
        uint256[ITEM_COUNT] memory amounts = _amounts();
        uint256 total;
        for (uint256 i = 0; i < ITEM_COUNT; i++) {
            total += amounts[i];
        }
        assertGt(total, dispute.DEPOSIT_WEI(), "the cap path must be reachable from the fixture");
    }

    function test_construction_registersThreeAdjudicatorsWithDistinctPinnedModels() public view {
        string[ADJUDICATOR_COUNT] memory ids = _modelIds();

        for (uint256 i = 0; i < ADJUDICATOR_COUNT; i++) {
            (address signer, string memory modelId) = dispute.adjudicatorAt(i);
            assertEq(signer, _signerAt(i), "registered signer");
            assertEq(modelId, ids[i], "pinned model id");
            assertEq(dispute.modelIdHashAt(i), keccak256(bytes(ids[i])), "model id hash");
            assertTrue(dispute.isAdjudicator(signer), "signer holds a slot");
        }

        assertFalse(dispute.isAdjudicator(stranger), "a stranger holds no slot");
        assertFalse(dispute.isAdjudicator(landlord), "nor does the landlord");
        assertFalse(dispute.isAdjudicator(tenant), "nor the tenant");
    }

    function test_construction_revertsOnZeroLandlord() public {
        vm.expectRevert(DepositDispute.ZeroParty.selector);
        _deploy(address(0), tenant, DEPOSIT, _amounts());
    }

    function test_construction_revertsOnZeroTenant() public {
        vm.expectRevert(DepositDispute.ZeroParty.selector);
        _deploy(landlord, address(0), DEPOSIT, _amounts());
    }

    function test_construction_revertsWhenOneAddressHoldsBothRoles() public {
        vm.expectRevert(abi.encodeWithSelector(DepositDispute.PartiesMustDiffer.selector, landlord));
        _deploy(landlord, landlord, DEPOSIT, _amounts());
    }

    function test_construction_revertsWhenNoDepositArrives() public {
        vm.expectRevert(DepositDispute.ZeroDeposit.selector);
        _deploy(landlord, tenant, 0, _amounts());
    }

    function test_construction_revertsOnAnUndescribedItem() public {
        bytes32[ITEM_COUNT] memory descs = _descHashes();
        descs[ITEM_WINDOW] = bytes32(0);

        vm.expectRevert(abi.encodeWithSelector(DepositDispute.ZeroItemDescription.selector, ITEM_WINDOW));
        new DepositDispute{value: DEPOSIT}(landlord, tenant, descs, _amounts(), _signers(), _modelIds());
    }

    function test_construction_revertsOnAnItemClaimingNothing() public {
        uint256[ITEM_COUNT] memory amounts = _amounts();
        amounts[ITEM_DOOR] = 0;

        vm.expectRevert(abi.encodeWithSelector(DepositDispute.ZeroItemAmount.selector, ITEM_DOOR));
        _deploy(landlord, tenant, DEPOSIT, amounts);
    }

    function test_construction_revertsOnZeroSigner() public {
        address[ADJUDICATOR_COUNT] memory signers = _signers();
        signers[ADJ_BETA] = address(0);

        vm.expectRevert(abi.encodeWithSelector(DepositDispute.ZeroSigner.selector, ADJ_BETA));
        new DepositDispute{value: DEPOSIT}(landlord, tenant, _descHashes(), _amounts(), signers, _modelIds());
    }

    function test_construction_revertsOnAnEmptyModelId() public {
        string[ADJUDICATOR_COUNT] memory ids = _modelIds();
        ids[ADJ_GAMMA] = "";

        vm.expectRevert(abi.encodeWithSelector(DepositDispute.EmptyModelId.selector, ADJ_GAMMA));
        new DepositDispute{value: DEPOSIT}(landlord, tenant, _descHashes(), _amounts(), _signers(), ids);
    }

    /// @dev A landlord who could also adjudicate would be voting on its own claim, and the
    ///      2-of-3 threshold would be a 1-of-2 threshold wearing a disguise.
    function test_construction_revertsWhenTheLandlordWouldAdjudicate() public {
        address[ADJUDICATOR_COUNT] memory signers = _signers();
        signers[ADJ_ALPHA] = landlord;

        vm.expectRevert(
            abi.encodeWithSelector(DepositDispute.PartyCannotAdjudicate.selector, ADJ_ALPHA, landlord)
        );
        new DepositDispute{value: DEPOSIT}(landlord, tenant, _descHashes(), _amounts(), signers, _modelIds());
    }

    function test_construction_revertsWhenTheTenantWouldAdjudicate() public {
        address[ADJUDICATOR_COUNT] memory signers = _signers();
        signers[ADJ_GAMMA] = tenant;

        vm.expectRevert(
            abi.encodeWithSelector(DepositDispute.PartyCannotAdjudicate.selector, ADJ_GAMMA, tenant)
        );
        new DepositDispute{value: DEPOSIT}(landlord, tenant, _descHashes(), _amounts(), signers, _modelIds());
    }

    function test_construction_revertsWhenOneAddressHoldsTwoSlots() public {
        address[ADJUDICATOR_COUNT] memory signers = _signers();
        signers[ADJ_BETA] = adjAlpha;

        vm.expectRevert(abi.encodeWithSelector(DepositDispute.DuplicateSigner.selector, adjAlpha));
        new DepositDispute{value: DEPOSIT}(landlord, tenant, _descHashes(), _amounts(), signers, _modelIds());
    }

    /// @dev Three copies of one model agree trivially, and a threshold over that measures
    ///      nothing at all.
    function test_construction_revertsWhenTwoSlotsDeclareTheSameModel() public {
        string[ADJUDICATOR_COUNT] memory ids = _modelIds();
        ids[ADJ_GAMMA] = ids[ADJ_ALPHA];

        vm.expectRevert(abi.encodeWithSelector(DepositDispute.DuplicateModelId.selector, ADJ_GAMMA));
        new DepositDispute{value: DEPOSIT}(landlord, tenant, _descHashes(), _amounts(), _signers(), ids);
    }

    /*//////////////////////////////////////////////////////////////
                                 FILING
    //////////////////////////////////////////////////////////////*/

    function test_fileClaim_commitsTheRootBeforeAnyoneIsAsked() public {
        bytes32 root = MerkleBuilder.rootOf(_evidence());

        vm.prank(landlord);
        dispute.fileClaim(root);

        assertEq(dispute.evidenceRoot(), root, "the commitment is on record");
    }

    function test_fileClaim_revertsForTheTenant() public {
        vm.expectRevert(abi.encodeWithSelector(DepositDispute.NotLandlord.selector, tenant));
        vm.prank(tenant);
        dispute.fileClaim(MerkleBuilder.rootOf(_evidence()));
    }

    function test_fileClaim_revertsForAnAdjudicator() public {
        vm.expectRevert(abi.encodeWithSelector(DepositDispute.NotLandlord.selector, adjAlpha));
        vm.prank(adjAlpha);
        dispute.fileClaim(MerkleBuilder.rootOf(_evidence()));
    }

    function test_fileClaim_revertsOnAZeroRoot() public {
        vm.expectRevert(DepositDispute.ZeroEvidenceRoot.selector);
        vm.prank(landlord);
        dispute.fileClaim(bytes32(0));
    }

    /// @dev A commitment that can be replaced after seeing a verdict is not a commitment.
    function test_fileClaim_revertsOnRefiling() public {
        bytes32 root = MerkleBuilder.rootOf(_evidence());
        _file(dispute);

        vm.expectRevert(abi.encodeWithSelector(DepositDispute.AlreadyFiled.selector, root));
        vm.prank(landlord);
        dispute.fileClaim(keccak256("a better story"));
    }

    /*//////////////////////////////////////////////////////////////
                               VERDICTS
    //////////////////////////////////////////////////////////////*/

    function test_submitVerdict_recordsTheModelItsSlotDeclaresNotTheOneItClaims() public {
        _file(dispute);
        _vote(dispute, ADJ_BETA, ITEM_CARPET, DepositDispute.ItemFinding.Established);

        assertEq(dispute.verdictCount(ITEM_CARPET), 1, "one verdict");
        DepositDispute.Verdict memory v = dispute.verdictAt(ITEM_CARPET, 0);

        assertEq(v.signer, adjBeta, "signer");
        assertTrue(v.finding == DepositDispute.ItemFinding.Established, "finding");
        assertEq(v.modelIdHash, dispute.modelIdHashAt(ADJ_BETA), "the slot's model, recorded by the contract");
        assertEq(v.promptHash, PROMPT_HASH, "prompt hash");
        assertEq(v.narrativeHash, NARRATIVE_HASH, "narrative hash");
        assertEq(v.itemEvidenceHash, _evidence()[ITEM_CARPET], "the evidence answered to");
        assertEq(v.reason, REASON, "reason");
        assertTrue(dispute.hasVoted(ITEM_CARPET, adjBeta), "the vote is spent");
    }

    function test_submitVerdict_oneVerdictDoesNotFreezeAnItem() public {
        _file(dispute);
        _vote(dispute, ADJ_ALPHA, ITEM_WALL, DepositDispute.ItemFinding.Established);

        (bool frozen,, uint256 votes, uint256 dissent) = dispute.itemStatus(ITEM_WALL);
        assertFalse(frozen, "one adjudicator is not a majority");
        assertEq(votes, 1, "one verdict recorded");
        assertEq(dissent, 0, "no dissent before there is a finding to dissent from");
        assertEq(dispute.settledItemCount(), 0, "nothing frozen");
    }

    function test_submitVerdict_theSecondAgreementFreezesTheItem() public {
        _file(dispute);
        _agree(dispute, ITEM_WALL, DepositDispute.ItemFinding.Established);

        (bool frozen, DepositDispute.ItemFinding finding, uint256 votes,) = dispute.itemStatus(ITEM_WALL);
        assertTrue(frozen, "two agreeing adjudicators freeze it");
        assertTrue(finding == DepositDispute.ItemFinding.Established, "on what they agreed");
        assertEq(votes, 2, "two verdicts");
        assertEq(dispute.settledItemCount(), 1, "one item frozen");
        assertTrue(dispute.findingOf(ITEM_WALL) == DepositDispute.ItemFinding.Established, "readable finding");
        assertEq(dispute.agreementCount(ITEM_WALL, DepositDispute.ItemFinding.Established), 2, "tally");
        assertEq(dispute.agreementCount(ITEM_WALL, DepositDispute.ItemFinding.NotEstablished), 0, "no others");
    }

    /// @dev The late verdict is the one that matters: it is accepted, it is counted as the
    ///      disagreement it is, and it provably cannot move the finding.
    function test_submitVerdict_aLateVerdictIsRecordedAsDissentAndChangesNothing() public {
        _file(dispute);
        _agree(dispute, ITEM_WINDOW, DepositDispute.ItemFinding.Established);
        _vote(dispute, ADJ_GAMMA, ITEM_WINDOW, DepositDispute.ItemFinding.NotEstablished);

        (bool frozen, DepositDispute.ItemFinding finding, uint256 votes, uint256 dissent) =
            dispute.itemStatus(ITEM_WINDOW);

        assertTrue(frozen, "still frozen");
        assertTrue(finding == DepositDispute.ItemFinding.Established, "the finding did not move");
        assertEq(votes, 3, "all three verdicts are on record");
        assertEq(dissent, 1, "and the minority is counted");
        assertEq(dispute.settledItemCount(), 1, "the item did not freeze twice");

        DepositDispute.Verdict[] memory all = dispute.verdictsOf(ITEM_WINDOW);
        assertEq(all.length, 3, "every verdict is readable");
        assertTrue(all[2].finding == DepositDispute.ItemFinding.NotEstablished, "including the dissent");
    }

    function test_submitVerdict_revertsForANonAdjudicator() public {
        _file(dispute);
        bytes32[ITEM_COUNT] memory ev = _evidence();

        vm.expectRevert(abi.encodeWithSelector(DepositDispute.NotAdjudicator.selector, landlord));
        vm.prank(landlord);
        dispute.submitVerdict(
            ITEM_CARPET,
            DepositDispute.ItemFinding.Established,
            ev[ITEM_CARPET],
            MerkleBuilder.proofFor(ev, ITEM_CARPET),
            PROMPT_HASH,
            NARRATIVE_HASH,
            REASON
        );
    }

    function test_submitVerdict_revertsForAnItemOffTheSchedule() public {
        _file(dispute);
        bytes32[ITEM_COUNT] memory ev = _evidence();

        vm.expectRevert(abi.encodeWithSelector(DepositDispute.UnknownItem.selector, ITEM_COUNT));
        vm.prank(adjAlpha);
        dispute.submitVerdict(
            ITEM_COUNT,
            DepositDispute.ItemFinding.Established,
            ev[ITEM_CARPET],
            MerkleBuilder.proofFor(ev, ITEM_CARPET),
            PROMPT_HASH,
            NARRATIVE_HASH,
            REASON
        );
    }

    function test_submitVerdict_revertsBeforeAnythingIsCommitted() public {
        bytes32[ITEM_COUNT] memory ev = _evidence();

        vm.expectRevert(DepositDispute.ClaimNotFiled.selector);
        vm.prank(adjAlpha);
        dispute.submitVerdict(
            ITEM_CARPET,
            DepositDispute.ItemFinding.Established,
            ev[ITEM_CARPET],
            MerkleBuilder.proofFor(ev, ITEM_CARPET),
            PROMPT_HASH,
            NARRATIVE_HASH,
            REASON
        );
    }

    function test_submitVerdict_revertsOnAnOverlongReason() public {
        _file(dispute);
        bytes32[ITEM_COUNT] memory ev = _evidence();

        uint256 bound = dispute.MAX_REASON_BYTES();
        string memory tooLong = string(new bytes(bound + 1));

        vm.expectRevert(abi.encodeWithSelector(DepositDispute.ReasonTooLong.selector, bound + 1, bound));
        vm.prank(adjAlpha);
        dispute.submitVerdict(
            ITEM_CARPET,
            DepositDispute.ItemFinding.Established,
            ev[ITEM_CARPET],
            MerkleBuilder.proofFor(ev, ITEM_CARPET),
            PROMPT_HASH,
            NARRATIVE_HASH,
            tooLong
        );
    }

    /// @dev A verdict nobody can re-run is not a verdict this system accepts.
    function test_submitVerdict_revertsOnAZeroPromptHash() public {
        _file(dispute);
        bytes32[ITEM_COUNT] memory ev = _evidence();

        vm.expectRevert(DepositDispute.ZeroPromptHash.selector);
        vm.prank(adjAlpha);
        dispute.submitVerdict(
            ITEM_CARPET,
            DepositDispute.ItemFinding.Established,
            ev[ITEM_CARPET],
            MerkleBuilder.proofFor(ev, ITEM_CARPET),
            bytes32(0),
            NARRATIVE_HASH,
            REASON
        );
    }

    function test_submitVerdict_revertsOnAZeroNarrativeHash() public {
        _file(dispute);
        bytes32[ITEM_COUNT] memory ev = _evidence();

        vm.expectRevert(DepositDispute.ZeroNarrativeHash.selector);
        vm.prank(adjAlpha);
        dispute.submitVerdict(
            ITEM_CARPET,
            DepositDispute.ItemFinding.Established,
            ev[ITEM_CARPET],
            MerkleBuilder.proofFor(ev, ITEM_CARPET),
            PROMPT_HASH,
            bytes32(0),
            REASON
        );
    }

    function test_submitVerdict_revertsOnASecondVoteFromTheSameAdjudicator() public {
        _file(dispute);
        _vote(dispute, ADJ_ALPHA, ITEM_CARPET, DepositDispute.ItemFinding.Established);

        vm.expectRevert(
            abi.encodeWithSelector(DepositDispute.DuplicateVerdict.selector, ITEM_CARPET, adjAlpha)
        );
        _vote(dispute, ADJ_ALPHA, ITEM_CARPET, DepositDispute.ItemFinding.NotEstablished);
    }

    /// @dev Item 4 is the one promoted unpaired up the tree, so its path is one sibling long
    ///      and every other item's is three. Offering one shape where the other is required
    ///      is refused before the fold runs.
    function test_submitVerdict_revertsWhenAPairedItemOffersThePromotedShortPath() public {
        _file(dispute);
        bytes32[ITEM_COUNT] memory ev = _evidence();

        vm.expectRevert(
            abi.encodeWithSelector(DepositDispute.ProofLengthMismatch.selector, ITEM_CARPET, 1, 3)
        );
        vm.prank(adjAlpha);
        dispute.submitVerdict(
            ITEM_CARPET,
            DepositDispute.ItemFinding.Established,
            ev[ITEM_CARPET],
            MerkleBuilder.proofFor(ev, ITEM_CLEANING),
            PROMPT_HASH,
            NARRATIVE_HASH,
            REASON
        );
    }

    function test_submitVerdict_revertsWhenThePromotedItemOffersALongPath() public {
        _file(dispute);
        bytes32[ITEM_COUNT] memory ev = _evidence();

        vm.expectRevert(
            abi.encodeWithSelector(DepositDispute.ProofLengthMismatch.selector, ITEM_CLEANING, 3, 1)
        );
        vm.prank(adjAlpha);
        dispute.submitVerdict(
            ITEM_CLEANING,
            DepositDispute.ItemFinding.Established,
            ev[ITEM_CLEANING],
            MerkleBuilder.proofFor(ev, ITEM_CARPET),
            PROMPT_HASH,
            NARRATIVE_HASH,
            REASON
        );
    }

    function test_submitVerdict_thePromotedItemVotesOnItsShortPath() public {
        _file(dispute);
        assertEq(MerkleBuilder.proofFor(_evidence(), ITEM_CLEANING).length, 1, "one sibling");

        _agree(dispute, ITEM_CLEANING, DepositDispute.ItemFinding.Established);

        (bool frozen, DepositDispute.ItemFinding finding,,) = dispute.itemStatus(ITEM_CLEANING);
        assertTrue(frozen, "the promoted item adjudicates like any other");
        assertTrue(finding == DepositDispute.ItemFinding.Established, "and reaches a finding");
    }

    function test_submitVerdict_revertsOnEvidenceThatWasNeverCommitted() public {
        _file(dispute);
        bytes32[ITEM_COUNT] memory ev = _evidence();
        bytes32 invented = keccak256("evidence-bundle:never-filed");

        vm.expectRevert(
            abi.encodeWithSelector(DepositDispute.EvidenceProofInvalid.selector, ITEM_CARPET, invented)
        );
        vm.prank(adjAlpha);
        dispute.submitVerdict(
            ITEM_CARPET,
            DepositDispute.ItemFinding.Established,
            invented,
            MerkleBuilder.proofFor(ev, ITEM_CARPET),
            PROMPT_HASH,
            NARRATIVE_HASH,
            REASON
        );
    }

    /// @dev THE POINT OF BINDING THE INDEX INTO THE LEAF. The carpet's evidence really is in
    ///      the committed tree, and its proof really is a valid proof — of membership at
    ///      index 0. Offered against the wall it produces a different leaf, folds to a
    ///      different root, and is refused. Both the paired case and the promoted case are
    ///      driven, because they differ in path length and could fail differently.
    function test_evidenceForOneItemCannotBeSpentOnAnother() public {
        _file(dispute);
        bytes32[ITEM_COUNT] memory ev = _evidence();

        for (uint256 target = 0; target < ITEM_COUNT; target++) {
            for (uint256 source = 0; source < ITEM_COUNT; source++) {
                if (source == target) continue;
                // Only pairs whose required path length agrees can even reach the fold; the
                // rest are refused earlier, on length, which is equally a refusal.
                bytes4 expected = MerkleBuilder.proofFor(ev, source).length
                        == MerkleBuilder.proofFor(ev, target).length
                    ? DepositDispute.EvidenceProofInvalid.selector
                    : DepositDispute.ProofLengthMismatch.selector;

                vm.expectPartialRevert(expected);
                vm.prank(adjAlpha);
                dispute.submitVerdict(
                    target,
                    DepositDispute.ItemFinding.Established,
                    ev[source],
                    MerkleBuilder.proofFor(ev, source),
                    PROMPT_HASH,
                    NARRATIVE_HASH,
                    REASON
                );
            }
        }
    }

    function test_leafFor_bindsTheIndexIntoTheHash() public view {
        bytes32 evidence = keccak256("one bundle");
        assertTrue(
            dispute.leafFor(ITEM_CARPET, evidence) != dispute.leafFor(ITEM_WALL, evidence),
            "the same bundle at two indices is two different leaves"
        );
        assertEq(
            dispute.leafFor(ITEM_CARPET, evidence),
            MerkleBuilder.leafFor(ITEM_CARPET, evidence),
            "the builder and the contract agree on the leaf"
        );
    }

    /*//////////////////////////////////////////////////////////////
                              SETTLEMENT
    //////////////////////////////////////////////////////////////*/

    function test_settle_givesTheTenantEverythingWhenNothingIsEstablished() public {
        _runToSettlement(dispute, _noneEstablished());

        assertTrue(dispute.settled(), "settled");
        assertEq(dispute.landlordAward(), 0, "the landlord established nothing");
        assertEq(dispute.tenantAward(), DEPOSIT, "so the tenant keeps the deposit");
        assertEq(dispute.pendingWithdrawals(landlord), 0, "credited nothing");
        assertEq(dispute.pendingWithdrawals(tenant), DEPOSIT, "credited everything");
        assertEq(dispute.totalPending(), DEPOSIT, "and the total is the deposit");
    }

    /// @dev THE SHOWPIECE. Both parties are credited a non-zero amount out of one deposit —
    ///      the state neither existing estate contract can reach, because both settle
    ///      all-or-nothing.
    function test_settle_splitsTheDepositWhenSomeItemsAreEstablished() public {
        _runToSettlement(dispute, _splitEstablished());

        uint256 owed = AMOUNT_CARPET + AMOUNT_WALL;
        assertEq(dispute.landlordAward(), owed, "the established items, and only those");
        assertEq(dispute.tenantAward(), DEPOSIT - owed, "the tenant keeps the rest");
        assertGt(dispute.landlordAward(), 0, "a genuine split: the landlord got something");
        assertGt(dispute.tenantAward(), 0, "and so did the tenant");
        assertEq(dispute.landlordAward() + dispute.tenantAward(), DEPOSIT, "conservation");
    }

    /// @dev THE CAP. The claim is 1.9 ether against a 1 ether deposit. The landlord is
    ///      credited the deposit, the tenant nothing, and the 0.9 ether difference is not
    ///      recorded as a debt anywhere — see the contract notice.
    function test_settle_capsAtTheDepositAndRecordsNoDebt() public {
        _runToSettlement(dispute, _allEstablished());

        uint256 claimed = AMOUNT_CARPET + AMOUNT_WALL + AMOUNT_WINDOW + AMOUNT_DOOR + AMOUNT_CLEANING;
        assertGt(claimed, DEPOSIT, "the fixture really does exceed the deposit");

        assertEq(dispute.landlordAward(), DEPOSIT, "capped at the deposit");
        assertEq(dispute.tenantAward(), 0, "nothing left for the tenant");
        assertEq(dispute.totalPending(), DEPOSIT, "and the contract owes exactly the deposit");
        assertEq(address(dispute).balance, DEPOSIT, "no more than the deposit was ever held");
    }

    /// @dev The boundary `owed < deposit` is written on. Established items sum to EXACTLY the
    ///      deposit, so the cap and the sum coincide and the tenant is credited zero without
    ///      the cap having been applied.
    function test_settle_theExactEqualityBoundary() public {
        _runToSettlement(dispute, _exactlyDepositEstablished());

        assertEq(AMOUNT_CARPET + AMOUNT_CLEANING, DEPOSIT, "the fixture hits the boundary exactly");
        assertEq(dispute.landlordAward(), DEPOSIT, "all of it, by sum rather than by cap");
        assertEq(dispute.tenantAward(), 0, "and nothing remains");
    }

    function test_settle_revertsBeforeEveryItemHasFrozen() public {
        _file(dispute);
        _agree(dispute, ITEM_CARPET, DepositDispute.ItemFinding.Established);
        _agree(dispute, ITEM_WALL, DepositDispute.ItemFinding.Established);

        vm.expectRevert(abi.encodeWithSelector(DepositDispute.ItemsNotAdjudicated.selector, 2, ITEM_COUNT));
        dispute.settle();
    }

    function test_settle_revertsBeforeAnythingIsFiledAtAll() public {
        vm.expectRevert(abi.encodeWithSelector(DepositDispute.ItemsNotAdjudicated.selector, 0, ITEM_COUNT));
        dispute.settle();
    }

    function test_settle_revertsOnASecondSettlement() public {
        _runToSettlement(dispute, _splitEstablished());

        vm.expectRevert(DepositDispute.AlreadySettled.selector);
        dispute.settle();
    }

    /// @dev Conservation over every one of the 32 possible finding combinations. Not a fuzz:
    ///      the space is small enough to walk exhaustively, and an exhaustive walk cannot
    ///      miss the one combination a fuzzer happened not to draw.
    function test_settle_conservesTheDepositOverEveryPossibleOutcome() public {
        uint256[ITEM_COUNT] memory amounts = _amounts();

        for (uint256 mask = 0; mask < (1 << ITEM_COUNT); mask++) {
            bool[ITEM_COUNT] memory established;
            uint256 owed;
            for (uint256 i = 0; i < ITEM_COUNT; i++) {
                if (mask & (1 << i) != 0) {
                    established[i] = true;
                    owed += amounts[i];
                }
            }

            DepositDispute d = _deployDefault();
            _runToSettlement(d, established);

            uint256 expectedLandlord = owed < DEPOSIT ? owed : DEPOSIT;
            assertEq(d.landlordAward(), expectedLandlord, "landlord award");
            assertEq(d.tenantAward(), DEPOSIT - expectedLandlord, "tenant award");
            assertEq(d.landlordAward() + d.tenantAward(), DEPOSIT, "conservation");
            assertLe(d.landlordAward(), DEPOSIT, "cap");
            assertEq(d.totalPending(), DEPOSIT, "the contract owes the whole deposit");
            assertGe(address(d).balance, d.totalPending(), "and holds at least that");
        }
    }

    /*//////////////////////////////////////////////////////////////
                              WITHDRAWAL
    //////////////////////////////////////////////////////////////*/

    function test_withdraw_paysBothPartiesAndEmptiesTheContract() public {
        _runToSettlement(dispute, _splitEstablished());

        uint256 owed = AMOUNT_CARPET + AMOUNT_WALL;

        vm.prank(landlord);
        dispute.withdraw();
        assertEq(landlord.balance, owed, "the landlord took its award");
        assertEq(dispute.pendingWithdrawals(landlord), 0, "and its credit is spent");
        assertEq(dispute.totalPending(), DEPOSIT - owed, "only the tenant is still owed");

        vm.prank(tenant);
        dispute.withdraw();
        assertEq(tenant.balance, DEPOSIT - owed, "the tenant took the rest");
        assertEq(dispute.totalPending(), 0, "nothing is owed");
        assertEq(address(dispute).balance, 0, "and nothing is held");
    }

    function test_withdraw_revertsForAStranger() public {
        _runToSettlement(dispute, _splitEstablished());

        vm.expectRevert(abi.encodeWithSelector(DepositDispute.NothingToWithdraw.selector, stranger));
        vm.prank(stranger);
        dispute.withdraw();
    }

    function test_withdraw_revertsForAPartyThatWonNothing() public {
        _runToSettlement(dispute, _allEstablished());
        assertEq(dispute.tenantAward(), 0, "the tenant won nothing");

        vm.expectRevert(abi.encodeWithSelector(DepositDispute.NothingToWithdraw.selector, tenant));
        vm.prank(tenant);
        dispute.withdraw();
    }

    function test_withdraw_revertsTwiceForTheSameParty() public {
        _runToSettlement(dispute, _splitEstablished());

        vm.prank(landlord);
        dispute.withdraw();

        vm.expectRevert(abi.encodeWithSelector(DepositDispute.NothingToWithdraw.selector, landlord));
        vm.prank(landlord);
        dispute.withdraw();
    }

    function test_withdraw_revertsWhenThePayeeRefusesTheTransfer() public {
        RejectingReceiver refuser = new RejectingReceiver();
        DepositDispute d = _deploy(address(refuser), tenant, DEPOSIT, _amounts());
        _runToSettlement(d, _splitEstablished());

        uint256 owed = AMOUNT_CARPET + AMOUNT_WALL;
        (bool ok, bytes memory ret) = refuser.call(address(d), abi.encodeCall(DepositDispute.withdraw, ()));

        assertFalse(ok, "the withdrawal failed");
        assertEq(
            ret,
            abi.encodeWithSelector(DepositDispute.TransferFailed.selector, address(refuser), owed),
            "and it failed for the reason it should"
        );
        assertEq(
            d.pendingWithdrawals(address(refuser)), owed, "the whole call reverted, so the credit stands"
        );
        assertEq(address(d).balance, DEPOSIT, "and the money is still here");
    }

    /// @dev The reentrancy guard, reached the only way it can be. The single external call in
    ///      this contract is the transfer inside `withdraw`, so a re-entry can only start in
    ///      a payee's fallback — and re-entering `withdraw` would meet a zeroed balance and
    ///      be refused by CEI before the guard was consulted. So the payee re-enters
    ///      `settle`, which the guard also covers, and CATCHES the revert rather than
    ///      bubbling it — left to bubble it would make the transfer fail and `withdraw`
    ///      revert with `TransferFailed`, a different error about a different thing, and the
    ///      test would pass while proving nothing about the guard.
    function test_withdraw_reentryIntoSettleIsRefusedByTheGuard() public {
        ReenteringParty attacker = new ReenteringParty();
        DepositDispute d = _deploy(address(attacker), tenant, DEPOSIT, _amounts());
        attacker.bind(d);
        _runToSettlement(d, _splitEstablished());

        uint256 owed = AMOUNT_CARPET + AMOUNT_WALL;
        (bool ok,) = attacker.call(address(d), abi.encodeCall(DepositDispute.withdraw, ()));

        assertTrue(ok, "the withdrawal itself succeeded");
        assertTrue(attacker.reentryAttempted(), "and the re-entry really was attempted");
        assertFalse(attacker.reentrySucceeded(), "it did not succeed");
        assertEq(
            attacker.reentryError(),
            abi.encodeWithSelector(DepositDispute.ReentrantCall.selector),
            "the guard is what refused it"
        );
        assertEq(address(attacker).balance, owed, "the payee was still paid");
        assertEq(d.landlordAward(), owed, "and the settlement was not run twice");
    }

    /*//////////////////////////////////////////////////////////////
                                 VIEWS
    //////////////////////////////////////////////////////////////*/

    function test_scheduleAt_revertsOffTheEnd() public {
        vm.expectRevert(abi.encodeWithSelector(DepositDispute.UnknownItem.selector, ITEM_COUNT));
        dispute.scheduleAt(ITEM_COUNT);
    }

    function test_itemStatus_revertsOffTheEnd() public {
        vm.expectRevert(abi.encodeWithSelector(DepositDispute.UnknownItem.selector, ITEM_COUNT));
        dispute.itemStatus(ITEM_COUNT);
    }

    function test_findingOf_revertsOffTheEnd() public {
        vm.expectRevert(abi.encodeWithSelector(DepositDispute.UnknownItem.selector, ITEM_COUNT));
        dispute.findingOf(ITEM_COUNT);
    }

    /// @dev An unfrozen item has no finding, and returning the zero value would be
    ///      indistinguishable from a genuine `NotEstablished`.
    function test_findingOf_revertsUntilTheItemFreezes() public {
        _file(dispute);
        _vote(dispute, ADJ_ALPHA, ITEM_DOOR, DepositDispute.ItemFinding.Established);

        vm.expectRevert(abi.encodeWithSelector(DepositDispute.ItemNotAdjudicated.selector, ITEM_DOOR));
        dispute.findingOf(ITEM_DOOR);
    }

    function test_agreementCount_revertsOffTheEnd() public {
        vm.expectRevert(abi.encodeWithSelector(DepositDispute.UnknownItem.selector, ITEM_COUNT));
        dispute.agreementCount(ITEM_COUNT, DepositDispute.ItemFinding.Established);
    }

    function test_verdictCount_revertsOffTheEnd() public {
        vm.expectRevert(abi.encodeWithSelector(DepositDispute.UnknownItem.selector, ITEM_COUNT));
        dispute.verdictCount(ITEM_COUNT);
    }

    function test_verdictAt_revertsOffTheEnd() public {
        vm.expectRevert(abi.encodeWithSelector(DepositDispute.UnknownItem.selector, ITEM_COUNT));
        dispute.verdictAt(ITEM_COUNT, 0);
    }

    function test_verdictAt_revertsPastTheLastVerdict() public {
        _file(dispute);
        _vote(dispute, ADJ_ALPHA, ITEM_CARPET, DepositDispute.ItemFinding.Established);

        vm.expectRevert(abi.encodeWithSelector(DepositDispute.UnknownVerdict.selector, ITEM_CARPET, 1));
        dispute.verdictAt(ITEM_CARPET, 1);
    }

    function test_verdictsOf_revertsOffTheEnd() public {
        vm.expectRevert(abi.encodeWithSelector(DepositDispute.UnknownItem.selector, ITEM_COUNT));
        dispute.verdictsOf(ITEM_COUNT);
    }

    function test_verdictsOf_isEmptyBeforeAnyoneVotes() public view {
        assertEq(dispute.verdictsOf(ITEM_CARPET).length, 0, "no verdicts yet");
        assertEq(dispute.verdictCount(ITEM_CARPET), 0, "and the count agrees");
    }

    function test_adjudicatorAt_revertsOffTheEnd() public {
        vm.expectRevert(abi.encodeWithSelector(DepositDispute.UnknownAdjudicator.selector, ADJUDICATOR_COUNT));
        dispute.adjudicatorAt(ADJUDICATOR_COUNT);
    }

    function test_modelIdHashAt_revertsOffTheEnd() public {
        vm.expectRevert(abi.encodeWithSelector(DepositDispute.UnknownAdjudicator.selector, ADJUDICATOR_COUNT));
        dispute.modelIdHashAt(ADJUDICATOR_COUNT);
    }
}
