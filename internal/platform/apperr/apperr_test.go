package apperr

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestError_Error(t *testing.T) {
	e := &Error{Code: "TEST", Message: "something failed"}
	assert.Equal(t, "[TEST] something failed", e.Error())
}

func TestNew(t *testing.T) {
	e := New("MY_CODE", 422)
	assert.Equal(t, "MY_CODE", e.Code)
	assert.Equal(t, 422, e.HTTP)
	assert.Empty(t, e.Message)
}

func TestError_WithMessage(t *testing.T) {
	base := New("BASE", 400)
	wrapped := base.WithMessage("detail here")
	assert.Equal(t, "BASE", wrapped.Code)
	assert.Equal(t, "detail here", wrapped.Message)
	assert.Equal(t, 400, wrapped.HTTP)
	assert.Empty(t, base.Message)
}

func TestWrap(t *testing.T) {
	inner := fmt.Errorf("db timeout")
	e := Wrap(inner, "INTERNAL", http.StatusInternalServerError)
	assert.Equal(t, "INTERNAL", e.Code)
	assert.Equal(t, "db timeout", e.Message)
	assert.Equal(t, 500, e.HTTP)
}

func TestAsAPIError_WithAppErr(t *testing.T) {
	e := ErrInsufficientBalance.WithMessage("need 100, have 50")
	apiErr, ok := AsAPIError(e)
	require.True(t, ok)
	assert.Equal(t, "INSUFFICIENT_BALANCE", apiErr.Code)
	assert.Equal(t, 422, apiErr.HTTP)
}

func TestAsAPIError_WithPlainErr(t *testing.T) {
	plain := errors.New("not an app error")
	apiErr, ok := AsAPIError(plain)
	assert.False(t, ok)
	assert.Nil(t, apiErr)
}

func TestAsAPIError_WithWrappedAppErr(t *testing.T) {
	base := ErrNotFound.WithMessage("order 123")
	wrapped := fmt.Errorf("lookup failed: %w", base)
	apiErr, ok := AsAPIError(wrapped)
	require.True(t, ok)
	assert.Equal(t, "NOT_FOUND", apiErr.Code)
}

func TestInvalid(t *testing.T) {
	inner := fmt.Errorf("must be positive")
	e := Invalid("amount", inner)
	assert.Equal(t, "INVALID_ARGUMENT", e.Code)
	assert.Equal(t, http.StatusBadRequest, e.HTTP)
	assert.Contains(t, e.Message, "invalid amount")
	assert.Contains(t, e.Message, "must be positive")
}

func TestSentinelCodes(t *testing.T) {
	tests := []struct {
		name     string
		err      *Error
		wantCode string
		wantHTTP int
	}{
		{"INVALID_ARGUMENT", ErrInvalidArgument, "INVALID_ARGUMENT", 400},
		{"ADDRESS_FORMAT_INVALID", ErrAddressFormat, "ADDRESS_FORMAT_INVALID", 400},
		{"UNAUTHORIZED", ErrUnauthorized, "UNAUTHORIZED", 401},
		{"FORBIDDEN", ErrForbidden, "FORBIDDEN", 403},
		{"NOT_FOUND", ErrNotFound, "NOT_FOUND", 404},
		{"IDEMPOTENCY_CONFLICT", ErrIdempotencyConflict, "IDEMPOTENCY_CONFLICT", 409},
		{"ILLEGAL_STATE_TRANSITION", ErrIllegalTransition, "ILLEGAL_STATE_TRANSITION", 409},
		{"VERSION_CONFLICT", ErrVersionConflict, "VERSION_CONFLICT", 409},
		{"INSUFFICIENT_BALANCE", ErrInsufficientBalance, "INSUFFICIENT_BALANCE", 422},
		{"BENEFICIARY_NOT_ALLOWED", ErrBeneficiaryNotAllowed, "BENEFICIARY_NOT_ALLOWED", 422},
		{"OUTSIDE_BANKING_WINDOW", ErrOutsideBankingWindow, "OUTSIDE_BANKING_WINDOW", 422},
		{"APPROVAL_REQUIRED", ErrApprovalRequired, "APPROVAL_REQUIRED", 422},
		{"MAE_DAY_LOCKED", ErrMAEDayLocked, "MAE_DAY_LOCKED", 423},
		{"RATE_LIMITED", ErrRateLimited, "RATE_LIMITED", 429},
		{"INTERNAL", ErrInternal, "INTERNAL", 500},
		{"UNKNOWN_EXTERNAL_STATE", ErrUnknownExternalState, "UNKNOWN_EXTERNAL_STATE", 503},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.wantCode, tt.err.Code)
			assert.Equal(t, tt.wantHTTP, tt.err.HTTP)
		})
	}
}

func TestSentinelCodes_Immutability(t *testing.T) {
	orig := ErrNotFound.Code
	_ = ErrNotFound.WithMessage("modified")
	assert.Equal(t, orig, ErrNotFound.Code)
	assert.Empty(t, ErrNotFound.Message)
}
