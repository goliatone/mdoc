package cli

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/goliatone/mdoc/internal/app"
	"github.com/goliatone/mdoc/internal/auth"
	"github.com/goliatone/mdoc/internal/config"
	"github.com/goliatone/mdoc/internal/report"
)

type fakeAuth struct {
	loginCalls  int
	statusCalls int
	logoutCalls int
}

func (f *fakeAuth) Login(context.Context) (auth.LoginResult, error) {
	f.loginCalls++
	return auth.LoginResult{TokenPath: "/private/token.json"}, nil
}

func (f *fakeAuth) Status(context.Context) (auth.Status, error) {
	f.statusCalls++
	return auth.Status{
		Configured:  true,
		SignedIn:    true,
		Refreshable: true,
		Account:     "author@example.test",
		ExpiresAt:   time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC),
		TokenPath:   "/private/token.json",
	}, nil
}

func (f *fakeAuth) Logout(context.Context) error {
	f.logoutCalls++
	return nil
}

func TestKongRoutesAuthCommands(t *testing.T) {
	service := &fakeAuth{}
	for _, arguments := range [][]string{{"auth", "login"}, {"auth", "status"}, {"auth", "logout"}} {
		var stdout strings.Builder
		var stderr strings.Builder
		if err := Execute(arguments, &stdout, &stderr, service); err != nil {
			t.Fatalf("%v: %v", arguments, err)
		}
		if stderr.Len() != 0 {
			t.Fatalf("%v stderr = %q", arguments, stderr.String())
		}
	}
	if service.loginCalls != 1 || service.statusCalls != 1 || service.logoutCalls != 1 {
		t.Fatalf("calls = login %d status %d logout %d", service.loginCalls, service.statusCalls, service.logoutCalls)
	}
}

func TestStatusOutputContainsNoTokenFields(t *testing.T) {
	service := &fakeAuth{}
	var stdout strings.Builder
	if err := Execute([]string{"auth", "status"}, &stdout, &strings.Builder{}, service); err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"access_token", "refresh_token", "client_secret"} {
		if strings.Contains(stdout.String(), forbidden) {
			t.Fatalf("status output contains %q", forbidden)
		}
	}
}

func TestUsageError(t *testing.T) {
	err := Execute([]string{"auth", "unknown"}, &strings.Builder{}, &strings.Builder{}, &fakeAuth{})
	var usageErr *UsageError
	if !errors.As(err, &usageErr) {
		t.Fatalf("error = %v", err)
	}
}

func TestBundleSelectorIsShownAndForwarded(t *testing.T) {
	service := &fakeApp{}
	if err := Execute([]string{"validate", "--bundle", "report"}, &strings.Builder{}, &strings.Builder{}, &fakeAuth{}, service); err != nil {
		t.Fatal(err)
	}
	if service.validateOptions.Bundle != "report" {
		t.Fatalf("bundle option = %q", service.validateOptions.Bundle)
	}
	field, ok := reflect.TypeOf(CommonFlags{}).FieldByName("Bundle")
	if !ok || field.Tag.Get("name") != "bundle" || !strings.Contains(field.Tag.Get("help"), "bundle publication") {
		t.Fatalf("bundle help tag = %q", field.Tag)
	}
}

type fakeApp struct {
	calls            []string
	abandonOperation bool
	validateOptions  app.ValidateOptions
	reviewOptions    app.ReviewPullOptions
	bootstrapOptions app.ReviewBootstrapOptions
}

func (f *fakeApp) record(name string) error                       { f.calls = append(f.calls, name); return nil }
func (f *fakeApp) Init(context.Context, app.InitOptions) error    { return f.record("init") }
func (f *fakeApp) Setup(context.Context, app.CommonOptions) error { return f.record("setup") }
func (f *fakeApp) Validate(_ context.Context, options app.ValidateOptions) error {
	f.validateOptions = options
	return f.record("validate")
}
func (f *fakeApp) Publish(context.Context, app.PublishOptions) error { return f.record("publish") }
func (f *fakeApp) Plan(context.Context, app.PublishOptions) error    { return f.record("plan") }
func (f *fakeApp) Status(context.Context, app.CommonOptions) error   { return f.record("status") }
func (f *fakeApp) Doctor(context.Context, app.DoctorOptions) error   { return f.record("doctor") }

