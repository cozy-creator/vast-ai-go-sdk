package vast

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// Sentinel errors, matched via errors.Is against errors returned by any SDK
// method. This is the taxonomy tensorhub's backoff logic consumes.
var (
	// ErrNotFound matches 404 responses (unknown instance id, etc.).
	ErrNotFound = errors.New("vast: not found")
	// ErrUnauthorized matches 401/403 responses (bad API key / permissions).
	ErrUnauthorized = errors.New("vast: unauthorized")
	// ErrRateLimited matches 429 responses.
	ErrRateLimited = errors.New("vast: rate limited")
	// ErrOfferGone matches instance-create failures where the offer was
	// already rented or withdrawn (404/410 from PUT /asks/{id}/, or a
	// no-longer-available error body). Marketplace offers expire fast —
	// callers should re-search and pick the next offer, not retry the id.
	ErrOfferGone = errors.New("vast: offer gone")
	// ErrInsufficientCredit matches create failures caused by account
	// balance (vast rejects rentals below a minimum credit threshold).
	// Retrying is pointless until the account is topped up.
	ErrInsufficientCredit = errors.New("vast: insufficient credit")
)

// APIError is an error response from the vast.ai API. It matches the
// package sentinels via errors.Is according to StatusCode and Code.
type APIError struct {
	StatusCode int
	// Code is vast's machine-readable error slug when present
	// (e.g. "invalid_args", "insufficient_credit").
	Code    string
	Message string
	// RetryAfter is populated from the Retry-After header on 429 responses.
	RetryAfter time.Duration
}

func (e *APIError) Error() string {
	if e.Code != "" {
		return fmt.Sprintf("vast: API error %d (%s): %s", e.StatusCode, e.Code, e.Message)
	}
	return fmt.Sprintf("vast: API error %d: %s", e.StatusCode, e.Message)
}

// Is maps status codes and error slugs onto the package sentinels.
func (e *APIError) Is(target error) bool {
	switch target {
	case ErrNotFound:
		return e.StatusCode == 404
	case ErrUnauthorized:
		return e.StatusCode == 401 || e.StatusCode == 403
	case ErrRateLimited:
		return e.StatusCode == 429
	case ErrInsufficientCredit:
		return e.indicatesInsufficientCredit()
	}
	return false
}

func (e *APIError) indicatesInsufficientCredit() bool {
	if e.StatusCode == 402 {
		return true
	}
	blob := strings.ToLower(e.Code + " " + e.Message)
	return strings.Contains(blob, "insufficient_credit") ||
		strings.Contains(blob, "insufficient credit") ||
		strings.Contains(blob, "insufficient balance") ||
		strings.Contains(blob, "add credit")
}

// indicatesOfferGone reports whether an asks/{id} failure means the offer
// is no longer rentable (rented out, withdrawn, host offline).
func (e *APIError) indicatesOfferGone() bool {
	if e.StatusCode == 404 || e.StatusCode == 410 {
		return true
	}
	blob := strings.ToLower(e.Code + " " + e.Message)
	return strings.Contains(blob, "no longer available") ||
		strings.Contains(blob, "no_such_ask") ||
		strings.Contains(blob, "already rented") ||
		strings.Contains(blob, "unavailable")
}

// IsServerError reports whether the response was a 5xx.
func (e *APIError) IsServerError() bool { return e.StatusCode >= 500 && e.StatusCode < 600 }

// OfferGoneError wraps an instance-create failure classified as the offer
// being gone. errors.Is(err, ErrOfferGone) is true; the underlying APIError
// is reachable via errors.As.
type OfferGoneError struct {
	OfferID int64
	Cause   error
}

func (e *OfferGoneError) Error() string {
	return fmt.Sprintf("vast: offer %d gone: %v", e.OfferID, e.Cause)
}
func (e *OfferGoneError) Unwrap() error        { return e.Cause }
func (e *OfferGoneError) Is(target error) bool { return target == ErrOfferGone }

// ValidationError is a client-side input validation failure; no request was
// sent to the API.
type ValidationError struct {
	Field   string
	Message string
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("vast: validation error for field %q: %s", e.Field, e.Message)
}
