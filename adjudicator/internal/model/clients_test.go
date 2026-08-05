package model_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/pigfox/deposit-dispute/adjudicator/internal/config"
	"github.com/pigfox/deposit-dispute/adjudicator/internal/model"
)

// doerFunc adapts a function to model.Doer. NO TEST IN THIS PACKAGE MAKES A
// VENDOR CALL; every round trip is answered here, so the suite spends nothing.
type doerFunc func(req *http.Request) (*http.Response, error)

func (f doerFunc) Do(req *http.Request) (*http.Response, error) { return f(req) }

// respond builds a canned HTTP response.
func respond(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body))}
}

// unreadableBody fails partway through Read, so the read-body path is reachable.
type unreadableBody struct{}

func (unreadableBody) Read([]byte) (int, error) { return 0, errors.New("connection reset") }
func (unreadableBody) Close() error             { return nil }

const anthropicOK = `{"model":"claude-opus-5-20260115","content":[{"text":"` +
	`{\"finding\":\"ESTABLISHED\",\"reason\":\"r\",\"narrative\":\"n\"}"}]}`

const openAIOK = `{"model":"gpt-5-2026-04-01","choices":[{"message":{"content":"` +
	`{\"finding\":\"NOT_ESTABLISHED\",\"reason\":\"r\",\"narrative\":\"n\"}"}}]}`

func TestNewDispatchesOnProvider(t *testing.T) {
	doer := doerFunc(func(*http.Request) (*http.Response, error) { return respond(200, "{}"), nil })

	anthropic, err := model.New(config.Slot{
		Provider: config.ProviderAnthropic, ModelID: "m", APIKey: "k", SendsTemperature: true,
	}, doer)
	if err != nil {
		t.Fatalf("anthropic: %v", err)
	}
	if anthropic.Endpoint() != config.AnthropicBaseURL {
		t.Errorf("anthropic endpoint = %q", anthropic.Endpoint())
	}

	openai, err := model.New(config.Slot{
		Provider: config.ProviderOpenAI, ModelID: "m", APIKey: "k", SendsTemperature: false,
	}, doer)
	if err != nil {
		t.Fatalf("openai: %v", err)
	}
	if openai.Endpoint() != config.OpenAIBaseURL {
		t.Errorf("openai endpoint = %q", openai.Endpoint())
	}
	if openai.ModelID() != "m" {
		t.Errorf("ModelID = %q", openai.ModelID())
	}

	if _, err := model.New(config.Slot{Provider: "nobody"}, doer); !errors.Is(err, model.ErrUnknownProvider) {
		t.Fatalf("err = %v, want ErrUnknownProvider", err)
	}
}

// TestTheTemperatureFieldIsAbsentNotZeroWhenTurnedOff is the whole point of the
// pointer. A model that REJECTS the field does not run warm when it is sent as
// zero — it does not run.
func TestTheTemperatureFieldIsAbsentNotZeroWhenTurnedOff(t *testing.T) {
	for _, tc := range []struct {
		name     string
		provider string
		ok       string
	}{
		{"anthropic", config.ProviderAnthropic, anthropicOK},
		{"openai", config.ProviderOpenAI, openAIOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, send := range []bool{true, false} {
				var sent map[string]any
				doer := doerFunc(func(req *http.Request) (*http.Response, error) {
					body, _ := io.ReadAll(req.Body)
					_ = json.Unmarshal(body, &sent)
					return respond(200, tc.ok), nil
				})

				client, err := model.New(config.Slot{
					Provider: tc.provider, ModelID: "m", APIKey: "k", SendsTemperature: send,
				}, doer)
				if err != nil {
					t.Fatalf("New: %v", err)
				}
				if _, err := client.Complete(context.Background(), "sys", "usr"); err != nil {
					t.Fatalf("Complete: %v", err)
				}

				value, present := sent["temperature"]
				if send {
					if !present {
						t.Fatal("the field should be on the wire")
					}
					if value != float64(0) {
						t.Fatalf("temperature = %v, want 0", value)
					}
				} else if present {
					t.Fatal("the field must be ABSENT, not zero")
				}
				if client.SendsTemperature() != send {
					t.Fatalf("SendsTemperature() = %v, want %v", client.SendsTemperature(), send)
				}
			}
		})
	}
}