func (f *fakeApp) Reconcile(_ context.Context, options app.ReconcileOptions) error {
	f.abandonOperation = options.AbandonOperation
	return f.record("reconcile")
}
func (f *fakeApp) Remap(context.Context, app.RemapOptions) error { return f.record("remap") }
func (f *fakeApp) Open(context.Context, app.OpenOptions) error   { return f.record("open") }
func (f *fakeApp) ReviewPull(_ context.Context, options app.ReviewPullOptions) error {
	f.reviewOptions = options
	return f.record("review-pull")
}
func (f *fakeApp) ReviewBootstrap(_ context.Context, options app.ReviewBootstrapOptions) error {
	f.bootstrapOptions = options
	return f.record("review-bootstrap")
}

func TestKongRoutesApplicationCommands(t *testing.T) {
	service := &fakeApp{}
	reviewExport := filepath.Join(t.TempDir(), "review.md")
	if err := os.WriteFile(reviewExport, []byte("# Review\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tests := [][]string{
		{"init"},
		{"setup"},
		{"validate"},
		{"publish", "--dry-run", "--file", "docs/a.md"},
		{"plan", "--file", "docs/a.md"},
		{"status"},
		{"doctor"},
		{"state", "reconcile", "--abandon-operation"},
		{"state", "remap", "--from", "a.md", "--to", "b.md"},
		{"open"},
		{"review", "pull", "report", "--json", "--output", "review.patch", "--overwrite"},
		{"review", "bootstrap", "report", "--document", "doc-123", "--review-export", reviewExport, "--partial", "--json", "--output", "bootstrap.patch", "--overwrite"},
	}
	for _, arguments := range tests {
		if err := Execute(arguments, &strings.Builder{}, &strings.Builder{}, &fakeAuth{}, service); err != nil {
			t.Fatalf("%v: %v", arguments, err)
		}
	}
	want := "init,setup,validate,publish,plan,status,doctor,reconcile,remap,open,review-pull,review-bootstrap"
	if got := strings.Join(service.calls, ","); got != want {
		t.Fatalf("calls = %q, want %q", got, want)
	}
	if !service.abandonOperation {
		t.Fatal("state reconcile did not receive --abandon-operation")
	}
	if service.reviewOptions.Publication != "report" || !service.reviewOptions.JSON || filepath.Base(service.reviewOptions.PatchOutput) != "review.patch" || !service.reviewOptions.Overwrite {
		t.Fatalf("review pull options = %#v", service.reviewOptions)
	}
	if service.bootstrapOptions.Publication != "report" || service.bootstrapOptions.Document != "doc-123" || service.bootstrapOptions.ReviewExport != reviewExport || !service.bootstrapOptions.Partial || !service.bootstrapOptions.JSON || filepath.Base(service.bootstrapOptions.PatchOutput) != "bootstrap.patch" || !service.bootstrapOptions.Overwrite {
		t.Fatalf("review bootstrap options = %#v", service.bootstrapOptions)
	}
}

func TestReviewPullDefaultsPublicationAndKeepsOutputContractsDistinct(t *testing.T) {
	service := &fakeApp{}
	if err := Execute([]string{"review", "pull", "--state", "custom.json"}, &strings.Builder{}, &strings.Builder{}, &fakeAuth{}, service); err != nil {
		t.Fatal(err)
	}
	if service.reviewOptions.Publication != "" || service.reviewOptions.PatchOutput != "" {
		t.Fatalf("review defaults = %#v", service.reviewOptions)
	}
	profile, ok := service.reviewOptions.Overrides["profile"].(map[string]any)
	if !ok || profile["state_file"] != "custom.json" {
		t.Fatalf("review state override = %#v", service.reviewOptions.Overrides)
	}
}

func TestReviewPullRejectsOverwriteWithoutOutput(t *testing.T) {
	err := Execute([]string{"review", "pull", "--overwrite"}, &strings.Builder{}, &strings.Builder{}, &fakeAuth{}, &fakeApp{})
	var usage *UsageError
	if !errors.As(err, &usage) || !strings.Contains(err.Error(), "requires --output") {
		t.Fatalf("overwrite error = %v", err)
	}
}

func TestReviewBootstrapRequiresDocumentAndRejectsOverwriteWithoutOutput(t *testing.T) {
	for _, arguments := range [][]string{
		{"review", "bootstrap"},
		{"review", "bootstrap", "--document", "doc-123", "--overwrite"},
		{"review", "bootstrap", "--document", "doc-123", "--partial"},
	} {
		err := Execute(arguments, &strings.Builder{}, &strings.Builder{}, &fakeAuth{}, &fakeApp{})
		var usage *UsageError
		if !errors.As(err, &usage) {
			t.Fatalf("%v error = %v", arguments, err)
		}
	}
}

func TestFieldFlagsPreserveDeclaredOrder(t *testing.T) {
	service := &fakeApp{}
	arguments := []string{"validate", "--fields-file", "one.yaml", "--field", `/name="first"`, "--fields-file", "two.json", "--field", `/name="second"`}
	if err := Execute(arguments, &strings.Builder{}, &strings.Builder{}, &fakeAuth{}, service); err != nil {
		t.Fatal(err)
	}
	if strings.Join(service.validateOptions.FieldFiles, ",") != "one.yaml,two.json" {
		t.Fatalf("field files = %#v", service.validateOptions.FieldFiles)
	}
	if strings.Join(service.validateOptions.FieldAssignments, ",") != `/name="first",/name="second"` {
		t.Fatalf("field assignments = %#v", service.validateOptions.FieldAssignments)
	}
}

func TestProfileCommandsUseLocalProjectOnly(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	path := filepath.Join(root, ".mdoc.yaml")
	if err := config.WriteNeutralProject(path, "testing", false); err != nil {
		t.Fatal(err)
	}
	service := &fakeApp{}
	var output strings.Builder
	if err := Execute([]string{"profile", "add", "review"}, &output, &strings.Builder{}, &fakeAuth{}, service); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	if err := Execute([]string{"profile", "list"}, &output, &strings.Builder{}, &fakeAuth{}, service); err != nil {
		t.Fatal(err)
	}
	if output.String() != "review\ntesting\n" {
		t.Fatalf("profile list = %q", output.String())
	}
	output.Reset()
	if err := Execute([]string{"profile", "show", "review", "--json"}, &output, &strings.Builder{}, &fakeAuth{}, service); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), `"name": "review"`) || !strings.Contains(output.String(), `"reference_docx": ""`) {
		t.Fatalf("profile show = %q", output.String())
	}
	if len(service.calls) != 0 {
		t.Fatalf("profile commands called application services: %#v", service.calls)
	}
}

