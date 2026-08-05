// SPDX-License-Identifier: MIT
pragma solidity 0.8.28;

/// @title DepositDispute
/// @notice One rental security deposit, one fixed schedule of five deduction line
///         items, and three independent adjudicators — each running a different
///         pinned model — deciding each item separately on 2-of-3 agreement.
///         The money is then split by arithmetic this contract performs, not by
///         anything a model said.
///
/// @dev THE MODEL NEVER TOUCHES A NUMBER. An adjudicator answers exactly one
///      question per line item: did the landlord ESTABLISH this deduction, yes or
///      no. The amounts were fixed at construction, before any evidence existed
///      and before any adjudicator was asked anything. {settle} sums the
///      established items and caps the total at the deposit. There is no path by
///      which a verdict names, returns, influences or rounds an amount, and that
///      is the single most important property of this design: a model that could
///      answer with a number could be argued into any number.
///
/// @dev NOTHING HERE IS OWNED, AND NOTHING HERE CAN BE CHANGED. No owner, no
///      admin, no setter, no proxy, no upgrade path. The parties, the deposit,
///      the five items and the three adjudicators are all fixed in the
///      constructor and the deposit arrives with it. An operator who could swap
///      an adjudicator after seeing which way the first verdict went could
///      manufacture any majority it liked, and no amount of process around such a
///      function would make its absence provable. Absence is provable.
///
/// @dev THE EVIDENCE IS COMMITTED PER ITEM, BY INDEX. The landlord files one
///      merkle root over five leaves, and each leaf binds an item's INDEX into
///      the hash. A verdict must carry the item's evidence hash plus the proof
///      that it sits at that index in the committed tree. Evidence gathered for
///      the carpet is therefore unusable against the wall: it proves membership
///      at a different index and the fold produces a different root.
///
/// @dev THIS IS A CUSTOM ADJUDICATION SET, built for this demo. It is not
///      Chainlink, not CRE, and not any external oracle network. Nothing about it
///      is decentralized: three keys sign, and one operator may hold all three.
///      What this contract enforces is AGREEMENT — no single key decides any item
///      — and what it cannot enforce is independence of interest. A reader who
///      meets the word "consensus" here and supplies the usual meaning has been
///      misled by an omission rather than a statement, so it is not used.
///
/// @dev KNOWN LIMITS, WRITTEN DOWN BECAUSE THEY BOUND WHAT THIS PROVES.
///      1. EXCESS IS NOT DEBT. If the established items sum to more than the
///         deposit, the landlord is credited the deposit and the remainder is
///         neither recorded nor recoverable here. A deposit dispute settles a
///         deposit; a claim for more than the deposit is a different proceeding
///         in a different forum, and inventing an on-chain debt that no party
///         agreed to fund would be a worse answer than declining to.
///      2. THERE IS NO FILING DEADLINE. If the landlord never calls {fileClaim},
///         no adjudicator can vote and the deposit stays here. That is a real
///         gap, deliberately left rather than papered over with a timeout whose
///         expiry would itself need a trusted clock and a default winner. It is
///         acceptable for a demo whose landlord is the party that wants to file.
///      3. LATE VERDICTS ARE ACCEPTED AND CANNOT MATTER. An item freezes the
///         moment two adjudicators agree, so a third verdict arriving afterwards
///         is recorded, counted as a dissent, and provably changes nothing.
///         Refusing it would mean the minority opinion frequently never reached
///         the chain at all.
contract DepositDispute {
    /*//////////////////////////////////////////////////////////////
                                  TYPES
    //////////////////////////////////////////////////////////////*/

    /// @notice What an adjudicator may conclude about one line item.
    /// @dev THERE IS NO ABSTAIN MEMBER, DELIBERATELY. `NotEstablished` is the zero
    ///      value and therefore the default, so insufficient evidence collapses
    ///      into it rather than into a third state that would have to be given a
    ///      meaning at settlement time. The burden sits on the landlord: an item
    ///      the evidence does not carry is an item the tenant keeps the money for.
    ///      An abstain member would let a model decline to decide and would leave
    ///      the split undefined, which is a decision by omission.
    enum ItemFinding {
        NotEstablished,
        Established
    }

    /// @notice One deduction the landlord proposes against the deposit.
    /// @param descHash Hash of the item's off-chain description. Fixed at
    ///        construction, so the thing being adjudicated cannot be reworded
    ///        after evidence exists.
    /// @param amountWei What the landlord claims for it. Fixed at construction,
    ///        before any evidence and before any adjudicator was asked anything.
    ///        Amounts are arbitrary and MAY sum to more than the deposit.
    struct Item {
        bytes32 descHash;
        uint256 amountWei;
    }

    /// @notice One registered adjudicator.
    /// @dev The hash lives HERE rather than in a parallel array beside this one. Two
    ///      same-length arrays that must stay index-aligned are a class of bug waiting for
    ///      an edit to one of them; a single record cannot fall out of step with itself.
    /// @param signer The address permitted to submit this adjudicator's verdicts.
    /// @param modelIdHash keccak256 of `modelId`, which is what verdicts carry. Recorded by
    ///        the contract, never supplied by a caller.
    /// @param modelId The pinned model identifier this slot speaks for.
    struct Adjudicator {
        address signer;
        bytes32 modelIdHash;
        string modelId;
    }

    /// @notice Everything the contract knows about one line item's adjudication.
    /// @dev ONE RECORD PER ITEM, not three parallel mappings keyed by the same index. The
    ///      earlier shape kept `frozen`, `finding` and the tally in three separate mappings,
    ///      which is three writes that have to agree and three places an edit can miss.
    /// @param frozen Whether two adjudicators have agreed. One-way: never unset.
    /// @param finding The finding they agreed on. Meaningless unless `frozen`.
    /// @param tally Votes cast for each finding, indexed by {ItemFinding}. `uint64` because
    ///        at most {ADJUDICATOR_COUNT} verdicts can ever be recorded against one item.
    struct ItemState {
        bool frozen;
        ItemFinding finding;
        uint64[2] tally;
    }

    /// @notice One submitted verdict, and everything a third party needs to re-run it.
    /// @param signer The adjudicator that submitted it.
    /// @param finding What it concluded about the item.
    /// @param submittedAt Block timestamp of submission.
    /// @param modelIdHash Hash of the model identifier registered for `signer`, recorded by
    ///        the contract rather than supplied by the caller, so a signer cannot claim to
    ///        have run a model other than the one its slot declares.
    /// @param promptHash Hash of the seeded, structured prompt.
    /// @param itemEvidenceHash The per-item evidence answered to. Proved to sit at this
    ///        item's index in the committed tree before the verdict was accepted.
    /// @param narrativeHash Hash of the model's narrative. The narrative itself is not stored.
    /// @param reason A short, length-bounded human-legible summary.
    struct Verdict {
        address signer;
        ItemFinding finding;
        uint64 submittedAt;
        bytes32 modelIdHash;
        bytes32 promptHash;
        bytes32 itemEvidenceHash;
        bytes32 narrativeHash;
        string reason;
    }

    /*//////////////////////////////////////////////////////////////
                                 ERRORS
    //////////////////////////////////////////////////////////////*/

    /// @notice A party address was zero.
    error ZeroParty();
    /// @notice The landlord and the tenant are the same address.
    /// @param party The address supplied for both roles.
    error PartiesMustDiffer(address party);
    /// @notice No deposit arrived with the constructor.
    error ZeroDeposit();
    /// @notice A line item carried no description hash.
    /// @param index The item slot.
    error ZeroItemDescription(uint256 index);
    /// @notice A line item claimed nothing.
    /// @param index The item slot.
    error ZeroItemAmount(uint256 index);
    /// @notice A signer address was zero.
    /// @param index The adjudicator slot that was zero.
    error ZeroSigner(uint256 index);
    /// @notice A model identifier was empty.
    /// @param index The adjudicator slot whose model identifier was empty.
    error EmptyModelId(uint256 index);
    /// @notice A party to the dispute was registered as one of its adjudicators.
    /// @param index The adjudicator slot.
    /// @param party The landlord or tenant address that was offered as a signer.
    error PartyCannotAdjudicate(uint256 index, address party);
    /// @notice The same address was registered as more than one adjudicator.
    /// @param signer The repeated address.
    error DuplicateSigner(address signer);
    /// @notice Two adjudicators declared the same model.
    /// @param index The slot that repeated an earlier model identifier.
    error DuplicateModelId(uint256 index);
    /// @notice Only the landlord may file the claim.
    /// @param caller The account that made the rejected call.
    error NotLandlord(address caller);
    /// @notice The evidence root was zero, so nothing would have been committed.
    error ZeroEvidenceRoot();
    /// @notice A claim has already been filed and its commitment cannot be replaced.
    /// @param committed The root fixed by the first filing.
    error AlreadyFiled(bytes32 committed);
    /// @notice No claim has been filed, so there is no commitment to answer.
    error ClaimNotFiled();
    /// @notice The caller is not a registered adjudicator.
    /// @param caller The account that made the rejected call.
    error NotAdjudicator(address caller);
    /// @notice A line item was read or voted on out of range.
    /// @param index The requested item slot.
    error UnknownItem(uint256 index);
    /// @notice An adjudicator slot was read out of range.
    /// @param index The requested slot.
    error UnknownAdjudicator(uint256 index);
    /// @notice A verdict was read out of range.
    /// @param index The item slot.
    /// @param position The requested verdict position.
    error UnknownVerdict(uint256 index, uint256 position);
    /// @notice The reason string exceeds the bound.
    /// @param length The supplied length in bytes.
    /// @param maxLength The maximum permitted length in bytes.
    error ReasonTooLong(uint256 length, uint256 maxLength);
    /// @notice The prompt hash was zero, so the adjudication could not be re-run.
    error ZeroPromptHash();
    /// @notice The narrative hash was zero, so the explanation is unpinned.
    error ZeroNarrativeHash();
    /// @notice This adjudicator has already voted on this item.
    /// @param index The item slot.
    /// @param signer The adjudicator that had already voted.
    error DuplicateVerdict(uint256 index, address signer);
    /// @notice The proof was not the length this item's position in the tree requires.
    /// @param index The item slot.
    /// @param supplied The proof length offered.
    /// @param expected The only length accepted for this index.
    error ProofLengthMismatch(uint256 index, uint256 supplied, uint256 expected);
    /// @notice The evidence did not prove membership at this item's index.
    /// @param index The item slot the verdict claimed to answer.
    /// @param itemEvidenceHash The evidence hash offered for it.
    error EvidenceProofInvalid(uint256 index, bytes32 itemEvidenceHash);
    /// @notice Not every line item has reached the threshold yet.
    /// @param adjudicated How many items have.
    /// @param required How many must.
    error ItemsNotAdjudicated(uint256 adjudicated, uint256 required);
    /// @notice The dispute has already settled and cannot settle again.
    error AlreadySettled();
    /// @notice The item has not reached the threshold, so it has no finding.
    /// @param index The item slot.
    error ItemNotAdjudicated(uint256 index);
    /// @notice The caller has no credited balance to withdraw.
    /// @param caller The account that made the rejected call.
    error NothingToWithdraw(address caller);
    /// @notice The ether transfer to the withdrawing party failed.
    /// @param payee The account being paid.
    /// @param amount The amount that failed to transfer.
    error TransferFailed(address payee, uint256 amount);
    /// @notice A value-moving entry point was re-entered.
    error ReentrantCall();

    /*//////////////////////////////////////////////////////////////
                                 EVENTS
    //////////////////////////////////////////////////////////////*/

    /// @notice Emitted once, at construction, when the deposit arrives.
    /// @param landlord The party claiming deductions.
    /// @param tenant The party whose deposit this is.
    /// @param depositWei The deposit, in wei. This is the entire sum this contract will
    ///        ever hold or pay out.
    event DisputeOpened(address indexed landlord, address indexed tenant, uint256 depositWei);
    /// @notice Emitted once per line item, at construction.
    /// @param index The item slot.
    /// @param descHash Hash of the item's off-chain description.
    /// @param amountWei What the landlord claims for it.
    event ItemScheduled(uint256 indexed index, bytes32 descHash, uint256 amountWei);
    /// @notice Emitted once per adjudicator, at construction.
    /// @param index The adjudicator slot.
    /// @param signer The address permitted to submit that slot's verdicts.
    /// @param modelId The pinned model identifier, published in full so a reader knows which
    ///        model the slot speaks for without reading storage.
    /// @param modelIdHash Its keccak256 hash, which is what verdicts carry.
    event AdjudicatorRegistered(
        uint256 indexed index, address indexed signer, string modelId, bytes32 modelIdHash
    );
    /// @notice Emitted when the landlord commits the evidence tree.
    /// @param landlord The filing party. Always the landlord.
    /// @param evidenceRoot The merkle root over the five per-item evidence leaves.
    /// @param filedAt Block timestamp of the filing.
    event ClaimFiled(address indexed landlord, bytes32 evidenceRoot, uint64 filedAt);
    /// @notice Emitted for every accepted verdict, including one that arrives after the
    ///         item it answers has already frozen.
    /// @param index The item adjudicated.
    /// @param signer The adjudicator that submitted it.
    /// @param finding What it concluded.
    /// @param modelIdHash Hash of the model identifier registered for that signer.
    /// @param promptHash Hash of the seeded, structured prompt the model was given.
    /// @param itemEvidenceHash The per-item evidence answered to, proved to sit at `index`.
    /// @param narrativeHash Hash of the model's narrative, which is pinned but not stored.
    event VerdictSubmitted(
        uint256 indexed index,
        address indexed signer,
        ItemFinding finding,
        bytes32 modelIdHash,
        bytes32 promptHash,
        bytes32 itemEvidenceHash,
        bytes32 narrativeHash
    );
    /// @notice Emitted when a line item reaches the threshold and freezes.
    /// @param index The item that froze.
    /// @param finding The finding that held a majority.
    /// @param agreeing How many adjudicators agreed on it at the moment it froze.
    event ItemAdjudicated(uint256 indexed index, ItemFinding finding, uint256 agreeing);
    /// @notice Emitted when the deposit is split.
    /// @param owedWei The sum of every established item, BEFORE the cap. A value above the
    ///        deposit is the cap path, and the difference is not a debt — see the contract
    ///        notice.
    /// @param landlordWei What the landlord was credited. Never more than the deposit.
    /// @param tenantWei What the tenant was credited. Always the remainder.
    event Settled(uint256 owedWei, uint256 landlordWei, uint256 tenantWei);
    /// @notice Emitted when a party's withdrawable balance grows.
    /// @param payee The account whose balance grew. Only ever the landlord or the tenant.
    /// @param amount The credit, in wei. May be zero: a party that won nothing is credited
    ///        nothing, and the zero is recorded rather than skipped so the log carries the
    ///        whole settlement.
    /// @param newBalance The payee's total withdrawable balance after the credit.
    event WithdrawalCredited(address indexed payee, uint256 amount, uint256 newBalance);
    /// @notice Emitted when a party takes its credited balance.
    /// @param payee The withdrawing account.
    /// @param amount The amount withdrawn, in wei.
    event Withdrawn(address indexed payee, uint256 amount);

    /*//////////////////////////////////////////////////////////////
                                CONSTANTS
    //////////////////////////////////////////////////////////////*/

    /// @notice How many deduction line items a dispute has. Fixed at five, forever.
    uint256 public constant ITEM_COUNT = 5;
    /// @notice How many adjudicators are registered. Fixed at three, forever.
    uint256 public constant ADJUDICATOR_COUNT = 3;
    /// @notice How many agreeing findings freeze a line item.
    uint256 public constant QUORUM = 2;
    /// @notice The longest reason string a verdict may carry, in bytes.
    /// @dev Bounded so the decision path can never hold arbitrary text. The narrative lives
    ///      off chain and is pinned by `narrativeHash`.
    uint256 public constant MAX_REASON_BYTES = 128;

    /// @dev Proof length for the four items that pair up in the bottom row of the tree.
    ///      See {_expectedProofLength} for the derivation; it is a property of ITEM_COUNT
    ///      being five and is a named constant rather than a literal at the call site.
    uint256 private constant PROOF_LENGTH_PAIRED = 3;
    /// @dev Proof length for the one item promoted unpaired up the tree.
    uint256 private constant PROOF_LENGTH_PROMOTED = 1;

    /// @dev Reentrancy sentinel values. Non-zero both ways so the guard never pays for a
    ///      zero-to-non-zero storage write on the happy path.
    uint256 private constant NOT_ENTERED = 1;
    uint256 private constant ENTERED = 2;

    /*//////////////////////////////////////////////////////////////
                                 STORAGE
    //////////////////////////////////////////////////////////////*/

    /// @notice The party claiming deductions against the deposit.
    address public immutable LANDLORD;
    /// @notice The party whose deposit this is.
    address public immutable TENANT;
    /// @notice The deposit, in wei. Arrived with the constructor and is the entire sum this
    ///         contract will ever hold or pay out.
    uint256 public immutable DEPOSIT_WEI;

    /// @notice The merkle root over the five per-item evidence leaves. Zero until filed.
    bytes32 public evidenceRoot;
    /// @notice Whether the deposit has been split.
    bool public settled;
    /// @notice How many line items have reached the threshold and frozen.
    uint256 public settledItemCount;
    /// @notice What the landlord was credited at settlement. Zero until then.
    uint256 public landlordAward;
    /// @notice What the tenant was credited at settlement. Zero until then.
    uint256 public tenantAward;

    /// @notice Credited balances awaiting withdrawal, by account.
    mapping(address payee => uint256 amount) public pendingWithdrawals;
    /// @notice Total of all {pendingWithdrawals}, tracked for cheap solvency checks.
    uint256 public totalPending;

    /// @notice Whether `signer` has already voted on item `index`.
    mapping(uint256 index => mapping(address signer => bool voted)) public hasVoted;

    Item[ITEM_COUNT] private _schedule;
    Adjudicator[ADJUDICATOR_COUNT] private _adjudicators;
    mapping(address signer => uint256 indexPlusOne) private _signerSlot;

    mapping(uint256 index => Verdict[] verdicts) private _verdicts;
    mapping(uint256 index => ItemState state) private _itemState;

    uint256 private _reentrancyStatus;

    /*//////////////////////////////////////////////////////////////
                                MODIFIERS
    //////////////////////////////////////////////////////////////*/

    /// @dev Guards the whole value-moving surface: {settle}, which decides the split, and
    ///      {withdraw}, which pays it out.
    ///
    ///      {withdraw} is already safe by CEI alone — the balance is zeroed and the running
    ///      total decremented before the transfer — so the guard is defense in depth there.
    ///
    ///      {settle} MAKES NO EXTERNAL CALL TODAY, and the guard on it is defense in depth
    ///      against a future edit that introduces one. That is a real risk rather than a
    ///      hypothetical: {settle} is the natural place for a later requirement to land —
    ///      notifying a registry, pulling a fee, paying an ERC-20 instead of ether — and any
    ///      of those hands control to another contract in the middle of a function that has
    ///      already computed a split and not yet written it. Adding the guard afterwards
    ///      depends on whoever makes that edit noticing; having it there already does not.
    ///      The cost is one warm storage slot per settlement, paid once per dispute.
    modifier nonReentrant() {
        if (_reentrancyStatus == ENTERED) revert ReentrantCall();
        _reentrancyStatus = ENTERED;
        _;
        _reentrancyStatus = NOT_ENTERED;
    }

    /*//////////////////////////////////////////////////////////////
                               CONSTRUCTOR
    //////////////////////////////////////////////////////////////*/

    /// @notice Opens the dispute, takes custody of the deposit, and fixes everything.
    /// @dev THE DEPOSIT ARRIVES HERE, NOT THROUGH A LATER `fund()` CALL, and that is the
    ///      whole point of the choice. A separate funding call would mean the schedule, the
    ///      parties and the adjudicators all exist for some window in which the amount at
    ///      stake is still zero or still changeable, and "what is being disputed" would be a
    ///      question with a time-dependent answer. Funding at construction makes the claim
    ///      immutable in the strongest available sense: the deposit is a constructor
    ///      argument in every meaningful way, `DEPOSIT_WEI` is `immutable`, and there is no
    ///      code path in this contract that increases it.
    /// @param landlord The party claiming deductions. Must be non-zero and not the tenant.
    /// @param tenant The party whose deposit this is. Must be non-zero and not the landlord.
    /// @param descHashes The five item description hashes. Each must be non-zero.
    /// @param amounts The five claimed amounts, in wei. Each must be non-zero. They MAY sum
    ///        to more than the deposit; {settle} caps rather than rejects.
    /// @param signers The three adjudicator addresses. Non-zero, distinct, and neither party.
    /// @param modelIds The three pinned model identifiers, positionally matching `signers`.
    ///        Must be non-empty and DISTINCT: three copies of one model would agree
    ///        trivially, and a threshold over that measures nothing.
    constructor(
        address landlord,
        address tenant,
        bytes32[ITEM_COUNT] memory descHashes,
        uint256[ITEM_COUNT] memory amounts,
        address[ADJUDICATOR_COUNT] memory signers,
        string[ADJUDICATOR_COUNT] memory modelIds
    ) payable {
        if (landlord == address(0) || tenant == address(0)) {
            revert ZeroParty();
        }
        if (landlord == tenant) revert PartiesMustDiffer(landlord);
        if (msg.value == 0) revert ZeroDeposit();

        LANDLORD = landlord;
        TENANT = tenant;
        DEPOSIT_WEI = msg.value;
        _reentrancyStatus = NOT_ENTERED;

        // SPLIT INTO TWO HELPERS, not for taste. Held inline this constructor carries a
        // cyclomatic complexity of 14, which Slither reports, and the report is right: it
        // does three separable jobs — validate the parties and take custody, fix the
        // schedule, register the panel. The helpers are `private` and called once, so the
        // deployed bytecode is unchanged in substance while each piece is readable on its
        // own. `landlord` and `tenant` are passed rather than read from `LANDLORD` and
        // `TENANT`, because an immutable is not readable from within the constructor that
        // assigns it.
        _scheduleItems(descHashes, amounts);
        _registerAdjudicators(landlord, tenant, signers, modelIds);

        emit DisputeOpened(landlord, tenant, msg.value);
    }

    /// @dev Fixes the five deduction line items, permanently. Every item must name something
    ///      and claim something: an item with no description is not adjudicable, and an item
    ///      claiming nothing could never change the split, so both are refused at the door
    ///      rather than carried as dead weight through every later loop.
    function _scheduleItems(bytes32[ITEM_COUNT] memory descHashes, uint256[ITEM_COUNT] memory amounts)
        private
    {
        for (uint256 i = 0; i < ITEM_COUNT; i++) {
            if (descHashes[i] == bytes32(0)) revert ZeroItemDescription(i);
            if (amounts[i] == 0) revert ZeroItemAmount(i);

            _schedule[i] = Item({descHash: descHashes[i], amountWei: amounts[i]});
            emit ItemScheduled(i, descHashes[i], amounts[i]);
        }
    }

    /// @dev Registers the three adjudicators, permanently. Four things must hold and each is
    ///      refused rather than tolerated: a slot must have a real signer and a named model,
    ///      no party to the dispute may hold a slot, no address may hold two, and no two
    ///      slots may name the same model.
    function _registerAdjudicators(
        address landlord,
        address tenant,
        address[ADJUDICATOR_COUNT] memory signers,
        string[ADJUDICATOR_COUNT] memory modelIds
    ) private {
        for (uint256 i = 0; i < ADJUDICATOR_COUNT; i++) {
            address signer = signers[i];
            if (signer == address(0)) revert ZeroSigner(i);
            if (bytes(modelIds[i]).length == 0) revert EmptyModelId(i);
            // A party that could also adjudicate would be voting on its own claim, and the
            // 2-of-3 threshold would then be a 1-of-2 threshold wearing a disguise.
            if (signer == landlord || signer == tenant) revert PartyCannotAdjudicate(i, signer);
            if (_signerSlot[signer] != 0) revert DuplicateSigner(signer);

            bytes32 modelIdHash = keccak256(bytes(modelIds[i]));
            for (uint256 j = 0; j < i; j++) {
                if (_adjudicators[j].modelIdHash == modelIdHash) revert DuplicateModelId(i);
            }

            _signerSlot[signer] = i + 1;
            _adjudicators[i] = Adjudicator({signer: signer, modelIdHash: modelIdHash, modelId: modelIds[i]});

            emit AdjudicatorRegistered(i, signer, modelIds[i], modelIdHash);
        }
    }

    /*//////////////////////////////////////////////////////////////
                                 FILING
    //////////////////////////////////////////////////////////////*/

    /// @notice The landlord commits the evidence tree, once and for all.
    /// @dev The root is committed BEFORE any adjudicator is asked anything, which is what
    ///      makes every later verdict checkable: a verdict proves membership in this tree or
    ///      it is refused. Re-filing is rejected outright rather than versioned — a
    ///      commitment that can be replaced after seeing a verdict is not a commitment.
    /// @param root The merkle root over the five leaves, leaf i = {leafFor}(i, evidence_i).
    ///        Rejected if zero: a zero root is what an unfiled claim reads as, so accepting
    ///        one would make "filed" and "not filed" the same state.
    function fileClaim(bytes32 root) external {
        if (msg.sender != LANDLORD) revert NotLandlord(msg.sender);
        if (root == bytes32(0)) revert ZeroEvidenceRoot();

        bytes32 committed = evidenceRoot;
        if (committed != bytes32(0)) revert AlreadyFiled(committed);

        evidenceRoot = root;
        emit ClaimFiled(msg.sender, root, uint64(block.timestamp));
    }

    /*//////////////////////////////////////////////////////////////
                              ADJUDICATION
    //////////////////////////////////////////////////////////////*/

    /// @notice Submit this adjudicator's finding on one line item.
    /// @dev THE INDEX IS BOUND INTO THE LEAF, and that is the point of this whole path. The
    ///      contract builds `leaf = keccak256(abi.encode(index, itemEvidenceHash))` itself
    ///      from the index being voted on, then folds the caller's proof. Evidence that
    ///      genuinely sits at index 2 in the committed tree produces the committed root only
    ///      when it is offered for index 2 — offer it for index 3 and the leaf differs, the
    ///      fold differs, and the call reverts. Photographs of the carpet cannot be spent on
    ///      the wall.
    ///
    ///      A verdict is still accepted after the item has frozen; see the contract notice.
    ///      It is recorded, counted, and provably cannot change the finding.
    /// @param index The line item being adjudicated.
    /// @param finding Established or NotEstablished. Both are real answers; there is no abstain.
    /// @param itemEvidenceHash The evidence bundle hash for THIS item.
    /// @param proof The merkle path from this item's leaf to the committed root. Must be
    ///        exactly the length this index requires — see {_expectedProofLength}.
    /// @param promptHash Hash of the seeded, structured prompt. Rejected if zero — a verdict
    ///        nobody can re-run is not a verdict this system accepts.
    /// @param narrativeHash Hash of the model's narrative. Rejected if zero.
    /// @param reason A short summary, at most {MAX_REASON_BYTES} bytes.
    function submitVerdict(
        uint256 index,
        ItemFinding finding,
        bytes32 itemEvidenceHash,
        bytes32[] calldata proof,
        bytes32 promptHash,
        bytes32 narrativeHash,
        string calldata reason
    ) external {
        // SPLIT INTO THREE, NOT FOR TASTE. Held as one function this body is stack-too-deep
        // at solc 0.8.28 with the optimizer on: seven parameters, two of which are
        // calldata references costing two slots each, plus the folded leaf and the eight
        // fields of the record. The alternative was `via_ir`, which would change what the
        // whole repo compiles to in order to fix the shape of one function. The order of
        // the checks is unchanged by the split and is asserted test by test.
        _requireEligibleSubmission(index, promptHash, narrativeHash, reason);
        _requireProvenEvidence(index, itemEvidenceHash, proof);
        _recordVerdict(index, finding, itemEvidenceHash, promptHash, narrativeHash, reason);
    }

    /*//////////////////////////////////////////////////////////////
                               SETTLEMENT
    //////////////////////////////////////////////////////////////*/

    /// @notice Split the deposit, once every line item has frozen.
    /// @dev PERMISSIONLESS, and that is not a gap. What is restricted is not who may poke
    ///      the contract but whether a split can happen at all: without all five items
    ///      frozen on 2-of-3 agreement this reverts for everyone, including both parties.
    ///
    ///      THE ARITHMETIC IS THE WHOLE CONTRIBUTION OF THIS FUNCTION. It sums the amounts
    ///      that were fixed at construction over the items the adjudicators established,
    ///      caps that at the deposit, and gives the tenant the rest. No input to this
    ///      calculation was supplied by a model, and no output of it was.
    function settle() external nonReentrant {
        if (settled) revert AlreadySettled();
        if (settledItemCount != ITEM_COUNT) {
            revert ItemsNotAdjudicated(settledItemCount, ITEM_COUNT);
        }

        uint256 owed = 0;
        for (uint256 i = 0; i < ITEM_COUNT; i++) {
            if (_itemState[i].finding == ItemFinding.Established) {
                owed += _schedule[i].amountWei;
            }
        }

        uint256 deposit = DEPOSIT_WEI;
        uint256 toLandlord = owed < deposit ? owed : deposit;
        uint256 toTenant = deposit - toLandlord;

        settled = true;
        landlordAward = toLandlord;
        tenantAward = toTenant;

        emit Settled(owed, toLandlord, toTenant);

        _credit(LANDLORD, toLandlord);
        _credit(TENANT, toTenant);
    }

    /*//////////////////////////////////////////////////////////////
                              PULL PAYMENTS
    //////////////////////////////////////////////////////////////*/

    /// @notice Withdraws the caller's credited balance.
    /// @dev CEI: the balance is zeroed and the running total decremented before the external
    ///      call, and the function is additionally guarded against reentrancy.
    function withdraw() external nonReentrant {
        uint256 amount = pendingWithdrawals[msg.sender];
        if (amount == 0) revert NothingToWithdraw(msg.sender);

        pendingWithdrawals[msg.sender] = 0;
        totalPending -= amount;

        emit Withdrawn(msg.sender, amount);

        (bool ok,) = payable(msg.sender).call{value: amount}("");
        if (!ok) revert TransferFailed(msg.sender, amount);
    }

    /*//////////////////////////////////////////////////////////////
                                  VIEWS
    //////////////////////////////////////////////////////////////*/

    /// @notice The leaf this contract will build for an item's evidence.
    /// @dev PUBLISHED SO AN OFF-CHAIN BUILDER CANNOT GUESS WRONG. The index is inside the
    ///      hash, which is what makes a leaf usable for exactly one line item.
    ///
    ///      `abi.encode` rather than `abi.encodePacked`: the leaf preimage is then a
    ///      fixed 64 bytes whose first word is a small integer below {ITEM_COUNT}, so the
    ///      classic second-preimage confusion between a leaf and an internal node would
    ///      require finding a keccak output equal to 0, 1, 2, 3 or 4.
    /// @param index The line item.
    /// @param itemEvidenceHash The evidence bundle hash for that item.
    /// @return The leaf.
    function leafFor(uint256 index, bytes32 itemEvidenceHash) public pure returns (bytes32) {
        return keccak256(abi.encode(index, itemEvidenceHash));
    }

    /// @notice One line item as fixed at construction.
    /// @param index The item slot.
    /// @return The item.
    function scheduleAt(uint256 index) external view returns (Item memory) {
        if (index >= ITEM_COUNT) revert UnknownItem(index);
        return _schedule[index];
    }

    /// @notice Everything about where one line item stands.
    /// @dev `dissent` is derived rather than frozen, so a verdict that arrives after the
    ///      item did is still counted as the disagreement it is.
    /// @param index The item slot.
    /// @return frozen Whether two adjudicators have agreed.
    /// @return finding The finding they agreed on. Meaningless unless `frozen`.
    /// @return votes How many verdicts have been recorded for this item.
    /// @return dissent How many of those disagree with `finding`. Zero unless `frozen`.
    function itemStatus(uint256 index)
        external
        view
        returns (bool frozen, ItemFinding finding, uint256 votes, uint256 dissent)
    {
        if (index >= ITEM_COUNT) revert UnknownItem(index);

        ItemState storage s = _itemState[index];
        frozen = s.frozen;
        finding = s.finding;
        votes = _verdicts[index].length;
        dissent = frozen ? votes - s.tally[uint256(finding)] : 0;
    }

    /// @notice The finding a frozen line item settled on.
    /// @param index The item slot. Must have frozen.
    /// @return The finding.
    function findingOf(uint256 index) external view returns (ItemFinding) {
        if (index >= ITEM_COUNT) revert UnknownItem(index);
        if (!_itemState[index].frozen) revert ItemNotAdjudicated(index);
        return _itemState[index].finding;
    }

    /// @notice How many recorded verdicts chose `finding` for item `index`.
    /// @param index The item slot.
    /// @param finding The finding to count.
    /// @return The number of adjudicators that chose it.
    function agreementCount(uint256 index, ItemFinding finding) external view returns (uint256) {
        if (index >= ITEM_COUNT) revert UnknownItem(index);
        return _itemState[index].tally[uint256(finding)];
    }

    /// @notice How many verdicts have been recorded for one line item.
    /// @param index The item slot.
    /// @return The count, at most one per registered adjudicator.
    function verdictCount(uint256 index) external view returns (uint256) {
        if (index >= ITEM_COUNT) revert UnknownItem(index);
        return _verdicts[index].length;
    }

    /// @notice One recorded verdict.
    /// @param index The item slot.
    /// @param position The verdict's place in submission order.
    /// @return The verdict record.
    function verdictAt(uint256 index, uint256 position) external view returns (Verdict memory) {
        if (index >= ITEM_COUNT) revert UnknownItem(index);
        if (position >= _verdicts[index].length) revert UnknownVerdict(index, position);
        return _verdicts[index][position];
    }

    /// @notice Every recorded verdict for one line item, in submission order.
    /// @param index The item slot.
    /// @return The verdicts.
    function verdictsOf(uint256 index) external view returns (Verdict[] memory) {
        if (index >= ITEM_COUNT) revert UnknownItem(index);
        return _verdicts[index];
    }

    /// @notice The signer and pinned model identifier registered in slot `index`.
    /// @param index The adjudicator slot, below {ADJUDICATOR_COUNT}.
    /// @return signer The address permitted to submit that slot's verdicts.
    /// @return modelId The pinned model identifier the slot speaks for.
    function adjudicatorAt(uint256 index) external view returns (address signer, string memory modelId) {
        if (index >= ADJUDICATOR_COUNT) revert UnknownAdjudicator(index);
        Adjudicator storage a = _adjudicators[index];
        return (a.signer, a.modelId);
    }

    /// @notice The model identifier hash registered in slot `index`.
    /// @param index The adjudicator slot, below {ADJUDICATOR_COUNT}.
    /// @return The keccak256 hash of that slot's model identifier, as verdicts carry it.
    function modelIdHashAt(uint256 index) external view returns (bytes32) {
        if (index >= ADJUDICATOR_COUNT) revert UnknownAdjudicator(index);
        return _adjudicators[index].modelIdHash;
    }

    /// @notice Whether `account` is one of the three registered adjudicators.
    /// @param account The address to test.
    /// @return True if it holds a slot.
    function isAdjudicator(address account) external view returns (bool) {
        return _signerSlot[account] != 0;
    }

    /*//////////////////////////////////////////////////////////////
                                 INTERNAL
    //////////////////////////////////////////////////////////////*/

    /// @dev Everything that decides whether this caller may say anything at all about this
    ///      item, in the order the checks are documented. Reads `_signerSlot` without
    ///      keeping the slot: {_recordVerdict} reads it again rather than threading it
    ///      through, which costs one extra warm SLOAD and buys the stack room that made the
    ///      split necessary in the first place.
    function _requireEligibleSubmission(
        uint256 index,
        bytes32 promptHash,
        bytes32 narrativeHash,
        string calldata reason
    ) private view {
        if (_signerSlot[msg.sender] == 0) revert NotAdjudicator(msg.sender);
        if (index >= ITEM_COUNT) revert UnknownItem(index);
        if (evidenceRoot == bytes32(0)) revert ClaimNotFiled();
        if (bytes(reason).length > MAX_REASON_BYTES) {
            revert ReasonTooLong(bytes(reason).length, MAX_REASON_BYTES);
        }
        if (promptHash == bytes32(0)) revert ZeroPromptHash();
        if (narrativeHash == bytes32(0)) revert ZeroNarrativeHash();
        if (hasVoted[index][msg.sender]) revert DuplicateVerdict(index, msg.sender);
    }

    /// @dev The evidence half of the guard: the proof is the length this index requires, and
    ///      it folds this index's leaf into the committed root. Both halves are needed —
    ///      length alone permits a wrong-index proof of the right length, and membership
    ///      alone permits a padded path.
    function _requireProvenEvidence(uint256 index, bytes32 itemEvidenceHash, bytes32[] calldata proof)
        private
        view
    {
        uint256 expectedLength = _expectedProofLength(index);
        if (proof.length != expectedLength) {
            revert ProofLengthMismatch(index, proof.length, expectedLength);
        }
        if (!_provesMembership(proof, evidenceRoot, leafFor(index, itemEvidenceHash))) {
            revert EvidenceProofInvalid(index, itemEvidenceHash);
        }
    }

    /// @dev Writes the verdict and, if it is the one that reaches the threshold, freezes the
    ///      item. The freeze is one-way: a later verdict finds `frozen` already set and is
    ///      recorded as a dissent instead.
    function _recordVerdict(
        uint256 index,
        ItemFinding finding,
        bytes32 itemEvidenceHash,
        bytes32 promptHash,
        bytes32 narrativeHash,
        string calldata reason
    ) private {
        bytes32 modelIdHash = _adjudicators[_signerSlot[msg.sender] - 1].modelIdHash;

        ItemState storage s = _itemState[index];
        hasVoted[index][msg.sender] = true;
        uint64 agreeing = s.tally[uint256(finding)] + 1;
        s.tally[uint256(finding)] = agreeing;
        _verdicts[index]
        .push(
            Verdict({
                signer: msg.sender,
                finding: finding,
                submittedAt: uint64(block.timestamp),
                modelIdHash: modelIdHash,
                promptHash: promptHash,
                itemEvidenceHash: itemEvidenceHash,
                narrativeHash: narrativeHash,
                reason: reason
            })
        );

        emit VerdictSubmitted(
            index, msg.sender, finding, modelIdHash, promptHash, itemEvidenceHash, narrativeHash
        );

        if (!s.frozen && agreeing >= QUORUM) {
            s.frozen = true;
            s.finding = finding;
            settledItemCount++;
            emit ItemAdjudicated(index, finding, agreeing);
        }
    }

    /// @dev Credits a party's pull-payment balance and keeps the running total in step, so
    ///      `totalPending` is always what this contract owes.
    ///
    ///      PRIVATE, AND CALLED FROM EXACTLY ONE PLACE with exactly two arguments: `LANDLORD`
    ///      and `TENANT`, both immutable. That is the whole of the "no third party is ever
    ///      credited" guarantee — it is structural, not a check that could be forgotten.
    ///
    ///      A zero credit is written and emitted rather than skipped: a party that won
    ///      nothing is part of the settlement, and a log that omitted it would read as an
    ///      incomplete split.
    function _credit(address payee, uint256 amount) private {
        uint256 newBalance = pendingWithdrawals[payee] + amount;
        pendingWithdrawals[payee] = newBalance;
        totalPending += amount;

        emit WithdrawalCredited(payee, amount, newBalance);
    }

    /// @dev Folds `proof` into `leaf` and reports whether it reproduces `root`.
    ///
    ///      Commutative pair hashing, so a proof carries no direction bits. Safe here
    ///      because the only leaves that exist are built by {leafFor} from an index below
    ///      {ITEM_COUNT}, and because the proof length is pinned per index before this is
    ///      reached — the two together leave no room to present an internal node as a leaf
    ///      or to grind a longer path.
    function _provesMembership(bytes32[] calldata proof, bytes32 root, bytes32 leaf)
        private
        pure
        returns (bool)
    {
        bytes32 computed = leaf;
        for (uint256 i = 0; i < proof.length; i++) {
            computed = _hashPair(computed, proof[i]);
        }
        return computed == root;
    }

    /// @dev Order-independent hash of two nodes.
    function _hashPair(bytes32 a, bytes32 b) private pure returns (bytes32) {
        return a < b ? keccak256(abi.encode(a, b)) : keccak256(abi.encode(b, a));
    }

    /// @dev The only proof length accepted for an item, derived from the fixed shape of a
    ///      five-leaf tree built by hashing adjacent pairs and promoting the odd node:
    ///
    ///        row 0   L0  L1  L2  L3  L4
    ///        row 1   h(L0,L1)  h(L2,L3)  L4          <- L4 promoted unpaired
    ///        row 2   h(row1_0,row1_1)    L4          <- promoted again
    ///        row 3   root = h(row2_0, L4)
    ///
    ///      Items 0 through 3 sit three rows below the root and need three siblings. Item 4
    ///      is promoted twice and sits one row below it, needing one. Pinning the length
    ///      rather than merely bounding it is what stops a caller offering item 4's short
    ///      path for item 0, or padding a path to search for a fold that happens to land on
    ///      the root.
    function _expectedProofLength(uint256 index) private pure returns (uint256) {
        return index == ITEM_COUNT - 1 ? PROOF_LENGTH_PROMOTED : PROOF_LENGTH_PAIRED;
    }
}
