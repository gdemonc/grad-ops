// Package llm is an OpenAI-compatible chat client.
//
// The client is deliberately small: one request shape, a hard response size
// bound, a timeout and no logging of the API key. It parses the model's text but
// never treats it as an instruction; callers validate the content before using
// it.
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

var (
	// ErrInvalidConfig means the client was configured with unusable values.
	ErrInvalidConfig = errors.New("invalid LLM configuration")
	// ErrResponseTooLarge means the provider returned more bytes than allowed.
	ErrResponseTooLarge = errors.New("LLM response exceeded the configured limit")
	// ErrEmptyResponse means the provider returned no usable choice.
	ErrEmptyResponse = errors.New("LLM returned no content")
)

// Request is one chat completion request.
type Request struct {
	System    string
	User      string
	MaxTokens int
}

// Usage reports token accounting when the provider supplies it.
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// Response is the text the model produced.
type Response struct {
	Content string
	Model   string
	Usage   Usage
}

// Completer is the seam between the investigation planner and a provider.
type Completer interface {
	Complete(ctx context.Context, req Request) (Response, error)
}

// CompleterFunc adapts a function to Completer. It is useful in tests and for
// embedding a deterministic planner.
type CompleterFunc func(ctx context.Context, req Request) (Response, error)

// Complete implements Completer.
func (f CompleterFunc) Complete(ctx context.Context, req Request) (Response, error) {
	return f(ctx, req)
}

// Config is the OpenAI-compatible client configuration.
type Config struct {
	// BaseURL is the API root, for example https://api.example.com/v1.
	BaseURL string
	// APIKey is sent as a bearer token when non-empty.
	APIKey string
	// Model is the provider's model name.
	Model string
	// Timeout bounds one request, including reading the response.
	Timeout time.Duration
	// MaxResponseBytes bounds the response body.
	MaxResponseBytes int64
	// HTTPClient overrides the default client. The timeout still applies
	// through the request context.
	HTTPClient *http.Client
}

// OpenAI is an OpenAI-compatible chat completion client.
type OpenAI struct {
	endpoint         string
	apiKey           string
	model            string
	timeout          time.Duration
	maxResponseBytes int64
	client           *http.Client
}

// NewOpenAI validates cfg and builds a client.
func NewOpenAI(cfg Config) (*OpenAI, error) {
	base := strings.TrimSpace(cfg.BaseURL)
	if base == "" {
		return nil, fmt.Errorf("%w: base URL is required", ErrInvalidConfig)
	}
	parsed, err := url.Parse(base)
	if err != nil {
		return nil, fmt.Errorf("%w: parse base URL: %v", ErrInvalidConfig, err)
	}
	if parsed.User != nil {
		return nil, fmt.Errorf("%w: base URL must not contain credentials", ErrInvalidConfig)
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, fmt.Errorf("%w: base URL must not contain a query or fragment", ErrInvalidConfig)
	}
	switch parsed.Scheme {
	case "https":
	case "http":
		// Plain HTTP is acceptable only when the endpoint is on this host, which
		// is how a local mock or sidecar is exercised. A remote key must not
		// cross the network in clear text.
		if !loopbackHost(parsed.Hostname()) {
			return nil, fmt.Errorf("%w: plain HTTP is allowed only for a loopback endpoint", ErrInvalidConfig)
		}
	default:
		return nil, fmt.Errorf("%w: base URL must use http or https", ErrInvalidConfig)
	}

	if strings.TrimSpace(cfg.Model) == "" {
		return nil, fmt.Errorf("%w: model is required", ErrInvalidConfig)
	}
	if cfg.Timeout <= 0 {
		return nil, fmt.Errorf("%w: timeout must be positive", ErrInvalidConfig)
	}
	if cfg.MaxResponseBytes <= 0 {
		return nil, fmt.Errorf("%w: response limit must be positive", ErrInvalidConfig)
	}

	client := cfg.HTTPClient
	if client == nil {
		// A provider redirect could send the request, including the bearer
		// token, to a different endpoint. The configured URL is the only one
		// this client talks to.
		client = &http.Client{
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		}
	}

	return &OpenAI{
		endpoint:         strings.TrimRight(parsed.String(), "/") + "/chat/completions",
		apiKey:           strings.TrimSpace(cfg.APIKey),
		model:            strings.TrimSpace(cfg.Model),
		timeout:          cfg.Timeout,
		maxResponseBytes: cfg.MaxResponseBytes,
		client:           client,
	}, nil
}

// Complete sends one chat request and returns the first choice's text.
func (c *OpenAI) Complete(ctx context.Context, req Request) (Response, error) {
	if strings.TrimSpace(req.User) == "" {
		return Response{}, fmt.Errorf("%w: user message is required", ErrInvalidConfig)
	}

	payload := chatRequest{
		Model: c.model,
		Messages: []message{
			{Role: "system", Content: req.System},
			{Role: "user", Content: req.User},
		},
		Temperature: 0,
		MaxTokens:   req.MaxTokens,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return Response{}, fmt.Errorf("encode LLM request: %w", err)
	}

	callCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(callCtx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return Response{}, fmt.Errorf("build LLM request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")
	if c.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	httpResp, err := c.client.Do(httpReq)
	if err != nil {
		// The URL may contain a tenant or deployment path; the key is never in
		// it. Returning the transport error is still more useful than hiding it.
		return Response{}, fmt.Errorf("LLM request failed: %w", err)
	}
	defer httpResp.Body.Close()

	limited := io.LimitReader(httpResp.Body, c.maxResponseBytes+1)
	raw, err := io.ReadAll(limited)
	if err != nil {
		return Response{}, fmt.Errorf("read LLM response: %w", err)
	}
	if int64(len(raw)) > c.maxResponseBytes {
		return Response{}, ErrResponseTooLarge
	}
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		return Response{}, fmt.Errorf("LLM provider returned HTTP %d: %s", httpResp.StatusCode, abbreviated(raw))
	}

	var decoded chatResponse
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return Response{}, fmt.Errorf("decode LLM response: %w", err)
	}
	if decoded.Error != nil {
		return Response{}, fmt.Errorf("LLM provider error: %s", abbreviated([]byte(decoded.Error.Message)))
	}
	if len(decoded.Choices) == 0 {
		return Response{}, ErrEmptyResponse
	}
	content := strings.TrimSpace(decoded.Choices[0].Message.Content)
	if content == "" {
		return Response{}, ErrEmptyResponse
	}

	return Response{
		Content: content,
		Model:   decoded.Model,
		Usage:   decoded.Usage,
	}, nil
}

type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRequest struct {
	Model       string    `json:"model"`
	Messages    []message `json:"messages"`
	Temperature float64   `json:"temperature"`
	MaxTokens   int       `json:"max_tokens,omitempty"`
}

type chatResponse struct {
	Model   string `json:"model"`
	Choices []struct {
		Message message `json:"message"`
	} `json:"choices"`
	Usage Usage `json:"usage"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

func loopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// abbreviated keeps provider error bodies short. An error body is untrusted
// text, so it is not interpreted; it is only shown as a bounded diagnostic.
func abbreviated(raw []byte) string {
	const limit = 512
	text := strings.TrimSpace(string(raw))
	if len(text) > limit {
		text = text[:limit] + "…"
	}
	return text
}
