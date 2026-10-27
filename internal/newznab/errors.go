// Package newznab implements core.Indexer against Newznab-compatible
// Usenet indexers, with per-indexer daily budgets and a result cache.
package newznab

import (
	"fmt"
	"strings"

	"github.com/braedonsaunders/ether/internal/core"
)

// APIError is a typed Newznab error document (<error code=".." description=".."/>)
// or an HTTP-level failure translated into the same shape.
type APIError struct {
	Code        int
	Description string
	Budget      bool // daily limit / rate limited; unwraps to core.ErrBudgetExhausted
}

func (e *APIError) Error() string {
	return fmt.Sprintf("newznab error %d: %s", e.Code, e.Description)
}

func (e *APIError) Unwrap() error {
	if e.Budget {
		return core.ErrBudgetExhausted
	}
	if e.Code == 100 || e.Code == 401 || e.Code == 403 {
		return core.ErrUnauthorized
	}
	return nil
}

// apiError builds an *APIError, classifying budget exhaustion.
func apiError(code int, desc string) error {
	lower := strings.ToLower(desc)
	budget := code == 429 ||
		strings.Contains(lower, "request limit reached") ||
		strings.Contains(lower, "download limit reached") ||
		strings.Contains(lower, "api limit") ||
		strings.Contains(lower, "limit reached") ||
		strings.Contains(lower, "too many requests")
	return &APIError{Code: code, Description: desc, Budget: budget}
}

// Codes 500 and 501 mean quota exhaustion in an API error document,
// but HTTP 500/501 statuses alone are server failures, not quota signals.
func documentError(code int, desc string) error {
	e := apiError(code, desc).(*APIError)
	e.Budget = e.Budget || code == 500 || code == 501
	return e
}

// humanError renders an error for the setup wizard.
func humanError(indexer string, err error) error {
	if err == nil {
		return nil
	}
	if ae, ok := err.(*APIError); ok {
		switch {
		case ae.Code == 100:
			return fmt.Errorf("indexer %q: incorrect API key", indexer)
		case ae.Budget:
			return fmt.Errorf("indexer %q: daily API limit reached", indexer)
		case ae.Code == 101:
			return fmt.Errorf("indexer %q: account suspended", indexer)
		case ae.Code == 102:
			return fmt.Errorf("indexer %q: account not verified", indexer)
		case ae.Code == 103:
			return fmt.Errorf("indexer %q: registration denied", indexer)
		default:
			return fmt.Errorf("indexer %q: %s (code %d)", indexer, ae.Description, ae.Code)
		}
	}
	return fmt.Errorf("indexer %q: %w", indexer, err)
}

// RateLimitError is a backend's rate-limit refusal (HTTP 429). The Client
// pauses the indexer until RetryAfter, a Retry-After header value ("" for
// the default pause), exactly as for a Newznab 429.
type RateLimitError struct {
	RetryAfter string
	Err        error
}

func (e *RateLimitError) Error() string { return e.Err.Error() }

func (e *RateLimitError) Unwrap() []error { return []error{e.Err, core.ErrBudgetExhausted} }
