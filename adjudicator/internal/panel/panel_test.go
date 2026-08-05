package panel_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"testing"

	"github.com/pigfox/deposit-dispute/adjudicator/internal/config"
	"github.com/pigfox/deposit-dispute/adjudicator/internal/evidence"
	"github.com/pigfox/deposit-dispute/adjudicator/internal/model"
	"github.com/pigfox/deposit-dispute/adjudicator/internal/panel"
)

// fakeClient is a slot that answers from a script. NO TEST HERE MAKES A VENDOR
// CALL.
type fakeClient struct {
	id       string
	endpoint string
	sendTemp bool
	resolved string
	reply    string
	err      error

	// calls records every (system, user) pair this slot was asked, so the tests
	// can assert HOW MANY calls were made and what was in them.
	calls [][2]string
}

func (c *fakeClient) Complete(_ context.Context, system, user string) (model.Reply, error) {
	c.calls = append(c.calls, [2]string{system, user})
	if c.err != nil {
		return model.Reply{}, c.err
	}
	return model.Reply{Text: c.reply, ResolvedModel: c.resolved}, nil
}

func (c *fakeClient) ModelID() string        { return c.id }
func (c *fakeClient) Endpoint() string       { return c.endpoint }
func (c *fakeClient) SendsTemperature() bool { return c.sendTemp }

func reply(finding, reason string) string {
	return fmt.Sprintf(`{"finding":%q,"reason":%q,"narrative":"full reasoning"}`, finding, reason)
}

// threeSlots builds a panel whose slots all answer the same way.
func threeSlots(finding string) []*fakeClient {
	out := make([]*fakeClient, 0, config.SlotCount)
	for i := 0; i < config.SlotCount; i++ {
		out = append(out, &fakeClient{
			id:       fmt.Sprintf("model-%d", i),
			endpoint: fmt.Sprintf("https://vendor%d.invalid", i),
			sendTemp: i != 1,
			reply:    reply(finding, "because the evidence shows it"),
		})
	}
	return out
}

func asClients(fakes []*fakeClient) []model.Client {
	out := make([]model.Client, 0, len(fakes))
	for _, f := range fakes {
		out = append(out, f)
	}
	return out
}

func testItem(index int) panel.Item {
	return panel.Item{
		Index:        index,
		Description:  "carpet staining beyond fair wear",
		AmountWei:    "100000000000000000",
		Evidence:     "check-in report and photographs",
		EvidenceHash: evidence.Keccak([]byte("check-in report and photographs")),
	}
}

func newPanel(t *testing.T, fakes []*fakeClient) (*panel.Panel, *bytes.Buffer) {
	t.Helper()
	var logbuf bytes.Buffer
	p, err := panel.New(asClients(fakes), log.New(&logbuf, "", 0))
	if err != nil {
		t.Fatalf("panel.New: %v", err)
	}
	return p, &logbuf
}

func TestNewRefusesAPanelThatCannotMeanWhatAThresholdWouldClaim(t *testing.T) {
	fakes := threeSlots(config.FindingEstablished)

	if _, err := panel.New(asClients(fakes[:2]), nil); !errors.Is(err, panel.ErrSlotCount) {
		t.Fatalf("err = %v, want ErrSlotCount", err)
	}

	fakes[2].id = fakes[0].id
	if _, err := panel.New(asClients(fakes), nil); !errors.Is(err, panel.ErrDuplicateModel) {
		t.Fatalf("err = %v, want ErrDuplicateModel", err)
	}
}

// TestOneItemIsExactlyOneCallPerSlot is the independence rule, asserted rather
// than commented. Five items must be fifteen round trips, never one.
func TestOneItemIsExactlyOneCallPerSlot(t *testing.T) {
	fakes := threeSlots(config.FindingEstablished)
	p, _ := newPanel(t, fakes)

	for index := 0; index < config.ItemCount; index++ {
		p.Adjudicate(context.Background(), testItem(index))
	}

	for slot, f := range fakes {
		if len(f.calls) != config.ItemCount {
			t.Fatalf("slot %d made %d calls for %d items; items must never be batched",
				slot, len(f.calls), config.ItemCount)
		}
	}
}

// TestEachCallCarriesOnlyItsOwnItem is the other half of independence: a slot
// must not be able to see another item's evidence.
func TestEachCallCarriesOnlyItsOwnItem(t *testing.T) {
	fakes := threeSlots(config.FindingEstablished)
	p, _ := newPanel(t, fakes)

	first := testItem(0)
	first.Evidence = "EVIDENCE-FOR-ITEM-ZERO"
	second := testItem(1)
	second.Evidence = "EVIDENCE-FOR-ITEM-ONE"

	p.Adjudicate(context.Background(), first)
	p.Adjudicate(context.Background(), second)

	for slot, f := range fakes {
		if strings.Contains(f.calls[0][1], "EVIDENCE-FOR-ITEM-ONE") {
			t.Fatalf("slot %d saw item 1's evidence while answering about item 0", slot)
		}
		if strings.Contains(f.calls[1][1], "EVIDENCE-FOR-ITEM-ZERO") {
			t.Fatalf("slot %d saw item 0's evidence while answering about item 1", slot)
		}
	}
}

