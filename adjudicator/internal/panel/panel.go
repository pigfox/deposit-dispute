// Package panel asks the three pinned adjudicators about one line item at a
// time and turns their replies into verdicts the contract will accept.
package panel

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"

	"github.com/pigfox/deposit-dispute/adjudicator/internal/config"
	"github.com/pigfox/deposit-dispute/adjudicator/internal/evidence"
	"github.com/pigfox/deposit-dispute/adjudicator/internal/model"
)

// Sentinel errors.
var (
	// ErrSlotCount means the panel was built with the wrong number of clients.
	ErrSlotCount = errors.New("panel: wrong number of slots")
	// ErrDuplicateModel means two slots declare the same model.
	//
	// THIS CHECK LIVES HERE. Three copies of one model agree trivially, and a
	// 2-of-3 threshold over one opinion measures nothing. The contract enforces
	// it too, on the hashes, at construction — catching it here means the
	// operator is told before a key is used rather than after a revert.
	ErrDuplicateModel = errors.New("panel: two slots declare the same model")
)

// Item is one line item put to the panel.
type Item struct {
	// Index is the item's position in the fixed schedule.
	Index int
	// Description is the human-readable deduction being claimed.
	Description string
	// AmountWei is what the landlord claims for it.
	//
	// PRESENT SO THE MODEL CAN SEE WHAT IS AT STAKE, AND NOT SO IT CAN CHANGE IT.
	// The model is never asked for an amount, and no reply this package accepts
	// can carry one. The number that settles the dispute was fixed in the
	// contract's constructor before any evidence existed.
	AmountWei string
	// Evidence is the human-readable evidence bundle for this item.
	Evidence string
	// EvidenceHash is the per-item hash committed in the merkle tree.
	EvidenceHash evidence.Hash
}

// Verdict is one slot's answer about one item, with everything the contract
// needs to accept it and a third party needs to re-run it.
type Verdict struct {
	// Slot is which adjudicator answered.
	Slot int
	// ItemIndex is the item it answered about.
	ItemIndex int
	// Finding is the constrained decision.
	Finding model.Finding
	// Reason is the bounded summary stored on chain verbatim.
	Reason string
	// PromptHash is the hash of the prompt pair. THE SAME VALUE FOR ALL THREE
	// SLOTS on one item — see Adjudicate.
	PromptHash evidence.Hash
	// NarrativeHash pins the model's full reasoning, which is not stored on chain.
	NarrativeHash evidence.Hash
	// RequestedModel is the identifier this slot was configured with.
	RequestedModel string
	// ResolvedModel is what the vendor said it ran. Empty when not exposed.
	ResolvedModel string
	// Refusal is set when the slot did not produce a usable answer. A refused
	// slot casts no verdict at all: nothing is submitted for it, and the item
	// simply has one fewer voice. Because the contract's zero value is
	// NotEstablished and two agreeing findings are needed to establish anything,
	// a refusal can only ever make it HARDER to take money from the tenant.
	Refusal error
}

// Refused reports whether this slot failed to answer.
func (v Verdict) Refused() bool { return v.Refusal != nil }

// Panel is the three pinned adjudicators.
type Panel struct {
	// Clients are the model backends, positionally matching the contract's slots.
	Clients []model.Client
	// Logger receives the spend surface, each call, and every refusal.
	Logger *log.Logger
}

// New builds a panel and refuses a set that cannot mean what a threshold over it
// would claim.
func New(clients []model.Client, logger *log.Logger) (*Panel, error) {
	if len(clients) != config.SlotCount {
		return nil, fmt.Errorf("%w: %d, want %d", ErrSlotCount, len(clients), config.SlotCount)
	}
	seen := make(map[string]int, len(clients))
	for i, c := range clients {
		id := c.ModelID()
		if first, dup := seen[id]; dup {
			return nil, fmt.Errorf("%w: slots %d and %d both name %q", ErrDuplicateModel, first, i, id)
		}
		seen[id] = i
	}
	return &Panel{Clients: clients, Logger: logger}, nil
}

