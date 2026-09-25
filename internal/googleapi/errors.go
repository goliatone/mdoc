package googleapi

import (
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"

	"google.golang.org/api/googleapi"
)

type ErrorKind string

const (
	KindNotFound    ErrorKind = "not_found"
	KindPermission  ErrorKind = "permission"
	KindQuota       ErrorKind = "quota"
	KindRetryable   ErrorKind = "retryable"
	KindConflict    ErrorKind = "conflict"
	KindAPI         ErrorKind = "api"
	KindUnsupported ErrorKind = "unsupported"
	KindSuggestions ErrorKind = "suggestions"
	KindUnstable    ErrorKind = "unstable"
	KindExportSize  ErrorKind = "export_size"
	KindEmptyExport ErrorKind = "empty_export"
)

type Error struct {
	Kind      ErrorKind
	Operation string
	Cause     error
}

func (e *Error) Error() string {
	switch e.Kind {
	case KindNotFound:
		return fmt.Sprintf("%s: Google resource was not found", e.Operation)
	case KindPermission:
		return fmt.Sprintf("%s: Google access was denied; run `mdoc auth login` and verify account access", e.Operation)
	case KindQuota:
		return fmt.Sprintf("%s: Google quota was exceeded; retry after the quota window resets", e.Operation)
	case KindConflict:
		return fmt.Sprintf("%s: %v", e.Operation, e.Cause)
	default:
		return fmt.Sprintf("%s: %v", e.Operation, e.Cause)
	}
}

func (e *Error) Unwrap() error { return e.Cause }

func classify(operation string, err error) error {
	if err == nil {
		return nil
	}
	kind := KindAPI
	var networkError net.Error
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.As(err, &networkError) {
		kind = KindRetryable
	}
	if apiError, ok := errors.AsType[*googleapi.Error](err); ok {
		switch apiError.Code {
		case http.StatusNotFound:
			kind = KindNotFound
		case http.StatusUnauthorized:
			kind = KindPermission
		case http.StatusForbidden:
			kind = KindPermission
			for _, item := range apiError.Errors {
				switch item.Reason {
				case "rateLimitExceeded", "userRateLimitExceeded", "quotaExceeded", "dailyLimitExceeded":
					kind = KindQuota
				case "exportSizeLimitExceeded":
					kind = KindExportSize
				case "backendError", "internalError":
					kind = KindRetryable
				}
			}
		case http.StatusTooManyRequests:
			kind = KindQuota
		case http.StatusConflict, http.StatusPreconditionFailed:
			kind = KindConflict
		case http.StatusRequestTimeout, http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
			kind = KindRetryable
		}
	}
	return &Error{Kind: kind, Operation: operation, Cause: err}
}