// TestTheResolvedModelIsReportedBack covers the drift the requested identifier
// cannot show: a vendor answering as a different snapshot than the one asked for.
func TestTheResolvedModelIsReportedBack(t *testing.T) {
	for _, tc := range []struct {
		name      string
		provider  string
		body      string
		requested string
		resolved  string
	}{
		{"anthropic", config.ProviderAnthropic, anthropicOK, "claude-opus-5", "claude-opus-5-20260115"},
		{"openai", config.ProviderOpenAI, openAIOK, "gpt-5", "gpt-5-2026-04-01"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doer := doerFunc(func(*http.Request) (*http.Response, error) { return respond(200, tc.body), nil })
			client, err := model.New(config.Slot{
				Provider: tc.provider, ModelID: tc.requested, APIKey: "k", SendsTemperature: true,
			}, doer)
			if err != nil {
				t.Fatalf("New: %v", err)
			}

			reply, err := client.Complete(context.Background(), "sys", "usr")
			if err != nil {
				t.Fatalf("Complete: %v", err)
			}
			if reply.ResolvedModel != tc.resolved {
				t.Fatalf("ResolvedModel = %q, want %q", reply.ResolvedModel, tc.resolved)
			}
			if reply.ResolvedModel == client.ModelID() {
				t.Fatal("this fixture exists to show the two differing")
			}
			if _, err := model.ParseReply(reply.Text); err != nil {
				t.Fatalf("the reply text should still parse: %v", err)
			}
		})
	}
}

func TestTheRequestCarriesTheRightHeadersAndPrompts(t *testing.T) {
	var anthropicReq *http.Request
	anthropicDoer := doerFunc(func(req *http.Request) (*http.Response, error) {
		anthropicReq = req
		return respond(200, anthropicOK), nil
	})
	a := model.NewAnthropicClient("m", "secret-key", anthropicDoer, true)
	if _, err := a.Complete(context.Background(), "sys", "usr"); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if got := anthropicReq.Header.Get(config.HeaderAPIKey); got != "secret-key" {
		t.Errorf("anthropic api key header = %q", got)
	}
	if got := anthropicReq.Header.Get(config.HeaderAnthropicVersion); got != config.AnthropicVersion {
		t.Errorf("anthropic version header = %q", got)
	}

	var openaiReq *http.Request
	openaiDoer := doerFunc(func(req *http.Request) (*http.Response, error) {
		openaiReq = req
		return respond(200, openAIOK), nil
	})
	o := model.NewOpenAIClient("m", "secret-key", openaiDoer, true)
	if _, err := o.Complete(context.Background(), "sys", "usr"); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if got := openaiReq.Header.Get(config.HeaderAuthorization); got != config.BearerPrefix+"secret-key" {
		t.Errorf("openai authorization header = %q", got)
	}
	if got := openaiReq.Header.Get(config.HeaderContentType); got != config.ContentTypeJSON {
		t.Errorf("openai content type = %q", got)
	}
}

// clientUnderTest builds one client of each vendor over the same seams, so the
// failure paths below are asserted for both without duplicating the table.
func clientsUnderTest(doer model.Doer) map[string]model.Client {
	return map[string]model.Client{
		"anthropic": model.NewAnthropicClient("m", "k", doer, true),
		"openai":    model.NewOpenAIClient("m", "k", doer, true),
	}
}

func TestCompleteReportsTransportFailures(t *testing.T) {
	cases := []struct {
		name string
		doer model.Doer
		want error
	}{
		{
			"round trip failed",
			doerFunc(func(*http.Request) (*http.Response, error) { return nil, errors.New("no route") }),
			model.ErrRequestFailed,
		},
		{
			"unreadable body",
			doerFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 200, Body: unreadableBody{}}, nil
			}),
			model.ErrReadBody,
		},
		{
			"non-200",
			doerFunc(func(*http.Request) (*http.Response, error) {
				return respond(429, `{"error":{"message":"rate limited"}}`), nil
			}),
			model.ErrUnexpectedStatus,
		},
		{
			"envelope is not json",
			doerFunc(func(*http.Request) (*http.Response, error) { return respond(200, "not json"), nil }),
			model.ErrDecodeResponse,
		},
	}

	for _, tc := range cases {
		for vendor, client := range clientsUnderTest(tc.doer) {
			t.Run(tc.name+"/"+vendor, func(t *testing.T) {
				if _, err := client.Complete(context.Background(), "sys", "usr"); !errors.Is(err, tc.want) {
					t.Fatalf("err = %v, want %v", err, tc.want)
				}
			})
		}
	}
}