// SpendSurface renders which models will be called at which endpoints, so a
// metered call is visible before it is made.
func (p *Panel) SpendSurface() []string {
	out := make([]string, 0, len(p.Clients)+1)
	out = append(out, config.LogSpendSurfaceHeader)
	for i, c := range p.Clients {
		temp := config.LogTemperatureOmitted
		if c.SendsTemperature() {
			temp = config.LogTemperatureSent
		}
		out = append(out, fmt.Sprintf(config.LogSpendSurfaceOne, i, c.ModelID(), c.Endpoint(), temp))
	}
	return out
}

// Adjudicate puts ONE line item to all three slots and returns one verdict per
// slot.
//
// ONE INDEPENDENT CALL PER ITEM PER SLOT. Five items and three slots are fifteen
// separate round trips, and the items are never batched into a single call.
// THE INDEPENDENCE IS THE DESIGN, not an implementation detail waiting to be
// optimized away:
//
//   - A batched call lets one item's evidence colour the answer to another. The
//     model would see the landlord's whole case at once, and a claim that looked
//     strong in aggregate would drag a weak item along with it. Each item is
//     supposed to stand or fall on the evidence filed for that item.
//   - The contract adjudicates PER ITEM and freezes each one separately on its
//     own 2-of-3. A batch would produce five findings that were never five
//     independent decisions, while presenting them as if they were.
//   - The merkle commitment is per item. A batched prompt would answer to
//     evidence from several leaves at once, and the promptHash published beside
//     each verdict would no longer identify the question that verdict answered.
//
// THE PROMPT HASH IS COMPUTED ONCE AND SHARED BY ALL THREE SLOTS. The prompts
// are identical across slots by construction — only the model differs — so a
// single hash is the honest record of that, and a third party comparing the three
// verdicts can see at a glance that they answered the same question. Computing it
// per slot would produce three equal values and quietly permit them to stop being
// equal.
//
// THE ITEM INDEX IS NOT RANGE-CHECKED HERE. Nothing this function does with the
// index can go wrong — it goes into the prompt and into the verdict label. The
// index matters at the merkle boundary, where an out-of-range value would name a
// leaf that does not exist, and evidence.Proof is where that check lives. A
// second copy here would be a branch no caller can reach.
func (p *Panel) Adjudicate(ctx context.Context, item Item) []Verdict {
	systemPrompt := SystemPrompt()
	userPrompt := UserPrompt(item)
	promptHash := evidence.Keccak([]byte(systemPrompt), []byte(userPrompt))

	verdicts := make([]Verdict, 0, len(p.Clients))
	for slot, client := range p.Clients {
		verdicts = append(verdicts, p.ask(ctx, slot, item, client, systemPrompt, userPrompt, promptHash))
	}

	established, refusals := 0, 0
	for _, v := range verdicts {
		switch {
		case v.Refused():
			refusals++
		case v.Finding == model.Established:
			established++
		}
	}
	p.logf(config.LogItemOutcome, item.Index, established, config.SlotCount, refusals)

	return verdicts
}

