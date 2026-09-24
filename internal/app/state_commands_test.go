package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/goliatone/mdoc/internal/config"
	"github.com/goliatone/mdoc/internal/googleapi"
	"github.com/goliatone/mdoc/internal/state"
)

type recordingBrowser struct{ target string }

func (b *recordingBrowser) Open(target string) error { b.target = target; return nil }

func TestOpenUsesEntryAndRequestedSource(t *testing.T) {
	directory, configPath, statePath := stateCommandFixture(t)
	_ = directory
	store := state.NewStore(statePath)
	if err := store.Acquire(); err != nil {
		t.Fatal(err)
	}
	publishState := state.New("workspace", "work")
	publishState.Documents["docs/a.md"] = state.Document{ActiveTarget: &state.Target{FileID: "a", URL: "https://example.test/a", ReviewSetID: "set", Generation: 1, OperationID: "op", PublishStatus: "ready"}}
	publishState.Documents["docs/b.md"] = state.Document{ActiveTarget: &state.Target{FileID: "b", URL: "https://example.test/b", ReviewSetID: "set", Generation: 1, OperationID: "op", PublishStatus: "ready"}}
	publishState.Documents["docs/caf\u00e9.md"] = state.Document{ActiveTarget: &state.Target{FileID: "c", URL: "https://example.test/c", ReviewSetID: "set", Generation: 1, OperationID: "op", PublishStatus: "ready"}}
	if err := store.Save(publishState); err != nil {
		t.Fatal(err)
	}
	store.Release()
	var output strings.Builder
	browser := &recordingBrowser{}
	application := New(&output)
	application.Browser = browser
	if err := application.Open(context.Background(), OpenOptions{CommonOptions: CommonOptions{Config: configPath, Profile: "work"}}); err != nil {
		t.Fatal(err)
	}
	if browser.target != "https://example.test/a" {
		t.Fatalf("entry target = %q", browser.target)
	}
	if err := application.Open(context.Background(), OpenOptions{CommonOptions: CommonOptions{Config: configPath, Profile: "work"}, File: "docs/b.md"}); err != nil {
		t.Fatal(err)
	}
	if browser.target != "https://example.test/b" {
		t.Fatalf("file target = %q", browser.target)
	}
	if err := application.Open(context.Background(), OpenOptions{CommonOptions: CommonOptions{Config: configPath, Profile: "work"}, File: "docs/cafe\u0301.md"}); err != nil {
		t.Fatal(err)
	}
	if browser.target != "https://example.test/c" {
		t.Fatalf("normalized file target = %q", browser.target)
	}
}

func TestOpenBlocksAStoredAccountMismatchBeforeOpeningBrowser(t *testing.T) {
	_, configPath, statePath := stateCommandFixture(t)
	store := state.NewStore(statePath)
	if err := store.Acquire(); err != nil {
		t.Fatal(err)
	}
	publishState := state.New("workspace", "work")
	publishState.Account = "author@example.com"
	publishState.Documents["docs/a.md"] = state.Document{ActiveTarget: &state.Target{FileID: "a", URL: "https://example.test/a", ReviewSetID: "set", Generation: 1, OperationID: "op", PublishStatus: "ready"}}
	if err := store.Save(publishState); err != nil {
		t.Fatal(err)
	}
	store.Release()
	browser := &recordingBrowser{}
	application := New(&strings.Builder{}, alternateAccountProvider{account: "other@example.com"})
	application.Browser = browser
	err := application.Open(context.Background(), OpenOptions{CommonOptions: CommonOptions{Config: configPath, Profile: "work"}})
	if err == nil || !strings.Contains(err.Error(), "belongs to Google account author@example.com") {
		t.Fatalf("open account error = %v", err)
	}
	if browser.target != "" {
		t.Fatalf("browser opened %q", browser.target)
	}
}

