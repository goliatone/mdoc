package portable

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"sync"
	"time"

	"golang.org/x/oauth2"
	"google.golang.org/api/drive/v3"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"
)

const docMIME = "application/vnd.google-apps.document"
const folderMIME = "application/vnd.google-apps.folder"
const fileFields = "id,name,mimeType,version,parents,driveId,trashed,size,resourceKey,capabilities(canDownload)"

type googleProvider struct {
	drive  *drive.Service
	client *http.Client
	mu     sync.Mutex
	keys   map[string]string
}

// cappedTransport also bounds SDK-decoded metadata and folder responses.
type cappedTransport struct{ base http.RoundTripper }

func (t cappedTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	resp, e := t.base.RoundTrip(r)
	if e != nil {
		return nil, e
	}
	if r.URL.Query().Get("alt") != "media" && r.URL.Host != "docs.googleapis.com" {
		resp.Body = &limitedBody{ReadCloser: resp.Body, left: 10 << 20}
	}
	return resp, nil
}

type limitedBody struct {
	io.ReadCloser
	left int64
}

func (b *limitedBody) Read(p []byte) (int, error) {
	if b.left <= 0 {
		var probe [1]byte
		n, e := b.ReadCloser.Read(probe[:])
		if n > 0 {
			return 0, fail("limit_exceeded", "provider response too large")
		}
		return 0, e
	}
	if int64(len(p)) > b.left {
		p = p[:b.left]
	}
	n, e := b.ReadCloser.Read(p)
	b.left -= int64(n)
	return n, e
}
func newGoogle(ctx context.Context, o Options) (Provider, error) {
	if o.HTTPClient == nil && o.TokenSource == nil {
		return nil, fail("account_unavailable", "explicit authenticated HTTP client or token source required")
	}
	c := http.Client{Timeout: 30 * time.Second}
	if o.HTTPClient != nil {
		c = *o.HTTPClient
	}
	if c.Timeout <= 0 || c.Timeout > 30*time.Second {
		c.Timeout = 30 * time.Second
	}
	base := c.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	if o.TokenSource != nil {
		base = &oauth2.Transport{Source: o.TokenSource, Base: base}
	}
	c.Transport = cappedTransport{base}
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	d, e := drive.NewService(ctx, option.WithHTTPClient(&c))
	if e != nil {
		return nil, safeProviderError(e)
	}
	return &googleProvider{drive: d, client: &c, keys: map[string]string{}}, nil
}
func (p *googleProvider) File(ctx context.Context, id string) (*drive.File, error) {
	call := p.drive.Files.Get(id).Fields(googleapi.Field(fileFields)).Context(ctx)
	p.accessHeader(id, call.Header())
	f, e := call.Do()
	if e == nil && f != nil {
		p.rememberKey(f.Id, f.ResourceKey)
	}
	return f, safeProviderError(e)
}
func (p *googleProvider) Children(ctx context.Context, id, cursor string) (*drive.FileList, error) {
	call := p.drive.Files.List().Q("'" + id + "' in parents and trashed = false").PageSize(100).PageToken(cursor).Fields(googleapi.Field("nextPageToken,incompleteSearch,files(" + fileFields + ")")).Context(ctx)
	p.accessHeader(id, call.Header())
	f, e := call.Do()
	if e == nil && f != nil {
		for _, item := range f.Files {
			if item != nil {
				p.rememberKey(item.Id, item.ResourceKey)
			}
		}
	}
	return f, safeProviderError(e)
}
func (p *googleProvider) Document(ctx context.Context, id string, max int64) (json.RawMessage, error) {
	r, _ := http.NewRequestWithContext(ctx, "GET", "https://docs.googleapis.com/v1/documents/"+url.PathEscape(id)+"?includeTabsContent=true&suggestionsViewMode=SUGGESTIONS_INLINE", nil)
	p.accessHeader(id, r.Header)
	resp, e := p.client.Do(r)
	if e != nil {
		return nil, safeProviderError(e)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, statusError(resp.StatusCode)
	}
	return readBounded(resp.Body, max)
}
func (p *googleProvider) Download(ctx context.Context, id string, max int64) ([]byte, error) {
	call := p.drive.Files.Get(id).Context(ctx)
	p.accessHeader(id, call.Header())
	resp, e := call.Download()
	if e != nil {
		return nil, safeProviderError(e)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, statusError(resp.StatusCode)
	}
	return readBounded(resp.Body, max)
}
func readBounded(r io.Reader, max int64) ([]byte, error) {
	b, e := io.ReadAll(io.LimitReader(r, max+1))
	if e != nil {
		return nil, e
	}
	if int64(len(b)) > max {
		return nil, fail("limit_exceeded", "source exceeds configured byte limit")
	}
	return b, nil
}
func statusError(code int) error {
	if code == 401 || code == 403 || code == 404 {
		return fail("access_denied", "required source is inaccessible with the selected account")
	}
	return fail("transport_failure", "Google read failed")
}
func safeProviderError(e error) error {
	if e == nil {
		return nil
	}
	if errors.Is(e, context.Canceled) || errors.Is(e, context.DeadlineExceeded) {
		return e
	}
	var g *googleapi.Error
	if errors.As(e, &g) {
		return statusError(g.Code)
	}
	var pe *Error
	if errors.As(e, &pe) {
		return pe
	}
	return fail("transport_failure", "Google read failed")
}
func (s *Service) requireProvider(ctx context.Context) error {
	s.providerMu.Lock()
	defer s.providerMu.Unlock()
	if s.provider != nil {
		return nil
	}
	p, e := newGoogle(ctx, s.options)
	if e != nil {
		return e
	}
	s.provider = p
	return nil
}

func (p *googleProvider) rememberKey(id, key string) {
	if idPattern.MatchString(id) && idPattern.MatchString(key) {
		p.mu.Lock()
		p.keys[id] = key
		p.mu.Unlock()
	}
}
func (p *googleProvider) accessHeader(id string, h http.Header) {
	p.mu.Lock()
	key := p.keys[id]
	p.mu.Unlock()
	if key != "" {
		h.Set("X-Goog-Drive-Resource-Keys", id+"/"+key)
	}
}
func (s *Service) sourceAccess(source string) {
	ref, e := ParseSource(source)
	if e != nil {
		return
	}
	u, e := url.Parse(source)
	if e != nil {
		return
	}
	if p, ok := s.provider.(*googleProvider); ok {
		p.rememberKey(ref.FileID, u.Query().Get("resourcekey"))
	}
}
func publicSourceLink(source string) string {
	if _, e := ParseSource(source); e != nil {
		return source
	}
	u, e := url.Parse(source)
	if e != nil {
		return source
	}
	q := u.Query()
	q.Del("resourcekey")
	u.RawQuery = q.Encode()
	return u.String()
}
