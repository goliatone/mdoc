package googleapi

import (
	"errors"
	"net/http"
	"testing"

	api "google.golang.org/api/googleapi"
)

func TestClassifyDistinguishesNotFoundFromPermission(t *testing.T) {
	tests := []struct {
		code int
		kind ErrorKind
	}{
		{code: http.StatusNotFound, kind: KindNotFound},
		{code: http.StatusForbidden, kind: KindPermission},
	}
	for _, test := range tests {
		err := classify("read file", &api.Error{Code: test.code})
		var typed *Error
		if !errors.As(err, &typed) || typed.Kind != test.kind {
			t.Fatalf("code %d classified as %#v", test.code, err)
		}
	}
}

func TestClassifyRecognizesDriveExportSizeLimit(t *testing.T) {
	err := classify("export", &api.Error{Code: http.StatusForbidden, Errors: []api.ErrorItem{{Reason: "exportSizeLimitExceeded"}}})
	var typed *Error
	if !errors.As(err, &typed) || typed.Kind != KindExportSize {
		t.Fatalf("error = %#v", err)
	}
}
