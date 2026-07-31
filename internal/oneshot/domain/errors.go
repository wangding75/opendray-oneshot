package domain

import (
	"errors"
	"fmt"
)

// ErrorCode is a stable API and event identifier from the frozen One-shot contract.
type ErrorCode string

const (
	ErrorDisabled             ErrorCode = "oneshot.disabled"
	ErrorUnauthorized         ErrorCode = "oneshot.unauthorized"
	ErrorForbidden            ErrorCode = "oneshot.forbidden"
	ErrorInvalidRequest       ErrorCode = "oneshot.invalid_request"
	ErrorIdempotencyRequired  ErrorCode = "oneshot.idempotency_required"
	ErrorIdempotencyConflict  ErrorCode = "oneshot.idempotency_conflict"
	ErrorTaskNotFound         ErrorCode = "oneshot.task_not_found"
	ErrorRunNotFound          ErrorCode = "oneshot.run_not_found"
	ErrorArtifactNotFound     ErrorCode = "oneshot.artifact_not_found"
	ErrorContextNotFound      ErrorCode = "oneshot.context_not_found"
	ErrorContextOwnerMismatch ErrorCode = "oneshot.context_owner_mismatch"
	ErrorUnsupportedProvider  ErrorCode = "oneshot.unsupported_provider"
	ErrorProviderUnavailable  ErrorCode = "oneshot.provider_unavailable"
	ErrorResumeUnsupported    ErrorCode = "oneshot.resume_unsupported"
	ErrorResumeFailed         ErrorCode = "oneshot.resume_failed"
	ErrorInvalidTransition    ErrorCode = "oneshot.invalid_transition"
	ErrorRunConflict          ErrorCode = "oneshot.run_conflict"
	ErrorQueueUnavailable     ErrorCode = "oneshot.queue_unavailable"
	ErrorDeliveryExhausted    ErrorCode = "oneshot.delivery_exhausted"
	ErrorExecutionFailed      ErrorCode = "oneshot.execution_failed"
	ErrorOutputPersistFailed  ErrorCode = "oneshot.output_persist_failed"
	ErrorArtifactUnavailable  ErrorCode = "oneshot.artifact_unavailable"
	ErrorCancelFailed         ErrorCode = "oneshot.cancel_failed"
	ErrorTimeout              ErrorCode = "oneshot.timeout"
	ErrorRateLimited          ErrorCode = "oneshot.rate_limited"
	ErrorInternal             ErrorCode = "oneshot.internal"
)

var errorRetryability = map[ErrorCode]bool{
	ErrorDisabled:             false,
	ErrorUnauthorized:         false,
	ErrorForbidden:            false,
	ErrorInvalidRequest:       false,
	ErrorIdempotencyRequired:  false,
	ErrorIdempotencyConflict:  false,
	ErrorTaskNotFound:         false,
	ErrorRunNotFound:          false,
	ErrorArtifactNotFound:     false,
	ErrorContextNotFound:      false,
	ErrorContextOwnerMismatch: false,
	ErrorUnsupportedProvider:  false,
	ErrorProviderUnavailable:  true,
	ErrorResumeUnsupported:    false,
	ErrorResumeFailed:         false,
	ErrorInvalidTransition:    false,
	ErrorRunConflict:          true,
	ErrorQueueUnavailable:     true,
	ErrorDeliveryExhausted:    false,
	ErrorExecutionFailed:      false,
	ErrorOutputPersistFailed:  true,
	ErrorArtifactUnavailable:  true,
	ErrorCancelFailed:         true,
	ErrorTimeout:              false,
	ErrorRateLimited:          true,
	ErrorInternal:             true,
}

// DomainError carries a stable code without coupling the domain to HTTP.
type DomainError struct {
	Code      ErrorCode
	Message   string
	Aggregate string
	From      string
	To        string
	Command   string
	Cause     error
}

func (e *DomainError) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.Message != "" {
		return e.Message
	}
	return string(e.Code)
}

func (e *DomainError) Unwrap() error { return e.Cause }

// Retryable reports the frozen advisory retryability for the error code.
func (e *DomainError) Retryable() bool { return IsRetryableCode(e.Code) }

// Is lets errors.Is match DomainError values by stable code.
func (e *DomainError) Is(target error) bool {
	other, ok := target.(*DomainError)
	return ok && other != nil && other.Code != "" && e.Code == other.Code
}

// IsRetryableCode reports the retryability frozen by errors.md.
func IsRetryableCode(code ErrorCode) bool { return errorRetryability[code] }

// IsKnownErrorCode reports whether code is part of contract version 1.0.0.
func IsKnownErrorCode(code ErrorCode) bool {
	_, ok := errorRetryability[code]
	return ok
}

// CodeOf extracts a stable One-shot code from err.
func CodeOf(err error) (ErrorCode, bool) {
	var domainErr *DomainError
	if !errors.As(err, &domainErr) || domainErr == nil {
		return "", false
	}
	return domainErr.Code, true
}

// HasCode reports whether err contains a DomainError with code.
func HasCode(err error, code ErrorCode) bool {
	actual, ok := CodeOf(err)
	return ok && actual == code
}

// NewDomainError constructs a stable domain error.
func NewDomainError(code ErrorCode, message string, cause error) *DomainError {
	if !IsKnownErrorCode(code) {
		code = ErrorInternal
	}
	return &DomainError{Code: code, Message: message, Cause: cause}
}

// InvalidRequestf creates a contract-valid invalid request error.
func InvalidRequestf(format string, args ...any) *DomainError {
	return &DomainError{Code: ErrorInvalidRequest, Message: fmt.Sprintf(format, args...)}
}

func invalidTransition(aggregate, from, to, command string) *DomainError {
	return &DomainError{
		Code:      ErrorInvalidTransition,
		Message:   fmt.Sprintf("%s cannot %s from %s to %s", aggregate, command, from, to),
		Aggregate: aggregate,
		From:      from,
		To:        to,
		Command:   command,
	}
}

func runConflict(message string) *DomainError {
	return &DomainError{Code: ErrorRunConflict, Message: message}
}

func contextOwnerMismatch() *DomainError {
	return &DomainError{Code: ErrorContextOwnerMismatch, Message: "runtime context owner, project, or provider does not match"}
}