// TestThePromptHashIsSharedByAllThreeSlots is what lets a third party see that
// the three verdicts answered the same question.
func TestThePromptHashIsSharedByAllThreeSlots(t *testing.T) {
	fakes := threeSlots(config.FindingEstablished)
	p, _ := newPanel(t, fakes)

	verdicts := p.Adjudicate(context.Background(), testItem(2))
	if len(verdicts) != config.SlotCount {
		t.Fatalf("got %d verdicts, want %d", len(verdicts), config.SlotCount)
	}

	shared := verdicts[0].PromptHash
	if shared == (evidence.Hash{}) {
		t.Fatal("the prompt hash was never computed")
	}
	for _, v := range verdicts[1:] {
		if v.PromptHash != shared {
			t.Fatal("the three slots do not share one prompt hash")
		}
	}

	// A different item must produce a different hash, or the value identifies
	// nothing.
	other := p.Adjudicate(context.Background(), testItem(3))
	if other[0].PromptHash == shared {
		t.Fatal("two different items produced the same prompt hash")
	}
}

func TestAVerdictCarriesWhatTheChainAndAThirdPartyNeed(t *testing.T) {
	fakes := threeSlots(config.FindingEstablished)
	fakes[0].resolved = "model-0-20260115"
	p, _ := newPanel(t, fakes)

	verdicts := p.Adjudicate(context.Background(), testItem(0))

	v := verdicts[0]
	if v.Refused() {
		t.Fatalf("unexpected refusal: %v", v.Refusal)
	}
	if v.Slot != 0 || v.ItemIndex != 0 {
		t.Errorf("verdict is mislabelled: slot %d item %d", v.Slot, v.ItemIndex)
	}
	if v.Finding != model.Established {
		t.Errorf("Finding = %v", v.Finding)
	}
	if v.Reason == "" || len(v.Reason) > config.MaxReasonBytes {
		t.Errorf("Reason is not within the on-chain bound: %q", v.Reason)
	}
	if v.NarrativeHash == (evidence.Hash{}) {
		t.Error("the narrative was not hashed")
	}
	if v.RequestedModel != "model-0" {
		t.Errorf("RequestedModel = %q", v.RequestedModel)
	}
	if v.ResolvedModel != "model-0-20260115" {
		t.Errorf("ResolvedModel = %q", v.ResolvedModel)
	}
}

// TestTheResolvedModelIsLogged covers the drift a requested identifier cannot
// show.
func TestTheResolvedModelIsLogged(t *testing.T) {
	fakes := threeSlots(config.FindingEstablished)
	fakes[1].resolved = "model-1-SILENTLY-UPGRADED"
	p, logbuf := newPanel(t, fakes)

	p.Adjudicate(context.Background(), testItem(0))
	if !strings.Contains(logbuf.String(), "model-1-SILENTLY-UPGRADED") {
		t.Fatalf("the resolved identifier is not in the log:\n%s", logbuf.String())
	}
	if !strings.Contains(logbuf.String(), "model-1\"") {
		t.Fatalf("the requested identifier should be logged beside it:\n%s", logbuf.String())
	}
}

// TestARefusalIsARefusalAndNotAGuess is the heart of the model-behavior rule.
func TestARefusalIsARefusalAndNotAGuess(t *testing.T) {
	cases := map[string]struct {
		reply string
		err   error
	}{
		"transport failure": {"", errors.New("no route to host")},
		"prose before json": {"Sure! Here you go:\n" + reply(config.FindingEstablished, "r"), nil},
		"outside the enum":  {`{"finding":"MAYBE","reason":"r","narrative":"n"}`, nil},
		"empty reply":       {"", nil},
		"reason over bound": {reply(config.FindingEstablished, strings.Repeat("x", config.MaxReasonBytes+1)), nil},
		"not json at all":   {"I am not able to help with that.", nil},
		"two fence pairs":   {"```json\n" + reply(config.FindingEstablished, "r") + "\n```\n```\n{}\n```", nil},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			fakes := threeSlots(config.FindingEstablished)
			fakes[1].reply = tc.reply
			fakes[1].err = tc.err
			p, logbuf := newPanel(t, fakes)

			verdicts := p.Adjudicate(context.Background(), testItem(0))

			if !verdicts[1].Refused() {
				t.Fatal("the slot should have refused")
			}
			// A refusal must be the ZERO finding, so that if it were ever counted
			// it could only count against the landlord.
			if verdicts[1].Finding != model.NotEstablished {
				t.Fatal("a refusal must leave the finding at NotEstablished")
			}
			if verdicts[1].Reason != "" {
				t.Fatal("a refused slot must not carry a reason to the chain")
			}
			if verdicts[0].Refused() || verdicts[2].Refused() {
				t.Fatal("one slot refusing must not disturb the others")
			}
			if !strings.Contains(logbuf.String(), "REFUSED") {
				t.Fatalf("the refusal was not logged:\n%s", logbuf.String())
			}
			// A refusal must carry the evidence of what was refused.
			if tc.err == nil && !strings.Contains(logbuf.String(), "raw reply") {
				t.Fatalf("the raw reply was not recorded:\n%s", logbuf.String())
			}
		})
	}
}

