package app

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/goliatone/mdoc/internal/config"
	"github.com/goliatone/mdoc/internal/document"
	"github.com/goliatone/mdoc/internal/googleapi"
	"github.com/goliatone/mdoc/internal/state"
)

type fakeClientProvider struct{}

func (fakeClientProvider) Client(context.Context) (*http.Client, error) {
	return http.DefaultClient, nil
}

func (fakeClientProvider) Account(context.Context) (string, error) {
	return "author@example.com", nil
}

type staticDocumentRunner struct{}

func (staticDocumentRunner) PandocJSON(context.Context, string) ([]byte, error) {
	return []byte(`{"blocks":[{"t":"Header","c":[1,["title",[],[]],[{"t":"Str","c":"Title"}]]}]}`), nil
}

type mappedDocumentRunner map[string][]byte

func (runner mappedDocumentRunner) PandocJSON(_ context.Context, path string) ([]byte, error) {
	return runner[path], nil
}

type fakeFactory struct {
	drive googleapi.Drive
	docs  googleapi.Docs
}

func (f fakeFactory) New(context.Context, *http.Client) (googleapi.Clients, error) {
	return googleapi.Clients{Drive: f.drive, Docs: f.docs}, nil
}

type fakeDrive struct{ pair googleapi.FolderPair }

func safeMappedFolder(id, name, workspace, profile, role string) googleapi.Folder {
	return googleapi.Folder{
		ID: id, Name: name, Role: role, MimeType: googleapi.FolderMimeType,
		OwnerEmails: []string{"author@example.com"}, CanAddChildren: true, CanEdit: true,
		AppProperties: map[string]string{"mdoc_workspace": workspace, "mdoc_profile": profile, "mdoc_role": role, "mdoc_kind": "folder"},
	}
}

func (f fakeDrive) EnsureFolders(context.Context, googleapi.FolderSpec) (googleapi.FolderPair, error) {
	return f.pair, nil
}
func (fakeDrive) FindFolders(context.Context, string, string, string) ([]googleapi.Folder, error) {
	return nil, nil
}
func (fakeDrive) CreateFolder(context.Context, string, string, string, string) (googleapi.Folder, error) {
	return googleapi.Folder{}, nil
}
func (fakeDrive) CreateDocument(context.Context, googleapi.CreateDocumentInput) (googleapi.File, error) {
	return googleapi.File{}, nil
}
func (fakeDrive) FindDocuments(context.Context, map[string]string) ([]googleapi.File, error) {
	return nil, nil
}
func (fakeDrive) GetFile(context.Context, string) (googleapi.File, error) {
	return googleapi.File{}, nil
}
func (fakeDrive) UpdateProperties(context.Context, string, map[string]string) (googleapi.File, error) {
	return googleapi.File{}, nil
}
func (fakeDrive) MoveFile(context.Context, string, string, string) (googleapi.File, error) {
	return googleapi.File{}, nil
}
func (fakeDrive) ListComments(context.Context, string) (googleapi.CommentStatus, error) {
	return googleapi.CommentStatus{}, nil
}
func (fakeDrive) ExportDocument(context.Context, string, string) ([]byte, error) {
	return []byte("# Export"), nil
}

type setupCountingDrive struct {
	fakeDrive
	ensureCalls int
}

func (f *setupCountingDrive) EnsureFolders(context.Context, googleapi.FolderSpec) (googleapi.FolderPair, error) {
	f.ensureCalls++
	return f.pair, nil
}

