package apperr

import (
	"errors"
	"fmt"
	"net/http"
)

type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	HTTP    int    `json:"-"`
}

func (e *Error) Error() string {
	return fmt.Sprintf("[%s] %s", e.Code, e.Message)
}

func New(code string, httpStatus int) *Error {
	return &Error{Code: code, HTTP: httpStatus}
}

func (e *Error) WithMessage(msg string) *Error {
	return &Error{Code: e.Code, Message: msg, HTTP: e.HTTP}
}

func Wrap(err error, code string, httpStatus int) *Error {
	return &Error{Code: code, Message: err.Error(), HTTP: httpStatus}
}

func AsAPIError(err error) (*Error, bool) {
	var appErr *Error
	if errors.As(err, &appErr) {
		return appErr, true
	}
	return nil, false
}

var (
	ErrInvalidArgument = New("INVALID_ARGUMENT", http.StatusBadRequest)
	ErrAddressFormat   = New("ADDRESS_FORMAT_INVALID", http.StatusBadRequest)
	ErrUnauthorized    = New("UNAUTHORIZED", http.StatusUnauthorized)
	ErrForbidden       = New("FORBIDDEN", http.StatusForbidden)
	ErrNotFound        = New("NOT_FOUND", http.StatusNotFound)

	ErrIdempotencyConflict   = New("IDEMPOTENCY_CONFLICT", http.StatusConflict)
	ErrIllegalTransition     = New("ILLEGAL_STATE_TRANSITION", http.StatusConflict)
	ErrVersionConflict       = New("VERSION_CONFLICT", http.StatusConflict)
	ErrInsufficientBalance   = New("INSUFFICIENT_BALANCE", http.StatusUnprocessableEntity)
	ErrBeneficiaryNotAllowed = New("BENEFICIARY_NOT_ALLOWED", http.StatusUnprocessableEntity)
	ErrOutsideBankingWindow  = New("OUTSIDE_BANKING_WINDOW", http.StatusUnprocessableEntity)
	ErrApprovalRequired      = New("APPROVAL_REQUIRED", http.StatusUnprocessableEntity)
	ErrConfigInconsistent    = New("CONFIG_INCONSISTENT", http.StatusUnprocessableEntity)
	ErrMAEDayLocked          = New("MAE_DAY_LOCKED", http.StatusLocked)
	ErrRateLimited           = New("RATE_LIMITED", http.StatusTooManyRequests)

	// G15: Provider capability 边界错误。
	// ErrFailedPrecondition 表示前置条件不满足（如缺少真实 adapter 或凭证）。
	ErrFailedPrecondition = New("FAILED_PRECONDITION", http.StatusFailedDependency)

	ErrInternal             = New("INTERNAL", http.StatusInternalServerError)
	ErrUnknownExternalState = New("UNKNOWN_EXTERNAL_STATE", http.StatusServiceUnavailable)
)

func Invalid(field string, err error) *Error {
	return &Error{
		Code:    "INVALID_ARGUMENT",
		Message: fmt.Sprintf("invalid %s: %s", field, err.Error()),
		HTTP:    http.StatusBadRequest,
	}
}
