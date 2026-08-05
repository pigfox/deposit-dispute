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

// AnthropicClient is a Client backed by the Anthropic Messages API.
type AnthropicClient struct {
	// BaseURL is the Messages endpoint.
	BaseURL string
	// APIKey authenticates the request. Never logged.
	APIKey string
	// Model is the pinned model id this slot speaks for.
	Model string
	// MaxTokens bounds the reply.
	MaxTokens int
	// Temperature is sent when non-nil and OMITTED ENTIRELY when nil. Some models
	// reject the field rather than ignoring it, so the absence has to be
	// structural: a zero in a pointer-free struct would still be serialized.
	Temperature *int
	// HTTP performs the round trip.
	HTTP Doer
	// Marshal encodes the request body.
	Marshal Marshaller
}

// NewAnthropicClient builds an AnthropicClient wired to the real endpoint.
func NewAnthropicClient(modelID, apiKey string, doer Doer, sendTemperature bool) *AnthropicClient {
	c := &AnthropicClient{
		BaseURL:   config.AnthropicBaseURL,
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
func (c *AnthropicClient) ModelID() string { return c.Model }

// Endpoint is the URL this client posts to.
func (c *AnthropicClient) Endpoint() string { return c.BaseURL }

// SendsTemperature reports whether the temperature field goes on the wire.
func (c *AnthropicClient) SendsTemperature() bool { return c.Temperature != nil }

// anthropicRequest is the Messages request body. Anthropic takes the system
// prompt as a top-level field rather than as a message.
type anthropicRequest struct {
	Model       string    `json:"model"`
	MaxTokens   int       `json:"max_tokens"`
	Temperature *int      `json:"temperature,omitempty"`
	System      string    `json:"system"`
	Messages    []message `json:"messages"`
}

// anthropicContent is one block in the response `content` array.
type anthropicContent struct {
	Text string `json:"text"`
}

// anthropicResponse is the subset of the Messages response this package reads.
type anthropicResponse struct {
	// Model is what the vendor says it actually ran.
	Model   string             `json:"model"`
	Content []anthropicContent `json:"content"`
}

// Complete implements Client against the Anthropic Messages API.
func (c *AnthropicClient) Complete(ctx context.Context, systemPrompt, userPrompt string) (Reply, error) {
	body, err := c.Marshal(anthropicRequest{
		Model:       c.Model,
		MaxTokens:   c.MaxTokens,
		Temperature: c.Temperature,
		System:      systemPrompt,
		Messages:    []message{{Role: config.RoleUser, Content: userPrompt}},
	})
	if err != nil {
		return Reply{}, fmt.Errorf("%w: %w", ErrEncodeRequest, err)
	}

	req, err := http.NewRequestWithContext(ctx, config.HTTPMethodPost, c.BaseURL, bytes.NewReader(body))
	if err != nil {
		return Reply{}, fmt.Errorf("%w: %w", ErrBuildRequest, err)
	}
	req.Header.Set(config.HeaderAPIKey, c.APIKey)
	req.Header.Set(config.HeaderAnthropicVersion, config.AnthropicVersion)
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
		// The status alone cannot distinguish a bad model id from an exhausted
		// quota from a rejected parameter, and the body already read here says
		// which. It carries the vendor's error description, never the request and
		// never the key.
		return Reply{}, fmt.Errorf("%w: %d: %s",
			ErrUnexpectedStatus, resp.StatusCode, truncate(string(raw), config.ErrorBodyMaxLen))
	}

	var decoded anthropicResponse
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return Reply{}, fmt.Errorf("%w: %w", ErrDecodeResponse, err)
	}

	for _, block := range decoded.Content {
		if block.Text != "" {
			return Reply{Text: block.Text, ResolvedModel: decoded.Model}, nil
		}
	}
	return Reply{}, ErrNoTextContent
}