// ask runs one slot's single call for one item and constrains the reply.
func (p *Panel) ask(
	ctx context.Context,
	slot int,
	item Item,
	client model.Client,
	systemPrompt, userPrompt string,
	promptHash evidence.Hash,
) Verdict {
	v := Verdict{
		Slot:           slot,
		ItemIndex:      item.Index,
		PromptHash:     promptHash,
		RequestedModel: client.ModelID(),
	}

	p.logf(config.LogAskingItem, item.Index, slot, client.ModelID())

	reply, err := client.Complete(ctx, systemPrompt, userPrompt)
	if err != nil {
		v.Refusal = err
		p.logf(config.LogRefusal, item.Index, slot, err)
		return v
	}
	v.ResolvedModel = reply.ResolvedModel

	// Logged whether or not it matches. A vendor silently resolving an alias to a
	// different snapshot is exactly the kind of drift that shows up first as an
	// answer nobody can explain.
	if reply.ResolvedModel != "" {
		p.logf(config.LogResolvedModel, item.Index, slot, reply.ResolvedModel, client.ModelID())
	}

	parsed, err := model.ParseReply(reply.Text)
	if err != nil {
		// A REFUSAL, NOT A GUESS. Nothing here retries with a looser parser, asks
		// the model to try again, or infers a finding from what the reply seemed
		// to lean towards. The slot abstains and says why — and says what it was
		// given, because "malformed JSON" without the text is a claim a reader
		// cannot check.
		v.Refusal = err
		p.logf(config.LogRefusal, item.Index, slot, err)
		p.logf(config.LogRefusalRaw, item.Index, slot, bounded(reply.Text))
		return v
	}

	v.Finding = parsed.Finding
	v.Reason = parsed.Reason
	v.NarrativeHash = evidence.Keccak([]byte(parsed.Narrative))
	return v
}

// rawReplyMaxLen bounds how much of a refused reply reaches the log. Enough to
// see what shape arrived; not so much that one runaway response floods the run.
const rawReplyMaxLen = 400

// bounded trims a refused reply for the log.
func bounded(s string) string {
	if len(s) <= rawReplyMaxLen {
		return s
	}
	return s[:rawReplyMaxLen] + config.TruncationSuffix
}

// logf writes a line when a logger is present.
func (p *Panel) logf(format string, args ...any) {
	if p.Logger == nil {
		return
	}
	p.Logger.Printf(format, args...)
}

// SystemPrompt is the instruction every slot receives, identical across slots and
// across items.
//
// IT ASKS FOR A TOKEN, NOT A NUMBER AND NOT AN OPINION ABOUT THE OUTCOME. The
// model never sees the deposit, never sees the other items, and is never invited
// to say what should be paid. Its whole authority is to answer one yes-or-no
// question about one piece of evidence.
func SystemPrompt() string {
	return strings.Join([]string{
		"You are one of three independent adjudicators in a rental security deposit dispute.",
		"You are shown ONE deduction the landlord claims, and the evidence filed for that",
		"deduction and nothing else. Decide only this: has the landlord ESTABLISHED this",
		"deduction on the evidence shown?",
		"",
		"The burden is on the landlord. If the evidence does not carry the claim, or you",
		"cannot tell, answer " + config.FindingNotEstablished + ".",
		"",
		"You do not decide amounts. You do not decide who wins the dispute. You do not see",
		"the deposit or the other deductions. Answer only about this one item.",
		"",
		"Reply with a single JSON object and nothing else. No prose before or after it.",
		`{"finding":"<TOKEN>","reason":"<short>","narrative":"<full reasoning>"}`,
		"",
		"<TOKEN> is exactly one of " + config.FindingEstablished + " or " +
			config.FindingNotEstablished + ".",
		fmt.Sprintf("The reason must be at most %d bytes. The narrative may be as long as needed.",
			config.MaxReasonBytes),
	}, "\n")
}

// UserPrompt is the one item put to a slot.
//
// THE ITEM INDEX IS IN THE PROMPT, which means it is inside the promptHash
// published beside every verdict. That mirrors the leaf construction, where the
// index is inside the hash for the same reason: a verdict must be pinned to the
// line item it answered about. Without it, two deductions that happened to carry
// the same description and the same evidence would produce the same promptHash,
// and a third party checking which question a verdict answered could not tell
// them apart.
func UserPrompt(item Item) string {
	return strings.Join([]string{
		fmt.Sprintf("Line item %d of %d.", item.Index, config.ItemCount),
		fmt.Sprintf("Deduction claimed: %s", item.Description),
		fmt.Sprintf("Amount claimed (wei, for context only; you do not decide amounts): %s",
			item.AmountWei),
		fmt.Sprintf("Evidence hash committed on chain: %s", item.EvidenceHash.Hex()),
		"",
		"Evidence filed for this deduction:",
		item.Evidence,
	}, "\n")
}
