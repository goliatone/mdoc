package workingdraft

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"golang.org/x/oauth2"
	"google.golang.org/api/docs/v1"
	"google.golang.org/api/drive/v3"
)

const ProviderGoogleDocs = "google_docs"
const ConversionVersion = "google-docs-prose-v1"
const SchemaVersion = 1

type ErrorCode string

const (
	InvalidSource        ErrorCode = "invalid_source"
	AccessDenied         ErrorCode = "access_denied"
	AccountUnavailable   ErrorCode = "account_unavailable"
	UnsupportedContent   ErrorCode = "unsupported_content"
	SuggestionsPending   ErrorCode = "suggestions_pending"
	RemoteUnstable       ErrorCode = "remote_unstable"
	IncompleteDiscussion ErrorCode = "incomplete_discussion"
	SnapshotCorrupt      ErrorCode = "snapshot_corrupt"
	TransportUnavailable ErrorCode = "transport_unavailable"
)

type Error struct {
	Code      ErrorCode `json:"code"`
	Message   string    `json:"message"`
	Retryable bool      `json:"retryable"`
}

func (e *Error) Error() string { return string(e.Code) + ": " + e.Message }

type SourceRef struct {
	Provider   string `json:"provider"`
	DocumentID string `json:"document_id"`
	TabID      string `json:"tab_id,omitempty"`
}
type Diagnostic struct {
	Code    ErrorCode `json:"code"`
	Message string    `json:"message"`
}
type Capabilities struct {
	Capture    bool `json:"capture"`
	Discussion bool `json:"discussion"`
}
type Inspection struct {
	Source             SourceRef    `json:"source"`
	Title              string       `json:"title"`
	Capabilities       Capabilities `json:"capabilities"`
	Diagnostics        []Diagnostic `json:"diagnostics"`
	SuggestionsPresent bool         `json:"suggestions_present"`
	Supported          bool         `json:"supported"`
}
type Content struct {
	Title string `json:"title"`
	Body  string `json:"body"`
}
type Snapshot struct {
	SchemaVersion     int             `json:"schema_version"`
	Source            SourceRef       `json:"source"`
	SourceURL         string          `json:"source_url"`
	Title             string          `json:"title"`
	ProviderRevision  string          `json:"provider_revision,omitempty"`
	ProviderVersion   string          `json:"provider_version,omitempty"`
	RetrievedAt       time.Time       `json:"retrieved_at"`
	ConversionVersion string          `json:"conversion_version"`
	RawContent        json.RawMessage `json:"raw_content"`
	NormalizedContent Content         `json:"normalized_content"`
	ContentDigest     string          `json:"content_digest"`
	SnapshotDigest    string          `json:"snapshot_digest"`
	Diagnostics       []Diagnostic    `json:"diagnostics"`
	BodyUsable        bool            `json:"body_usable"`
}
type Change struct {
	Field  string `json:"field"`
	Before string `json:"before"`
	After  string `json:"after"`
}
type Proposal struct {
	BaseSnapshotDigest     string       `json:"base_snapshot_digest"`
	IncomingSnapshotDigest string       `json:"incoming_snapshot_digest"`
	CurrentContentDigest   string       `json:"current_content_digest"`
	ProposedContent        Content      `json:"proposed_content"`
	Changes                []Change     `json:"changes"`
	LocalDivergence        bool         `json:"local_divergence"`
	Diagnostics            []Diagnostic `json:"diagnostics"`
	Digest                 string       `json:"digest"`
}
type CompareInput struct {
	Baseline Snapshot `json:"baseline"`
	Incoming Snapshot `json:"incoming"`
	Current  Content  `json:"current"`
}
type Author struct {
	DisplayName string `json:"display_name,omitempty"`
	Email       string `json:"email,omitempty"`
	Me          bool   `json:"me"`
}
type Reply struct {
	ID         string  `json:"id"`
	Content    string  `json:"content"`
	Author     *Author `json:"author"`
	CreatedAt  string  `json:"created_at,omitempty"`
	ModifiedAt string  `json:"modified_at,omitempty"`
	Deleted    bool    `json:"deleted"`
	Action     string  `json:"action,omitempty"`
}
type Thread struct {
	ID            string  `json:"id"`
	Content       string  `json:"content"`
	Author        *Author `json:"author"`
	CreatedAt     string  `json:"created_at,omitempty"`
	ModifiedAt    string  `json:"modified_at,omitempty"`
	Resolved      bool    `json:"resolved"`
	Deleted       bool    `json:"deleted"`
	QuotedContext string  `json:"quoted_context,omitempty"`
	Anchor        *string `json:"anchor"`
	SourceURL     string  `json:"source_url,omitempty"`
	Replies       []Reply `json:"replies"`
}
type DiscussionInput struct {
	Source SourceRef `json:"source"`
	Cursor string    `json:"cursor,omitempty"`
}
type DiscussionPage struct {
	Source      SourceRef `json:"source"`
	Threads     []Thread  `json:"threads"`
	NextCursor  string    `json:"next_cursor,omitempty"`
	Complete    bool      `json:"complete"`
	RetrievedAt time.Time `json:"retrieved_at"`
}

type SnapshotStore interface {
	Put(context.Context, string, []byte) error
	Get(context.Context, string) ([]byte, error)
}
type Provider interface {
	File(context.Context, string) (*drive.File, error)
	Document(context.Context, string) (*docs.Document, error)
	Comments(context.Context, string, string) (*drive.CommentList, error)
}
type Converter interface {
	Convert(context.Context, *docs.Document) (Content, []Diagnostic, error)
}
type Options struct {
	HTTPClient  *http.Client
	TokenSource oauth2.TokenSource
	Provider    Provider
	Converter   Converter
	Store       SnapshotStore
	Now         func() time.Time
	Attempts    int
}
type API interface {
	Inspect(context.Context, SourceRef) (Inspection, error)
	Capture(context.Context, SourceRef) (Snapshot, error)
	Compare(context.Context, CompareInput) (Proposal, error)
	ReadDiscussion(context.Context, DiscussionInput) (DiscussionPage, error)
}
