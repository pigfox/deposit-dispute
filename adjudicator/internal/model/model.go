// Package model wraps the vendor APIs the adjudicator slots speak to and turns a
// model's reply into a constrained finding.
//
// The Client interface is the seam every test uses: the real implementations
// speak HTTP, and the fakes return canned text or errors without touching the
// network. THE TEST SUITE MAKES NO VENDOR CALL AND SPENDS NOTHING.
package model

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/pigfox/deposit-dispute/adjudicator/internal/config"
)

// Transport and envelope errors.
var (
	// ErrRequestFailed means the HTTP round trip did not complete.
	ErrRequestFailed = errors.New("model: request failed")
	// ErrUnexpectedStatus means the API answered with a non-200 status.
	ErrUnexpectedStatus = errors.New("model: unexpected status")
	// ErrReadBody means the response body could not be read.
	ErrReadBody = errors.New("model: reading response body")
	// ErrDecodeResponse means the API envelope was not valid JSON.
	ErrDecodeResponse = errors.New("model: decoding response")
	// ErrNoTextContent means the envelope carried no answer.
	ErrNoTextContent = errors.New("model: response had no text content")
	// ErrBuildRequest means the HTTP request could not be constructed.
	ErrBuildRequest = errors.New("model: building request")
	// ErrEncodeRequest means the request body could not be marshalled.
	ErrEncodeRequest = errors.New("model: encoding request")
	// ErrUnknownProvider means a slot named a backend this package does not
	// implement.
	ErrUnknownProvider = errors.New("model: unknown provider")
)

// Reply-content errors. Each one is a REFUSAL: the slot abstains for this item
// and the abstention collapses to NotEstablished, which is the contract's zero
// value and the correct answer when the landlord has not established a thing.
var (
	// ErrEmptyReply means the model returned nothing to parse.
	ErrEmptyReply = errors.New("model: the model returned an empty reply")
	// ErrProseBeforePayload means the reply carried text outside the payload.
	ErrProseBeforePayload = errors.New(
		"model: the reply carries prose outside the payload; no JSON is hunted for inside it")
	// ErrMalformedReply means the reply was not the agreed JSON object.
	ErrMalformedReply = errors.New("model: the model returned malformed JSON")
	// ErrUnknownFinding means the finding was not one of the two permitted tokens.
	ErrUnknownFinding = errors.New("model: the model returned a finding outside the enum")
	// ErrEmptyReason means the reason was blank, which the chain would store as
	// nothing readable.
	ErrEmptyReason = errors.New("model: the model returned an empty reason")
	// ErrReasonTooLong means the reason exceeded the on-chain bound.
	ErrReasonTooLong = errors.New("model: the reason exceeds the on-chain bound")
	// ErrEmptyNarrative means the narrative was blank, so narrativeHash would pin
	// nothing.
	ErrEmptyNarrative = errors.New("model: the model returned an empty narrative")
)

// Finding is what an adjudicator may conclude about one line item. The values
// match the contract's ItemFinding enum exactly, and NotEstablished is zero in
// both.
type Finding uint8

// The two findings. There is no abstain member, in this package or in the
// contract: a slot that cannot answer refuses, and a refusal is counted as
// NotEstablished because the burden sits on the landlord.
const (
	NotEstablished Finding = 0
	Established    Finding = 1
)

// String renders a finding as the token the model is asked to answer with.
func (f Finding) String() string {
	if f == Established {
		return config.FindingEstablished
	}
	return config.FindingNotEstablished
}

// ParseFinding maps one of the two permitted tokens to a Finding.
//
// EXACTLY TWO STRINGS PARSE, after trimming and upper-casing. "established, I
// think", "ESTABLISHED (probably)" and "yes" are all refusals. A near-miss is not
// evidence of intent — it is evidence that the model was not answering the
// question it was asked, and a verdict that had to be interpreted is not one a
// third party could re-run and compare.
func ParseFinding(s string) (Finding, error) {
	switch strings.ToUpper(strings.TrimSpace(s)) {
	case config.FindingEstablished:
		return Established, nil
	case config.FindingNotEstablished:
		return NotEstablished, nil
	default:
		return NotEstablished, fmt.Errorf("%w: %q", ErrUnknownFinding, s)
	}
}

