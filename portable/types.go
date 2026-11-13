package portable

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"golang.org/x/oauth2"
	"google.golang.org/api/drive/v3"
)

const SchemaVersion = 1
const ConversionVersion = "portable-gfm-v1"

type SourceRef struct {
	FileID   string `json:"file_id"`
	ObjectID string `json:"object_id,omitempty"`
	TabID    string `json:"tab_id,omitempty"`
	Kind     string `json:"kind"`
}
type IndexEntry struct {
	ID      string `json:"id"`
	Source  string `json:"source"`
	Label   string `json:"label"`
	Section string `json:"section,omitempty"`
	Path    string `json:"path,omitempty"`
	Role    string `json:"role"`
}
type Diagnostic struct {
	Code       string `json:"code"`
	Severity   string `json:"severity"`
	DocumentID string `json:"document_id,omitempty"`
	Reference  string `json:"reference,omitempty"`
	Message    string `json:"message"`
}
type Reservation struct {
	Path            string `json:"path"`
	ID              string `json:"id"`
	Kind            string `json:"kind"`
	PendingEquality bool   `json:"pending_equality,omitempty"`
}
type PlanRequest struct{ Config Config }
type Plan struct {
	SchemaVersion int           `json:"schema_version"`
	Project       string        `json:"project"`
	IndexDigest   string        `json:"index_digest"`
	ConfigDigest  string        `json:"config_digest"`
	Entries       []IndexEntry  `json:"entries"`
	Destinations  []Reservation `json:"destinations"`
	Diagnostics   []Diagnostic  `json:"diagnostics"`
}
type ExportRequest struct {
	Config      Config
	Destination string
}
type Result struct {
	Manifest    Manifest     `json:"manifest"`
	EntryPath   string       `json:"entry_path"`
	Status      string       `json:"status"`
	Diagnostics []Diagnostic `json:"diagnostics"`
}
type VerifyRequest struct{ Directory string }
type Verification struct {
	LocalComplete        bool         `json:"local_complete"`
	NoOnlineDependencies bool         `json:"no_online_dependencies"`
	Diagnostics          []Diagnostic `json:"diagnostics"`
}
type InitRequest struct {
	Directory, Project, IndexSource string
	Layout                          Layout
}
type InitResult struct {
	Files []string `json:"files"`
}
type Evidence struct {
	Source       SourceRef `json:"source"`
	CanonicalURL string    `json:"canonical_url,omitempty"`
	Name         string    `json:"name"`
	MIME         string    `json:"mime"`
	Version      int64     `json:"version,omitempty"`
	Revision     string    `json:"revision,omitempty"`
	Parents      []string  `json:"parents,omitempty"`
	SourceDigest string    `json:"source_digest"`
	CapturedFrom time.Time `json:"captured_from"`
	CapturedTo   time.Time `json:"captured_to"`
}
type DocumentRecord struct {
	ID            string   `json:"id"`
	Evidence      Evidence `json:"evidence"`
	Path          string   `json:"path"`
	ContentDigest string   `json:"content_digest"`
}
type Occurrence struct {
	DocumentID   string `json:"document_id"`
	SourceDigest string `json:"source_digest"`
	Position     int    `json:"position"`
	ObjectID     string `json:"object_id,omitempty"`
	Alt          string `json:"alt"`
	Caption      string `json:"caption,omitempty"`
	AssetPath    string `json:"asset_path"`
}
type AssetRecord struct {
	Path        string       `json:"path"`
	MIME        string       `json:"mime"`
	Digest      string       `json:"digest"`
	Size        int64        `json:"size"`
	Sources     []Evidence   `json:"sources"`
	Occurrences []Occurrence `json:"occurrences"`
}
type GeneratedFile struct {
	Path          string `json:"path"`
	ContentDigest string `json:"content_digest"`
}
type LinkRecord struct {
	From   string `json:"from"`
	Target string `json:"target"`
	Online bool   `json:"online"`
}
type Manifest struct {
	SchemaVersion        int              `json:"schema_version"`
	Project              string           `json:"project"`
	Index                DocumentRecord   `json:"index"`
	ConfigDigest         string           `json:"config_digest"`
	ConversionVersion    string           `json:"conversion_version"`
	CapturedFrom         time.Time        `json:"captured_from"`
	CapturedTo           time.Time        `json:"captured_to"`
	Documents            []DocumentRecord `json:"documents"`
	Assets               []AssetRecord    `json:"assets"`
	GeneratedFiles       []GeneratedFile  `json:"generated_files"`
	Links                []LinkRecord     `json:"links"`
	Diagnostics          []Diagnostic     `json:"diagnostics"`
	LocalComplete        bool             `json:"local_complete"`
	NoOnlineDependencies bool             `json:"no_online_dependencies"`
}

// Provider exposes read operations only. Implementations must honor response bounds.
// Native image capabilities stay in private capture state and never enter results.
type Provider interface {
	File(context.Context, string) (*drive.File, error)
	Document(context.Context, string, int64) (json.RawMessage, error)
	Download(context.Context, string, int64) ([]byte, error)
	Children(context.Context, string, string) (*drive.FileList, error)
}
type Converter interface {
	Parse(context.Context, []byte) (map[string]any, error)
	Render(context.Context, map[string]any) ([]byte, error)
}
type Options struct {
	OutputStore OutputStore
	// ImageHTTPClient is a separate, credential-free transport; never reuse an OAuth client.
	ImageHTTPClient *http.Client
	Provider        Provider
	HTTPClient      *http.Client
	TokenSource     oauth2.TokenSource
	Converter       Converter
	Now             func() time.Time
}
type API interface {
	Init(context.Context, InitRequest) (InitResult, error)
	Plan(context.Context, PlanRequest) (Plan, error)
	Export(context.Context, ExportRequest) (Result, error)
	Verify(context.Context, VerifyRequest) (Verification, error)
}

// These types deliberately cannot be constructed, serialized or accepted by a
// public output API. Only the coordinator seals a complete, checked attempt.
type captureDraft struct {
	attempt  *attemptID
	evidence Evidence
	model    map[string]any
	handles  map[string]string
	headings map[string]string
}
type attemptID struct{ marker byte }
type materializedAssets struct {
	attempt     *attemptID
	assets      []capturedAsset
	occurrences []Occurrence
}
type capturedAsset struct {
	evidence                     Evidence
	bytes                        []byte
	mime, digest, path, objectID string
}
type finalizedCapture struct {
	seal        *attemptID
	evidence    Evidence
	model       map[string]any
	assets      []capturedAsset
	occurrences []Occurrence
	headings    map[string]string
}
type materializer interface {
	materialize(context.Context, *captureDraft) (materializedAssets, error)
}