func TestProfileReportsHonorResolvedOutputFormat(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	path := filepath.Join(root, ".mdoc.yaml")
	if err := config.WriteNeutralProject(path, "testing", false); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MDOC_OUTPUT_FORMAT", "json")
	var output strings.Builder
	if err := Execute([]string{"profile", "list"}, &output, &strings.Builder{}, &fakeAuth{}, &fakeApp{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), `"profiles": [`) {
		t.Fatalf("environment JSON profile list = %q", output.String())
	}

	t.Setenv("MDOC_OUTPUT_QUIET", "true")
	output.Reset()
	if err := Execute([]string{"profile", "list"}, &output, &strings.Builder{}, &fakeAuth{}, &fakeApp{}); err != nil {
		t.Fatal(err)
	}
	if output.Len() != 0 {
		t.Fatalf("quiet profile list = %q", output.String())
	}

	if err := Execute([]string{"profile", "show", "testing", "--output=human", "--quiet=false"}, &output, &strings.Builder{}, &fakeAuth{}, &fakeApp{}); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(output.String(), "Profile: testing\n") {
		t.Fatalf("explicit human profile show = %q", output.String())
	}
}

func TestConfiguredJSONKeepsCommandFailureMachineReadable(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	path := filepath.Join(root, ".mdoc.yaml")
	if err := config.WriteNeutralProject(path, "testing", false); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	data = []byte(strings.Replace(string(data), "format: human", "format: json", 1))
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}

	args := []string{"profile", "show", "missing"}
	var stdout strings.Builder
	var stderr strings.Builder
	err = Execute(args, &stdout, &stderr, &fakeAuth{}, &fakeApp{})
	if err == nil {
		t.Fatal("expected profile selection failure")
	}
	WriteError(&stderr, err, WantsJSON(args))
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q", stdout.String())
	}
	var report ErrorOutput
	if decodeErr := json.Unmarshal([]byte(stderr.String()), &report); decodeErr != nil {
		t.Fatalf("stderr is not JSON: %q: %v", stderr.String(), decodeErr)
	}
	if report.Code != "profile_invalid" || report.ExitCode != 2 {
		t.Fatalf("error report = %#v", report)
	}
}