// Reply is one vendor round trip's result.
type Reply struct {
	// Text is the model's raw answer, before any constraint is applied.
	Text string
	// ResolvedModel is the model identifier THE VENDOR SAYS IT RAN, which is not
	// always the one that was requested — an alias, a dated snapshot or a silent
	// upgrade all show up here. Empty when the API does not expose it. Logged
	// beside the requested identifier so a divergence is visible in the run
	// rather than inferred later from a changed answer.
	ResolvedModel string
}

// Client is an adjudicator slot's view of its model.
type Client interface {
	// Complete sends the prompts and returns the model's raw reply.
	Complete(ctx context.Context, systemPrompt, userPrompt string) (Reply, error)
	// ModelID is the pinned identifier this slot speaks for. It is what the
	// contract's slot declares, and its keccak256 is the published modelIdHash.
	ModelID() string
	// Endpoint is the URL this client posts to. Published in the spend surface so
	// a metered call is visible before it is made.
	Endpoint() string
	// SendsTemperature reports whether the temperature field goes on the wire.
	SendsTemperature() bool
}

// Doer is the subset of *http.Client the clients need, so tests can swap in a
// transport that fails or returns an unreadable body.
type Doer interface {
	Do(req *http.Request) (*http.Response, error)
}

// Marshaller encodes the request body. It is a field rather than a direct call to
// json.Marshal so the encoding failure path is reachable from a test.
type Marshaller func(v any) ([]byte, error)

// message is one entry in a `messages` array. Both vendors use this shape.
type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// New builds the client for one slot from its pinned provider and model id.
//
// THE TEMPERATURE VALUE IS NOT A PARAMETER — it is zero wherever it is sent, and
// nothing can raise it. What IS a parameter is WHETHER the field is sent at all,
// because that is a fact about the vendor's API rather than a setting: some
// models reject the field outright, and a request carrying a parameter the model
// refuses does not run at a different temperature, it does not run.
func New(slot config.Slot, doer Doer) (Client, error) {
	switch slot.Provider {
	case config.ProviderAnthropic:
		return NewAnthropicClient(slot.ModelID, slot.APIKey, doer, slot.SendsTemperature), nil
	case config.ProviderOpenAI:
		return NewOpenAIClient(slot.ModelID, slot.APIKey, doer, slot.SendsTemperature), nil
	default:
		return nil, fmt.Errorf("%w: %q", ErrUnknownProvider, slot.Provider)
	}
}

// payload is the JSON contract the system prompt demands.
type payload struct {
	Finding   string `json:"finding"`
	Reason    string `json:"reason"`
	Narrative string `json:"narrative"`
}

// Parsed is one model's answer, after the reply has been constrained.
type Parsed struct {
	// Finding is the enum value.
	Finding Finding
	// Reason is the short summary the chain stores verbatim.
	Reason string
	// Narrative is the full reasoning, hashed on chain but not stored.
	Narrative string
}

