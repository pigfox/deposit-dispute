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

**On PIGFOX SOLIDITY PIPELINE v1.** Every gate runs: the gate self-test, both
linters, the doctrine gate, the static property count, formatting, build, tests
and invariants, the 100% coverage gate, Slither at `fail-on: low`, Echidna and
Medusa. Every gate comes from `lib/solidity-pipeline`, vendored at a commit this
repo pins and consumed verbatim, so a local run and a CI run are the same bytes.

The off-chain panel lives in `adjudicator/` — Go, 100% covered on every package,
`golangci-lint` clean, and it makes no vendor call and reaches no node in test.

Not yet done:

- **Nothing is deployed.** No address, no `deployments/` file, no broadcast. The
  deploy script and the two seeded schedules are written and gated; the
  deployment itself is waiting on credentials — see **Deployment** below. The
  adjudicator renders the `cast send` argument list for every verdict and sends
  none of them.
- **No live model run has happened**, so this repository makes no claim about how
  the three vendors actually behave. The drift-handling in
  `internal/model` is written from the estate's documented experience elsewhere,
  not from anything observed here.

## What this repo depends on

Two submodules, and the distinction between them is the whole dependency policy:

| | What it is | Why it is here |
|---|---|---|
| `lib/forge-std` | Foundry's test library | `forge test` needs it |
| `lib/solidity-pipeline` | **Estate infrastructure** | The one verification standard, consumed rather than copied so a fix to a gate lands once instead of once per repo |

**No demo repo.** This repo depends on `zk-escrow`, on
`ai-parametric-insurance` and on every other demo repo exactly not at all — no
submodule, no import, no shared file. The custody and evidence-commitment
patterns here were *read* from those repos and written fresh.

```shell
git clone --recursive <this repo>
forge build
```

The pipeline is what supplies `pipeline/PigfoxProperties.sol`, the declaration
base `test/Properties.sol` extends. This repo briefly carried its own
re-implementation of that file while it was standing alone; that file was
**deleted** when the pipeline was adopted rather than reconciled or kept
alongside, because two copies of a contract whose entire job is to be a single
source of truth is the failure it exists to prevent.

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

## Deployment

**Live on Base Sepolia 84532**, deployed and verified 5 August 2026. Both
disputes ran their full lifecycle — filed, adjudicated by three models over
thirty independent calls, settled, and withdrawn.

