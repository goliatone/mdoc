package app

import "context"

type CommonOptions struct {
	Config           string
	Profile          string
	JSON             bool
	Quiet            bool
	AllowDirect      bool
	Sources          []string
	Root             string
	Entry            string
	Remote           bool
	Overrides        map[string]any
	FieldFiles       []string
	FieldAssignments []string
	Bundle           string
}

type InitOptions struct {
	Config  string
	Force   bool
	Profile string
}

type ValidateOptions struct {
	CommonOptions
	Remote bool
}

type DoctorOptions struct {
	CommonOptions
}

type PublishOptions struct {
	CommonOptions
	DryRun    bool
	Files     []string
	NewReview bool
}

type ReconcileOptions struct {
	CommonOptions
	AbandonOperation bool
}

type RemapOptions struct {
	CommonOptions
	From string
	To   string
}

type OpenOptions struct {
	CommonOptions
	File string
}

type ReviewPullOptions struct {
	CommonOptions
	Publication string
	PatchOutput string
	Overwrite   bool
}

type ReviewBootstrapOptions struct {
	ReviewPullOptions
	Document     string
	ReviewExport string
	Partial      bool
}

type ReviewResultCode string

const (
	ReviewCodeNoChanges          ReviewResultCode = "review_no_changes"
	ReviewCodeCleanPatch         ReviewResultCode = "review_clean_patch"
	ReviewCodeConflict           ReviewResultCode = "review_conflict"
	ReviewCodeUnsupported        ReviewResultCode = "review_unsupported"
	ReviewCodeSuggestionsOpen    ReviewResultCode = "review_suggestions_open"
	ReviewCodeBaselineMissing    ReviewResultCode = "review_baseline_missing"
	ReviewCodeSnapshotInvalid    ReviewResultCode = "review_snapshot_invalid"
	ReviewCodeExportUnstable     ReviewResultCode = "review_export_unstable"
	ReviewCodeTargetMismatch     ReviewResultCode = "review_target_mismatch"
	ReviewCodePullDisabled       ReviewResultCode = "review_pull_disabled"
	ReviewCodePublicationInvalid ReviewResultCode = "review_publication_invalid"
)

type Service interface {
	Init(context.Context, InitOptions) error
	Setup(context.Context, CommonOptions) error
	Validate(context.Context, ValidateOptions) error
	Publish(context.Context, PublishOptions) error
	Plan(context.Context, PublishOptions) error
	Status(context.Context, CommonOptions) error
	Doctor(context.Context, DoctorOptions) error
	Reconcile(context.Context, ReconcileOptions) error
	Remap(context.Context, RemapOptions) error
	Open(context.Context, OpenOptions) error
	ReviewPull(context.Context, ReviewPullOptions) error
	ReviewBootstrap(context.Context, ReviewBootstrapOptions) error
}

type UnavailableService struct{}

func (UnavailableService) Init(context.Context, InitOptions) error    { return Unavailable("init") }
func (UnavailableService) Setup(context.Context, CommonOptions) error { return Unavailable("setup") }
func (UnavailableService) Validate(context.Context, ValidateOptions) error {
	return Unavailable("validate")
}
func (UnavailableService) Publish(context.Context, PublishOptions) error {
	return Unavailable("publish")
}
func (UnavailableService) Plan(context.Context, PublishOptions) error  { return Unavailable("plan") }
func (UnavailableService) Status(context.Context, CommonOptions) error { return Unavailable("status") }
func (UnavailableService) Doctor(context.Context, DoctorOptions) error { return Unavailable("doctor") }
func (UnavailableService) Reconcile(context.Context, ReconcileOptions) error {
	return Unavailable("state reconcile")
}
func (UnavailableService) Remap(context.Context, RemapOptions) error {
	return Unavailable("state remap")
}
func (UnavailableService) Open(context.Context, OpenOptions) error { return Unavailable("open") }
func (UnavailableService) ReviewPull(context.Context, ReviewPullOptions) error {
	return Unavailable("review pull")
}
func (UnavailableService) ReviewBootstrap(context.Context, ReviewBootstrapOptions) error {
	return Unavailable("review bootstrap")
}
