package model

import (
	"errors"
	"fmt"
	"net/http"
)

// Error codes from the API contract.
const (
	ErrBudgetExceeded = "BUDGET_EXCEEDED"
	ErrNoCapacity     = "NO_CAPACITY"
	ErrProviderFailed = "PROVIDER_FAILED"
	ErrBoxSleeping    = "BOX_SLEEPING"
	ErrTimeout        = "TIMEOUT"
	ErrPolicyDenied   = "POLICY_DENIED"
	ErrNotFound       = "NOT_FOUND"
	ErrUnauthorized   = "UNAUTHORIZED"
	ErrInvalid        = "INVALID"
	ErrRateLimited    = "RATE_LIMITED"
	ErrInternal       = "INTERNAL"
)

// Codes lists every error code with its HTTP status (served by GET /v1/errors).
var Codes = []string{ErrBudgetExceeded, ErrNoCapacity, ErrProviderFailed, ErrBoxSleeping, ErrTimeout,
	ErrPolicyDenied, ErrNotFound, ErrUnauthorized, ErrInvalid, ErrRateLimited, ErrInternal}

// APIError is the canonical error returned to agents. Message/Hint are English
// at creation; the gateway localizes them (i18n) from Format/Args at write time.
type APIError struct {
	Code        string `json:"code"`
	Message     string `json:"message"`
	Hint        string `json:"hint"`
	RetryAfterS *int   `json:"retry_after_s,omitempty"`
	DocsURL     string `json:"docs_url,omitempty"`
	// Next (v1.3) lists suggested next tool calls that usually resolve the error.
	Next   []string `json:"next,omitempty"`
	Status int      `json:"-"`
	format string
	args   []any
}

// Format returns the English message format (i18n key) and its args.
func (e *APIError) Format() (string, []any) {
	if e.format == "" {
		return e.Message, nil
	}
	return e.format, e.args
}

// WithRetry sets retry_after_s.
func (e *APIError) WithRetry(s int) *APIError { e.RetryAfterS = &s; return e }

// StatusFor returns the HTTP status for a code.
func StatusFor(code string) int { return statusFor(code) }

// DefaultRetryAfter is the suggested wait (seconds) for transient codes; 0 = not retryable as-is.
func DefaultRetryAfter(code string) int {
	switch code {
	case ErrNoCapacity:
		return 60
	case ErrProviderFailed:
		return 30
	case ErrRateLimited:
		return 10
	case ErrInternal:
		return 5
	}
	return 0
}

// DefaultHint returns the English default hint for a code.
func DefaultHint(code string) string { return defaultHints[code] }

func (e *APIError) Error() string { return e.Code + ": " + e.Message }

func statusFor(code string) int {
	switch code {
	case ErrBudgetExceeded:
		return http.StatusPaymentRequired
	case ErrNoCapacity, ErrProviderFailed:
		return http.StatusServiceUnavailable
	case ErrBoxSleeping:
		return http.StatusConflict
	case ErrTimeout:
		return http.StatusGatewayTimeout
	case ErrPolicyDenied:
		return http.StatusForbidden
	case ErrNotFound:
		return http.StatusNotFound
	case ErrUnauthorized:
		return http.StatusUnauthorized
	case ErrInvalid:
		return http.StatusBadRequest
	case ErrRateLimited:
		return http.StatusTooManyRequests
	}
	return http.StatusInternalServerError
}

var defaultHints = map[string]string{
	ErrBudgetExceeded: "Budget is used up; ask the operator to raise the agent budget or wait for the next period.",
	ErrNoCapacity:     "No host has room for this spec; retry with a smaller spec or ask the operator to add a host.",
	ErrProviderFailed: "The cloud provider failed; retry in a minute or choose another provider.",
	ErrBoxSleeping:    "The box is sleeping; call wake (POST /boxes/:id/wake) and retry.",
	ErrTimeout:        "The operation took too long; use async=true for long commands and poll the job.",
	ErrPolicyDenied:   "Your agent policy does not allow this; ask the operator to change the policy.",
	ErrNotFound:       "Check the id; list your boxes with GET /boxes.",
	ErrUnauthorized:   "Send a valid API key as 'Authorization: Bearer <key>' with the required scope.",
	ErrInvalid:        "Fix the request fields described in the message and retry.",
	ErrRateLimited:    "Slow down and retry after retry_after_s seconds.",
	ErrInternal:       "Unexpected server error; retry once, then report it to the operator.",
}

// E builds an APIError with the default hint for the code.
func E(code, format string, args ...any) *APIError {
	return &APIError{Code: code, Message: fmt.Sprintf(format, args...), Hint: defaultHints[code], Status: statusFor(code), format: format, args: args}
}

// EH builds an APIError with a custom hint.
func EH(code, hint, format string, args ...any) *APIError {
	e := E(code, format, args...)
	e.Hint = hint
	return e
}

// AsAPIError converts any error into an APIError (internal errors map to 500).
func AsAPIError(err error) *APIError {
	var ae *APIError
	if errors.As(err, &ae) {
		if ae.Status == 0 {
			ae.Status = statusFor(ae.Code)
		}
		return ae
	}
	return &APIError{Code: ErrInternal, Message: err.Error(), Hint: defaultHints[ErrInternal], Status: 500}
}

// ErrNoRows is returned by the store when a record does not exist.
var ErrNoRows = errors.New("not found")
