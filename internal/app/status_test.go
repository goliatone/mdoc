package app

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/goliatone/mdoc/internal/config"
	"github.com/goliatone/mdoc/internal/document"
	"github.com/goliatone/mdoc/internal/googleapi"
	"github.com/goliatone/mdoc/internal/report"
	"github.com/goliatone/mdoc/internal/reviewsync"
	"github.com/goliatone/mdoc/internal/state"
)

type panicClientProvider struct{}

func (panicClientProvider) Client(context.Context) (*http.Client, error) {
	panic("local status created an auth client")
}

type invalidDocumentRunner struct{}

func (invalidDocumentRunner) PandocJSON(context.Context, string) ([]byte, error) {
	return []byte(`{"blocks":[{"t":"Header","c":[2,["section",[],[]],[{"t":"Str","c":"Section"}]]}]}`), nil
}

func TestLocalStatusMissingPandocIsSuccessfulPartial(t *testing.T) {
	root := t.TempDir()
	projectPath := filepath.Join(root, ".mdoc.yaml")
	if err := config.WriteNeutralProject(projectPath, "testing", false); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("# Title\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var output strings.Builder
	application := New(&output, panicClientProvider{})
	overrides := map[string]any{"pandoc_binary": "mdoc-pandoc-that-does-not-exist", "output": map[string]any{"format": "json"}}
	if err := application.Status(context.Background(), CommonOptions{Config: projectPath, Overrides: overrides}); err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{`"schema_version": 1`, `"kind": "local_status"`, `"result": "partial"`, `"state": "unavailable"`, `Pandoc is required for Markdown validation`} {
		if !strings.Contains(output.String(), expected) {
			t.Fatalf("partial status output = %q", output.String())
		}
	}
}

func TestProjectStatusAllowsDirectoryWithSeveralSourcesAndNoEntry(t *testing.T) {
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
	projectPath := filepath.Join(root, ".mdoc.yaml")
	project := `version: 2
project_id: optional-entry
output:
  format: json
profiles:
  testing:
    sources:
      include: [docs]
    destination:
      staging_folder_name: Staging
      review_folder_name: Review
`
	if err := os.WriteFile(projectPath, []byte(project), 0o600); err != nil {
		t.Fatal(err)
	}
	var output strings.Builder
	application := New(&output, panicClientProvider{})
	application.DocumentRunner = staticDocumentRunner{}
	if err := application.Status(context.Background(), CommonOptions{Config: projectPath, Profile: "testing"}); err != nil {
		t.Fatal(err)
	}
	var status report.LocalStatusReport
	if err := json.Unmarshal([]byte(output.String()), &status); err != nil {
		t.Fatal(err)
	}
	if status.Result != "partial" || status.Entry != "" || len(status.Files) != 2 || status.Checks["validation"].State != report.CheckPassed {
		t.Fatalf("status = %#v", status)
	}
}

func TestRemoteStatusContinuesRemoteChecksWhenPandocIsUnavailable(t *testing.T) {
	for _, test := range []struct {
		name   string
		binary func(string) string
		text   string
	}{
		{name: "missing", binary: func(root string) string { return filepath.Join(root, "missing-pandoc") }, text: "run Pandoc"},
		{name: "unsupported", binary: func(root string) string {
			path := filepath.Join(root, "pandoc-old")
			if err := os.WriteFile(path, []byte("#!/bin/sh\nprintf 'pandoc 2.19.2\\n'\n"), 0o700); err != nil {
				t.Fatal(err)
			}
			return path
		}, text: "unsupported Pandoc version"},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			projectPath := filepath.Join(root, ".mdoc.yaml")
			if err := config.WriteNeutralProject(projectPath, "testing", false); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("# Title\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			statePath := filepath.Join(root, "private", "state.json")
			overrides := map[string]any{
				"pandoc_binary": test.binary(root),
				"output":        map[string]any{"format": "json"},
				"profile":       map[string]any{"state_file": statePath},
			}
			resolved, err := config.Resolve(context.Background(), config.ResolverOptions{ProjectPath: projectPath, Profile: "testing", Overrides: overrides})
			if err != nil {
				t.Fatal(err)
			}
			store := state.NewStore(statePath)
			if err := store.Acquire(); err != nil {
				t.Fatal(err)
			}
			publishState := state.New(resolved.Identity(), "testing")
			publishState.Account = "author@example.com"
			publishState.Folders = state.Folders{StagingID: "staging", ReviewID: "review"}
			if err := store.Save(publishState); err != nil {
				t.Fatal(err)
			}
			store.Release()

			docs := newBatchDocs()
			drive := newBatchDrive(docs)
			drive.folders["staging"] = []googleapi.Folder{safeMappedFolder("staging", "Staging", resolved.Identity(), "testing", "staging")}
			drive.folders["review"] = []googleapi.Folder{safeMappedFolder("review", "Review", resolved.Identity(), "testing", "review")}
			var output strings.Builder
			application := New(&output, fakeClientProvider{})
			application.GoogleFactory = fakeFactory{drive: drive, docs: docs}
			if err := application.Status(context.Background(), CommonOptions{Config: projectPath, Profile: "testing", Remote: true, Overrides: overrides}); err != nil {
				t.Fatal(err)
			}
			var remote report.RemoteStatusReport
			if err := json.Unmarshal([]byte(output.String()), &remote); err != nil {
				t.Fatal(err)
			}
			if remote.Result != "partial" || remote.Checks["pandoc"].State != report.CheckUnavailable || remote.Checks["validation"].State != report.CheckUnavailable || remote.Checks["remote"].State != report.CheckPassed {
				t.Fatalf("remote status = %#v", remote)
			}
			if !strings.Contains(remote.Checks["pandoc"].Message, test.text) {
				t.Fatalf("pandoc check = %#v", remote.Checks["pandoc"])
			}
		})
	}
}

