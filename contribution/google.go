package contribution

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/goliatone/mdoc/workingdraft"
	"golang.org/x/oauth2"
	"google.golang.org/api/docs/v1"
	"google.golang.org/api/drive/v3"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"
)

const maxResponse = 10 << 20

// boundTransport does not retry and bounds both success and error bodies before
// the SDK decodes them. Hosts must inject non-retrying authenticated transports.
type boundTransport struct{ base http.RoundTripper }

func (t boundTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	response, e := t.base.RoundTrip(r)
	if e != nil {
		return nil, e
	}
	if response == nil || response.Body == nil {
		return nil, fail(TransportFailure, "missing provider response")
	}
	b, e := io.ReadAll(io.LimitReader(response.Body, maxResponse+1))
	response.Body.Close()
	if e != nil {
		return nil, e
	}
	if len(b) > maxResponse {
		return nil, fail(LimitExceeded, "provider response exceeds limit")
	}
	response.Body = io.NopCloser(bytes.NewReader(b))
	return response, nil
}

type Service struct {
	options Options
	drive   *drive.Service
	docs    *docs.Service
	draft   *workingdraft.Service
}

func New(o Options) (*Service, error) {
	if o.AccountBinding == "" || len(o.AccountBinding) > 256 || (o.HTTPClient == nil) == (o.TokenSource == nil) || o.HTTPClient != nil && o.Transport != nil {
		return nil, fail(InvalidInput, "supply one account-bound authentication configuration")
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	c := http.Client{Timeout: 30 * time.Second}
	if o.HTTPClient != nil {
		c = *o.HTTPClient
	}
	if c.Timeout <= 0 || c.Timeout > 30*time.Second {
		c.Timeout = 30 * time.Second
	}
	base := c.Transport
	if o.TokenSource != nil {
		base = o.Transport
		if base == nil {
			base = http.DefaultTransport
		}
		base = &oauth2.Transport{Source: o.TokenSource, Base: base}
	}
	if base == nil {
		base = http.DefaultTransport
	}
	c.Transport = boundTransport{base}
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	c.Jar = nil
	d, e := drive.NewService(context.Background(), option.WithHTTPClient(&c))
	if e != nil {
		return nil, safe(e)
	}
	g, e := docs.NewService(context.Background(), option.WithHTTPClient(&c))
	if e != nil {
		return nil, safe(e)
	}
	w, e := workingdraft.New(workingdraft.Options{HTTPClient: &c, Attempts: 1})
	if e != nil {
		return nil, safe(e)
	}
	return &Service{options: o, drive: d, docs: g, draft: w}, nil
}
func safe(e error) error {
	if e == nil {
		return nil
	}
	var local *Error
	if errors.As(e, &local) {
		return local
	}
	var wd *workingdraft.Error
	if errors.As(e, &wd) {
		switch wd.Code {
		case workingdraft.InvalidSource:
			return fail(InvalidInput, "invalid working-draft source")
		case workingdraft.AccessDenied:
			return fail(AccessDenied, "document access unavailable")
		case workingdraft.AccountUnavailable:
			return fail(AccountUnavailable, "account credentials unavailable")
		case workingdraft.UnsupportedContent, workingdraft.SuggestionsPending:
			return fail(UnsupportedContent, "document body is unsupported or has pending suggestions")
		case workingdraft.RemoteUnstable:
			return fail(Stale, "document changed during capture")
		case workingdraft.IncompleteDiscussion:
			return fail(LimitExceeded, "discussion evidence is incomplete")
		case workingdraft.SnapshotCorrupt:
			return fail(InvalidPlan, "native snapshot evidence is invalid")
		}
	}
	var g *googleapi.Error
	if errors.As(e, &g) {
		switch g.Code {
		case 401:
			return fail(AccountUnavailable, "Google credentials unavailable; reconnect")
		case 403, 404:
			return fail(AccessDenied, "Google resource is inaccessible to this account and app")
		case 409, 412:
			return fail(Stale, "Google precondition changed")
		case 400:
			return fail(InvalidInput, "Google rejected the request")
		}
	}
	return fail(TransportFailure, "Google request failed or evidence is incomplete")
}
func (s *Service) meta(m OperationMeta) error {
	if e := validMeta(m); e != nil {
		return e
	}
	if m.AccountBinding != s.options.AccountBinding {
		return fail(InvalidPlan, "plan belongs to a different account connection")
	}
	return nil
}
func outcome(k Kind, m OperationMeta, d string, a Attempt) Outcome {
	return Outcome{Kind: k, Meta: m, RequestDigest: d, AttemptID: a.ID, Status: NotSent}
}
func rejected(o Outcome, e error) (Outcome, error) {
	e = safe(e)
	o.Code = e.(*Error).Code
	if o.Code == ReconciliationRequired {
		o.Status = Unknown
	}
	if o.Code == Stale || o.Code == InvalidPlan {
		o.Status = Conflict
	}
	typed := *e.(*Error)
	typed.Status = o.Status
	return o, &typed
}
func (s *Service) authorize(m OperationMeta, d, expected string, a Attempt) error {
	if e := s.meta(m); e != nil {
		return e
	}
	if d == "" || d != expected {
		return fail(InvalidPlan, "expected plan digest differs")
	}
	if !identifier.MatchString(a.ID) {
		return fail(InvalidInput, "a fresh attempt identity is required")
	}
	if !a.FirstTransmission {
		return fail(ReconciliationRequired, "previous transmission requires inspection, not resend")
	}
	return nil
}
func mutationError(o Outcome, e error) (Outcome, error) {
	o.Status = Unknown
	var g *googleapi.Error
	if errors.As(e, &g) {
		switch g.Code {
		case 400, 401, 403, 404, 405, 409, 412, 413, 415, 422, 429:
			o.Status = NotSent
		}
	}
	if g != nil && g.Code == 400 && o.Kind == UpdateKind {
		return rejected(o, fail(Stale, "Google rejected the guarded batch; prepare again from the current revision"))
	}
	result, err := rejected(o, e)
	return result, err
}
func (s *Service) committed(o Outcome) Outcome {
	o.Status = Committed
	o.Code = ""
	o.AcknowledgedAt = s.options.Now().UTC()
	if o.DocumentID != "" {
		o.URL = "https://docs.google.com/document/d/" + o.DocumentID + "/edit"
	}
	return o
}