| | Address | Deposit | Outcome |
|---|---|---|---|
| **A — the split** | [`0x025A2A50995485C03D0De9604133DfdaCddD9410`](https://sepolia.basescan.org/address/0x025A2A50995485C03D0De9604133DfdaCddD9410#code) | 0.002 ETH | landlord 0.001, tenant 0.001 |
| **B — the cap** | [`0x6698DfB87Db53f63C6480559C0bDf4Fa2Ff501F2`](https://sepolia.basescan.org/address/0x6698DfB87Db53f63C6480559C0bDf4Fa2Ff501F2#code) | 0.0002 ETH | landlord 0.0002, tenant 0 |

Both verified on Basescan. Both contracts now hold **zero** — every wei that went
in came out to one of the two parties, and `landlord + tenant == deposit` in each.

DIRECT-CHAIN ONLY: Base Sepolia 84532, and nothing else. `foundry.toml` names no
endpoint, the deploy script has no local-node path and no rehearsal mode, and the
adjudicator reads the chain id back and refuses to run against any other chain.

### Two disputes, because one cannot show both states

A dispute settles exactly once, so it demonstrates a partial split **or** the cap,
never both. `script/Deploy.s.sol` therefore deploys a pair:

| | Deposit | Schedule | Why |
|---|---|---|---|
| **A — the split** | 0.002 ether | five items summing to 0.0019 | The claim cannot reach the deposit even if every item is established, so **both parties are credited** whatever the panel decides — provided it establishes at least one item |
| **B — the cap** | 0.0002 ether | five items of 0.0003 each | **Any single** established item already puts the claim over the deposit, so the landlord is capped and the excess is recorded as no debt anywhere |

The one outcome that is not a split is the panel establishing nothing at all in
dispute A. That is a real possible answer, and it would be reported as one. The
run will not be repeated until it produces a nicer result.

The evidence bundles are chosen to match: `adjudicator/evidence/example.json`
mixes well-evidenced deductions with weak and contested ones, and
`cap-example.json` is a badly damaged property where the claims are strong.
`TestTheDeployedScheduleMatchesThePublishedBundles` holds the deploy script's
description literals and the bundles' descriptions together — if they diverged,
the panel would be answering about a deduction the chain does not describe, and
**nothing on chain could detect it**, because the contract commits to the
description it was given rather than the one the model saw.

### What a deployment needs

Everything comes from `.env` (copy `.env.example`), and nothing in it is ever
committed, echoed or logged — `./scripts/with-env.sh 'CMD'` is the only wrapper
that reads it.

- `RPC_URL` — a Base Sepolia endpoint, shared by `forge` and the adjudicator so
  there is one endpoint rather than two variables that drift.
- `DEMO_DEPLOYER_PK` — funded with **0.0022 ether plus gas**. A DepositDispute
  takes custody at construction and has no later funding path, so the deposits
  leave the deployer in the deployment transaction itself.
- `DD_LANDLORD_ADDR` / `DD_TENANT_ADDR` and a key for each — the landlord files
  the claim, and both parties withdraw.
- Three `DD_SLOT{n}_SIGNER_ADDR` and keys — only a registered signer may submit
  that slot's verdicts.
- Three `DD_SLOT{n}_MODEL_ID` — the contract stores `keccak256` of each, and the
  adjudicator refuses to run if what it is configured with does not match.
- `ANTHROPIC_API_KEY` and `OPENAI_API_KEY` — the run is **fifteen metered calls**,
  five items by three slots.
- `ETHERSCAN_API_KEY` — for Basescan verification.

### The sequence

```shell
./scripts/with-env.sh 'forge script script/Deploy.s.sol:Deploy \
    --rpc-url "$RPC_URL" --private-key "$DEMO_DEPLOYER_PK" --broadcast --verify'
```

Then, per dispute: the landlord files the bundle root, the adjudicator is pointed
at the address and renders fifteen verdicts, each verdict is submitted by its own
slot signer, anyone settles, and both parties withdraw.

---

## The adjudicator

`adjudicator/` is the off-chain panel: it checks the published evidence bundle
against the claim's on-chain commitment, verifies that the three configured
models are the ones the contract was constructed with, and asks each of them
whether the landlord established each deduction.

**It signs nothing and broadcasts nothing.** Every verdict is rendered as the
argument list that would submit it. That is the mode a third party follows to
re-run an adjudication and check the published hashes, and it needs no private
key because the program holds none.

### One item is one call

Five items and three slots are **fifteen separate round trips**. Items are never
batched, and that is the design rather than an implementation detail waiting to
be optimized away:

- A batched call lets one item's evidence colour the answer to another. The model
  would see the landlord's whole case at once, and a claim that looked strong in
  aggregate would drag a weak item along with it.
- The contract adjudicates per item and freezes each one on its own 2-of-3. A
  batch would produce five findings that were never five independent decisions,
  while presenting them as if they were.
- The merkle commitment is per item, so a batched prompt would answer to evidence
  from several leaves at once and the `promptHash` published beside each verdict
  would no longer identify the question that verdict answered.

`TestOneItemIsExactlyOneCallPerSlot` asserts the call count, and
`TestEachCallCarriesOnlyItsOwnItem` asserts that no slot ever saw another item's
evidence.

### The slots

Three, pinned by environment, positionally matching the contract's. **At least
two vendors**, refused at load time if not: the contract enforces distinct model
*identifiers* but has no idea which company serves them, and three distinct
models from one vendor share a tokenizer, a safety layer, a serving stack and an
outage — a correlation a 2-of-3 threshold silently assumes away.

Before any model is called, `internal/chain` reads `modelIdHashAt` back off chain
and compares it against `keccak256` of the configured identifier. **A mismatch
stops the program.** An adjudicator running a model the contract did not register
would publish verdicts under a slot claiming a different model than the one that
answered, and the published `modelIdHash` would be a lie a third party could not
detect — they would re-run the model the chain named. The chain id is checked the
same way: DIRECT-CHAIN ONLY, verified rather than assumed.

### A pinned model identifier does not pin model behavior

The same identifier can start fencing its replies, start prefacing them, or start
returning a near-miss token, with no version change and no notice. Each of these
is handled explicitly and each has a test:

| What the model does | What happens |
|---|---|
| Wraps the payload in a code fence | **Stripped** — a fence is transport, and only when the *entire* reply is one fence pair |
| Puts prose before the payload | **Refused**, by name. No JSON is hunted for inside prose |
| Answers `PROBABLY`, `yes`, `ESTABLISHED (on balance)` | **Refused**. A near-miss is not evidence of intent |
| Rejects the `temperature` field | The field is **absent from the wire**, not zero — per slot, from environment |
| Answers as a different snapshot than requested | Both identifiers are **logged side by side** |

**A refusal is a refusal, not a guess.** Nothing retries with a looser parser,
asks the model to try again, or infers a finding from what the reply seemed to
lean towards. The slot abstains and says why, and because the contract's zero
value is `NotEstablished` and two agreeing findings are needed to establish
anything, **a refusal can only ever make it harder to take money from the
tenant**.

### What the vendors actually did across thirty live calls

Observed **5 August 2026**: two disputes × five items × three slots, one
independent call each. This is what happened on that day, to those three models.
It is not a claim about vendors in general, about other models, or about any
other date — thirty calls in one afternoon is not a survey.

**Identifier resolution: 30 of 30 exact.** Every call came back reporting the
identifier it was asked for. Slot 1 had already been pinned from the alias
`gpt-5.4` to the snapshot `gpt-5.4-2026-03-05` after the reachability probe
below, and once pinned it never diverged again.

**Temperature: held for all 30.** Slots 0 and 2 omitted the field, slot 1 sent
it as zero. No HTTP 400s during the run.

**Two parse refusals out of thirty — both malformed JSON, both on item 4.**

| Dispute | Item | Slot | Model | Error |
|---|---|---|---|---|
| A | 4 | 0 | `claude-opus-5` | `unexpected end of JSON input` — the reply was truncated mid-object |
| B | 4 | 2 | `claude-sonnet-5` | `invalid character '}' after object key` |

Neither was fenced and neither was prose-prefixed; both were the agreed shape,
started, and then broken. The truncation is most likely the 1024-token reply
bound being reached inside a long narrative.

**What the refusals cost: nothing, by design.** Each affected item was decided by
the two slots that did answer — item 4 of dispute A went 2–0 `NotEstablished`,
item 4 of dispute B went 2–0 `Established`. A refusal removes a voice, and
because two agreeing findings are needed to establish anything, it can only ever
make it *harder* to take money from the tenant. Nothing was retried, nothing was
salvaged from the broken text, and no finding was inferred from it.

**A gap this run exposed, now closed.** The refusals above could be reported as
*malformed* but not *quoted* — the panel logged the error and not the text it
refused. That is a report without its evidence, so `LogRefusalRaw` now records a
bounded copy of the raw reply alongside every refusal. It was added after the run
rather than before it, and the two raw replies above are therefore lost; the next
run will have them.

**Dissent: on two items, both in dispute A.**

| Dispute | Item | Split | Finding |
|---|---|---|---|
| A | 1 — unfilled fixings | 2–1 | Established |
| A | 2 — cracked pane | 2–1 | NotEstablished |

Every other adjudicated item was unanimous among the slots that answered. Item 2
is the one the evidence was written to be genuinely contested — the tenant claims
the crack pre-dated the tenancy and the check-in inventory has no photograph —
and the panel split on it, which is the behaviour the design is for.

### What the reachability probe found earlier the same day

Before the run, all three slots were probed with one minimal call each.

| Slot | Requested | Vendor answered as | Temperature field |
|---|---|---|---|
| 0 | `claude-opus-5` | `claude-opus-5` — **exactly** | **HTTP 400**, rejected: `` `temperature` is deprecated for this model.`` |
| 1 | `gpt-5.4` | `gpt-5.4-2026-03-05` — **a dated snapshot** | accepted |
| 2 | `claude-sonnet-5` | `claude-sonnet-5` — **exactly** | **HTTP 400**, rejected: `` `temperature` is deprecated for this model.`` |

Two things came out of that, and neither was assumed in advance — both were found
by asking.

**An identifier asymmetry.** Both Anthropic identifiers came back exactly as
requested. The OpenAI one was an alias that resolved to a dated snapshot. That
matters here more than it usually would, because the identifier is not a
configuration detail: `keccak256` of it is stored in the contract's constructor
and published beside every verdict. **An alias makes that hash a pointer to a
moving target** — a third party re-running the adjudication next month would get
whatever the alias points to then, while the chain still names the alias, and
nothing would reveal the difference. Since reproducibility by a third party is
the whole claim, slot 1 is pinned to `gpt-5.4-2026-03-05`. The accepted cost is
that a snapshot rotation means a redeployment. Re-probed after pinning: the
snapshot resolves to itself, so it is not an alias in turn.

**Two of the three models reject `temperature` outright.** Not ignore it —
reject it, with an HTTP 400 and no completion. A request carrying a parameter the
model refuses does not run at a different temperature; it does not run. This is
why whether the field is *sent* is per-slot configuration while its *value* is
not configurable anywhere: the value is zero and always has been, and what varies
is a fact about a vendor's API. With the field omitted for the two Anthropic
slots, all three answer 200.

The probe that found this is `adjudicator/probe_live_test.go`, build-tagged
`live` so it never runs in CI or under the coverage gate. It is the only test in
the repository that spends money.

### The two halves are pinned to each other

The Go side builds the merkle tree by laying out rows; the contract folds a
proof. Two different algorithms over one shape, in two languages. A single shared
literal holds them together — the same five strings and the same expected root
appear in `adjudicator/internal/evidence/evidence_test.go` and in
`test/DepositDispute.t.sol`. If they ever disagree the agent would build proofs
the contract refuses, and the first symptom would otherwise be a reverted
transaction on a live dispute.

---

## What the fuzzing campaign under-samples

Stated plainly because a verification claim without its limits is a stronger
claim than the evidence supports.

**The filed-but-unsettled phase is under-sampled.** Two of the harness's nine
entry points — `settleWithAPartialSplit` and `settleAtTheCap` — open a dispute,
file its evidence, adjudicate all five items and settle, all inside one call.
That is what makes the two showpiece states reliably reachable, and it is also
why the disputes those drivers create never *sit* in the state where a claim is
filed but not yet settled. Only the slower `openDispute` → `fileClaim` path
leaves one there.

Two entry points need a dispute in exactly that phase, because they are the ones
that attack it: `submitVerdictForWrongItem`, which offers one line item's
evidence against another, and `voteTwice`, which tries to spend an adjudicator's
vote a second time. So those two attacks are attempted less often than the rest
of the harness runs.

**How that was found, and what was done about it.** It was measured, not
assumed. `test_aRandomSequenceReachesBothShowpieceStates` walks four independent
pseudo-random sequences over the same nine entry points the fuzzer draws from,
and its first run turned up a sequence that attacked the evidence binding zero
times. The gate was moved to the aggregate across sequences, and the per-sequence
figures are logged so the distribution is visible rather than reduced to a
pass/fail.

**What is not weakened.** Both attack paths are additionally driven
*deterministically*, one per dedicated test —
`test_crossItemEvidenceIsOfferedAndRefused` offers three cross-item verdicts and
asserts all three were refused and none recorded;
`test_aSecondVoteIsOfferedAndRefused` does the same for duplicate votes. A
deterministic assertion is a stronger check than any frequency claim, so the
properties themselves are not resting on how often the campaign happens to reach
the state. What the campaign contributes for these two properties is
corroboration, not the proof.

The honest fix — a driver that leaves a dispute parked in the filed phase — is
open work, not a decision to leave it as is.

---

## Running it

Every gate, in pipeline order. These are the same scripts CI runs, taken from the
same submodule commit:

```shell
lib/solidity-pipeline/scripts/selftest.sh
lib/solidity-pipeline/scripts/lint-config-check.sh all
forge lint
npx --yes solhint@6.2.3 -c lib/solidity-pipeline/.solhint.json --max-warnings 0 'src/**/*.sol'
lib/solidity-pipeline/scripts/no-chain-copy-gate.sh all
lib/solidity-pipeline/scripts/property-count.sh --source test/Properties.sol --expected 7
forge fmt --check src test
forge build --sizes
forge test
lib/solidity-pipeline/scripts/coverage.sh
slither . --config-file lib/solidity-pipeline/slither.config.json --ignore-compile
```

Both fuzzers, which need artifacts from a **forced** build — `forge coverage`
overwrites `out/` with instrumented artifacts that crytic-compile will consume
without complaint, registering fewer properties than the source has:

```shell
rm -rf crytic-export && forge build --force
echidna . --contract Properties --config echidna.yaml | tee echidna-output.txt
lib/solidity-pipeline/scripts/property-count.sh --engine echidna --output echidna-output.txt --expected 7

rm -rf crytic-export && forge build --force
medusa fuzz --config medusa.json 2>&1 | tee medusa-output.txt
lib/solidity-pipeline/scripts/property-count.sh --engine medusa --output medusa-output.txt --expected 7
```

`forge test -vv --match-test test_aRandomSequenceReachesBothShowpieceStates`
prints how often pseudo-random sequences reach each showpiece state.

The adjudicator, from `adjudicator/`:

```shell
gofmt -l .
go vet ./...
golangci-lint run --timeout=5m
go test ./... -race -shuffle=on -count=1
go test ./... -covermode=set -coverprofile=coverage.out
go tool cover -func=coverage.out
```

No key is needed for any of that. Every test substitutes the model seam and the
chain seam, so the suite makes no vendor call, reaches no node, and spends
nothing.

## A note on the address-checksum gate

The pipeline's EIP-55 gate refuses a scan set of zero addresses, because that is
the shape a broken matcher makes and it is indistinguishable from a clean tree.
Through Unit 2 this repository pinned no addresses at all — nothing was deployed
— so the stage could not pass, and it was **left red as a stated decision**
rather than silenced with `--allow-empty` plumbing or a decorative address
pinned to satisfy it.

It passes now, and not because anything was done to make it pass: the
adjudicator's test fixtures carry addresses, so the gate has a real scan set to
check. The address it checks most is `0xDD00…00DD0`, which is deliberately
synthetic — this repository still has no deployment, and the first real address
lands with one.

## License

MIT. Copyright (c) 2026 Pigfox LLC.
