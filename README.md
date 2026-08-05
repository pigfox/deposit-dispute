# deposit-dispute

One rental security deposit, five fixed deduction line items, three independent
adjudicators each running a different pinned model, and a split that is computed
by the contract rather than named by a model.

**The model never touches a number.** An adjudicator answers exactly one question
per line item — did the landlord *establish* this deduction, yes or no. The
amounts were fixed at construction, before any evidence existed and before any
adjudicator was asked anything. `settle()` sums the established items, caps the
total at the deposit, and gives the tenant the remainder. There is no path by
which a verdict names, returns, influences or rounds an amount. That is the
single most important property of this design: a model that could answer with a
number could be argued into any number.

---

## Status

**Unit 1 — scaffold and contract skeleton.** The contract compiles, the full
property set executes under Foundry, coverage on `src/` is 100% on all four
dimensions, and Slither is clean at `fail-on: low` with no per-repo exclusion.

Not yet done, and deliberately so:

- **Nothing is deployed.** No address, no `deployments/` file, no broadcast.
- **No off-chain adjudicator service.** The Go panel that calls the three models
  and submits their findings is a later unit. The `.golangci.yml` here is the
  estate's canonical config, waiting for it.
- **This repo is not yet on the PIGFOX SOLIDITY PIPELINE.** See below — this is a
  live obligation, not a nice-to-have.

## What this repo does not depend on

It clones and builds standing alone. There is no pigfox submodule, no pigfox
import, and no file shared with another repo. The only `lib/` entry is
`forge-std`, which `forge test` requires:

```shell
git clone --recursive <this repo>
forge build
```

The cost of that is real and is written down where it is paid:
`test/PigfoxProperties.sol` is a **re-implementation** of the estate's shared
property-harness base, not a copy that anyone will remember to reconcile. When
this repo joins the shared pipeline that file is **deleted** and the import
re-pointed — it is not merged, and it is not kept alongside.

**Standing obligation.** PIGFOX SOLIDITY PIPELINE v1 binds every contract in
every pigfox repo *before it is deployed or demoed*. This repo has not been
deployed or demoed, so it is not yet in breach — but it must be consumed as a
submodule (`lib/solidity-pipeline`, HTTPS, calling the reusable workflow) before
anything here reaches a chain or a demo page. Echidna and Medusa configs, the
static property-count gate and the doctrine gate all arrive with it.

## Deployment target

DIRECT-CHAIN ONLY. Base Sepolia 84532, supplied from the environment at deploy
time. Every test in this repo is a pure in-process EVM built from this repo's own
source: `forge test` constructs the contracts fresh, in process, reaching no
network. `foundry.toml` carries no `[rpc_endpoints]` block.

---

## The contract

`src/DepositDispute.sol`. No owner, no admin, no setter, no proxy, no upgrade
path. One dispute is one deployment.

### Why the deposit arrives at construction

The deposit is `payable` on the constructor and `DEPOSIT_WEI` is `immutable`.
There is no `fund()` call, and that is a decision rather than an omission.

A separate funding call would mean the parties, the schedule and the adjudicators
all exist for some window in which the amount at stake is still zero or still
changeable. "What is being disputed" would then be a question with a
time-dependent answer, and the evidence commitment — which is the thing that
makes every verdict checkable — would be pinned to a claim whose size had not
settled. Funding at construction makes the claim immutable in the strongest
available sense: the deposit is a constructor argument in every meaningful way,
and no code path in the contract increases it.

The cost is that the opener must hold the deposit at deployment time. For a
demo, where the landlord deploys and funds in one transaction, that is not a
cost at all.

### The schedule

Exactly five line items, each `{bytes32 descHash; uint256 amountWei}`, fixed at
construction. Every item must name something and claim something; zero is
refused for both.

**Amounts may sum to more than the deposit, and the fixture deliberately does.**
The cap path is not a corner case to be avoided — it is the ordinary case where a
landlord claims more damage than the deposit covers, and both the unit suite and
the property harness drive it directly.

### Findings

