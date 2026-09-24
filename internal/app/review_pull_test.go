package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/goliatone/mdoc/internal/googleapi"
	"github.com/goliatone/mdoc/internal/report"
	"github.com/goliatone/mdoc/internal/reviewsync"
	"github.com/goliatone/mdoc/internal/state"
)

type reviewPullFixture struct {
	application  *Application
	drive        *batchDrive
	docs         *batchDocs
	output       *strings.Builder
	configPath   string
	sourcePath   string
	statePath    string
	snapshotRoot string
	fileID       string
}

func newReviewPullFixture(t *testing.T) reviewPullFixture {
	t.Helper()
	directory := t.TempDir()
	sourcePath := filepath.Join(directory, "report.md")
	baseline := []byte("# Title\n\nBody.\n")
	if err := os.WriteFile(sourcePath, baseline, 0o600); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(directory, ".mdoc.yaml")
	project := `version: 3
project_id: workspace
root: .
profiles:
  work:
    sources:
      include: [report.md]
    entry: report
    destination:
      review_folder_name: Review
      staging_folder_name: Staging
    state_file: private/state.json
    publications:
      report:
        kind: source
        source: report.md
        review:
          pull:
            enabled: true
`
	if err := os.WriteFile(configPath, []byte(project), 0o600); err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(directory, "private", "state.json")
	store := state.NewStore(statePath)
	if err := store.Acquire(); err != nil {
		t.Fatal(err)
	}
	initial := state.New("workspace", "work")
	initial.Folders = state.Folders{StagingID: "staging", ReviewID: "review"}
	initial.Account = "author@example.com"
	if err := store.Save(initial); err != nil {
		t.Fatal(err)
	}
	if err := store.Release(); err != nil {
		t.Fatal(err)
	}
	docs := newBatchDocs()
	drive := newBatchDrive(docs)
	drive.exportContent = baseline
	drive.folders["staging"] = []googleapi.Folder{safeMappedFolder("staging", "Staging", "workspace", "work", "staging")}
	drive.folders["review"] = []googleapi.Folder{safeMappedFolder("review", "Review", "workspace", "work", "review")}
	output := &strings.Builder{}
	application := New(output, fakeClientProvider{})
	application.DocumentRunner = staticDocumentRunner{}
	application.GoogleFactory = fakeFactory{drive: drive, docs: docs}
	if err := application.Publish(context.Background(), PublishOptions{CommonOptions: CommonOptions{Config: configPath, Profile: "work"}}); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	active := loaded.Targets["publication:report"].ActiveTarget
	if active == nil || active.ReviewSnapshot == nil {
		t.Fatalf("publish did not seal review baseline: %#v", loaded.Targets)
	}
	snapshots, err := reviewsync.NewSnapshotStore(statePath)
	if err != nil {
		t.Fatal(err)
	}
	output.Reset()
	return reviewPullFixture{application: application, drive: drive, docs: docs, output: output, configPath: configPath, sourcePath: sourcePath, statePath: statePath, snapshotRoot: snapshots.Root(), fileID: active.FileID}
}

func (f reviewPullFixture) pull(jsonOutput bool) error {
	return f.application.ReviewPull(context.Background(), ReviewPullOptions{CommonOptions: CommonOptions{Config: f.configPath, Profile: "work", JSON: jsonOutput}})
}

