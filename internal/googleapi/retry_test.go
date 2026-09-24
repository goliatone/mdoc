package googleapi

import (
	"context"
	"errors"
	"net/http"
	"testing"

	gapi "google.golang.org/api/googleapi"
)

func TestClassifyRetryableAndQuotaErrors(t *testing.T) {
	tests := []struct {
		name  string
		error *gapi.Error
		kind  ErrorKind
		retry bool
	}{
		{name: "internal", error: &gapi.Error{Code: http.StatusInternalServerError}, kind: KindRetryable, retry: true},
		{name: "too many", error: &gapi.Error{Code: http.StatusTooManyRequests}, kind: KindQuota, retry: true},
		{name: "rate reason", error: &gapi.Error{Code: http.StatusForbidden, Errors: []gapi.ErrorItem{{Reason: "rateLimitExceeded"}}}, kind: KindQuota, retry: true},
		{name: "backend reason", error: &gapi.Error{Code: http.StatusForbidden, Errors: []gapi.ErrorItem{{Reason: "backendError"}}}, kind: KindRetryable, retry: true},
		{name: "permission", error: &gapi.Error{Code: http.StatusForbidden, Errors: []gapi.ErrorItem{{Reason: "insufficientPermissions"}}}, kind: KindPermission, retry: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := classify("test", test.error)
			var remote *Error
			if !errors.As(err, &remote) || remote.Kind != test.kind || IsRetryable(err) != test.retry {
				t.Fatalf("error=%#v retry=%v", err, IsRetryable(err))
			}
		})
	}
}

func TestRetryPolicyRetriesAndBoundsAttempts(t *testing.T) {
	attempts := 0
	policy := RetryPolicy{Attempts: 3, Backoff: func(context.Context, int) error { return nil }}
	err := policy.Do(context.Background(), func() error {
		attempts++
		if attempts < 3 {
			return &Error{Kind: KindRetryable, Operation: "test", Cause: errors.New("transient")}
		}
		return nil
	})
	if err != nil || attempts != 3 {
		t.Fatalf("error=%v attempts=%d", err, attempts)
	}
	attempts = 0
	err = policy.Do(context.Background(), func() error {
		attempts++
		return &Error{Kind: KindQuota, Operation: "test", Cause: errors.New("quota")}
	})
	if err == nil || attempts != 3 {
		t.Fatalf("error=%v attempts=%d", err, attempts)
	}
}