func TestConfiguredJSONKeepsStatusConfigFailureReportsMachineReadable(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	path := filepath.Join(root, ".mdoc.yaml")
	if err := os.WriteFile(path, []byte(`version: 2
project_id: testing
output:
  format: json
profiles:
  testing:
    unexpected: true
`), 0o600); err != nil {
		t.Fatal(err)
	}
	args := []string{"status"}
	var stdout strings.Builder
	var stderr strings.Builder
	err := Execute(args, &stdout, &stderr, &fakeAuth{})
	if err == nil {
		t.Fatal("expected invalid config")
	}
	WriteError(&stderr, err, WantsJSON(args))
	var status report.LocalStatusReport
	if decodeErr := json.Unmarshal([]byte(stdout.String()), &status); decodeErr != nil {
		t.Fatalf("stdout is not JSON: %q: %v", stdout.String(), decodeErr)
	}
	var failure ErrorOutput
	if decodeErr := json.Unmarshal([]byte(stderr.String()), &failure); decodeErr != nil {
		t.Fatalf("stderr is not JSON: %q: %v", stderr.String(), decodeErr)
	}
	if status.Result != "invalid" || failure.Code != "local_status_config_invalid" {
		t.Fatalf("status=%#v failure=%#v", status, failure)
	}
}

func TestWantsJSONRecognizesBooleanAndOutputForms(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	path := filepath.Join(root, ".mdoc.yaml")
	if err := config.WriteNeutralProject(path, "testing", false); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Replace(string(data), "format: human", "format: json", 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		args []string
		want bool
	}{
		{[]string{"status", "--json=true"}, true},
		{[]string{"status", "--output=json"}, true},
		{[]string{"status", "--output", "json"}, true},
		{[]string{"status"}, true},
		{[]string{"status", "--json=false"}, false},
		{[]string{"status", "--output=human"}, false},
		{[]string{"status", "--output=json", "--json=false"}, false},
		{[]string{"status", "--", "--json=false"}, true},
	} {
		if got := WantsJSON(test.args); got != test.want {
			t.Fatalf("WantsJSON(%#v) = %v, want %v", test.args, got, test.want)
		}
	}
}