func TestReviewPullEndToEndOutcomes(t *testing.T) {
	tests := []struct {
		name       string
		prepare    func(*testing.T, reviewPullFixture)
		wantCode   ReviewResultCode
		wantOutput []string
		wantAbsent []string
	}{
		{name: "unchanged", wantOutput: []string{`"result": "review_no_changes"`, `"kind": "review_pull"`}},
		{name: "clean patch", prepare: func(_ *testing.T, fixture reviewPullFixture) {
			fixture.drive.exportContent = []byte("# Title\n\nEdited remotely.\n")
		}, wantOutput: []string{`"result": "review_clean_patch"`, `-Body.`, `+Edited remotely.`}},
		{name: "conflict", prepare: func(t *testing.T, fixture reviewPullFixture) {
			fixture.drive.exportContent = []byte("# Title\n\nEdited remotely.\n")
			if err := os.WriteFile(fixture.sourcePath, []byte("# Title\n\nEdited locally.\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}, wantCode: ReviewCodeConflict, wantOutput: []string{`"result": "review_conflict"`, `"conflicts"`}, wantAbsent: []string{`"patch":`}},
		{name: "unsupported", prepare: func(_ *testing.T, fixture reviewPullFixture) {
			fixture.drive.exportContent = []byte("# Title\n\n```text\ncode\n```\n")
		}, wantCode: ReviewCodeUnsupported, wantOutput: []string{`"result": "review_unsupported"`, `"unsupported": 1`}, wantAbsent: []string{`"patch":`}},
		{name: "comments", prepare: func(_ *testing.T, fixture reviewPullFixture) {
			fixture.drive.comments[fixture.fileID] = googleapi.CommentStatus{Unresolved: 2, Pages: 1}
		}, wantOutput: []string{`"result": "review_no_changes"`, `"open_comments": 2`}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newReviewPullFixture(t)
			if test.prepare != nil {
				test.prepare(t, fixture)
			}
			stateBefore := readTestFile(t, fixture.statePath)
			snapshotBefore := readTree(t, fixture.snapshotRoot)
			sourceBefore := readTestFile(t, fixture.sourcePath)
			propertyCalls := sumIntMap(fixture.drive.propertyCalls)
			updateCalls := fixture.docs.updateCalls
			err := fixture.pull(true)
			if test.wantCode == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				assertReviewCode(t, err, test.wantCode)
			}
			for _, want := range test.wantOutput {
				if !strings.Contains(fixture.output.String(), want) {
					t.Fatalf("output missing %q: %s", want, fixture.output.String())
				}
			}
			for _, absent := range test.wantAbsent {
				if strings.Contains(fixture.output.String(), absent) {
					t.Fatalf("output contains unsafe field %q: %s", absent, fixture.output.String())
				}
			}
			if got := readTestFile(t, fixture.statePath); got != stateBefore {
				t.Fatal("review pull changed state")
			}
			if got := readTree(t, fixture.snapshotRoot); got != snapshotBefore {
				t.Fatal("review pull changed snapshots")
			}
			if got := readTestFile(t, fixture.sourcePath); got != sourceBefore {
				t.Fatal("review pull changed a source")
			}
			if sumIntMap(fixture.drive.propertyCalls) != propertyCalls || fixture.docs.updateCalls != updateCalls {
				t.Fatal("review pull made a Google write")
			}
		})
	}
}

func TestReviewPullBlocksUnsafeRemoteAndBaselineStates(t *testing.T) {
	tests := []struct {
		name     string
		prepare  func(*testing.T, reviewPullFixture)
		wantCode ReviewResultCode
	}{
		{name: "suggestions", prepare: func(_ *testing.T, fixture reviewPullFixture) {
			document := fixture.docs.documents[fixture.fileID]
			document.SuggestionsDetected = true
			fixture.docs.documents[fixture.fileID] = document
		}, wantCode: ReviewCodeSuggestionsOpen},
		{name: "missing snapshot", prepare: func(t *testing.T, fixture reviewPullFixture) {
			if err := os.Rename(fixture.snapshotRoot, fixture.snapshotRoot+".missing"); err != nil {
				t.Fatal(err)
			}
		}, wantCode: ReviewCodeBaselineMissing},
		{name: "damaged snapshot", prepare: func(t *testing.T, fixture reviewPullFixture) {
			path := findTreeFile(t, fixture.snapshotRoot, "baseline.md")
			data := []byte(readTestFile(t, path))
			data[len(data)-2] ^= 1
			if err := os.WriteFile(path, data, 0o600); err != nil {
				t.Fatal(err)
			}
		}, wantCode: ReviewCodeSnapshotInvalid},
		{name: "unstable export", prepare: func(_ *testing.T, fixture reviewPullFixture) {
			fixture.application.GoogleFactory = fakeFactory{drive: &changingReviewDrive{batchDrive: fixture.drive}, docs: fixture.docs}
		}, wantCode: ReviewCodeExportUnstable},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newReviewPullFixture(t)
			test.prepare(t, fixture)
			assertReviewCode(t, fixture.pull(true), test.wantCode)
		})
	}
}