// ParseReply turns a model's raw text into a constrained answer, refusing
// anything the contract would refuse and anything that would have to be
// interpreted.
//
// STRICT ABOUT THE DECISION. TOLERANT OF EXACTLY ONE ENVELOPE, AND NOTHING ELSE.
//
// A PINNED MODEL IDENTIFIER DOES NOT PIN MODEL BEHAVIOR. The same identifier can
// start fencing its replies, start prefacing them, or start returning a
// near-miss token, with no version change and no notice. This parser draws one
// syntactic line and refuses everything on the far side of it:
//
//   - A CODE FENCE IS TRANSPORT and is stripped — but only when the ENTIRE reply
//     is one fence pair. The payload inside was already fully constrained, so
//     refusing it would have been strictness about the envelope rather than about
//     the decision.
//   - PROSE OUTSIDE THE PAYLOAD IS A REFUSAL. No preamble is skipped, no JSON is
//     hunted for inside prose, no candidate substrings are tried, and there is no
//     looser second attempt when the strict parse fails. "Strip obvious wrappers"
//     is a slope; one syntactic shape is not.
//
// The line is syntactic rather than a judgement, which is what stops the
// tolerance growing. Every refusal returns a named error, is logged with the item
// and slot it came from, and abstains — which the contract reads as
// NotEstablished.
func ParseReply(raw string) (Parsed, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return Parsed{}, ErrEmptyReply
	}
	trimmed = stripFence(trimmed)

	// Named ahead of the JSON decoder so the common drift — a model that starts
	// explaining itself before answering — reports what actually happened instead
	// of a generic syntax error.
	if !strings.HasPrefix(trimmed, "{") {
		return Parsed{}, fmt.Errorf("%w: reply begins %q",
			ErrProseBeforePayload, truncate(trimmed, prosePreviewLen))
	}

	var decoded payload
	if err := json.Unmarshal([]byte(trimmed), &decoded); err != nil {
		return Parsed{}, fmt.Errorf("%w: %w", ErrMalformedReply, err)
	}

	finding, err := ParseFinding(decoded.Finding)
	if err != nil {
		return Parsed{}, err
	}

	reason := strings.TrimSpace(decoded.Reason)
	if reason == "" {
		return Parsed{}, ErrEmptyReason
	}
	if len(reason) > config.MaxReasonBytes {
		return Parsed{}, fmt.Errorf("%w: %d > %d bytes",
			ErrReasonTooLong, len(reason), config.MaxReasonBytes)
	}

	narrative := strings.TrimSpace(decoded.Narrative)
	if narrative == "" {
		return Parsed{}, ErrEmptyNarrative
	}

	return Parsed{Finding: finding, Reason: reason, Narrative: narrative}, nil
}

// prosePreviewLen bounds how much of a refused reply is quoted back.
const prosePreviewLen = 60

// stripFence removes a code fence when — and ONLY when — the entire reply is one
// fence pair. Anything else is returned untouched, so it goes on to be refused
// exactly as it was before.
//
// THE WHOLE POINT IS THAT THIS RULE HAS NO JUDGEMENT IN IT. It is: the text
// starts with a fence delimiter, optionally followed by a one-word language tag
// on that same line, and ends with a fence delimiter, and nothing survives
// outside those two. A preamble before the fence, a postamble after it, a second
// fence pair, or a lone opening fence all fall through unchanged.
func stripFence(s string) string {
	if !strings.HasPrefix(s, config.FenceDelimiter) || !strings.HasSuffix(s, config.FenceDelimiter) {
		return s
	}
	// Both delimiters must be present AND distinct. A bare "```" satisfies the
	// prefix and the suffix test while being one delimiter, not a pair.
	if len(s) < 2*len(config.FenceDelimiter) {
		return s
	}

	// Both delimiters are known to be there, so take them off first and reason
	// about what was between them. Doing it in this order — rather than hunting
	// for the closing delimiter after splitting the first line — is what keeps
	// every branch below reachable: a check that could never fire is not a safety
	// net, it is a claim about a risk that does not exist here.
	inner := s[len(config.FenceDelimiter) : len(s)-len(config.FenceDelimiter)]

	// The opening delimiter runs to the end of its line, which is where a
	// language tag would sit. There must BE such a line break.
	lineBreak := strings.IndexByte(inner, '\n')
	if lineBreak < 0 {
		return s
	}
	if tag := strings.TrimSpace(inner[:lineBreak]); strings.ContainsAny(tag, " \t") {
		// A language tag is one word. Anything else is prose on the opening line,
		// which means this is not the shape being permitted.
		return s
	}

	body := inner[lineBreak+1:]

	// Any remaining delimiter means more than one pair was present, and choosing
	// between them would be exactly the judgement this rule refuses to make.
	if strings.Contains(body, config.FenceDelimiter) {
		return s
	}
	return strings.TrimSpace(body)
}

// truncate bounds a string so a runaway response cannot flood the log, while
// keeping enough of it to be diagnostic.
func truncate(s string, limit int) string {
	s = strings.TrimSpace(s)
	if len(s) <= limit {
		return s
	}
	return s[:limit] + config.TruncationSuffix
}
