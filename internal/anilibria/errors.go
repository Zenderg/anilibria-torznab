package anilibria

import (
	"fmt"
	"time"
)

// ErrorClass is a stable, log-safe upstream failure classification.
type ErrorClass string

const (
	ErrorClassRateLimited ErrorClass = "rate_limited"
	ErrorClassTemporary   ErrorClass = "temporary_upstream"
	ErrorClassPermanent   ErrorClass = "permanent_upstream"
	ErrorClassUnexpected  ErrorClass = "unexpected_upstream"
	ErrorClassResponse    ErrorClass = "invalid_response"
	ErrorClassTransport   ErrorClass = "transport"
	ErrorClassCanceled    ErrorClass = "canceled"
)

// Error describes an upstream failure without retaining response bodies,
// request URLs, queries, or header values in its printable representation.
type Error struct {
	Class      ErrorClass
	Operation  string
	StatusCode int
	Attempts   int
	Duration   time.Duration
	cause      error
}

func (err *Error) Error() string {
	if err.StatusCode != 0 {
		return fmt.Sprintf("anilibria %s: %s (status %d, attempts %d)", err.Operation, err.Class, err.StatusCode, err.Attempts)
	}
	return fmt.Sprintf("anilibria %s: %s (attempts %d)", err.Operation, err.Class, err.Attempts)
}

// Unwrap permits errors.Is/errors.As without exposing the cause in Error.
func (err *Error) Unwrap() error {
	return err.cause
}

func newError(class ErrorClass, operation string, status, attempts int, started time.Time, cause error) *Error {
	return &Error{
		Class:      class,
		Operation:  operation,
		StatusCode: status,
		Attempts:   attempts,
		Duration:   time.Since(started),
		cause:      cause,
	}
}