func TestReviewPullWritesPrivatePatchWithOverwriteProtection(t *testing.T) {
	fixture := newReviewPullFixture(t)
	fixture.drive.exportContent = []byte("# Title\n\nEdited remotely.\n")
	patchPath := filepath.Join(filepath.Dir(fixture.statePath), "review.patch")
	options := ReviewPullOptions{CommonOptions: CommonOptions{Config: fixture.configPath, Profile: "work"}, Publication: "report", PatchOutput: patchPath}
	if err := fixture.application.ReviewPull(context.Background(), options); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Account: author@example.com", "Document: " + fixture.fileID, "Patch:"} {
		if !strings.Contains(fixture.output.String(), want) {
			t.Fatalf("human output missing %q: %s", want, fixture.output.String())
		}
	}
	info, err := os.Stat(patchPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 || !strings.Contains(readTestFile(t, patchPath), "+Edited remotely.") {
		t.Fatalf("patch mode=%o content=%q", info.Mode().Perm(), readTestFile(t, patchPath))
	}
	if err := fixture.application.ReviewPull(context.Background(), options); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("second patch output error = %v", err)
	}
	options.Overwrite = true
	if err := fixture.application.ReviewPull(context.Background(), options); err != nil {
		t.Fatal(err)
	}
	options.PatchOutput = fixture.sourcePath
	if err := fixture.application.ReviewPull(context.Background(), options); err == nil || !strings.Contains(err.Error(), "publication source") {
		t.Fatalf("source output error = %v", err)
	}
}

