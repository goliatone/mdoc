package workingdraft

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	transport "github.com/goliatone/mdoc/internal/googleapi"
	"golang.org/x/oauth2"
	"google.golang.org/api/docs/v1"
	"google.golang.org/api/drive/v3"
	gapi "google.golang.org/api/googleapi"
)

type Service struct {
	provider  Provider
	converter Converter
	store     SnapshotStore
	now       func() time.Time
	attempts  int
}

func New(options Options) (*Service, error) {
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.Attempts <= 0 {
		options.Attempts = 3
	}
	if options.Attempts > 3 {
		options.Attempts = 3
	}
	if options.Converter == nil {
		options.Converter = nativeConverter{}
	}
	if options.Provider == nil {
		if options.HTTPClient == nil && options.TokenSource == nil {
			return nil, fail(AccountUnavailable, "an authenticated HTTP client or token source is required")
		}
		client := http.Client{Timeout: 30 * time.Second}
		if options.HTTPClient != nil {
			client = *options.HTTPClient
			if client.Timeout <= 0 || client.Timeout > 30*time.Second {
				client.Timeout = 30 * time.Second
			}
		}
		if options.TokenSource != nil {
			client.Transport = &oauth2.Transport{Source: options.TokenSource, Base: client.Transport}
		}
		client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		provider, err := transport.NewWorkingDraftClient(context.Background(), &client)
		if err != nil {
			return nil, safeError(err)
		}
		options.Provider = provider
	}
	return &Service{options.Provider, options.Converter, options.Store, options.Now, options.Attempts}, nil
}

var identityPattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

func ParseSource(value string) (SourceRef, error) {
	id := value
	tab := ""
	if strings.Contains(value, "://") {
		u, err := url.Parse(value)
		if err != nil || u.Scheme != "https" || u.Host != "docs.google.com" || u.User != nil {
			return SourceRef{}, fail(InvalidSource, "use a Google Docs document ID or https://docs.google.com/document/d/... URL")
		}
		parts := strings.Split(strings.Trim(u.Path, "/"), "/")
		if len(parts) < 3 || parts[0] != "document" || parts[1] != "d" {
			return SourceRef{}, fail(InvalidSource, "invalid Google Docs URL")
		}
		id = parts[2]
		tab = u.Query().Get("tab")
	}
	return canonicalSource(SourceRef{Provider: ProviderGoogleDocs, DocumentID: id, TabID: tab})
}
func canonicalSource(ref SourceRef) (SourceRef, error) {
	if ref.Provider == "" {
		ref.Provider = ProviderGoogleDocs
	}
	if ref.Provider != ProviderGoogleDocs || !identityPattern.MatchString(ref.DocumentID) || len(ref.DocumentID) > 256 || (ref.TabID != "" && (!regexp.MustCompile(`^[A-Za-z0-9_.-]+$`).MatchString(ref.TabID) || len(ref.TabID) > 256)) {
		return ref, fail(InvalidSource, "invalid Google Docs source identity")
	}
	return ref, nil
}
func sourceURL(ref SourceRef) string {
	target := "https://docs.google.com/document/d/" + ref.DocumentID + "/edit"
	if ref.TabID != "" {
		target += "?tab=" + url.QueryEscape(ref.TabID)
	}
	return target
}
func fail(code ErrorCode, message string) *Error { return &Error{Code: code, Message: message} }
func safeError(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return &Error{Code: TransportUnavailable, Message: "Google request canceled or timed out", Retryable: true}
	}
	if ge, ok := errors.AsType[*gapi.Error](err); ok {
		switch ge.Code {
		case 401:
			return fail(AccountUnavailable, "Google authorization expired; reconnect the account")
		case 403, 404:
			return fail(AccessDenied, "Google document is unavailable; grant this app access using the same account and OAuth application")
		}
	}
	return &Error{Code: TransportUnavailable, Message: "Google request failed", Retryable: true}
}
func (s *Service) read(ctx context.Context, ref SourceRef) (*drive.File, *docs.Document, Inspection, error) {
	ref, err := canonicalSource(ref)
	if err != nil {
		return nil, nil, Inspection{}, err
	}
	f, err := s.provider.File(ctx, ref.DocumentID)
	if err != nil {
		return nil, nil, Inspection{}, safeError(err)
	}
	if f == nil || f.Id != ref.DocumentID || f.Trashed || f.MimeType != transport.DocumentMimeType {
		return nil, nil, Inspection{}, fail(InvalidSource, "source must be an accessible native Google Doc")
	}
	if f.DriveId != "" {
		return nil, nil, Inspection{}, fail(UnsupportedContent, "Shared Drive documents are not supported")
	}
	d, err := s.provider.Document(ctx, ref.DocumentID)
	if err != nil {
		return nil, nil, Inspection{}, safeError(err)
	}
	if d == nil || d.DocumentId != ref.DocumentID {
		return nil, nil, Inspection{}, fail(InvalidSource, "Google returned a different document identity")
	}
	result := inspect(ref, d)
	if result.Source.TabID != ref.TabID && ref.TabID != "" {
		return nil, nil, Inspection{}, fail(InvalidSource, "requested tab does not match the document root tab")
	}
	return f, d, result, nil
}
func (s *Service) Inspect(ctx context.Context, source SourceRef) (Inspection, error) {
	_, _, i, e := s.read(ctx, source)
	return i, e
}
func (s *Service) Capture(ctx context.Context, source SourceRef) (Snapshot, error) {
	for attempt := 0; attempt < s.attempts; attempt++ {
		f, d, i, err := s.read(ctx, source)
		if err != nil {
			return Snapshot{}, err
		}
		raw, err := json.Marshal(d)
		if err != nil {
			return Snapshot{}, fail(UnsupportedContent, "document representation cannot be retained")
		}
		after, afterDoc, afterInspection, err := s.read(ctx, source)
		if err != nil {
			return Snapshot{}, err
		}
		afterRaw, err := json.Marshal(afterDoc)
		if err != nil {
			return Snapshot{}, fail(UnsupportedContent, "document representation cannot be retained")
		}
		if f.Version != after.Version || f.Name != after.Name || d.RevisionId != afterDoc.RevisionId || string(raw) != string(afterRaw) || i.Source != afterInspection.Source {
			continue
		}
		result := Snapshot{SchemaVersion: SchemaVersion, Source: i.Source, SourceURL: sourceURL(i.Source), Title: d.Title, ProviderRevision: d.RevisionId, ProviderVersion: versionString(f.Version), RetrievedAt: s.now().UTC(), ConversionVersion: ConversionVersion, RawContent: raw, Diagnostics: i.Diagnostics, BodyUsable: i.Supported && !i.SuggestionsPresent}
		if result.BodyUsable {
			content, diagnostics, err := s.converter.Convert(ctx, d)
			if err != nil {
				return Snapshot{}, fail(UnsupportedContent, "document conversion failed")
			}
			result.Diagnostics = append(result.Diagnostics, diagnostics...)
			result.BodyUsable = len(result.Diagnostics) == 0
			if result.BodyUsable {
				result.NormalizedContent = content
				result.NormalizedContent.Title = d.Title
			}
		}
		if err := seal(&result); err != nil {
			return Snapshot{}, err
		}
		if s.store != nil {
			if err := SaveSnapshot(ctx, s.store, result); err != nil {
				return Snapshot{}, err
			}
		}
		return result, nil
	}
	return Snapshot{}, &Error{Code: RemoteUnstable, Message: "document changed during capture; retry when editing pauses", Retryable: true}
}
