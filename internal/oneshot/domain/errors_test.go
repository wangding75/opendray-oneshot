package domain

import (
	"errors"
	"testing"
)

func TestFrozenErrorCodesAndRetryability(t *testing.T) {
	expected := map[ErrorCode]bool{
		ErrorDisabled: false, ErrorUnauthorized: false, ErrorForbidden: false,
		ErrorInvalidRequest: false, ErrorIdempotencyRequired: false,
		ErrorIdempotencyConflict: false, ErrorTaskNotFound: false,
		ErrorRunNotFound: false, ErrorArtifactNotFound: false,
		ErrorContextNotFound: false, ErrorContextOwnerMismatch: false,
		ErrorUnsupportedProvider: false, ErrorProviderUnavailable: true,
		ErrorResumeUnsupported: false, ErrorResumeFailed: false,
		ErrorInvalidTransition: false, ErrorRunConflict: true,
		ErrorQueueUnavailable: true, ErrorDeliveryExhausted: false,
		ErrorExecutionFailed: false, ErrorOutputPersistFailed: true,
		ErrorArtifactUnavailable: true, ErrorCancelFailed: true,
		ErrorTimeout: false, ErrorRateLimited: true, ErrorInternal: true,
	}
	if len(errorRetryability) != 26 {
		t.Fatalf("error registry has %d codes, want 26", len(errorRetryability))
	}
	for code, retryable := range expected {
		if !IsKnownErrorCode(code) {
			t.Fatalf("unknown frozen code %s", code)
		}
		if got := IsRetryableCode(code); got != retryable {
			t.Fatalf("retryability for %s = %v, want %v", code, got, retryable)
		}
	}
}

func TestDomainErrorExtractionAndWrapping(t *testing.T) {
	cause := errors.New("disk unavailable")
	err := NewDomainError(ErrorArtifactUnavailable, "artifact unavailable", cause)
	if !errors.Is(err, cause) {
		t.Fatal("DomainError does not unwrap cause")
	}
	if code, ok := CodeOf(err); !ok || code != ErrorArtifactUnavailable {
		t.Fatalf("CodeOf = %s, %v", code, ok)
	}
	if !err.Retryable() {
		t.Fatal("expected retryable error")
	}
}

func TestUnknownDomainErrorCodeFallsBackToInternal(t *testing.T) {
	err := NewDomainError(ErrorCode("oneshot.unknown"), "unknown", nil)
	if err.Code != ErrorInternal {
		t.Fatalf("code = %s", err.Code)
	}
}