```solidity
enum ItemFinding { NotEstablished, Established }
```

`NotEstablished` is the zero value and therefore the default. **There is no
abstain member.** Insufficient evidence collapses into `NotEstablished` rather
than into a third state that would have to be given a meaning at settlement time.
The burden sits on the landlord: an item the evidence does not carry is an item
the tenant keeps the money for. An abstain member would let a model decline to
decide and would leave the split undefined, which is a decision by omission.

### Adjudication is per item, not per dispute

Three slots, fixed at construction, each with a pinned model identifier whose
`keccak256` is what verdicts carry. Duplicate signers and duplicate model
identifiers are both refused — three copies of one model agree trivially, and a
2-of-3 threshold over one opinion measures nothing. Neither party may hold a slot.

Each adjudicator votes **once per item**. An item freezes the moment two
adjudicators agree on it. Dissent is recorded per item and derived rather than
frozen, so a third verdict arriving after the item did is still counted as the
disagreement it is — and provably cannot move the finding.

This is a **custom adjudication set built for this demo**. It is not Chainlink,
not CRE, and not any external oracle network. Nothing about it is decentralized:
three keys sign, and one operator may hold all three. What the contract enforces
is *agreement* — no single key decides any item. What it cannot enforce is
independence of interest, and the word "consensus" is not used here for that
reason.

### The evidence commitment binds the index

At filing the landlord commits one merkle root over five leaves:

```
leaf_i = keccak256(abi.encode(i, itemEvidenceHash_i))
```

A verdict for item *i* must carry `itemEvidenceHash_i` **and** its merkle proof.
The contract builds the leaf itself, from the index being voted on, and folds the
caller's proof against the committed root.

**Binding the index into the leaf is the point.** Evidence gathered for the
carpet is unusable against the wall: it proves membership at a different index,
so the leaf differs, the fold differs, and the call reverts. The unit suite
drives all twenty ordered cross-item pairs and the property harness attacks the
same path from the fuzzer.

The tree has a fixed shape, because `ITEM_COUNT` is fixed:

```
row 0   L0  L1  L2  L3  L4
row 1   h(L0,L1)  h(L2,L3)  L4        <- L4 promoted unpaired
row 2   h(row1_0,row1_1)    L4        <- promoted again
row 3   root = h(row2_0, L4)
```

Items 0–3 need a three-sibling path; item 4 needs one. The proof length is
**pinned per index**, not merely bounded — that is what stops a caller offering
item 4's short path for item 0, or padding a path to search for a fold that lands
on the root by accident. Pair hashing is order-independent, so proofs carry no
direction bits; that is safe here because every leaf the contract will ever build
has a small integer below `ITEM_COUNT` in its first word, so confusing a leaf
with an internal node would require finding a keccak output equal to 0, 1, 2, 3
or 4.

A zero root is refused, and re-filing is refused outright rather than versioned.
A commitment that can be replaced after seeing a verdict is not a commitment.

### Settlement

```
owed           = sum of amountWei over items whose finding is Established
landlordCredit = min(owed, deposit)
tenantCredit   = deposit - landlordCredit
```

`settle()` is permissionless and reverts for everyone, both parties included,
until all five items have frozen.

Both credits go through a pull payment — `pendingWithdrawals`, `totalPending`,
`withdraw()` under CEI and a reentrancy guard. `_credit` is `private` and called
from exactly one place with exactly two arguments, `LANDLORD` and `TENANT`, both
`immutable`. "No third party is ever credited" is therefore structural rather
than a check that could be forgotten.

## Known limits

Written down because they bound what this proves.

1. **Excess is not debt.** If the established items sum to more than the deposit,
   the landlord is credited the deposit and the remainder is neither recorded nor
   recoverable here. A deposit dispute settles a deposit; a claim for more than
   the deposit is a different proceeding in a different forum, and inventing an
   on-chain debt that no party agreed to fund would be a worse answer than
   declining to.