func TestConfigShowIncludesExplicitOverridesAndOrigins(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	if err := config.WriteNeutralProject(filepath.Join(root, ".mdoc.yaml"), "testing", false); err != nil {
		t.Fatal(err)
	}
	var output strings.Builder
	if err := Execute([]string{"config", "show", "--pandoc", "custom-pandoc", "--json"}, &output, &strings.Builder{}, &fakeAuth{}, &fakeApp{}); err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{`"pandoc_binary": "custom-pandoc"`, `"kind": "flag"`} {
		if !strings.Contains(output.String(), expected) {
			t.Fatalf("config show output = %q", output.String())
		}
	}
}

func TestConfigAndProfileShowRedactFieldValues(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	project := `version: 3
project_id: redacted-fields
profiles:
  review:
    sources:
      include: [README.md]
    entry: report
    field_defaults:
      default_secret: do-not-print-default
    publications:
      report:
        kind: source
        source: README.md
        fields:
          publication_secret: do-not-print-publication
`
	if err := os.WriteFile(filepath.Join(root, ".mdoc.yaml"), []byte(project), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, arguments := range [][]string{{"config", "show", "--json"}, {"profile", "show", "review", "--json"}} {
		var output strings.Builder
		if err := Execute(arguments, &output, &strings.Builder{}, &fakeAuth{}, &fakeApp{}); err != nil {
			t.Fatalf("%v: %v", arguments, err)
		}
		for _, secret := range []string{"do-not-print-default", "do-not-print-publication"} {
			if strings.Contains(output.String(), secret) {
				t.Fatalf("%v leaked %q: %s", arguments, secret, output.String())
			}
		}
		if !strings.Contains(output.String(), `"field_summary"`) || !strings.Contains(output.String(), `"warning"`) {
			t.Fatalf("%v missing summary: %s", arguments, output.String())
		}
	}
}

func TestExitCodeMapping(t *testing.T) {
	tests := []struct {
		err  error
		want int
	}{
		{app.NewError(app.ClassCommand, "bad", "bad"), 2},
		{app.NewError(app.ClassValidation, "bad", "bad"), 3},
		{app.NewError(app.ClassAuthentication, "bad", "bad"), 4},
		{app.NewError(app.ClassConflict, "bad", "bad"), 5},
		{app.NewError(app.ClassConversion, "bad", "bad"), 6},
		{app.NewError(app.ClassGoogleAPI, "bad", "bad"), 7},
		{app.NewError(app.ClassPartial, "bad", "bad"), 8},
	}
	for _, test := range tests {
		if got := ExitCode(test.err); got != test.want {
			t.Fatalf("ExitCode(%v) = %d, want %d", test.err, got, test.want)
		}
	}
}

func TestJSONErrorIncludesStableClassCodeAndExit(t *testing.T) {
	err := app.NewError(app.ClassValidation, "invalid_documents", "documents are invalid")
	var output strings.Builder
	WriteError(&output, err, true)
	for _, value := range []string{`"class": "validation"`, `"code": "invalid_documents"`, `"exit_code": 3`} {
		if !strings.Contains(output.String(), value) {
			t.Fatalf("output = %q", output.String())
		}
	}
}

func TestJSONErrorIncludesStructuredRecovery(t *testing.T) {
	err := app.WithRecovery(
		app.NewError(app.ClassGoogleAPI, "write_failed", "write failed"),
		"publish_recovery_required",
		app.Recovery{
			Kind:        "publish",
			OperationID: "operation",
			ReviewSetID: "review-set",
			FileIDs:     map[string]string{"docs/a.md": "file-a"},
			JournalPath: "/private/state.journal",
			NextCommand: "mdoc publish --profile work",
		},
	)
	var output strings.Builder
	WriteError(&output, err, true)
	for _, value := range []string{`"class": "partial"`, `"code": "publish_recovery_required"`, `"exit_code": 8`, `"operation_id": "operation"`, `"review_set_id": "review-set"`, `"docs/a.md": "file-a"`, `"journal_path": "/private/state.journal"`, `"next_command": "mdoc publish --profile work"`} {
		if !strings.Contains(output.String(), value) {
			t.Fatalf("output = %q", output.String())
		}
	}
}
