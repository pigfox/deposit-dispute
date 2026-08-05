package model_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/pigfox/deposit-dispute/adjudicator/internal/config"
	"github.com/pigfox/deposit-dispute/adjudicator/internal/model"
)

// good is a reply that parses. Tests copy it and break one thing.
const good = `{"finding":"ESTABLISHED","reason":"the check-in report shows it clean","narrative":"full reasoning"}`

func TestParseReplyAcceptsTheAgreedShape(t *testing.T) {
	got, err := model.ParseReply(good)
	if err != nil {
		t.Fatalf("ParseReply: %v", err)
	}
	if got.Finding != model.Established {
		t.Errorf("Finding = %v, want Established", got.Finding)
	}
	if got.Reason != "the check-in report shows it clean" {
		t.Errorf("Reason = %q", got.Reason)
	}
	if got.Narrative != "full reasoning" {
		t.Errorf("Narrative = %q", got.Narrative)
	}
}

// TestParseReplyStripsAWholeCodeFence is the drift this parser tolerates, and
// the ONLY one. A fence is transport: the payload inside it was already fully
// constrained.
func TestParseReplyStripsAWholeCodeFence(t *testing.T) {
	cases := map[string]string{
		"bare fence":     "```\n" + good + "\n```",
		"tagged fence":   "```json\n" + good + "\n```",
		"trailing space": "  ```json\n" + good + "\n```  ",
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := model.ParseReply(raw)
			if err != nil {
				t.Fatalf("ParseReply: %v", err)
			}
			if got.Finding != model.Established {
				t.Errorf("Finding = %v", got.Finding)
			}
		})
	}
}

// TestParseReplyRefusesEverythingAroundAFence is what stops the tolerance
// becoming a slope. Each of these falls through the fence rule unchanged and is
// then refused.
func TestParseReplyRefusesEverythingAroundAFence(t *testing.T) {
	cases := map[string]string{
		"preamble before the fence":    "Here is my answer:\n```json\n" + good + "\n```",
		"postamble after the fence":    "```json\n" + good + "\n```\nHope that helps!",
		"two fence pairs":              "```json\n" + good + "\n```\n```json\n" + good + "\n```",
		"lone opening fence":           "```json\n" + good,
		"all on one line":              "```json " + good + "```",
		"prose on the opening line":    "```json here you go\n" + good + "\n```",
		"a bare delimiter and nothing": "```",
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := model.ParseReply(raw); err == nil {
				t.Fatal("a reply that is not exactly one fence pair must be refused")
			}
		})
	}
}

// TestParseReplyNamesProseOutsideThePayload proves the common drift reports what
// actually happened rather than a generic syntax error.
func TestParseReplyNamesProseOutsideThePayload(t *testing.T) {
	raw := "Sure! Based on the evidence provided, here is my assessment:\n" + good

	_, err := model.ParseReply(raw)
	if !errors.Is(err, model.ErrProseBeforePayload) {
		t.Fatalf("err = %v, want ErrProseBeforePayload", err)
	}
	if strings.Contains(err.Error(), "ESTABLISHED") {
		t.Error("the refusal should quote the prose, not the payload it declined to read")
	}
}

// TestParseReplyNeverHuntsForJSONInsideProse is the rule stated as a test. There
// IS a valid payload in each of these; the parser must not go looking for it.
func TestParseReplyNeverHuntsForJSONInsideProse(t *testing.T) {
	cases := []string{
		"I think " + good + " is my answer.",
		"Answer: " + good,
		good + "\n\nLet me know if you need more detail.",
	}
	for _, raw := range cases {
		if _, err := model.ParseReply(raw); err == nil {
			t.Fatalf("the parser salvaged a payload out of prose: %q", raw)
		}
	}
}

