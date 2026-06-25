package main

import (
	"context"
	"errors"

	"github.com/formancehq/formance-sdk-go/v3/pkg/models/sdkerrors"
	"github.com/formancehq/formance-sdk-go/v3/pkg/models/shared"
)

// reasonInsufficientFund is the model's rejection reason for an overdrafting
// transaction, matched against the server's error code in validateFailure.
const reasonInsufficientFund = string(shared.V2ErrorsEnumInsufficientFund)

// errorResponse extracts a v2 API error from err, if it is one.
func errorResponse(err error) (*sdkerrors.V2ErrorResponse, bool) {
	var e *sdkerrors.V2ErrorResponse
	if errors.As(err, &e) {
		return e, true
	}

	return nil, false
}

// isShutdownError reports whether err is a context cancellation/deadline — what
// in-flight calls return once MODEL_MAX_SECONDS expires or the parent context is
// cancelled. It's a clean teardown, not a server rejection to validate.
func isShutdownError(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

// isTransient reports whether the model treats err as "the operation didn't
// happen": shutdown, a transport error surviving the client's retries, or a
// server-side INTERNAL/TIMEOUT. A definitive business error (NOT_FOUND,
// INSUFFICIENT_FUND, CONFLICT, ALREADY_REVERT, …) is not transient — it is
// validated against the model.
func isTransient(err error) bool {
	if err == nil {
		return false
	}

	if isShutdownError(err) {
		return true
	}

	if e, ok := errorResponse(err); ok {
		switch e.ErrorCode {
		case shared.V2ErrorsEnumInternal, shared.V2ErrorsEnumTimeout:
			return true
		default:
			return false
		}
	}

	// Non-API error (connection refused, EOF, TLS, timeout) after the client's
	// retries — transient under fault injection.
	return true
}

// hasReason reports whether err carries the v2 error code reason.
func hasReason(err error, reason string) bool {
	if e, ok := errorResponse(err); ok {
		return string(e.ErrorCode) == reason
	}

	return false
}