func TestSetupPersistsUniqueAppFolders(t *testing.T) {
	directory := t.TempDir()
	configPath := filepath.Join(directory, ".mdoc.yaml")
	statePath := "private/state.json"
	yaml := strings.Replace(config.DefaultYAML, `state_file: ""`, `state_file: "`+statePath+`"`, 1)
	if err := os.WriteFile(configPath, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	var output strings.Builder
	application := New(&output, fakeClientProvider{})
	application.GoogleFactory = fakeFactory{drive: fakeDrive{pair: googleapi.FolderPair{
		Staging: googleapi.Folder{ID: "staging-id", Name: "Staging"},
		Review:  googleapi.Folder{ID: "review-id", Name: "Review"},
	}}}
	if err := application.Setup(context.Background(), CommonOptions{Config: configPath, Profile: "work"}); err != nil {
		t.Fatal(err)
	}
	store := state.NewStore(filepath.Join(directory, statePath))
	loaded, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Folders.StagingID != "staging-id" || loaded.Folders.ReviewID != "review-id" || loaded.Account != "author@example.com" {
		t.Fatalf("state folders = %#v", loaded.Folders)
	}
	if !strings.Contains(output.String(), "review-id") {
		t.Fatalf("output = %q", output.String())
	}
}

func TestSetupAcceptsNewProjectWithNonWorkProfile(t *testing.T) {
	directory := t.TempDir()
	configPath := filepath.Join(directory, ".mdoc.yaml")
	if err := config.WriteNeutralProject(configPath, "testing", false); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	data = []byte(strings.Replace(string(data), "state_file: \"\"", "state_file: .private/state.json", 1))
	if err := os.WriteFile(configPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	application := New(io.Discard, fakeClientProvider{})
	application.GoogleFactory = fakeFactory{drive: fakeDrive{pair: googleapi.FolderPair{
		Staging: googleapi.Folder{ID: "staging-id", Name: "Documents Staging"},
		Review:  googleapi.Folder{ID: "review-id", Name: "Documents Review"},
	}}}
	if err := application.Setup(context.Background(), CommonOptions{Config: configPath, Profile: "testing"}); err != nil {
		t.Fatal(err)
	}
	loaded, err := state.NewStore(filepath.Join(directory, ".private", "state.json")).Load()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Profile != "testing" || loaded.WorkspaceID == "" || loaded.Account != "author@example.com" {
		t.Fatalf("new project state = %#v", loaded)
	}
}

func TestDirectStatusNeedsNoProjectStateOrGoogleClient(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("# Title\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var output strings.Builder
	application := New(&output)
	application.DocumentRunner = staticDocumentRunner{}
	if err := application.Status(context.Background(), CommonOptions{AllowDirect: true, Sources: []string{"README.md"}, JSON: true}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), `"kind": "local_status"`) || !strings.Contains(output.String(), `"result": "ok"`) {
		t.Fatalf("direct status output = %q", output.String())
	}
	if _, err := os.Stat(filepath.Join(root, ".mdoc.yaml")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("direct status created project config: %v", err)
	}
}

func TestDirectStatusAndValidationAllowMultipleSourcesWithoutEntry(t *testing.T) {
	root := t.TempDir()
	docs := filepath.Join(root, "docs")
	if err := os.MkdirAll(docs, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"one.md", "two.md"} {
		if err := os.WriteFile(filepath.Join(docs, name), []byte("# "+name+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(root)
	var output strings.Builder
	application := New(&output)
	application.DocumentRunner = staticDocumentRunner{}
	common := CommonOptions{AllowDirect: true, Sources: []string{"docs"}, JSON: true}
	if err := application.Status(context.Background(), common); err != nil {
		t.Fatal(err)
	}
	var status struct {
		Entry string `json:"entry"`
	}
	if err := json.Unmarshal([]byte(output.String()), &status); err != nil {
		t.Fatal(err)
	}
	if status.Entry != "" {
		t.Fatalf("status entry = %q", status.Entry)
	}

	output.Reset()
	if err := application.Validate(context.Background(), ValidateOptions{CommonOptions: common}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), `"entry": ""`) {
		t.Fatalf("validation output = %q", output.String())
	}
}

func TestValidationUsesConfiguredOutputAndExplicitOverride(t *testing.T) {
	root := t.TempDir()
	projectPath := filepath.Join(root, ".mdoc.yaml")
	if err := config.WriteNeutralProject(projectPath, "testing", false); err != nil {
		t.Fatal(err)
	}
	projectData, err := os.ReadFile(projectPath)
	if err != nil {
		t.Fatal(err)
	}
	projectData = []byte(strings.Replace(string(projectData), "format: human", "format: json", 1))
	if err := os.WriteFile(projectPath, projectData, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("# Title\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var output strings.Builder
	application := New(&output)
	application.DocumentRunner = staticDocumentRunner{}
	if err := application.Validate(context.Background(), ValidateOptions{CommonOptions: CommonOptions{Config: projectPath}}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), `"kind": "validation"`) {
		t.Fatalf("configured JSON output = %q", output.String())
	}

	output.Reset()
	if err := application.Validate(context.Background(), ValidateOptions{CommonOptions: CommonOptions{Config: projectPath, Overrides: map[string]any{"output": map[string]any{"format": "human"}}}}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "Validation passed.") || strings.Contains(output.String(), `"kind"`) {
		t.Fatalf("explicit human output = %q", output.String())
	}
}

func TestValidationResolvesVersionThreeBundleMembersLocally(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"a.md", "excluded.md"} {
		if err := os.WriteFile(filepath.Join(root, "docs", name), []byte("# "+name+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	projectPath := filepath.Join(root, ".mdoc.yaml")
	project := `version: 3
project_id: local-bundle
profiles:
  review:
    sources:
      include: [docs/a.md]
    entry: report
    publications:
      report:
        kind: bundle
        members:
          - source: docs/a.md
          - source: docs/excluded.md
          - source: docs/missing.md
`
	if err := os.WriteFile(projectPath, []byte(project), 0o600); err != nil {
		t.Fatal(err)
	}
	var output strings.Builder
	application := New(&output)
	application.DocumentRunner = staticDocumentRunner{}
	err := application.Validate(context.Background(), ValidateOptions{CommonOptions: CommonOptions{Config: projectPath, JSON: true}})
	if err == nil {
		t.Fatal("expected local bundle validation failure")
	}
	for _, code := range []string{"publication_source_excluded", "publication_source_missing"} {
		if !strings.Contains(output.String(), code) {
			t.Fatalf("missing %s in %s", code, output.String())
		}
	}
}

func TestVersionThreeValidationReportsReferenceLinkPolicies(t *testing.T) {
	for _, policy := range []string{"warning", "error"} {
		t.Run(policy, func(t *testing.T) {
			root := t.TempDir()
			definitionPath := filepath.Join(root, "definitions.md")
			referencePath := filepath.Join(root, "references.md")
			for _, path := range []string{definitionPath, referencePath} {
				if err := os.WriteFile(path, []byte("# Source\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			projectPath := filepath.Join(root, ".mdoc.yaml")
			project := `version: 3
project_id: reference-policy
output:
  format: json
profiles:
  review:
    sources:
      include: ["*.md"]
    entry: report
    publications:
      report:
        kind: bundle
        members:
          - source: definitions.md
          - source: references.md
        reference_links:
          - id: operations
            pattern: '\bOP[0-9]{2}\b'
            definition:
              source: definitions.md
              match: heading_prefix
            unresolved: ` + policy + `
`
			if err := os.WriteFile(projectPath, []byte(project), 0o600); err != nil {
				t.Fatal(err)
			}
			var output strings.Builder
			application := New(&output)
			application.DocumentRunner = mappedDocumentRunner{
				definitionPath: []byte(`{"pandoc-api-version":[1,23,1],"meta":{},"blocks":[{"t":"Header","c":[2,["op01",[],[]],[{"t":"Str","c":"OP01"},{"t":"Space"},{"t":"Str","c":"Definition"}]]}]}`),
				referencePath:  []byte(`{"pandoc-api-version":[1,23,1],"meta":{},"blocks":[{"t":"Para","c":[{"t":"Str","c":"OP99"}]}]}`),
			}
			err := application.Validate(context.Background(), ValidateOptions{CommonOptions: CommonOptions{Config: projectPath}})
			if policy == "error" && err == nil {
				t.Fatal("expected unresolved reference validation failure")
			}
			if policy == "warning" && err != nil {
				t.Fatal(err)
			}
			code := "bundle_reference_unresolved"
			if policy == "error" {
				code = "publication_render_inputs_invalid"
			}
			for _, expected := range []string{code, "references.md", "OP99"} {
				if !strings.Contains(output.String(), expected) {
					t.Fatalf("validation output missing %q: %s", expected, output.String())
				}
			}
		})
	}
}

func TestVersionThreeDomainFixturesValidate(t *testing.T) {
	for _, domain := range []string{"software-delivery", "policy-manual"} {
		t.Run(domain, func(t *testing.T) {
			configPath, err := filepath.Abs(filepath.Join("..", "..", "testdata", "publications", domain, ".mdoc.yaml"))
			if err != nil {
				t.Fatal(err)
			}
			application := New(io.Discard)
			if err := application.Validate(context.Background(), ValidateOptions{CommonOptions: CommonOptions{Config: configPath, Profile: "review"}}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestVersionThreeValidationAndStatusUseRedactedFieldSummaries(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("# Report\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "fields.yaml"), []byte("from_file: file-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	projectPath := filepath.Join(root, ".mdoc.yaml")
	project := `version: 3
project_id: local-fields
output:
  format: json
profiles:
  review:
    sources:
      include: [README.md]
    entry: report
    field_defaults:
      default_value: default-secret
    publications:
      report:
        kind: source
        source: README.md
        field_files: [fields.yaml]
        fields:
          publication_value: publication-secret
`
	if err := os.WriteFile(projectPath, []byte(project), 0o600); err != nil {
		t.Fatal(err)
	}
	application := New(io.Discard)
	application.DocumentRunner = staticDocumentRunner{}
	for _, run := range []struct {
		name string
		call func(*Application, *strings.Builder) error
	}{
		{name: "validate", call: func(application *Application, output *strings.Builder) error {
			application.Out = output
			return application.Validate(context.Background(), ValidateOptions{CommonOptions: CommonOptions{Config: projectPath, FieldAssignments: []string{`/command_value="command-secret"`}}})
		}},
		{name: "status", call: func(application *Application, output *strings.Builder) error {
			application.Out = output
			return application.Status(context.Background(), CommonOptions{Config: projectPath, FieldAssignments: []string{`/command_value="command-secret"`}})
		}},
	} {
		t.Run(run.name, func(t *testing.T) {
			var output strings.Builder
			if err := run.call(application, &output); err != nil {
				t.Fatal(err)
			}
			for _, secret := range []string{"default-secret", "file-secret", "publication-secret", "command-secret"} {
				if strings.Contains(output.String(), secret) {
					t.Fatalf("output leaked %q: %s", secret, output.String())
				}
			}
			if !strings.Contains(output.String(), `"field_summaries"`) || !strings.Contains(output.String(), `"assignment_count": 1`) {
				t.Fatalf("missing redacted field summary: %s", output.String())
			}
		})
	}
}

func TestVersionThreeTemplateValidationRejectsMissingAndMisplacedPageFields(t *testing.T) {
	for _, test := range []struct {
		name  string
		extra string
	}{
		{name: "missing field", extra: `        title: '{{ field "missing.value" }}'
        layout:
          footer:
            right: 'Page {{ page "number" }} of {{ page "count" }}'
`},
		{name: "page field in body", extra: `        layout:
          footer:
            right: 'Page {{ page "number" }} of {{ page "count" }}'
          cover:
            enabled: true
            title: '{{ page "number" }}'
`},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("# Report\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			project := `version: 3
project_id: template-validation
output:
  format: json
profiles:
  review:
    sources:
      include: [README.md]
    entry: report
    publications:
      report:
        kind: source
        source: README.md
` + test.extra
			projectPath := filepath.Join(root, ".mdoc.yaml")
			if err := os.WriteFile(projectPath, []byte(project), 0o600); err != nil {
				t.Fatal(err)
			}
			var output strings.Builder
			application := New(&output)
			application.DocumentRunner = staticDocumentRunner{}
			err := application.Validate(context.Background(), ValidateOptions{CommonOptions: CommonOptions{Config: projectPath}})
			if err == nil || !strings.Contains(output.String(), "publication_template_invalid") {
				t.Fatalf("error=%v output=%s", err, output.String())
			}
		})
	}
}

func TestSetupBlocksActivePublishJournalBeforeRemoteWrites(t *testing.T) {
	directory, configPath, store := setupFixture(t)
	_ = directory
	if err := store.Acquire(); err != nil {
		t.Fatal(err)
	}
	journal := &state.Journal{
		WorkspaceID: "workspace", Profile: "work", OperationID: "operation", ReviewSetID: "set", StagingID: "staging", ReviewID: "review",
		Sources: []string{"docs/a.md"}, Entries: map[string]state.JournalEntry{"docs/a.md": {SourceKey: "docs/a.md", Generation: 1, SourceHash: "source", RenderHash: "render", DOCXHash: "docx", Stage: state.StageCreating}},
	}
	if err := store.WriteJournal(journal); err != nil {
		t.Fatal(err)
	}
	store.Release()
	drive := &setupCountingDrive{}
	application := New(io.Discard, fakeClientProvider{})
	application.GoogleFactory = fakeFactory{drive: drive, docs: newBatchDocs()}
	err := application.Setup(context.Background(), CommonOptions{Config: configPath, Profile: "work"})
	if err == nil || !strings.Contains(err.Error(), "publish") || drive.ensureCalls != 0 {
		t.Fatalf("error=%v ensure calls=%d", err, drive.ensureCalls)
	}
}

func TestSetupBlocksMismatchedStateIdentityBeforeRemoteWrites(t *testing.T) {
	_, configPath, store := setupFixture(t)
	if err := store.Acquire(); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(state.New("other-workspace", "work")); err != nil {
		t.Fatal(err)
	}
	store.Release()
	drive := &setupCountingDrive{}
	application := New(io.Discard, fakeClientProvider{})
	application.GoogleFactory = fakeFactory{drive: drive, docs: newBatchDocs()}
	err := application.Setup(context.Background(), CommonOptions{Config: configPath, Profile: "work"})
	if err == nil || !strings.Contains(err.Error(), "state belongs") || drive.ensureCalls != 0 {
		t.Fatalf("error=%v ensure calls=%d", err, drive.ensureCalls)
	}
}

func setupFixture(t *testing.T) (string, string, *state.Store) {
	t.Helper()
	directory := t.TempDir()
	configPath := filepath.Join(directory, ".mdoc.yaml")
	yaml := "version: 1\nworkspace_id: workspace\nprofiles:\n  work:\n    sources: [docs/*.md]\n    entry: docs/a.md\n    review_folder_name: Review\n    staging_folder_name: Staging\n    reference_docx: reference.docx\n    state_file: private/state.json\n"
	if err := os.WriteFile(configPath, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	return directory, configPath, state.NewStore(filepath.Join(directory, "private", "state.json"))
}

func TestVerifyPublishDestinationBlocksStaleFolderMappingAndShowsAccount(t *testing.T) {
	docs := newBatchDocs()
	drive := newBatchDrive(docs)
	drive.folders["staging"] = []googleapi.Folder{safeMappedFolder("remote-staging", "Staging", "workspace", "work", "staging")}
	drive.folders["review"] = []googleapi.Folder{safeMappedFolder("review", "Review", "workspace", "work", "review")}
	var output strings.Builder
	application := New(&output, fakeClientProvider{})
	publishState := state.New("workspace", "work")
	publishState.Folders = state.Folders{StagingID: "stale-staging", ReviewID: "review"}
	_, err := application.verifyPublishDestination(context.Background(), &config.Config{WorkspaceID: "workspace"}, config.SelectedProfile{Name: "work"}, publishState, googleapi.Clients{Drive: drive, Docs: docs})
	if err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("error = %v", err)
	}
	publishState.Folders.StagingID = "remote-staging"
	destination, err := application.verifyPublishDestination(context.Background(), &config.Config{WorkspaceID: "workspace"}, config.SelectedProfile{Name: "work"}, publishState, googleapi.Clients{Drive: drive, Docs: docs})
	if err != nil {
		t.Fatal(err)
	}
	application.showPublishDestination(destination)
	if !strings.Contains(output.String(), "author@example.com") || !strings.Contains(output.String(), "remote-staging") || !strings.Contains(output.String(), "review") {
		t.Fatalf("output = %q", output.String())
	}
}

type alternateAccountProvider struct{ account string }

func (alternateAccountProvider) Client(context.Context) (*http.Client, error) {
	return http.DefaultClient, nil
}
func (p alternateAccountProvider) Account(context.Context) (string, error) { return p.account, nil }

func TestVerifyPublishDestinationBlocksDifferentStoredAccount(t *testing.T) {
	provider := alternateAccountProvider{account: "other@example.com"}
	application := New(io.Discard, provider)
	publishState := state.New("workspace", "work")
	publishState.Account = "author@example.com"
	_, err := application.verifyPublishDestination(context.Background(), &config.Config{WorkspaceID: "workspace"}, config.SelectedProfile{Name: "work"}, publishState, googleapi.Clients{})
	if err == nil || !strings.Contains(err.Error(), "belongs to Google account author@example.com") {
		t.Fatalf("error = %v", err)
	}
}

func TestMappedDestinationUsesSetupSafetyPolicy(t *testing.T) {
	cfg := &config.Config{WorkspaceID: "workspace"}
	profile := config.SelectedProfile{Name: "work"}
	tests := []struct {
		name   string
		change func(*googleapi.Folder)
	}{
		{name: "shared drive", change: func(folder *googleapi.Folder) { folder.DriveID = "shared" }},
		{name: "trashed", change: func(folder *googleapi.Folder) { folder.Trashed = true }},
		{name: "wrong type", change: func(folder *googleapi.Folder) { folder.MimeType = googleapi.DocumentMimeType }},
		{name: "cannot add children", change: func(folder *googleapi.Folder) { folder.CanAddChildren = false }},
		{name: "cannot edit", change: func(folder *googleapi.Folder) { folder.CanEdit = false }},
		{name: "wrong owner", change: func(folder *googleapi.Folder) { folder.OwnerEmails = []string{"other@example.com"} }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			folder := safeMappedFolder("staging", "Staging", "workspace", "work", "staging")
			test.change(&folder)
			if err := validateMappedDestinationFolder(folder, "author@example.com", cfg, profile, "staging", "staging"); err == nil {
				t.Fatal("expected unsafe destination error")
			}
		})
	}
}

func TestValidateRejectsCorruptReferenceDOCX(t *testing.T) {
	directory := t.TempDir()
	if err := os.Mkdir(filepath.Join(directory, "docs"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "docs", "a.md"), []byte("# A\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "reference.docx"), []byte("not a DOCX"), 0o600); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(directory, ".mdoc.yaml")
	yaml := "version: 1\nworkspace_id: workspace\nprofiles:\n  work:\n    sources: [docs/*.md]\n    entry: docs/a.md\n    review_folder_name: Review\n    staging_folder_name: Staging\n    reference_docx: reference.docx\n    state_file: private/state.json\n"
	if err := os.WriteFile(configPath, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	var output strings.Builder
	application := New(&output)
	err := application.Validate(context.Background(), ValidateOptions{CommonOptions: CommonOptions{Config: configPath, Profile: "work"}})
	if err == nil || !strings.Contains(output.String(), "style_invalid") {
		t.Fatalf("error=%v output=%q", err, output.String())
	}
}

func TestRemoteValidationInspectsMappingsTargetsAndReviewState(t *testing.T) {
	directory := t.TempDir()
	store := state.NewStore(filepath.Join(directory, "state.json"))
	if err := store.Acquire(); err != nil {
		t.Fatal(err)
	}
	publishState := state.New("workspace", "work")
	publishState.Folders = state.Folders{StagingID: "staging", ReviewID: "review"}
	target := state.Target{FileID: "doc", URL: "url", ReviewSetID: "set", Generation: 1, OperationID: "operation", PublishStatus: "ready", RemoteVersion: "1", DocsRevision: "r1"}
	publishState.Documents["docs/a.md"] = state.Document{ActiveTarget: &target}
	if err := store.Save(publishState); err != nil {
		t.Fatal(err)
	}
	store.Release()
	docs := newBatchDocs()
	docs.documents["doc"] = googleapi.Document{ID: "doc", RevisionID: "r2", SuggestionsDetected: true, Links: map[string][]googleapi.TextRange{}}
	drive := newBatchDrive(docs)
	drive.folders["staging"] = []googleapi.Folder{safeMappedFolder("staging", "Staging", "workspace", "work", "staging")}
	drive.folders["review"] = []googleapi.Folder{safeMappedFolder("review", "Review", "workspace", "work", "review")}
	drive.files["doc"] = googleapi.File{ID: "doc", Name: "Renamed", URL: "url", MimeType: googleapi.DocumentMimeType, Version: "2", ModifiedTime: time.Now(), Parents: []string{"review"}, AppProperties: map[string]string{"mdoc_workspace": "workspace", "mdoc_profile": "work", "mdoc_source": "docs/a.md", "mdoc_review_set": "set", "mdoc_generation": "1", "mdoc_operation": "operation", "mdoc_expected_set_size": "1", "mdoc_status": "ready"}}
	drive.comments["doc"] = googleapi.CommentStatus{Unresolved: 2, Pages: 2}
	application := New(&strings.Builder{}, fakeClientProvider{})
	application.GoogleFactory = fakeFactory{drive: drive, docs: docs}
	graph := &document.Graph{ByKey: map[string]*document.Document{"docs/a.md": {SourceKey: "docs/a.md", Title: "A"}}}
	issues, err := application.validateRemote(context.Background(), &config.Config{WorkspaceID: "workspace"}, config.SelectedProfile{Name: "work"}, store, graph)
	if err != nil {
		t.Fatal(err)
	}
	for _, code := range []string{"remote_change", "remote_rename", "unresolved_comments", "unresolved_suggestions", "docs_revision_change"} {
		found := false
		for _, issue := range issues {
			if issue.Code == code {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("missing %s in %#v", code, issues)
		}
	}
	drive.files["doc"].AppProperties["mdoc_operation"] = "different-operation"
	issues, err = application.validateRemote(context.Background(), &config.Config{WorkspaceID: "workspace"}, config.SelectedProfile{Name: "work"}, store, graph)
	if err != nil {
		t.Fatal(err)
	}
	metadataMismatch := false
	for _, issue := range issues {
		if issue.Code == "target_metadata_mismatch" {
			metadataMismatch = true
			break
		}
	}
	if !metadataMismatch {
		t.Fatalf("missing target metadata mismatch in %#v", issues)
	}
	drive.files["doc"].AppProperties["mdoc_operation"] = "operation"
	drive.files["doc"].AppProperties["mdoc_expected_set_size"] = "2"
	issues, err = application.validateRemote(context.Background(), &config.Config{WorkspaceID: "workspace"}, config.SelectedProfile{Name: "work"}, store, graph)
	if err != nil {
		t.Fatal(err)
	}
	if !hasIssue(issues, "review_set_incomplete") {
		t.Fatalf("missing review set integrity issue in %#v", issues)
	}
}

func TestRemoteValidationPreservesLookupErrorClasses(t *testing.T) {
	directory := t.TempDir()
	store := state.NewStore(filepath.Join(directory, "state.json"))
	if err := store.Acquire(); err != nil {
		t.Fatal(err)
	}
	publishState := state.New("workspace", "work")
	publishState.Folders = state.Folders{StagingID: "staging", ReviewID: "review"}
	target := state.Target{FileID: "doc", URL: "url", ReviewSetID: "set", Generation: 1, OperationID: "operation", PublishStatus: "ready"}
	publishState.Documents["docs/a.md"] = state.Document{ActiveTarget: &target}
	if err := store.Save(publishState); err != nil {
		t.Fatal(err)
	}
	store.Release()
	docs := newBatchDocs()
	drive := newBatchDrive(docs)
	drive.folders["staging"] = []googleapi.Folder{safeMappedFolder("staging", "Staging", "workspace", "work", "staging")}
	drive.folders["review"] = []googleapi.Folder{safeMappedFolder("review", "Review", "workspace", "work", "review")}
	drive.files["doc"] = googleapi.File{ID: "doc", URL: "url", MimeType: googleapi.DocumentMimeType, Parents: []string{"review"}, AppProperties: map[string]string{
		"mdoc_workspace": "workspace", "mdoc_profile": "work", "mdoc_source": "docs/a.md", "mdoc_review_set": "set",
		"mdoc_generation": "1", "mdoc_operation": "operation", "mdoc_expected_set_size": "1", "mdoc_status": "ready",
	}}
	application := New(&strings.Builder{}, fakeClientProvider{})
	application.GoogleFactory = fakeFactory{drive: drive, docs: docs}
	graph := &document.Graph{ByKey: map[string]*document.Document{"docs/a.md": {SourceKey: "docs/a.md", Title: "A"}}}
	drive.getErrors["doc"] = &googleapi.Error{Kind: googleapi.KindNotFound, Operation: "get", Cause: errors.New("missing")}
	issues, err := application.validateRemote(context.Background(), &config.Config{WorkspaceID: "workspace"}, config.SelectedProfile{Name: "work"}, store, graph)
	if err != nil {
		t.Fatal(err)
	}
	if !hasIssue(issues, "target_missing") {
		t.Fatalf("issues = %#v", issues)
	}
	drive.getErrors["doc"] = &googleapi.Error{Kind: googleapi.KindPermission, Operation: "get", Cause: errors.New("denied")}
	_, err = application.validateRemote(context.Background(), &config.Config{WorkspaceID: "workspace"}, config.SelectedProfile{Name: "work"}, store, graph)
	var typed *Error
	if !errors.As(err, &typed) || typed.Class != ClassAuthentication {
		t.Fatalf("error = %#v", err)
	}
}

func hasIssue(issues []document.Issue, code string) bool {
	for _, issue := range issues {
		if issue.Code == code {
			return true
		}
	}
	return false
}
