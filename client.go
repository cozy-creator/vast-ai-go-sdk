// Package vast is a Go client for the vast.ai GPU marketplace API
// (https://console.vast.ai/api/v0). It covers the four-verb lifecycle the
// cozy platform consumes — search offers, create instance, poll, destroy —
// plus account balance and a static GPU catalog bridging vast gpu_name
// strings to the compilecache SKU-slug space.
package vast

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	// DefaultBaseURL is the vast.ai REST API base. Endpoints below it are
	// versioned per path (/api/v0/..., /api/v1/...).
	DefaultBaseURL = "https://console.vast.ai"

	// DefaultTimeout is the default HTTP client timeout.
	DefaultTimeout = 30 * time.Second

	// DefaultUserAgent identifies the SDK.
	DefaultUserAgent = "vast-ai-go/0.1.0"

	// DefaultMaxRetryAttempts is the default number of retries for
	// retryable failures (429, and 5xx on idempotent requests).
	DefaultMaxRetryAttempts = 3

	// DefaultRetryDelay is the base delay for exponential backoff.
	DefaultRetryDelay = 1 * time.Second

	// maxRetryDelay caps the exponential backoff.
	maxRetryDelay = 30 * time.Second
)

// Logger is the minimal logging interface used for debug output.
type Logger interface {
	Printf(format string, v ...interface{})
}

type defaultLogger struct{}

func (l *defaultLogger) Printf(format string, v ...interface{}) { log.Printf(format, v...) }

// Client is the vast.ai API client. Construct with NewClient; safe for
// concurrent use.
type Client struct {
	apiKey  string
	baseURL string

	httpClient *http.Client
	userAgent  string

	debug            bool
	maxRetryAttempts int
	retryDelay       time.Duration
	logger           Logger
}

// ClientOption configures the client.
type ClientOption func(*Client)

// WithBaseURL overrides the API base URL (e.g. an httptest server).
func WithBaseURL(baseURL string) ClientOption {
	return func(c *Client) { c.baseURL = strings.TrimRight(baseURL, "/") }
}

// WithTimeout sets the HTTP client timeout.
func WithTimeout(timeout time.Duration) ClientOption {
	return func(c *Client) { c.httpClient.Timeout = timeout }
}

// WithHTTPClient sets a custom HTTP client.
func WithHTTPClient(httpClient *http.Client) ClientOption {
	return func(c *Client) { c.httpClient = httpClient }
}

// WithDebug enables request/response logging.
func WithDebug(debug bool) ClientOption {
	return func(c *Client) { c.debug = debug }
}

// WithUserAgent sets the User-Agent header.
func WithUserAgent(ua string) ClientOption {
	return func(c *Client) { c.userAgent = ua }
}

// WithMaxRetryAttempts sets the maximum number of retry attempts.
func WithMaxRetryAttempts(n int) ClientOption {
	return func(c *Client) { c.maxRetryAttempts = n }
}

// WithRetryDelay sets the base delay for exponential backoff.
func WithRetryDelay(d time.Duration) ClientOption {
	return func(c *Client) { c.retryDelay = d }
}

// WithLogger sets a custom logger for debug output.
func WithLogger(logger Logger) ClientOption {
	return func(c *Client) { c.logger = logger }
}

// NewClient creates a vast.ai API client authenticated with apiKey.
func NewClient(apiKey string, opts ...ClientOption) (*Client, error) {
	if strings.TrimSpace(apiKey) == "" {
		return nil, &ValidationError{Field: "apiKey", Message: "cannot be empty"}
	}
	c := &Client{
		apiKey:           apiKey,
		baseURL:          DefaultBaseURL,
		httpClient:       &http.Client{Timeout: DefaultTimeout},
		userAgent:        DefaultUserAgent,
		maxRetryAttempts: DefaultMaxRetryAttempts,
		retryDelay:       DefaultRetryDelay,
		logger:           &defaultLogger{},
	}
	for _, opt := range opts {
		opt(c)
	}
	if c.httpClient == nil {
		c.httpClient = &http.Client{Timeout: DefaultTimeout}
	}
	if c.logger == nil {
		c.logger = &defaultLogger{}
	}
	return c, nil
}