func TestSpendSurfaceNamesEveryMeteredCallBeforeItIsMade(t *testing.T) {
	fakes := threeSlots(config.FindingEstablished)
	p, _ := newPanel(t, fakes)

	lines := p.SpendSurface()
	if len(lines) != config.SlotCount+1 {
		t.Fatalf("got %d lines, want %d", len(lines), config.SlotCount+1)
	}
	joined := strings.Join(lines, "\n")
	for _, f := range fakes {
		if !strings.Contains(joined, f.id) || !strings.Contains(joined, f.endpoint) {
			t.Fatalf("the spend surface omits a slot:\n%s", joined)
		}
	}
	if !strings.Contains(joined, config.LogTemperatureOmitted) {
		t.Error("the surface should say when the temperature field is omitted")
	}
	if !strings.Contains(joined, config.LogTemperatureSent) {
		t.Error("the surface should say when it is sent")
	}
}

// TestARunawayReplyIsBoundedInTheLog keeps one bad response from flooding a run
// while still recording enough of it to be evidence.
func TestARunawayReplyIsBoundedInTheLog(t *testing.T) {
	fakes := threeSlots(config.FindingEstablished)
	fakes[0].reply = strings.Repeat("A", 5000)
	p, logbuf := newPanel(t, fakes)

	p.Adjudicate(context.Background(), testItem(0))

	written := logbuf.String()
	if !strings.Contains(written, config.TruncationSuffix) {
		t.Fatalf("a runaway reply was not truncated:\n%s", written[:400])
	}
	if len(written) > 5000 {
		t.Fatalf("the log is %d bytes; the bound is not holding", len(written))
	}
}

// TestAPanelWithoutALoggerStillRuns keeps the nil-logger branch honest.
func TestAPanelWithoutALoggerStillRuns(t *testing.T) {
	p, err := panel.New(asClients(threeSlots(config.FindingEstablished)), nil)
	if err != nil {
		t.Fatalf("panel.New: %v", err)
	}
	verdicts := p.Adjudicate(context.Background(), testItem(0))
	if len(verdicts) != config.SlotCount {
		t.Fatalf("got %d verdicts", len(verdicts))
	}
}

// TestThePromptsAskForATokenAndNeverForAnAmount is the copy rule of this system,
// asserted on the prompt actually sent.
func TestThePromptsAskForATokenAndNeverForAnAmount(t *testing.T) {
	system := panel.SystemPrompt()
	user := panel.UserPrompt(testItem(0))

	for _, token := range []string{config.FindingEstablished, config.FindingNotEstablished} {
		if !strings.Contains(system, token) {
			t.Errorf("the system prompt does not name %q", token)
		}
	}
	if !strings.Contains(system, "burden is on the landlord") {
		t.Error("the system prompt must state where the burden sits")
	}
	if !strings.Contains(system, "You do not decide amounts") {
		t.Error("the system prompt must say the model does not decide amounts")
	}
	if !strings.Contains(user, "you do not decide amounts") {
		t.Error("the amount must be marked as context only where it appears")
	}
	if !strings.Contains(user, testItem(0).EvidenceHash.Hex()) {
		t.Error("the user prompt should name the committed evidence hash")
	}
}

// TestTheItemOutcomeIsSummarizedInTheLog gives a reader the count without
// re-deriving it from three separate lines.
func TestTheItemOutcomeIsSummarizedInTheLog(t *testing.T) {
	fakes := threeSlots(config.FindingEstablished)
	fakes[2].reply = reply(config.FindingNotEstablished, "the evidence does not carry it")
	p, logbuf := newPanel(t, fakes)

	p.Adjudicate(context.Background(), testItem(4))
	if !strings.Contains(logbuf.String(), "item 4: 2/3 established, 0 refusals") {
		t.Fatalf("the outcome summary is missing or wrong:\n%s", logbuf.String())
	}
}