2. **There is no filing deadline.** If the landlord never calls `fileClaim`, no
   adjudicator can vote and the deposit stays in the contract. This is a real gap,
   left deliberately rather than papered over with a timeout whose expiry would
   itself need a trusted clock and a default winner. It is acceptable for a demo
   whose landlord is the party that wants to file, and it is the first thing to
   revisit if this ever stops being a demo.
3. **Late verdicts are accepted and cannot matter.** An item freezes the moment
   two adjudicators agree, so a third verdict arriving afterwards is recorded,
   counted as a dissent, and provably changes nothing. Refusing it would mean the
   minority opinion frequently never reached the chain at all.

---

## The properties

`test/Properties.sol` is the single property harness. Foundry's invariant runner
drives it today; Echidna and Medusa will drive the same file when this repo joins
the pipeline. It is **engine-pure** — it imports no forge-std and uses no
cheatcodes — because a harness that reached for a cheatcode would work under
Foundry and be undrivable by the fuzzers.

Seven `echidna_*` predicates, declared as a literal in `pigfoxPropertyCount()`:

| # | Predicate | What it says |
|---|-----------|--------------|
| a | `echidna_deposit_is_conserved` | In any terminal state the two awards sum to exactly the deposit |
| b | `echidna_landlord_credit_never_exceeds_the_deposit` | The landlord is capped, whatever the schedule claimed |
| c | `echidna_unestablished_items_never_pay` | An item fewer than two adjudicators established never contributes |
| d | `echidna_only_the_parties_hold_credit` | No address outside a dispute's own two parties holds a credit against it |
| e | `echidna_one_vote_per_adjudicator_per_item` | No adjudicator records two verdicts on one item |
| f | `echidna_evidence_binds_to_its_own_item` | Evidence proving membership at one index is never accepted at another |
| g | `echidna_every_dispute_is_solvent` | Every dispute always holds at least what it owes |

The harness records what it **independently expects** at the moment of each
write and compares that against the contract's own record later. It never asks
the contract what it should have concluded — that would let a bug agree with
itself. `EXPECTED_QUORUM` is a literal for the same reason
`pigfoxPropertyCount()` is: reading the threshold from the contract would make
the harness agree with any threshold that contract happened to hold, and every
property about the threshold would be unfalsifiable.

### Reachability is proved before the invariants are believed

PF-S134 shipped a property that survived deleting the guard it existed to
protect, because the campaign never reached the state under test. A property over
an unreached state is not a weaker proof — it is no proof, and it reports the
same green.

So two states are **driven**, not hoped for, each with a ghost counter that says
how often it was actually reached:

- **`settleWithAPartialSplit` → `ghostPartialSplitsView`.** The showpiece: both
  parties credited a non-zero amount out of one deposit. This is the state
  neither existing estate contract can reach — `zk-escrow` rules
  `BuyerWins`/`SellerWins` and `ai-parametric-insurance` pays a fixed payout or
  nothing, so both settle all-or-nothing. A run that never produced a partial
  split is a failed run, not a green one, and `InvariantsTest` fails on it.
- **`settleAtTheCap` → `ghostCapHitsView`.** The claim exceeds the deposit and
  the cap bites. The cap property is unfalsifiable over a run in which no
  schedule ever exceeded its deposit.

Both attack paths are counted from the other side too:
`ghostRejectedCrossItemView` and `ghostRejectedDoubleVotesView` prove the attacks
were *offered* and refused, because a flag staying false proves nothing if
nothing was ever tried.

`afterInvariant` asserts only what a single sequence reaches with effective
certainty — a dispute was opened, a claim was filed. Everything probabilistic is
asserted deterministically instead, which is the stronger check anyway.

---

## Running it

```shell
forge build --sizes
forge fmt --check
forge test
forge coverage --no-match-coverage '(test|script)/' --no-match-test 'invariant_'
slither . --config-file slither.config.json
```

`forge test -vv --match-test test_aRandomSequenceReachesBothShowpieceStates`
prints how often pseudo-random sequences reach each showpiece state.

## Licence

MIT. Copyright (c) 2026 Pigfox LLC.
