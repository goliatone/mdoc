package contribution

import (
	"context"
	"net/http"
	"time"

	"github.com/goliatone/mdoc/workingdraft"
	"golang.org/x/oauth2"
)

const SchemaVersion = 1
const SelectionPolicy = "interior-run-v1"
const MaxMarkdown = 1 << 20
const MaxDOCX = 10 << 20
const MaxText = 64 << 10
const MaxPlan = 16 << 20

type Kind string

const (
	CreateKind Kind = "create"
	UpdateKind Kind = "update"
	ReplyKind  Kind = "reply"
)

type Status string

const (
	Committed Status = "committed"
	NotSent   Status = "not_sent"
	Conflict  Status = "conflict"
	Unknown   Status = "unknown"
)

type OperationMeta struct {
	SchemaVersion  int    `json:"schema_version"`
	Namespace      string `json:"namespace"`
	OperationID    string `json:"operation_id"`
	CorrelationID  string `json:"correlation_id,omitempty"`
	AccountBinding string `json:"account_binding"`
}
type CreateInput struct {
	Meta     OperationMeta `json:"meta"`
	ParentID string        `json:"parent_id"`
	Title    string        `json:"title"`
	Markdown string        `json:"markdown"`
}
type TextSelection struct {
	ParagraphStart  int64  `json:"paragraph_start"`
	Start           int    `json:"start"`
	End             int    `json:"end"`
	ExpectedText    string `json:"expected_text"`
	ParagraphDigest string `json:"paragraph_digest"`
}
type UpdateInput struct {
	Meta             OperationMeta          `json:"meta"`
	Source           workingdraft.SourceRef `json:"source"`
	ExpectedRevision string                 `json:"expected_revision"`
	Selection        TextSelection          `json:"selection"`
	Replacement      string                 `json:"replacement"`
}
type ReplyInput struct {
	Meta      OperationMeta          `json:"meta"`
	Source    workingdraft.SourceRef `json:"source"`
	CommentID string                 `json:"comment_id"`
	Content   string                 `json:"content"`
}
type Artifact struct {
	DOCX            []byte `json:"docx"`
	Digest          string `json:"digest"`
	RendererVersion string `json:"renderer_version"`
}
type CreatePlan struct {
	Kind     Kind        `json:"kind"`
	Input    CreateInput `json:"input"`
	Artifact Artifact    `json:"artifact"`
	Digest   string      `json:"digest"`
}
type RunEvidence struct {
	Start           int64  `json:"start"`
	End             int64  `json:"end"`
	StyleDigest     string `json:"style_digest"`
	SelectionPolicy string `json:"selection_policy"`
}
type UpdatePlan struct {
	Kind           Kind        `json:"kind"`
	Input          UpdateInput `json:"input"`
	Before         string      `json:"before"`
	After          string      `json:"after"`
	SnapshotDigest string      `json:"snapshot_digest"`
	ContentDigest  string      `json:"content_digest"`
	Start          int64       `json:"start"`
	End            int64       `json:"end"`
	Run            RunEvidence `json:"run"`
	Digest         string      `json:"digest"`
}
type ThreadState struct {
	Fingerprint string `json:"fingerprint"`
	ModifiedAt  string `json:"modified_at"`
}
type ReplyPlan struct {
	Kind   Kind        `json:"kind"`
	Input  ReplyInput  `json:"input"`
	Thread ThreadState `json:"thread"`
	Digest string      `json:"digest"`
}

// Attempt is a host assertion, not a lock. FirstTransmission may be true only
// after the host atomically persisted a fresh sending claim for this operation.
// A previously sending/unknown operation must use InspectOutcome, never dispatch.
type Attempt struct {
	ID                string `json:"id"`
	FirstTransmission bool   `json:"first_transmission"`
}
type CreateRequest struct {
	Plan           CreatePlan
	ExpectedDigest string
	Attempt        Attempt
}
type UpdateRequest struct {
	Plan           UpdatePlan
	ExpectedDigest string
	Attempt        Attempt
}
type ReplyRequest struct {
	Plan           ReplyPlan
	ExpectedDigest string
	Attempt        Attempt
}
type Outcome struct {
	Kind             Kind          `json:"kind"`
	Meta             OperationMeta `json:"meta"`
	RequestDigest    string        `json:"request_digest"`
	AttemptID        string        `json:"attempt_id"`
	Status           Status        `json:"status"`
	Code             Code          `json:"code,omitempty"`
	DocumentID       string        `json:"document_id,omitempty"`
	TabID            string        `json:"tab_id,omitempty"`
	CommentID        string        `json:"comment_id,omitempty"`
	ReplyID          string        `json:"reply_id,omitempty"`
	URL              string        `json:"url,omitempty"`
	ProviderRevision string        `json:"provider_revision,omitempty"`
	AcknowledgedAt   time.Time     `json:"acknowledged_at,omitempty"`
	Diagnostics      []string      `json:"diagnostics,omitempty"`
}
type InspectInput struct {
	Create   *CreatePlan `json:"create,omitempty"`
	Update   *UpdatePlan `json:"update,omitempty"`
	Reply    *ReplyPlan  `json:"reply,omitempty"`
	Previous Outcome     `json:"previous"`
}
type Evidence string

const (
	Observed     Evidence = "observed"
	Incomplete   Evidence = "incomplete"
	Inaccessible Evidence = "inaccessible"
	Ambiguous    Evidence = "ambiguous"
	AbsentAtRead Evidence = "absent_at_read"
)

type Inspection struct {
	Outcome       Outcome   `json:"outcome"`
	Evidence      Evidence  `json:"evidence"`
	ObservedAt    time.Time `json:"observed_at"`
	Postcondition *bool     `json:"postcondition,omitempty"`
	Candidates    []string  `json:"candidates,omitempty"`
	Diagnostics   []string  `json:"diagnostics,omitempty"`
}
type Renderer interface {
	Render(context.Context, string) (Artifact, error)
}
type Options struct {
	AccountBinding string
	TokenSource    oauth2.TokenSource
	Transport      http.RoundTripper
	HTTPClient     *http.Client
	Renderer       Renderer
	Now            func() time.Time
}
type API interface {
	PrepareCreate(context.Context, CreateInput) (CreatePlan, error)
	Create(context.Context, CreateRequest) (Outcome, error)
	PrepareUpdate(context.Context, UpdateInput) (UpdatePlan, error)
	ApplyUpdate(context.Context, UpdateRequest) (Outcome, error)
	PrepareReply(context.Context, ReplyInput) (ReplyPlan, error)
	Reply(context.Context, ReplyRequest) (Outcome, error)
	InspectOutcome(context.Context, InspectInput) (Inspection, error)
}
