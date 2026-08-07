package main

import (
	"context"
	"errors"

	"github.com/formancehq/formance-sdk-go/v3/pkg/models/sdkerrors"
	"github.com/formancehq/formance-sdk-go/v3/pkg/models/shared"
)

// Model rejection reasons, matched against the server's error code in
// validateFailure. They are the v2 error codes the model can predict.
const (
	reasonInsufficientFund = string(shared.V2ErrorsEnumInsufficientFund)
	reasonAlreadyReverted  = string(shared.V2ErrorsEnumAlreadyRevert)
	reasonNotFound         = string(shared.V2ErrorsEnumNotFound)
	reasonNoPostings       = string(shared.V2ErrorsEnumNoPostings)
	reasonConflict         = string(shared.V2ErrorsEnumConflict)
)

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

// isNotFound reports whether err is a NOT_FOUND business error.
func isNotFound(err error) bool {
	e, ok := errorResponse(err)
	return ok && e.ErrorCode == shared.V2ErrorsEnumNotFound
}

// reasonMatches reports whether err's v2 error code is the one the model
// predicted. A predicted ALREADY_REVERT also accepts REVERT_OCCURRING: both mean
// the target is being or has been reverted in some serialization, and which one
// the server returns depends only on whether the competing revert has committed
// yet — a timing detail the committed-state model does not track.
func reasonMatches(err error, reason string) bool {
	e, ok := errorResponse(err)
	if !ok {
		return false
	}

	code := string(e.ErrorCode)
	if reason == reasonAlreadyReverted {
		return code == reasonAlreadyReverted || code == string(shared.V2ErrorsEnumRevertOccurring)
	}

	return code == reason
}
