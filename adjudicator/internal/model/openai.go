package model

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/pigfox/deposit-dispute/adjudicator/internal/config"
)

// OpenAIClient is a Client backed by the OpenAI Chat Completions API.
//
// It is interchangeable with AnthropicClient: same prompts in, same raw text out,
// and the caller still runs the reply through ParseReply. Only the request
// envelope and the auth header differ — OpenAI carries the system prompt as a
// message with role "system", where Anthropic takes it as a top-level field.
type OpenAIClient struct {
	// BaseURL is the Chat Completions endpoint.
	BaseURL string
	// APIKey authenticates the request. Never logged.
	APIKey string
	// Model is the pinned model id this slot speaks for.
	Model string
	// MaxTokens bounds the reply.
	MaxTokens int
	// Temperature is sent when non-nil and OMITTED ENTIRELY when nil. Same
	// reasoning as the Anthropic client.
	Temperature *int
	// HTTP performs the round trip.
	HTTP Doer
	// Marshal encodes the request body.
	Marshal Marshaller
}

// NewOpenAIClient builds an OpenAIClient wired to the real endpoint.
func NewOpenAIClient(modelID, apiKey string, doer Doer, sendTemperature bool) *OpenAIClient {
	c := &OpenAIClient{
		BaseURL:   config.OpenAIBaseURL,
		APIKey:    apiKey,
		Model:     modelID,
		MaxTokens: config.MaxTokens,
		HTTP:      doer,
		Marshal:   json.Marshal,
	}
	if sendTemperature {
		t := config.Temperature
		c.Temperature = &t
	}
	return c
}

// ModelID is the pinned identifier this client speaks for.
func (c *OpenAIClient) ModelID() string { return c.Model }

// Endpoint is the URL this client posts to.
func (c *OpenAIClient) Endpoint() string { return c.BaseURL }

// SendsTemperature reports whether the temperature field goes on the wire.
func (c *OpenAIClient) SendsTemperature() bool { return c.Temperature != nil }

// openAIRequest is the Chat Completions request body.
type openAIRequest struct {
	Model       string    `json:"model"`
	MaxTokens   int       `json:"max_completion_tokens"`
	Temperature *int      `json:"temperature,omitempty"`
	Messages    []message `json:"messages"`
}

// openAIChoice is one entry in the response `choices` array.
type openAIChoice struct {
	Message struct {
		Content string `json:"content"`
	} `json:"message"`
}

// openAIResponse is the subset of the Chat Completions response this package
// reads.
type openAIResponse struct {
	// Model is what the vendor says it actually ran. An alias like "gpt-5"
	// resolves to a dated snapshot here, which is the divergence worth logging.
	Model   string         `json:"model"`
	Choices []openAIChoice `json:"choices"`
}

// Complete implements Client against the OpenAI Chat Completions API.
func (c *OpenAIClient) Complete(ctx context.Context, systemPrompt, userPrompt string) (Reply, error) {
	body, err := c.Marshal(openAIRequest{
		Model:       c.Model,
		MaxTokens:   c.MaxTokens,
		Temperature: c.Temperature,
		Messages: []message{
			{Role: config.RoleSystem, Content: systemPrompt},
			{Role: config.RoleUser, Content: userPrompt},
		},
	})
	if err != nil {
		return Reply{}, fmt.Errorf("%w: %w", ErrEncodeRequest, err)
	}

	req, err := http.NewRequestWithContext(ctx, config.HTTPMethodPost, c.BaseURL, bytes.NewReader(body))
	if err != nil {
		return Reply{}, fmt.Errorf("%w: %w", ErrBuildRequest, err)
	}
	req.Header.Set(config.HeaderAuthorization, config.BearerPrefix+c.APIKey)
	req.Header.Set(config.HeaderContentType, config.ContentTypeJSON)

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return Reply{}, fmt.Errorf("%w: %w", ErrRequestFailed, err)
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return Reply{}, fmt.Errorf("%w: %w", ErrReadBody, err)
	}

	if resp.StatusCode != config.HTTPStatusOK {
		return Reply{}, fmt.Errorf("%w: %d: %s",
			ErrUnexpectedStatus, resp.StatusCode, truncate(string(raw), config.ErrorBodyMaxLen))
	}

	var decoded openAIResponse
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return Reply{}, fmt.Errorf("%w: %w", ErrDecodeResponse, err)
	}

	for _, choice := range decoded.Choices {
		if choice.Message.Content != "" {
			return Reply{Text: choice.Message.Content, ResolvedModel: decoded.Model}, nil
		}
	}
	return Reply{}, ErrNoTextContent
}