func TestReviewBootstrapUsesSealedBaselineWithExternalDocument(t *testing.T) {
	fixture := newReviewPullFixture(t)
	externalID := "external-doc-123"
	fixture.drive.files[externalID] = googleapi.File{
		ID: externalID, Name: "Uploaded review", URL: "https://docs.google.com/document/d/" + externalID + "/edit",
		MimeType: googleapi.DocumentMimeType, Version: "12", Parents: []string{"user-folder"}, AppProperties: map[string]string{},
	}
	fixture.docs.documents[externalID] = googleapi.Document{
		ID: externalID, RevisionID: "external-r2", Tabs: []googleapi.TabTopology{{ID: "tab", Title: "Tab", Index: 0}},
	}
	fixture.drive.exportContent = []byte("# Title\n\nEdited in the uploaded document.\n")
	fixture.drive.comments[externalID] = googleapi.CommentStatus{Unresolved: 2, Pages: 1}
	patchPath := filepath.Join(filepath.Dir(fixture.statePath), "bootstrap.patch")
	stateBefore := readTestFile(t, fixture.statePath)
	snapshotBefore := readTree(t, fixture.snapshotRoot)
	sourceBefore := readTestFile(t, fixture.sourcePath)
	propertyCalls := sumIntMap(fixture.drive.propertyCalls)
	updateCalls := fixture.docs.updateCalls

	err := fixture.application.ReviewBootstrap(context.Background(), ReviewBootstrapOptions{
		ReviewPullOptions: ReviewPullOptions{
			CommonOptions: CommonOptions{Config: fixture.configPath, Profile: "work", JSON: true},
			Publication:   "report", PatchOutput: patchPath,
		},
		Document: "https://docs.google.com/document/d/" + externalID + "/edit?tab=t.0",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`"kind": "review_bootstrap"`,
		`"baseline_file_id": "` + fixture.fileID + `"`,
		`"file_id": "` + externalID + `"`,
		`"open_comments": 2`,
		`+Edited in the uploaded document.`,
	} {
		if !strings.Contains(fixture.output.String(), want) {
			t.Fatalf("bootstrap output missing %q: %s", want, fixture.output.String())
		}
	}
	if !strings.Contains(readTestFile(t, patchPath), "+Edited in the uploaded document.") {
		t.Fatalf("bootstrap patch = %q", readTestFile(t, patchPath))
	}
	if got := readTestFile(t, fixture.statePath); got != stateBefore {
		t.Fatal("review bootstrap changed state")
	}
	if got := readTree(t, fixture.snapshotRoot); got != snapshotBefore {
		t.Fatal("review bootstrap changed snapshots")
	}
	if got := readTestFile(t, fixture.sourcePath); got != sourceBefore {
		t.Fatal("review bootstrap changed a source")
	}
	if sumIntMap(fixture.drive.propertyCalls) != propertyCalls || fixture.docs.updateCalls != updateCalls {
		t.Fatal("review bootstrap made a Google write")
	}
}

func TestReviewBootstrapUsesLocalMarkdownExportWithoutReadingExternalGoogleDoc(t *testing.T) {
	fixture := newReviewPullFixture(t)
	externalID := "external-doc-outside-drive-file-scope"
	exportPath := filepath.Join(t.TempDir(), "edited-review.md")
	if err := os.WriteFile(exportPath, []byte("# Title\n\nEdited in the downloaded document.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	patchPath := filepath.Join(filepath.Dir(fixture.statePath), "bootstrap-export.patch")
	stateBefore := readTestFile(t, fixture.statePath)
	snapshotBefore := readTree(t, fixture.snapshotRoot)
	sourceBefore := readTestFile(t, fixture.sourcePath)

	err := fixture.application.ReviewBootstrap(context.Background(), ReviewBootstrapOptions{
		ReviewPullOptions: ReviewPullOptions{
			CommonOptions: CommonOptions{Config: fixture.configPath, Profile: "work", JSON: true},
			Publication:   "report", PatchOutput: patchPath,
		},
		Document:     externalID,
		ReviewExport: exportPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`"kind": "review_bootstrap"`,
		`"file_id": "` + externalID + `"`,
		`"review_source": "local_markdown_export"`,
		`"remote_checks_verified": false`,
		`+Edited in the downloaded document.`,
	} {
		if !strings.Contains(fixture.output.String(), want) {
			t.Fatalf("bootstrap output missing %q: %s", want, fixture.output.String())
		}
	}
	if fixture.drive.exportCalls[externalID] != 0 {
		t.Fatalf("external Google export calls = %d", fixture.drive.exportCalls[externalID])
	}
	if got := readTestFile(t, fixture.statePath); got != stateBefore {
		t.Fatal("review bootstrap changed state")
	}
	if got := readTree(t, fixture.snapshotRoot); got != snapshotBefore {
		t.Fatal("review bootstrap changed snapshots")
	}
	if got := readTestFile(t, fixture.sourcePath); got != sourceBefore {
		t.Fatal("review bootstrap changed a source")
	}
}

func TestReviewBootstrapCanWriteSafePartialPatchWhileReturningBlockedResult(t *testing.T) {
	fixture := newReviewPullFixture(t)
	exportPath := filepath.Join(t.TempDir(), "edited-review.md")
	content := "# Changed title\n\nBody.\n\n```text\nunsupported\n```\n"
	if err := os.WriteFile(exportPath, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	patchPath := filepath.Join(filepath.Dir(fixture.statePath), "partial.patch")
	err := fixture.application.ReviewBootstrap(context.Background(), ReviewBootstrapOptions{
		ReviewPullOptions: ReviewPullOptions{
			CommonOptions: CommonOptions{Config: fixture.configPath, Profile: "work", JSON: true},
			Publication:   "report", PatchOutput: patchPath,
		},
		Document: "external-doc", ReviewExport: exportPath, Partial: true,
	})
	if err == nil {
		t.Fatal("partial bootstrap should retain its blocked result")
	}
	if !strings.Contains(fixture.output.String(), `"partial_patch": true`) || !strings.Contains(fixture.output.String(), `"patch_output":`) {
		t.Fatalf("partial report = %s", fixture.output.String())
	}
	if patch := readTestFile(t, patchPath); !strings.Contains(patch, "+# Changed title") {
		t.Fatalf("partial patch = %q", patch)
	}
}

func TestReadLocalReviewExportRejectsUnsafeFiles(t *testing.T) {
	directory := t.TempDir()
	empty := filepath.Join(directory, "empty.md")
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readLocalReviewExport(empty); err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("empty export error = %v", err)
	}
	if runtime.GOOS != "windows" {
		regular := filepath.Join(directory, "review.md")
		link := filepath.Join(directory, "link.md")
		if err := os.WriteFile(regular, []byte("# Review\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(regular, link); err != nil {
			t.Fatal(err)
		}
		if _, err := readLocalReviewExport(link); err == nil || !strings.Contains(err.Error(), "symlink") {
			t.Fatalf("symlink export error = %v", err)
		}
	}
}

func TestReviewBootstrapDocumentIDValidation(t *testing.T) {
	tests := []struct {
		value string
		want  string
		ok    bool
	}{
		{value: "abc_DEF-123", want: "abc_DEF-123", ok: true},
		{value: "https://docs.google.com/document/d/abc_DEF-123/edit?tab=t.0", want: "abc_DEF-123", ok: true},
		{value: "http://docs.google.com/document/d/abc/edit"},
		{value: "https://drive.google.com/file/d/abc/view"},
		{value: "https://docs.google.com/spreadsheets/d/abc/edit"},
		{value: "not a document"},
	}
	for _, test := range tests {
		got, err := reviewDocumentID(test.value)
		if test.ok {
			if err != nil || got != test.want {
				t.Fatalf("reviewDocumentID(%q) = %q, %v", test.value, got, err)
			}
		} else if err == nil {
			t.Fatalf("reviewDocumentID(%q) = %q, want error", test.value, got)
		}
	}
}

func TestReviewBootstrapRejectsUnsafeExternalDocument(t *testing.T) {
	fixture := newReviewPullFixture(t)
	externalID := "external-doc"
	fixture.drive.files[externalID] = googleapi.File{ID: externalID, MimeType: googleapi.DOCXMimeType, Version: "1"}
	fixture.docs.documents[externalID] = googleapi.Document{ID: externalID, RevisionID: "r1", Tabs: []googleapi.TabTopology{{ID: "tab", Title: "Tab", Index: 0}}}
	err := fixture.application.ReviewBootstrap(context.Background(), ReviewBootstrapOptions{
		ReviewPullOptions: ReviewPullOptions{CommonOptions: CommonOptions{Config: fixture.configPath, Profile: "work"}, Publication: "report"},
		Document:          externalID,
	})
	assertReviewCode(t, err, ReviewCodeTargetMismatch)
}

func TestReviewPullRejectsLockedStateAndSymlinkPatchOutput(t *testing.T) {
	t.Run("state lock", func(t *testing.T) {
		fixture := newReviewPullFixture(t)
		store := state.NewStore(fixture.statePath)
		if err := store.Acquire(); err != nil {
			t.Fatal(err)
		}
		defer store.Release()
		exports := fixture.drive.exportCalls[fixture.fileID]
		if err := fixture.pull(false); errorCode(err) != "state_locked" {
			t.Fatalf("locked pull error = %v", err)
		}
		if fixture.drive.exportCalls[fixture.fileID] != exports {
			t.Fatal("locked pull reached Google export")
		}
	})

	if runtime.GOOS != "windows" {
		t.Run("symlink patch output", func(t *testing.T) {
			fixture := newReviewPullFixture(t)
			fixture.drive.exportContent = []byte("# Title\n\nEdited remotely.\n")
			target := filepath.Join(t.TempDir(), "target.patch")
			if err := os.WriteFile(target, []byte("keep"), 0o600); err != nil {
				t.Fatal(err)
			}
			link := filepath.Join(filepath.Dir(target), "output.patch")
			if err := os.Symlink(target, link); err != nil {
				t.Fatal(err)
			}
			err := fixture.application.ReviewPull(context.Background(), ReviewPullOptions{CommonOptions: CommonOptions{Config: fixture.configPath, Profile: "work"}, PatchOutput: link, Overwrite: true})
			if err == nil || !strings.Contains(err.Error(), "symlink") || readTestFile(t, target) != "keep" {
				t.Fatalf("symlink output error=%v target=%q", err, readTestFile(t, target))
			}
		})
	}
}

func TestReviewPullDiagnosticsRedactLocalBodyAndCommentContent(t *testing.T) {
	fixture := newReviewPullFixture(t)
	secret := "access_token=private-review-body"
	if err := os.WriteFile(fixture.sourcePath, []byte("# Title\n\n"+secret+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fixture.drive.comments[fixture.fileID] = googleapi.CommentStatus{Unresolved: 3, Pages: 2}
	fixture.output.Reset()
	if err := fixture.pull(false); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(fixture.output.String(), secret) || !strings.Contains(fixture.output.String(), "Open comments: 3") {
		t.Fatalf("diagnostic output = %q", fixture.output.String())
	}
	document := fixture.docs.documents[fixture.fileID]
	document.SuggestionsDetected = true
	fixture.docs.documents[fixture.fileID] = document
	fixture.drive.exportContent = []byte(secret)
	fixture.output.Reset()
	err := fixture.pull(false)
	if errorCode(err) != string(ReviewCodeSuggestionsOpen) || strings.Contains(err.Error(), secret) || strings.Contains(fixture.output.String(), secret) {
		t.Fatalf("suggestion error=%v output=%q", err, fixture.output.String())
	}
}

func TestReconcileRecoversOnlyExactLocalReviewSnapshotReferences(t *testing.T) {
	t.Run("valid sealed snapshot", func(t *testing.T) {
		fixture := newReviewPullFixture(t)
		file := fixture.drive.files[fixture.fileID]
		file.Version = increment(file.Version)
		fixture.drive.files[fixture.fileID] = file
		document := fixture.docs.documents[fixture.fileID]
		document.RevisionID = "edited-r2"
		fixture.docs.documents[fixture.fileID] = document
		if err := os.Rename(fixture.statePath, fixture.statePath+".lost"); err != nil {
			t.Fatal(err)
		}
		fixture.output.Reset()
		if err := fixture.application.Reconcile(context.Background(), ReconcileOptions{CommonOptions: CommonOptions{Config: fixture.configPath, Profile: "work"}}); err != nil {
			t.Fatal(err)
		}
		loaded, err := state.NewStore(fixture.statePath).Load()
		if err != nil {
			t.Fatal(err)
		}
		active := loaded.Targets["publication:report"].ActiveTarget
		if active == nil || active.ReviewSnapshot == nil || !strings.Contains(fixture.output.String(), "Recovered 1 sealed review snapshot references") {
			t.Fatalf("recovered state=%#v output=%q", loaded.Targets, fixture.output.String())
		}
		fixture.output.Reset()
		if err := fixture.application.Status(context.Background(), CommonOptions{Config: fixture.configPath, Profile: "work", JSON: true, Remote: true}); err != nil {
			t.Fatal(err)
		}
		var status report.RemoteStatusReport
		if err := json.Unmarshal([]byte(fixture.output.String()), &status); err != nil {
			t.Fatal(err)
		}
		if len(status.Targets) != 1 || status.Targets[0].Review == nil || status.Targets[0].Review.State != reviewStateChanged {
			t.Fatalf("recovered edited target status = %#v", status.Targets)
		}
	})

	t.Run("edited remote without local snapshot", func(t *testing.T) {
		fixture := newReviewPullFixture(t)
		if err := os.Rename(fixture.snapshotRoot, fixture.snapshotRoot+".missing"); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(fixture.statePath, fixture.statePath+".lost"); err != nil {
			t.Fatal(err)
		}
		file := fixture.drive.files[fixture.fileID]
		file.Version = increment(file.Version)
		fixture.drive.files[fixture.fileID] = file
		doc := fixture.docs.documents[fixture.fileID]
		doc.RevisionID = "edited-r2"
		fixture.docs.documents[fixture.fileID] = doc
		exports := fixture.drive.exportCalls[fixture.fileID]
		if err := fixture.application.Reconcile(context.Background(), ReconcileOptions{CommonOptions: CommonOptions{Config: fixture.configPath, Profile: "work"}}); err != nil {
			t.Fatal(err)
		}
		loaded, err := state.NewStore(fixture.statePath).Load()
		if err != nil {
			t.Fatal(err)
		}
		active := loaded.Targets["publication:report"].ActiveTarget
		if active == nil || active.ReviewSnapshot != nil {
			t.Fatalf("edited remote was adopted as a baseline: %#v", active)
		}
		if fixture.drive.exportCalls[fixture.fileID] != exports {
			t.Fatal("reconcile exported edited remote content")
		}
		if _, err := os.Stat(fixture.snapshotRoot); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("reconcile manufactured snapshot storage: %v", err)
		}
	})

	t.Run("sealed snapshot with another operation", func(t *testing.T) {
		fixture := newReviewPullFixture(t)
		if err := os.Rename(fixture.statePath, fixture.statePath+".lost"); err != nil {
			t.Fatal(err)
		}
		file := fixture.drive.files[fixture.fileID]
		file.AppProperties["mdoc_operation"] = "different-operation"
		fixture.drive.files[fixture.fileID] = file
		if err := fixture.application.Reconcile(context.Background(), ReconcileOptions{CommonOptions: CommonOptions{Config: fixture.configPath, Profile: "work"}}); err != nil {
			t.Fatal(err)
		}
		loaded, err := state.NewStore(fixture.statePath).Load()
		if err != nil {
			t.Fatal(err)
		}
		active := loaded.Targets["publication:report"].ActiveTarget
		if active == nil || active.ReviewSnapshot != nil {
			t.Fatalf("mismatched snapshot was recovered: %#v", active)
		}
	})
}

type changingReviewDrive struct {
	*batchDrive
	reads int
}

func (drive *changingReviewDrive) GetFile(ctx context.Context, id string) (googleapi.File, error) {
	file, err := drive.batchDrive.GetFile(ctx, id)
	if err != nil {
		return googleapi.File{}, err
	}
	drive.reads++
	file.Version = fmt.Sprintf("changing-%d", drive.reads)
	return file, nil
}

func assertReviewCode(t *testing.T, err error, want ReviewResultCode) {
	t.Helper()
	var typed *Error
	if !errors.As(err, &typed) || typed.Code != string(want) {
		t.Fatalf("error = %#v, want code %s", err, want)
	}
}

func readTestFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func readTree(t *testing.T, root string) string {
	t.Helper()
	var result strings.Builder
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		result.WriteString(strings.TrimPrefix(path, root))
		result.Write(data)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return result.String()
}

func findTreeFile(t *testing.T, root, name string) string {
	t.Helper()
	result := ""
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Name() == name {
			result = path
		}
		return nil
	})
	if err != nil || result == "" {
		t.Fatalf("find %s: path=%q err=%v", name, result, err)
	}
	return result
}

func sumIntMap(values map[string]int) int {
	total := 0
	for _, value := range values {
		total += value
	}
	return total
}