func TestOpenUsesApprovedExternalEntryAndPath(t *testing.T) {
	root := t.TempDir()
	external := t.TempDir()
	externalEntry := filepath.Join(external, "entry.md")
	if err := os.WriteFile(externalEntry, []byte("# External\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(root, ".mdoc.yaml")
	if err := config.WriteNeutralProject(configPath, "testing", false); err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(root, "private", "state.json")
	overrides := map[string]any{"profile": map[string]any{
		"sources": map[string]any{"include": []string{externalEntry}, "external_roots": []string{external}},
		"entry":   externalEntry, "state_file": statePath,
	}}
	resolved, err := config.Resolve(context.Background(), config.ResolverOptions{ProjectPath: configPath, Profile: "testing", Overrides: overrides})
	if err != nil {
		t.Fatal(err)
	}
	store := state.NewStore(statePath)
	if err := store.Acquire(); err != nil {
		t.Fatal(err)
	}
	publishState := state.New(resolved.Identity(), "testing")
	publishState.Documents["external/1/entry.md"] = state.Document{ActiveTarget: &state.Target{FileID: "external", URL: "https://example.test/external", ReviewSetID: "set", Generation: 1, OperationID: "op", PublishStatus: "ready"}}
	if err := store.Save(publishState); err != nil {
		t.Fatal(err)
	}
	store.Release()
	browser := &recordingBrowser{}
	application := New(&strings.Builder{})
	application.Browser = browser
	for _, entry := range []string{externalEntry, "external/1/entry.md"} {
		overrides["profile"].(map[string]any)["entry"] = entry
		common := CommonOptions{Config: configPath, Profile: "testing", Overrides: overrides}
		if err := application.Open(context.Background(), OpenOptions{CommonOptions: common}); err != nil {
			t.Fatalf("open entry %q: %v", entry, err)
		}
		if browser.target != "https://example.test/external" {
			t.Fatalf("external entry %q target = %q", entry, browser.target)
		}
	}
	browser.target = ""
	overrides["profile"].(map[string]any)["entry"] = externalEntry
	common := CommonOptions{Config: configPath, Profile: "testing", Overrides: overrides}
	if err := application.Open(context.Background(), OpenOptions{CommonOptions: common, File: externalEntry}); err != nil {
		t.Fatal(err)
	}
	if browser.target != "https://example.test/external" {
		t.Fatalf("external path target = %q", browser.target)
	}
}

func TestOpenInfersOnlySelectedSourceAndRequiresEntryForSeveral(t *testing.T) {
	root := t.TempDir()
	docs := filepath.Join(root, "docs")
	if err := os.MkdirAll(docs, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(docs, "only.md"), []byte("# Only\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(root, ".mdoc.yaml")
	statePath := filepath.Join(root, "private", "state.json")
	yaml := "version: 2\nproject_id: workspace\nprofiles:\n  work:\n    sources:\n      include: [docs]\n    destination:\n      review_folder_name: Review\n      staging_folder_name: Staging\n    state_file: private/state.json\n"
	if err := os.WriteFile(configPath, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	store := state.NewStore(statePath)
	if err := store.Acquire(); err != nil {
		t.Fatal(err)
	}
	publishState := state.New("workspace", "work")
	publishState.Documents["docs/only.md"] = state.Document{ActiveTarget: &state.Target{
		FileID: "only", URL: "https://example.test/only", ReviewSetID: "set", Generation: 1,
		OperationID: "operation", PublishStatus: "ready",
	}}
	if err := store.Save(publishState); err != nil {
		t.Fatal(err)
	}
	store.Release()

	browser := &recordingBrowser{}
	application := New(&strings.Builder{})
	application.Browser = browser
	common := CommonOptions{Config: configPath, Profile: "work"}
	if err := application.Open(context.Background(), OpenOptions{CommonOptions: common}); err != nil {
		t.Fatal(err)
	}
	if browser.target != "https://example.test/only" {
		t.Fatalf("inferred target = %q", browser.target)
	}
	if err := os.WriteFile(filepath.Join(docs, "second.md"), []byte("# Second\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := application.Open(context.Background(), OpenOptions{CommonOptions: common})
	if err == nil || !strings.Contains(err.Error(), "entry is required") {
		t.Fatalf("missing entry error = %v", err)
	}
}

func TestRemapUpdatesRemoteMetadataAndLocalState(t *testing.T) {
	directory, configPath, statePath := stateCommandFixture(t)
	if err := os.WriteFile(filepath.Join(directory, "docs", "new.md"), []byte("# New"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := state.NewStore(statePath)
	if err := store.Acquire(); err != nil {
		t.Fatal(err)
	}
	publishState := state.New("workspace", "work")
	target := state.Target{FileID: "doc", URL: "url", ReviewSetID: "set", Generation: 1, OperationID: "op", PublishStatus: "ready", RemoteVersion: "1"}
	publishState.Documents["docs/old.md"] = state.Document{ActiveTarget: &target}
	if err := store.Save(publishState); err != nil {
		t.Fatal(err)
	}
	store.Release()
	docs := newBatchDocs()
	drive := newBatchDrive(docs)
	drive.files["doc"] = googleapi.File{ID: "doc", Version: "1", AppProperties: map[string]string{"mdoc_workspace": "workspace", "mdoc_profile": "work", "mdoc_source": "docs/old.md"}}
	application := New(&strings.Builder{}, fakeClientProvider{})
	application.GoogleFactory = fakeFactory{drive: drive, docs: docs}
	if err := application.Remap(context.Background(), RemapOptions{CommonOptions: CommonOptions{Config: configPath, Profile: "work"}, From: "docs/old.md", To: "docs/new.md"}); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := loaded.Documents["docs/new.md"]; !ok {
		t.Fatalf("state = %#v", loaded.Documents)
	}
	if drive.files["doc"].AppProperties["mdoc_source"] != "docs/new.md" {
		t.Fatalf("properties = %#v", drive.files["doc"].AppProperties)
	}
}

func TestRemapRejectsExplicitPublicationTargets(t *testing.T) {
	application := New(&strings.Builder{})
	err := application.Remap(context.Background(), RemapOptions{From: "publication:old", To: "publication:new"})
	if err == nil || !strings.Contains(err.Error(), "keep their identity") {
		t.Fatalf("remap publication error = %v", err)
	}
}

func TestRemapResumesPartialRemoteMetadataUpdates(t *testing.T) {
	directory, configPath, statePath := stateCommandFixture(t)
	if err := os.WriteFile(filepath.Join(directory, "docs", "new.md"), []byte("# New"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := state.NewStore(statePath)
	if err := store.Acquire(); err != nil {
		t.Fatal(err)
	}
	publishState := state.New("workspace", "work")
	prior := state.Target{FileID: "prior", URL: "prior-url", ReviewSetID: "set-one", Generation: 1, OperationID: "op-one", PublishStatus: "ready", RemoteVersion: "1"}
	active := state.Target{FileID: "active", URL: "active-url", ReviewSetID: "set-two", Generation: 2, OperationID: "op-two", PublishStatus: "ready", RemoteVersion: "1"}
	publishState.Documents["docs/old.md"] = state.Document{PriorTargets: []state.Target{prior}, ActiveTarget: &active}
	if err := store.Save(publishState); err != nil {
		t.Fatal(err)
	}
	store.Release()
	docs := newBatchDocs()
	drive := newBatchDrive(docs)
	properties := func() map[string]string {
		return map[string]string{"mdoc_workspace": "workspace", "mdoc_profile": "work", "mdoc_source": "docs/old.md"}
	}
	drive.files["prior"] = googleapi.File{ID: "prior", Version: "1", AppProperties: properties()}
	drive.files["active"] = googleapi.File{ID: "active", Version: "1", AppProperties: properties()}
	drive.failPropertyAfter = 1
	application := New(&strings.Builder{}, fakeClientProvider{})
	application.GoogleFactory = fakeFactory{drive: drive, docs: docs}
	options := RemapOptions{CommonOptions: CommonOptions{Config: configPath, Profile: "work"}, From: "docs/old.md", To: "docs/new.md"}
	if err := application.Remap(context.Background(), options); err == nil {
		t.Fatal("expected partial remap failure")
	} else {
		var typed *Error
		if !errors.As(err, &typed) || typed.Recovery == nil {
			t.Fatalf("recovery error = %#v", err)
		}
		if typed.Recovery.Kind != "remap" || typed.Recovery.OperationID == "" || typed.Recovery.FileIDs["generation-1"] != "prior" || typed.Recovery.FileIDs["generation-2"] != "active" || typed.Recovery.JournalPath != store.RemapJournalPath() || !strings.Contains(typed.Recovery.NextCommand, "state remap") {
			t.Fatalf("recovery = %#v", typed.Recovery)
		}
	}
	if _, err := store.LoadRemapJournal(); err != nil {
		t.Fatalf("remap journal: %v", err)
	}
	if err := os.Remove(filepath.Join(directory, "docs", "new.md")); err != nil {
		t.Fatal(err)
	}
	if err := application.Remap(context.Background(), options); err == nil {
		t.Fatal("expected missing target to block remap resume")
	} else {
		var typed *Error
		if !errors.As(err, &typed) || typed.Recovery == nil || typed.Recovery.OperationID == "" || typed.Recovery.JournalPath != store.RemapJournalPath() {
			t.Fatalf("missing target recovery error = %#v", err)
		}
	}
	if err := os.WriteFile(filepath.Join(directory, "docs", "new.md"), []byte("# New"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := application.Reconcile(context.Background(), ReconcileOptions{CommonOptions: CommonOptions{Config: configPath, Profile: "work"}}); err == nil || !strings.Contains(err.Error(), "must be resumed") {
		t.Fatalf("reconcile error = %v", err)
	}
	if err := application.Remap(context.Background(), options); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if _, oldExists := loaded.Documents["docs/old.md"]; oldExists || loaded.Documents["docs/new.md"].ActiveTarget == nil {
		t.Fatalf("state = %#v", loaded.Documents)
	}
	if drive.files["prior"].AppProperties["mdoc_source"] != "docs/new.md" || drive.files["active"].AppProperties["mdoc_source"] != "docs/new.md" {
		t.Fatalf("remote files = %#v", drive.files)
	}
	if drive.propertyCalls["prior"] != 1 || drive.propertyCalls["active"] != 1 {
		t.Fatalf("property writes = %#v", drive.propertyCalls)
	}
	if _, err := store.LoadRemapJournal(); !errors.Is(err, state.ErrNotFound) {
		t.Fatalf("remap journal remains: %v", err)
	}
}

func TestReconcileRebuildsActivePriorFoldersAndLeavesMovedSetPending(t *testing.T) {
	_, configPath, statePath := stateCommandFixture(t)
	docs := newBatchDocs()
	drive := newBatchDrive(docs)
	drive.folders["staging"] = []googleapi.Folder{{ID: "staging", Role: "staging"}}
	drive.folders["review"] = []googleapi.Folder{{ID: "review", Role: "review"}}
	add := func(id, set string, generation int, parent string) {
		properties := map[string]string{"mdoc_workspace": "workspace", "mdoc_profile": "work", "mdoc_source": "docs/a.md", "mdoc_review_set": set, "mdoc_generation": fmt.Sprint(generation), "mdoc_operation": "op-" + id, "mdoc_expected_set_size": "1", "mdoc_status": "ready"}
		drive.files[id] = googleapi.File{ID: id, URL: "https://example.test/" + id, Version: fmt.Sprint(generation), Parents: []string{parent}, AppProperties: properties, ModifiedTime: time.Unix(int64(generation), 0).UTC()}
		docs.documents[id] = googleapi.Document{ID: id, RevisionID: "r" + fmt.Sprint(generation), Links: map[string][]googleapi.TextRange{}}
	}
	add("one", "set-one", 1, "review")
	add("two", "set-two", 2, "review")
	add("moved", "set-three", 3, "staging")
	var output strings.Builder
	application := New(&output, fakeClientProvider{})
	application.GoogleFactory = fakeFactory{drive: drive, docs: docs}
	if err := application.Reconcile(context.Background(), ReconcileOptions{CommonOptions: CommonOptions{Config: configPath, Profile: "work"}}); err != nil {
		t.Fatal(err)
	}
	loaded, err := state.NewStore(statePath).Load()
	if err != nil {
		t.Fatal(err)
	}
	documentState := loaded.Documents["docs/a.md"]
	if loaded.Folders.StagingID != "staging" || loaded.Folders.ReviewID != "review" || documentState.ActiveTarget.Generation != 2 || len(documentState.PriorTargets) != 1 || !strings.Contains(output.String(), "set-three") {
		t.Fatalf("state=%#v output=%q", loaded, output.String())
	}
	if loaded.Account != "author@example.com" {
		t.Fatalf("recovered account = %q", loaded.Account)
	}
}

func TestReconcileBindsRecoveredDamagedStateAndRejectsExistingMismatch(t *testing.T) {
	_, configPath, statePath := stateCommandFixture(t)
	if err := os.MkdirAll(filepath.Dir(statePath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(statePath, []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	docs := newBatchDocs()
	drive := newBatchDrive(docs)
	drive.folders["staging"] = []googleapi.Folder{{ID: "staging", Role: "staging"}}
	drive.folders["review"] = []googleapi.Folder{{ID: "review", Role: "review"}}
	application := New(&strings.Builder{}, fakeClientProvider{})
	application.GoogleFactory = fakeFactory{drive: drive, docs: docs}
	options := ReconcileOptions{CommonOptions: CommonOptions{Config: configPath, Profile: "work"}}
	if err := application.Reconcile(context.Background(), options); err != nil {
		t.Fatal(err)
	}
	loaded, err := state.NewStore(statePath).Load()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Account != "author@example.com" {
		t.Fatalf("damaged state recovery account = %q", loaded.Account)
	}

	loaded.Account = "original@example.com"
	store := state.NewStore(statePath)
	if err := store.Acquire(); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(loaded); err != nil {
		t.Fatal(err)
	}
	store.Release()
	mismatch := New(&strings.Builder{}, alternateAccountProvider{account: "other@example.com"})
	err = mismatch.Reconcile(context.Background(), options)
	if err == nil || !strings.Contains(err.Error(), "belongs to Google account original@example.com") {
		t.Fatalf("reconcile account error = %v", err)
	}
}

func TestReconcileExplicitlyAbandonsDamagedJournalAfterSafeScan(t *testing.T) {
	_, configPath, statePath := stateCommandFixture(t)
	store := state.NewStore(statePath)
	if err := store.Acquire(); err != nil {
		t.Fatal(err)
	}
	if err := store.Release(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.JournalPath(), []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	docs := newBatchDocs()
	drive := newBatchDrive(docs)
	drive.folders["staging"] = []googleapi.Folder{{ID: "staging", Role: "staging"}}
	drive.folders["review"] = []googleapi.Folder{{ID: "review", Role: "review"}}
	application := New(&strings.Builder{}, fakeClientProvider{})
	application.GoogleFactory = fakeFactory{drive: drive, docs: docs}
	common := CommonOptions{Config: configPath, Profile: "work"}
	if err := application.Reconcile(context.Background(), ReconcileOptions{CommonOptions: common}); err == nil || !strings.Contains(err.Error(), "--abandon-operation") {
		t.Fatalf("reconcile error = %v", err)
	}
	if err := application.Reconcile(context.Background(), ReconcileOptions{CommonOptions: common, AbandonOperation: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LoadJournal(); !errors.Is(err, state.ErrNotFound) {
		t.Fatalf("journal remains: %v", err)
	}
}

func TestReconcilePreservesValidPublishJournalWithoutAbandon(t *testing.T) {
	_, configPath, statePath := stateCommandFixture(t)
	store := state.NewStore(statePath)
	if err := store.Acquire(); err != nil {
		t.Fatal(err)
	}
	journal := &state.Journal{
		WorkspaceID: "workspace", Profile: "work", OperationID: "operation", ReviewSetID: "set", StagingID: "staging", ReviewID: "review",
		Sources: []string{"docs/a.md"}, Entries: map[string]state.JournalEntry{"docs/a.md": {SourceKey: "docs/a.md", Generation: 1, SourceHash: "source", RenderHash: "render", DOCXHash: "docx", Stage: state.StagePlanned}},
	}
	if err := store.WriteJournal(journal); err != nil {
		t.Fatal(err)
	}
	store.Release()
	docs := newBatchDocs()
	drive := newBatchDrive(docs)
	drive.folders["staging"] = []googleapi.Folder{{ID: "staging", Role: "staging"}}
	drive.folders["review"] = []googleapi.Folder{{ID: "review", Role: "review"}}
	application := New(&strings.Builder{}, fakeClientProvider{})
	application.GoogleFactory = fakeFactory{drive: drive, docs: docs}
	if err := application.Reconcile(context.Background(), ReconcileOptions{CommonOptions: CommonOptions{Config: configPath, Profile: "work"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LoadJournal(); err != nil {
		t.Fatalf("journal was not preserved: %v", err)
	}
}

func TestReconcileRefusesToAbandonJournalWithPendingRemoteSet(t *testing.T) {
	_, configPath, statePath := stateCommandFixture(t)
	store := state.NewStore(statePath)
	if err := store.Acquire(); err != nil {
		t.Fatal(err)
	}
	if err := store.Release(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.JournalPath(), []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	docs := newBatchDocs()
	drive := newBatchDrive(docs)
	drive.folders["staging"] = []googleapi.Folder{{ID: "staging", Role: "staging"}}
	drive.folders["review"] = []googleapi.Folder{{ID: "review", Role: "review"}}
	drive.files["pending"] = googleapi.File{ID: "pending", Parents: []string{"staging"}, AppProperties: map[string]string{
		"mdoc_workspace": "workspace", "mdoc_profile": "work", "mdoc_source": "docs/a.md", "mdoc_review_set": "pending-set",
		"mdoc_generation": "1", "mdoc_operation": "operation", "mdoc_expected_set_size": "1", "mdoc_status": "ready",
	}}
	docs.documents["pending"] = googleapi.Document{ID: "pending", RevisionID: "r1", Links: map[string][]googleapi.TextRange{}}
	application := New(&strings.Builder{}, fakeClientProvider{})
	application.GoogleFactory = fakeFactory{drive: drive, docs: docs}
	err := application.Reconcile(context.Background(), ReconcileOptions{CommonOptions: CommonOptions{Config: configPath, Profile: "work"}, AbandonOperation: true})
	if err == nil || !strings.Contains(err.Error(), "pending-set") {
		t.Fatalf("reconcile error = %v", err)
	}
	if _, statErr := os.Stat(store.JournalPath()); statErr != nil {
		t.Fatalf("journal was cleared: %v", statErr)
	}
}

func stateCommandFixture(t *testing.T) (string, string, string) {
	t.Helper()
	directory := t.TempDir()
	if err := os.Mkdir(filepath.Join(directory, "docs"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"a.md", "b.md"} {
		if err := os.WriteFile(filepath.Join(directory, "docs", name), []byte("# "+name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	configPath := filepath.Join(directory, ".mdoc.yaml")
	yaml := "version: 1\nworkspace_id: workspace\nprofiles:\n  work:\n    sources: [docs/*.md]\n    entry: docs/a.md\n    review_folder_name: Review\n    staging_folder_name: Staging\n    reference_docx: reference.docx\n    max_table_columns: 5\n    state_file: private/state.json\n"
	if err := os.WriteFile(configPath, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	return directory, configPath, filepath.Join(directory, "private", "state.json")
}