// do performs an HTTP request against path (e.g. "/api/v0/bundles/"),
// marshalling body (when non-nil) as JSON and unmarshalling a 2xx response
// into out (when non-nil).
//
// Retry policy: 429 is retried for every method (the request was rejected
// before processing), honoring Retry-After. 5xx and transport errors are
// retried only when idempotent is true — PUT /asks/{id}/ creates an
// instance and MUST NOT be replayed after an ambiguous failure, while
// searches, gets, and deletes are safe.
func (c *Client) do(ctx context.Context, method, path string, body, out interface{}, idempotent bool) error {
	var jsonBody []byte
	if body != nil {
		var err error
		jsonBody, err = json.Marshal(body)
		if err != nil {
			return fmt.Errorf("vast: marshal request body: %w", err)
		}
	}

	var lastErr error
	var retryAfter time.Duration
	for attempt := 0; attempt <= c.maxRetryAttempts; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(c.backoff(attempt, retryAfter)):
			}
			retryAfter = 0
		}

		err := c.doOnce(ctx, method, path, jsonBody, out)
		if err == nil {
			return nil
		}
		lastErr = err

		var apiErr *APIError
		if errors.As(err, &apiErr) {
			retryable := apiErr.StatusCode == http.StatusTooManyRequests ||
				(idempotent && apiErr.StatusCode >= 500)
			if !retryable {
				return err
			}
			retryAfter = apiErr.RetryAfter
			continue
		}
		if ctx.Err() != nil {
			return err
		}
		if !idempotent {
			// Transport error after the request may have been sent —
			// ambiguous for a non-idempotent call, surface it.
			return err
		}
	}
	return lastErr
}

func (c *Client) doOnce(ctx context.Context, method, path string, jsonBody []byte, out interface{}) error {
	var reader io.Reader
	if jsonBody != nil {
		reader = bytes.NewReader(jsonBody)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return fmt.Errorf("vast: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.userAgent)
	if jsonBody != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	if c.debug {
		c.logger.Printf("vast: %s %s body=%s", method, path, string(jsonBody))
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("vast: %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("vast: read response: %w", err)
	}
	if c.debug {
		c.logger.Printf("vast: %s %s -> %d body=%s", method, path, resp.StatusCode, truncate(string(respBody), 2048))
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return newAPIError(resp.StatusCode, resp.Header, respBody)
	}

	// vast sometimes signals failure inside a 200 body: {"success": false, ...}.
	var envelope struct {
		Success *bool  `json:"success"`
		Error   string `json:"error"`
		Msg     string `json:"msg"`
	}
	if json.Unmarshal(respBody, &envelope) == nil && envelope.Success != nil && !*envelope.Success {
		return &APIError{
			StatusCode: resp.StatusCode,
			Code:       envelope.Error,
			Message:    firstNonEmpty(envelope.Msg, envelope.Error, "request failed"),
		}
	}

	if out != nil {
		if err := json.Unmarshal(respBody, out); err != nil {
			return fmt.Errorf("vast: decode response (%s %s): %w", method, path, err)
		}
	}
	return nil
}

// backoff computes the wait before retry attempt n (1-based), honoring a
// server-provided Retry-After when longer.
func (c *Client) backoff(attempt int, retryAfter time.Duration) time.Duration {
	d := c.retryDelay * time.Duration(1<<uint(attempt-1))
	if d > maxRetryDelay {
		d = maxRetryDelay
	}
	// Full jitter on the exponential component.
	d = time.Duration(rand.Int63n(int64(d)) + int64(c.retryDelay)/2)
	if retryAfter > d {
		d = retryAfter
	}
	if d > maxRetryDelay {
		d = maxRetryDelay
	}
	return d
}

func newAPIError(status int, header http.Header, body []byte) *APIError {
	apiErr := &APIError{StatusCode: status, Message: truncate(strings.TrimSpace(string(body)), 512)}
	var parsed struct {
		Error  string `json:"error"`
		Msg    string `json:"msg"`
		Detail string `json:"detail"`
	}
	if json.Unmarshal(body, &parsed) == nil {
		apiErr.Code = parsed.Error
		if m := firstNonEmpty(parsed.Msg, parsed.Detail); m != "" {
			apiErr.Message = m
		}
	}
	if status == http.StatusTooManyRequests {
		if ra := header.Get("Retry-After"); ra != "" {
			if secs, err := strconv.Atoi(ra); err == nil && secs > 0 {
				apiErr.RetryAfter = time.Duration(secs) * time.Second
			}
		}
	}
	return apiErr
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