func TestLocalStatusValidationFailureIsInvalid(t *testing.T) {
	root := t.TempDir()
	projectPath := filepath.Join(root, ".mdoc.yaml")
	if err := config.WriteNeutralProject(projectPath, "testing", false); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("## Section\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var output strings.Builder
	application := New(&output, panicClientProvider{})
	application.DocumentRunner = invalidDocumentRunner{}
	overrides := map[string]any{
		"output":  map[string]any{"format": "json"},
		"profile": map[string]any{"validation": map[string]any{"title": "required"}},
	}
	err := application.Status(context.Background(), CommonOptions{Config: projectPath, Overrides: overrides})
	if err == nil || ExitCode(err) != 3 {
		t.Fatalf("invalid status error = %v exit=%d", err, ExitCode(err))
	}
	if !strings.Contains(output.String(), `"result": "invalid"`) || !strings.Contains(output.String(), `"code": "title_count"`) {
		t.Fatalf("invalid status output = %q", output.String())
	}
}

func TestStatusReportsReferenceBuildPolicies(t *testing.T) {
	for _, test := range []struct {
		name   string
		policy string
		remote bool
		code   string
		result string
	}{
		{name: "local error", policy: "error", code: "publication_render_inputs_invalid", result: "invalid"},
		{name: "remote error", policy: "error", remote: true, code: "publication_render_inputs_invalid", result: "invalid"},
		{name: "local warning", policy: "warning", code: "bundle_reference_unresolved", result: "partial"},
	} {
		t.Run(test.name, func(t *testing.T) {
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
project_id: reference-status
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
            unresolved: ` + test.policy + `
`
			if err := os.WriteFile(projectPath, []byte(project), 0o600); err != nil {
				t.Fatal(err)
			}
			var output strings.Builder
			application := New(&output, panicClientProvider{})
			application.DocumentRunner = mappedDocumentRunner{
				definitionPath: []byte(`{"pandoc-api-version":[1,23,1],"meta":{},"blocks":[{"t":"Header","c":[2,["op01",[],[]],[{"t":"Str","c":"OP01"},{"t":"Space"},{"t":"Str","c":"Definition"}]]}]}`),
				referencePath:  []byte(`{"pandoc-api-version":[1,23,1],"meta":{},"blocks":[{"t":"Para","c":[{"t":"Str","c":"OP99"}]}]}`),
			}
			options := CommonOptions{Config: projectPath, Profile: "review", Remote: test.remote}
			err := application.Status(context.Background(), options)
			if test.policy == "error" && (err == nil || ExitCode(err) != 3) {
				t.Fatalf("status error = %v exit=%d", err, ExitCode(err))
			}
			if test.policy == "warning" && err != nil {
				t.Fatal(err)
			}
			for _, expected := range []string{`"result": "` + test.result + `"`, `"code": "` + test.code + `"`, "references.md", "OP99"} {
				if !strings.Contains(output.String(), expected) {
					t.Fatalf("status output missing %q: %s", expected, output.String())
				}
			}
		})
	}
}

func TestLocalStatusConfigFailureWritesVersionedInvalidReport(t *testing.T) {
	root := t.TempDir()
	projectPath := filepath.Join(root, ".mdoc.yaml")
	if err := os.WriteFile(projectPath, []byte("version: [invalid\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var output strings.Builder
	application := New(&output, panicClientProvider{})
	err := application.Status(context.Background(), CommonOptions{Config: projectPath, JSON: true})
	if err == nil || ExitCode(err) != 3 {
		t.Fatalf("invalid config status error = %v exit=%d", err, ExitCode(err))
	}
	for _, expected := range []string{`"schema_version": 1`, `"kind": "local_status"`, `"result": "invalid"`, `"config"`} {
		if !strings.Contains(output.String(), expected) {
			t.Fatalf("invalid config status output = %q", output.String())
		}
	}
}

func TestVersionThreeStatusReportsEveryReviewReadinessState(t *testing.T) {
	tests := []struct {
		name         string
		remote       bool
		prepare      func(*testing.T, reviewPullFixture)
		wantState    string
		wantEnabled  bool
		wantBaseline bool
	}{
		{name: "baseline available", wantState: reviewStateBaselineAvailable, wantEnabled: true, wantBaseline: true},
		{name: "pull ready", remote: true, wantState: reviewStatePullReady, wantEnabled: true, wantBaseline: true},
		{name: "changed", remote: true, prepare: func(_ *testing.T, fixture reviewPullFixture) {
			file := fixture.drive.files[fixture.fileID]
			file.Version = increment(file.Version)
			fixture.drive.files[fixture.fileID] = file
			doc := fixture.docs.documents[fixture.fileID]
			doc.RevisionID = "r2"
			fixture.docs.documents[fixture.fileID] = doc
		}, wantState: reviewStateChanged, wantEnabled: true, wantBaseline: true},
		{name: "blocked remote", remote: true, prepare: func(_ *testing.T, fixture reviewPullFixture) {
			doc := fixture.docs.documents[fixture.fileID]
			doc.SuggestionsDetected = true
			fixture.docs.documents[fixture.fileID] = doc
		}, wantState: reviewStateBlocked, wantEnabled: true, wantBaseline: true},
		{name: "unavailable", prepare: func(t *testing.T, fixture reviewPullFixture) {
			updateReviewFixtureState(t, fixture, func(target *state.Target) { target.ReviewSnapshot = nil })
		}, wantState: reviewStateUnavailable, wantEnabled: true},
		{name: "blocked local", prepare: func(t *testing.T, fixture reviewPullFixture) {
			path := findTreeFile(t, fixture.snapshotRoot, "baseline.md")
			data := []byte(readTestFile(t, path))
			data[len(data)-2] ^= 1
			if err := os.WriteFile(path, data, 0o600); err != nil {
				t.Fatal(err)
			}
		}, wantState: reviewStateBlocked, wantEnabled: true},
		{name: "disabled", prepare: func(t *testing.T, fixture reviewPullFixture) {
			data := strings.Replace(readTestFile(t, fixture.configPath), "enabled: true", "enabled: false", 1)
			if err := os.WriteFile(fixture.configPath, []byte(data), 0o600); err != nil {
				t.Fatal(err)
			}
		}, wantState: reviewStateDisabled},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newReviewPullFixture(t)
			if test.prepare != nil {
				test.prepare(t, fixture)
			}
			fixture.output.Reset()
			if err := fixture.application.Status(context.Background(), CommonOptions{Config: fixture.configPath, Profile: "work", JSON: true, Remote: test.remote}); err != nil {
				t.Fatal(err)
			}
			var targets []report.TargetSummary
			if test.remote {
				var value report.RemoteStatusReport
				if err := json.Unmarshal([]byte(fixture.output.String()), &value); err != nil {
					t.Fatal(err)
				}
				targets = value.Targets
			} else {
				var value report.LocalStatusReport
				if err := json.Unmarshal([]byte(fixture.output.String()), &value); err != nil {
					t.Fatal(err)
				}
				targets = value.Targets
			}
			if len(targets) != 1 || targets[0].Review == nil || targets[0].Review.State != test.wantState || targets[0].Review.PullEnabled != test.wantEnabled || targets[0].Review.BaselineAvailable != test.wantBaseline {
				t.Fatalf("review targets = %#v", targets)
			}
		})
	}
}

func TestLocalStatusReportsAndRetainsOrphanReviewSnapshots(t *testing.T) {
	fixture := newReviewPullFixture(t)
	store, err := reviewsync.NewSnapshotStore(fixture.statePath)
	if err != nil {
		t.Fatal(err)
	}
	activeState, err := state.NewStore(fixture.statePath).Load()
	if err != nil {
		t.Fatal(err)
	}
	active := activeState.Targets["publication:report"].ActiveTarget
	loaded, err := store.Load("publication:report", active.Generation, true)
	if err != nil {
		t.Fatal(err)
	}
	input := reviewsync.CandidateInput{
		WorkspaceID: "workspace", Profile: "work", TargetKey: "publication:orphan", PublicationID: "orphan", PublicationKind: "source", Reader: loaded.Manifest.Reader,
		FileID: "orphan-file", Generation: 7, ReviewSetID: "orphan-set", OperationID: "orphan-operation",
		Capture:  reviewsync.CaptureTuple{FileID: "orphan-file", DriveVersion: "1", DocsRevision: "orphan-r1", Tabs: loaded.Manifest.Capture.Tabs},
		Baseline: loaded.Baseline, Sources: []reviewsync.SnapshotSource{{SourceKey: "report.md", Content: loaded.Sources["report.md"]}}, SourceMap: loaded.Manifest.SourceMap, CapturedAt: time.Unix(100, 0).UTC(),
	}
	if _, err := store.WriteCandidate(input); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Seal(input.TargetKey, input.Generation, reviewsync.ActivationSeal{FileID: input.FileID, DriveVersion: "2", DocsRevision: input.Capture.DocsRevision, ReviewParent: "review", ReadyMetadata: map[string]string{"mdoc_status": "ready"}, Tabs: input.Capture.Tabs, ActivatedAt: time.Unix(101, 0).UTC()}); err != nil {
		t.Fatal(err)
	}
	fixture.output.Reset()
	if err := fixture.application.Status(context.Background(), CommonOptions{Config: fixture.configPath, Profile: "work", JSON: true}); err != nil {
		t.Fatal(err)
	}
	var value report.LocalStatusReport
	if err := json.Unmarshal([]byte(fixture.output.String()), &value); err != nil {
		t.Fatal(err)
	}
	if len(value.ReviewOrphans) != 1 || value.ReviewOrphans[0].Target != input.TargetKey || !strings.Contains(value.Checks["review_pull"].Message, "retained") {
		t.Fatalf("status = %#v", value)
	}
	if _, err := store.Load(input.TargetKey, input.Generation, true); err != nil {
		t.Fatalf("status deleted orphan snapshot: %v", err)
	}
}

func TestVersionThreeReviewReadinessSurvivesMissingPandoc(t *testing.T) {
	fixture := newReviewPullFixture(t)
	fixture.application.DocumentRunner = nil
	fixture.output.Reset()
	overrides := map[string]any{"pandoc_binary": filepath.Join(t.TempDir(), "missing-pandoc")}
	if err := fixture.application.Status(context.Background(), CommonOptions{Config: fixture.configPath, Profile: "work", JSON: true, Overrides: overrides}); err != nil {
		t.Fatal(err)
	}
	var value report.LocalStatusReport
	if err := json.Unmarshal([]byte(fixture.output.String()), &value); err != nil {
		t.Fatal(err)
	}
	if value.Result != "partial" || len(value.Targets) != 1 || value.Targets[0].Review == nil || value.Targets[0].Review.State != reviewStateBaselineAvailable {
		t.Fatalf("status = %#v", value)
	}
}

func TestRemoteReviewReadinessScopesTargetFailures(t *testing.T) {
	targets := []report.TargetSummary{
		{Target: "publication:a", Review: &report.ReviewTargetStatus{PullEnabled: true, BaselineAvailable: true, State: reviewStateBaselineAvailable}},
		{Target: "publication:b", Review: &report.ReviewTargetStatus{PullEnabled: true, BaselineAvailable: true, State: reviewStateBaselineAvailable}},
	}
	issues := []document.Issue{{Severity: document.SeverityError, Code: "review_set_incomplete", Source: "publication:b"}}
	got := remoteReviewDiagnostics(targets, issues)
	if got[0].Review.State != reviewStatePullReady || got[1].Review.State != reviewStateBlocked {
		t.Fatalf("scoped readiness = %#v", got)
	}
	got = remoteReviewDiagnostics(targets, []document.Issue{{Severity: document.SeverityError, Code: "folder_collision"}})
	if got[0].Review.State != reviewStateBlocked || got[1].Review.State != reviewStateBlocked {
		t.Fatalf("global readiness = %#v", got)
	}
}

func updateReviewFixtureState(t *testing.T, fixture reviewPullFixture, update func(*state.Target)) {
	t.Helper()
	store := state.NewStore(fixture.statePath)
	if err := store.Acquire(); err != nil {
		t.Fatal(err)
	}
	defer store.Release()
	value, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	stored := value.Targets["publication:report"]
	update(stored.ActiveTarget)
	value.Targets["publication:report"] = stored
	if err := store.Save(value); err != nil {
		t.Fatal(err)
	}
}

func TestLocalAndRemoteStatusReportRenderInputChangesWithoutExecutingFilters(t *testing.T) {
	root := t.TempDir()
	projectPath := filepath.Join(root, ".mdoc.yaml")
	if err := config.WriteNeutralProject(projectPath, "testing", false); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("# Title\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	filterPath := filepath.Join(root, "status-filter.lua")
	if err := os.WriteFile(filterPath, []byte("function Pandoc(doc) return doc end\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(root, "private", "state.json")
	overrides := map[string]any{
		"output": map[string]any{"format": "json"},
		"profile": map[string]any{
			"state_file": statePath,
			"style":      map[string]any{"filters": []string{filterPath}},
		},
	}
	options := CommonOptions{Config: projectPath, Profile: "testing", Overrides: overrides}
	var output strings.Builder
	application := New(&output, fakeClientProvider{})
	application.DocumentRunner = staticDocumentRunner{}
	cfg, profile, store, err := application.load(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	graph, err := document.Discover(context.Background(), cfg.Root, profile, application.documentRunner(cfg))
	if err != nil {
		t.Fatal(err)
	}
	publishState := state.New(cfg.WorkspaceID, profile.Name)
	publishState.Folders = state.Folders{StagingID: "staging", ReviewID: "review"}
	profileBytes, _ := json.Marshal(newProfileFingerprint(cfg, profile))
	doc := graph.ByKey["README.md"]
	request := documentRenderRequest(cfg, profile, graph, doc, profileBytes, map[string]string{})
	renderHash, _, err := application.renderer(cfg).Fingerprint(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	publishState.Documents["README.md"] = state.Document{SourceHash: doc.SourceHash, RenderHash: renderHash}
	if err := store.Acquire(); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(publishState); err != nil {
		t.Fatal(err)
	}
	store.Release()

	if err := application.Status(context.Background(), options); err != nil {
		t.Fatal(err)
	}
	var local report.LocalStatusReport
	if err := json.Unmarshal([]byte(output.String()), &local); err != nil {
		t.Fatal(err)
	}
	if len(local.Changes) != 1 || local.Changes[0].State != "unchanged" {
		t.Fatalf("initial local changes = %#v", local.Changes)
	}

	markerPath := filepath.Join(root, "status-filter-executed")
	filterSource := "local marker = assert(io.open(" + strconv.Quote(markerPath) + ", \"w\"))\nmarker:write(\"executed\")\nmarker:close()\n"
	if err := os.WriteFile(filterPath, []byte(filterSource), 0o600); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	if err := application.Status(context.Background(), options); err != nil {
		t.Fatal(err)
	}
	local = report.LocalStatusReport{}
	if err := json.Unmarshal([]byte(output.String()), &local); err != nil {
		t.Fatal(err)
	}
	if len(local.Changes) != 1 || local.Changes[0].State != "modified" {
		t.Fatalf("render-aware local changes = %#v", local.Changes)
	}
	if _, err := os.Stat(markerPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("local status executed filter: %v", err)
	}

	folderProperties := func(role string) map[string]string {
		return map[string]string{"mdoc_workspace": cfg.WorkspaceID, "mdoc_profile": profile.Name, "mdoc_role": role, "mdoc_kind": "folder"}
	}
	docs := newBatchDocs()
	drive := newBatchDrive(docs)
	drive.folders["staging"] = []googleapi.Folder{{ID: "staging", Role: "staging", MimeType: googleapi.FolderMimeType, CanAddChildren: true, CanEdit: true, AppProperties: folderProperties("staging")}}
	drive.folders["review"] = []googleapi.Folder{{ID: "review", Role: "review", MimeType: googleapi.FolderMimeType, CanAddChildren: true, CanEdit: true, AppProperties: folderProperties("review")}}
	application.GoogleFactory = fakeFactory{drive: drive, docs: docs}
	output.Reset()
	remoteOptions := options
	remoteOptions.Remote = true
	if err := application.Status(context.Background(), remoteOptions); err != nil {
		t.Fatal(err)
	}
	var remote report.RemoteStatusReport
	if err := json.Unmarshal([]byte(output.String()), &remote); err != nil {
		t.Fatal(err)
	}
	if len(remote.Files) != 1 || remote.Files[0] != "README.md" || len(remote.Changes) != 1 || remote.Changes[0].State != "modified" {
		t.Fatalf("remote local view = %#v", remote)
	}
	if _, err := os.Stat(markerPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("remote status executed filter: %v", err)
	}
}

func TestDirectDoctorIsReadyWithoutAuthOrDestination(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("# Title\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var output strings.Builder
	application := New(&output, panicClientProvider{})
	application.DocumentRunner = staticDocumentRunner{}
	overrides := map[string]any{"output": map[string]any{"format": "json"}}
	err := application.Doctor(context.Background(), DoctorOptions{CommonOptions: CommonOptions{AllowDirect: true, Sources: []string{"README.md"}, Overrides: overrides}})
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{`"kind": "doctor"`, `"result": "ready"`, `direct mode does not use Google auth`, `direct mode has no Google destination`} {
		if !strings.Contains(output.String(), expected) {
			t.Fatalf("direct doctor output = %q", output.String())
		}
	}
}

func TestDoctorRejectsUnsupportedPandocVersion(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("# Title\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(root, "pandoc-old")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nprintf 'pandoc 2.19.2\\n'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	var output strings.Builder
	application := New(&output, panicClientProvider{})
	application.DocumentRunner = staticDocumentRunner{}
	overrides := map[string]any{"pandoc_binary": binary, "output": map[string]any{"format": "json"}}
	err := application.Doctor(context.Background(), DoctorOptions{CommonOptions: CommonOptions{AllowDirect: true, Sources: []string{"README.md"}, Overrides: overrides}})
	if err == nil || !strings.Contains(output.String(), `"result": "not_ready"`) || !strings.Contains(output.String(), "unsupported Pandoc version") {
		t.Fatalf("doctor error=%v output=%q", err, output.String())
	}
}

func TestDoctorRejectsCorruptReferenceDOCX(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("# Title\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	reference := filepath.Join(root, "reference.docx")
	if err := os.WriteFile(reference, []byte("not a DOCX"), 0o600); err != nil {
		t.Fatal(err)
	}
	var output strings.Builder
	application := New(&output, panicClientProvider{})
	application.DocumentRunner = staticDocumentRunner{}
	overrides := map[string]any{
		"output":  map[string]any{"format": "json"},
		"profile": map[string]any{"style": map[string]any{"reference_docx": reference}},
	}
	err := application.Doctor(context.Background(), DoctorOptions{CommonOptions: CommonOptions{AllowDirect: true, Sources: []string{"README.md"}, Overrides: overrides}})
	if err == nil || !strings.Contains(output.String(), `"result": "not_ready"`) || !strings.Contains(output.String(), "reference DOCX") || !strings.Contains(output.String(), "invalid") {
		t.Fatalf("doctor error=%v output=%q", err, output.String())
	}
}

func TestDoctorRemoteStatusAndPlanRejectUnsafeDestination(t *testing.T) {
	root := t.TempDir()
	projectPath := filepath.Join(root, ".mdoc.yaml")
	if err := config.WriteNeutralProject(projectPath, "testing", false); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("# Title\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(root, "private", "state.json")
	overrides := map[string]any{
		"output":  map[string]any{"format": "json"},
		"profile": map[string]any{"state_file": statePath},
	}
	resolved, err := config.Resolve(context.Background(), config.ResolverOptions{ProjectPath: projectPath, Profile: "testing", Overrides: overrides})
	if err != nil {
		t.Fatal(err)
	}
	store := state.NewStore(statePath)
	if err := store.Acquire(); err != nil {
		t.Fatal(err)
	}
	publishState := state.New(resolved.Identity(), "testing")
	publishState.Folders = state.Folders{StagingID: "staging", ReviewID: "review"}
	if err := store.Save(publishState); err != nil {
		t.Fatal(err)
	}
	store.Release()

	docs := newBatchDocs()
	drive := newBatchDrive(docs)
	drive.folders["staging"] = []googleapi.Folder{safeMappedFolder("staging", "Staging", resolved.Identity(), "testing", "staging")}
	unsafeReview := safeMappedFolder("review", "Review", resolved.Identity(), "testing", "review")
	unsafeReview.DriveID = "shared-drive"
	drive.folders["review"] = []googleapi.Folder{unsafeReview}
	var output strings.Builder
	application := New(&output, fakeClientProvider{})
	application.DocumentRunner = staticDocumentRunner{}
	application.GoogleFactory = fakeFactory{drive: drive, docs: docs}
	common := CommonOptions{Config: projectPath, Profile: "testing", Overrides: overrides}

	if err := application.Doctor(context.Background(), DoctorOptions{CommonOptions: common}); err == nil || !strings.Contains(output.String(), "Shared Drive") {
		t.Fatalf("doctor error=%v output=%q", err, output.String())
	}
	output.Reset()
	remote := common
	remote.Remote = true
	if err := application.Status(context.Background(), remote); err == nil || !strings.Contains(output.String(), "Shared Drive") {
		t.Fatalf("remote status error=%v output=%q", err, output.String())
	}
	output.Reset()
	if err := application.Plan(context.Background(), PublishOptions{CommonOptions: common}); err == nil || !strings.Contains(err.Error(), "Shared Drive") {
		t.Fatalf("plan error=%v output=%q", err, output.String())
	}
	if drive.nextID != 0 {
		t.Fatalf("readiness checks wrote %d documents", drive.nextID)
	}
}

func TestGenericFixtureStatusFromNestedDirectory(t *testing.T) {
	fixtureRoot, err := filepath.Abs(filepath.Join("..", "..", "testdata", "generic"))
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(filepath.Join(fixtureRoot, "docs", "guide"))
	var output strings.Builder
	application := New(&output, panicClientProvider{})
	application.DocumentRunner = staticDocumentRunner{}
	if err := application.Status(context.Background(), CommonOptions{}); err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{`"kind": "local_status"`, `"profile": "testing"`, `"docs/README.md"`, `"docs/guide/setup.md"`} {
		if !strings.Contains(output.String(), expected) {
			t.Fatalf("nested fixture status = %q", output.String())
		}
	}
	if strings.Contains(output.String(), "ignored.md") {
		t.Fatalf("excluded draft was selected: %q", output.String())
	}
}