func TestParseReplyRefuses(t *testing.T) {
	long := strings.Repeat("x", config.MaxReasonBytes+1)

	cases := []struct {
		name string
		raw  string
		want error
	}{
		{"empty", "", model.ErrEmptyReply},
		{"whitespace only", "   \n\t ", model.ErrEmptyReply},
		// Starts with `{`, so it reaches the decoder and fails there.
		{"truncated object", `{"finding":"ESTABLISHED","reason":"r","narrative":"n"`, model.ErrMalformedReply},
		// Does not start with `{`, so it is refused before the decoder sees it —
		// a JSON array is still not the agreed payload.
		{"json that is not an object", `["ESTABLISHED"]`, model.ErrProseBeforePayload},
		{
			"a finding outside the enum",
			`{"finding":"PROBABLY","reason":"r","narrative":"n"}`,
			model.ErrUnknownFinding,
		},
		{
			"a hedged finding",
			`{"finding":"ESTABLISHED, on balance","reason":"r","narrative":"n"}`,
			model.ErrUnknownFinding,
		},
		{
			"an empty reason",
			`{"finding":"ESTABLISHED","reason":"  ","narrative":"n"}`,
			model.ErrEmptyReason,
		},
		{
			"a reason past the on-chain bound",
			`{"finding":"ESTABLISHED","reason":"` + long + `","narrative":"n"}`,
			model.ErrReasonTooLong,
		},
		{
			"an empty narrative",
			`{"finding":"ESTABLISHED","reason":"r","narrative":""}`,
			model.ErrEmptyNarrative,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := model.ParseReply(tc.raw); !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
}

// TestAReasonExactlyAtTheBoundIsAccepted pins the boundary the contract is
// written on, so the agent and the chain agree about what "too long" means.
func TestAReasonExactlyAtTheBoundIsAccepted(t *testing.T) {
	raw := `{"finding":"NOT_ESTABLISHED","reason":"` +
		strings.Repeat("x", config.MaxReasonBytes) + `","narrative":"n"}`

	got, err := model.ParseReply(raw)
	if err != nil {
		t.Fatalf("a reason of exactly %d bytes must be accepted: %v", config.MaxReasonBytes, err)
	}
	if got.Finding != model.NotEstablished {
		t.Errorf("Finding = %v, want NotEstablished", got.Finding)
	}
}

func TestParseFinding(t *testing.T) {
	accepted := map[string]model.Finding{
		config.FindingEstablished:    model.Established,
		config.FindingNotEstablished: model.NotEstablished,
		"  established  ":            model.Established,
		"not_established":            model.NotEstablished,
	}
	for raw, want := range accepted {
		got, err := model.ParseFinding(raw)
		if err != nil {
			t.Errorf("ParseFinding(%q): %v", raw, err)
			continue
		}
		if got != want {
			t.Errorf("ParseFinding(%q) = %v, want %v", raw, got, want)
		}
	}

	// A near-miss is not evidence of intent.
	for _, raw := range []string{"", "yes", "no", "true", "ESTABLISH", "ESTABLISHED!", "abstain", "unknown"} {
		if _, err := model.ParseFinding(raw); !errors.Is(err, model.ErrUnknownFinding) {
			t.Errorf("ParseFinding(%q) err = %v, want ErrUnknownFinding", raw, err)
		}
	}
}

// TestARefusalCollapsesToNotEstablished pins the safety direction of the whole
// design: whatever goes wrong, the tenant keeps the money.
func TestARefusalCollapsesToNotEstablished(t *testing.T) {
	got, err := model.ParseFinding("something the panel cannot read")
	if err == nil {
		t.Fatal("expected a refusal")
	}
	if got != model.NotEstablished {
		t.Fatal("a refused parse must yield the zero value, which the contract reads as NotEstablished")
	}
	if model.NotEstablished != 0 {
		t.Fatal("NotEstablished must be zero to match the contract's enum")
	}
}

func TestFindingStringsAreTheTokensTheModelIsAskedFor(t *testing.T) {
	if model.Established.String() != config.FindingEstablished {
		t.Errorf("Established.String() = %q", model.Established.String())
	}
	if model.NotEstablished.String() != config.FindingNotEstablished {
		t.Errorf("NotEstablished.String() = %q", model.NotEstablished.String())
	}
	// Anything outside the enum reads as the safe answer rather than as a panic
	// or an empty string that would serialize as nothing.
	if model.Finding(9).String() != config.FindingNotEstablished {
		t.Error("an out-of-range Finding must render as NOT_ESTABLISHED")
	}
}