func TestCompleteReportsAnEmptyEnvelope(t *testing.T) {
	empty := map[string]string{
		"anthropic": `{"model":"m","content":[{"text":""}]}`,
		"openai":    `{"model":"m","choices":[{"message":{"content":""}}]}`,
	}
	for vendor, body := range empty {
		t.Run(vendor, func(t *testing.T) {
			doer := doerFunc(func(*http.Request) (*http.Response, error) { return respond(200, body), nil })
			client := clientsUnderTest(doer)[vendor]
			if _, err := client.Complete(context.Background(), "sys", "usr"); !errors.Is(err, model.ErrNoTextContent) {
				t.Fatalf("err = %v, want ErrNoTextContent", err)
			}
		})
	}
}

func TestCompleteReportsAnEncodingFailure(t *testing.T) {
	boom := func(any) ([]byte, error) { return nil, errors.New("cannot encode") }
	doer := doerFunc(func(*http.Request) (*http.Response, error) { return respond(200, "{}"), nil })

	a := model.NewAnthropicClient("m", "k", doer, true)
	a.Marshal = boom
	if _, err := a.Complete(context.Background(), "s", "u"); !errors.Is(err, model.ErrEncodeRequest) {
		t.Fatalf("anthropic err = %v, want ErrEncodeRequest", err)
	}

	o := model.NewOpenAIClient("m", "k", doer, true)
	o.Marshal = boom
	if _, err := o.Complete(context.Background(), "s", "u"); !errors.Is(err, model.ErrEncodeRequest) {
		t.Fatalf("openai err = %v, want ErrEncodeRequest", err)
	}
}

func TestCompleteReportsAnUnbuildableRequest(t *testing.T) {
	doer := doerFunc(func(*http.Request) (*http.Response, error) { return respond(200, "{}"), nil })
	const badURL = "://not a url"

	a := model.NewAnthropicClient("m", "k", doer, true)
	a.BaseURL = badURL
	if _, err := a.Complete(context.Background(), "s", "u"); !errors.Is(err, model.ErrBuildRequest) {
		t.Fatalf("anthropic err = %v, want ErrBuildRequest", err)
	}

	o := model.NewOpenAIClient("m", "k", doer, true)
	o.BaseURL = badURL
	if _, err := o.Complete(context.Background(), "s", "u"); !errors.Is(err, model.ErrBuildRequest) {
		t.Fatalf("openai err = %v, want ErrBuildRequest", err)
	}
}

// TestAnErrorBodyIsBoundedAndCarriesNoKey keeps a runaway vendor response out of
// the log and a credential out of the error.
func TestAnErrorBodyIsBoundedAndCarriesNoKey(t *testing.T) {
	huge := strings.Repeat("e", config.ErrorBodyMaxLen*4)
	doer := doerFunc(func(*http.Request) (*http.Response, error) { return respond(500, huge), nil })

	for vendor, client := range clientsUnderTest(doer) {
		t.Run(vendor, func(t *testing.T) {
			_, err := client.Complete(context.Background(), "sys", "usr")
			if err == nil {
				t.Fatal("expected a failure")
			}
			msg := err.Error()
			if !strings.Contains(msg, config.TruncationSuffix) {
				t.Error("a long error body was not truncated")
			}
			if len(msg) > config.ErrorBodyMaxLen*2 {
				t.Errorf("the error is %d bytes; the bound is not holding", len(msg))
			}
			if strings.Contains(msg, "k") && strings.Contains(msg, "api") {
				t.Error("the error mentions the key material")
			}
		})
	}
}
