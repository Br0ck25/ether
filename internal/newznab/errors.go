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
