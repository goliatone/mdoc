package state

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

func TestStoreSaveLoadBackupAndPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private", "state.json")
	store := NewStore(path)
	if err := store.Acquire(); err != nil {
		t.Fatal(err)
	}
	defer store.Release()
	value := New("workspace", "work")
	if err := store.Save(value); err != nil {
		t.Fatal(err)
	}
	value.Folders.ReviewID = "review"
	if err := store.Save(value); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Folders.ReviewID != "review" {
		t.Fatalf("review = %q", loaded.Folders.ReviewID)
	}
	if _, err := os.Stat(store.BackupPath()); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("state mode = %#o", info.Mode().Perm())
		}
		parent, err := os.Stat(filepath.Dir(path))
		if err != nil {
			t.Fatal(err)
		}
		if parent.Mode().Perm() != 0o700 {
			t.Fatalf("state dir mode = %#o", parent.Mode().Perm())
		}
	}
}

func TestStorePreservesExistingParentDirectoryMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("directory permission bits are not portable on Windows")
	}
	parent := filepath.Join(t.TempDir(), "workspace")
	if err := os.Mkdir(parent, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(parent, 0o755); err != nil {
		t.Fatal(err)
	}
	store := NewStore(filepath.Join(parent, "state.json"))
	if err := store.Acquire(); err != nil {
		t.Fatal(err)
	}
	defer store.Release()
	info, err := os.Stat(parent)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Fatalf("parent mode changed to %#o", info.Mode().Perm())
	}
}

func TestStoreMigratesVersionOne(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	legacy := map[string]any{
		"version": 1, "workspace_id": "workspace", "profile": "work",
		"folders": map[string]any{}, "documents": map[string]any{},
	}
	data, _ := json.Marshal(legacy)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := NewStore(path).Load()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Version != CurrentVersion {
		t.Fatalf("version = %d", loaded.Version)
	}
}

func TestStoreMigratesVersionTwoSourceKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	legacy := map[string]any{
		"version": 2, "workspace_id": "workspace", "profile": "work",
		"folders": map[string]any{}, "documents": map[string]any{"docs/cafe\u0301.md": map[string]any{}},
	}
	data, _ := json.Marshal(legacy)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := NewStore(path).Load()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := loaded.Documents["docs/caf\u00e9.md"]; !ok {
		t.Fatalf("documents = %#v", loaded.Documents)
	}
}

func TestStoreRejectsVersionTwoSourceKeyCollision(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	legacy := map[string]any{
		"version": 2, "workspace_id": "workspace", "profile": "work",
		"folders": map[string]any{}, "documents": map[string]any{
			"docs/cafe\u0301.md": map[string]any{},
			"docs/caf\u00e9.md":  map[string]any{},
		},
	}
	data, _ := json.Marshal(legacy)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := NewStore(path).Load()
	if !errors.Is(err, ErrDamaged) || !strings.Contains(err.Error(), "collide") {
		t.Fatalf("error = %v", err)
	}
}

func TestStoreRejectsDamagedState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(path, []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := NewStore(path).Load()
	if !errors.Is(err, ErrDamaged) {
		t.Fatalf("error = %v", err)
	}
}

func TestStateRejectsNonCanonicalSourceKeys(t *testing.T) {
	value := New("workspace", "work")
	value.Documents["docs/cafe\u0301.md"] = Document{}
	if err := value.Validate(); err == nil {
		t.Fatal("expected decomposed source key to be rejected")
	}
}

func TestStoreLockPreventsConcurrentCommands(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	first := NewStore(path)
	second := NewStore(path)
	if err := first.Acquire(); err != nil {
		t.Fatal(err)
	}
	defer first.Release()
	if err := second.Acquire(); err == nil {
		second.Release()
		t.Fatal("expected lock conflict")
	}
}

func TestStoreLockRejectsConcurrentContenders(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	owner := NewStore(path)
	if err := owner.Acquire(); err != nil {
		t.Fatal(err)
	}
	defer owner.Release()
	const contenders = 16
	start := make(chan struct{})
	results := make(chan error, contenders)
	var group sync.WaitGroup
	for range contenders {
		group.Go(func() {
			<-start
			store := NewStore(path)
			err := store.Acquire()
			if err == nil {
				_ = store.Release()
			}
			results <- err
		})
	}
	close(start)
	group.Wait()
	close(results)
	for err := range results {
		if err == nil {
			t.Fatal("a contender acquired an owned state lock")
		}
	}
}

func TestStoreJournalIsDurableAndExplicit(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "state.json"))
	if err := store.Acquire(); err != nil {
		t.Fatal(err)
	}
	defer store.Release()
	journal := &Journal{
		WorkspaceID: "workspace", Profile: "work", OperationID: "operation", ReviewSetID: "set", StagingID: "staging", ReviewID: "review",
		Sources: []string{"docs/a.md"},
		Entries: map[string]JournalEntry{"docs/a.md": {SourceKey: "docs/a.md", Generation: 1, SourceHash: "source", RenderHash: "render", DOCXHash: "docx", Stage: StageCreating}},
	}
	if err := store.WriteJournal(journal); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.LoadJournal()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Entries["docs/a.md"].Stage != StageCreating {
		t.Fatalf("stage = %q", loaded.Entries["docs/a.md"].Stage)
	}
	if err := store.AdvanceJournal(loaded, "docs/a.md", StageCreated, "file-id"); err != nil {
		t.Fatal(err)
	}
	if err := store.AdvanceJournal(loaded, "docs/a.md", StageReady, ""); err == nil {
		t.Fatal("expected skipped journal stage to fail")
	}
	if err := store.ClearJournal(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LoadJournal(); !errors.Is(err, ErrNotFound) {
		t.Fatalf("journal error = %v", err)
	}
}

func TestStoreRejectsLegacyJournalWithoutFrozenInputs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	legacy := []byte(`{"version":1,"workspace_id":"workspace","profile":"work","operation_id":"operation","review_set_id":"set","sources":["docs/a.md"],"entries":{"docs/a.md":{"source_key":"docs/a.md","generation":1,"stage":"planned"}}}`)
	if err := os.WriteFile(path+".journal", legacy, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := NewStore(path).LoadJournal()
	if err == nil || !strings.Contains(err.Error(), "state reconcile") {
		t.Fatalf("error = %v", err)
	}
}

func TestStoreMigratesVersionTwoJournalSourceKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	legacy := &Journal{
		Version: 2, Kind: "publish", WorkspaceID: "workspace", Profile: "work", OperationID: "operation", ReviewSetID: "set", StagingID: "staging", ReviewID: "review",
		KnownTargets: map[string]string{"docs/cafe\u0301.md": "old-file"},
		Sources:      []string{"docs/cafe\u0301.md"},
		Entries: map[string]JournalEntry{"docs/cafe\u0301.md": {
			SourceKey: "docs/cafe\u0301.md", Generation: 1, SourceHash: "source", RenderHash: "render", DOCXHash: "docx", Stage: StageCreating,
			Links: []JournalLink{{Placeholder: "placeholder", TargetSource: "docs/targete\u0301.md"}},
		}},
	}
	data, _ := json.Marshal(legacy)
	if err := os.WriteFile(path+".journal", data, 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := NewStore(path).LoadJournal()
	if err != nil {
		t.Fatal(err)
	}
	key := "source:docs/caf\u00e9.md"
	if loaded.Version != CurrentJournalVersion || loaded.Entries[key].SourceKey != key || loaded.Entries[key].Links[0].TargetSource != "source:docs/target\u00e9.md" || loaded.KnownTargets[key] != "old-file" {
		t.Fatalf("journal = %#v", loaded)
	}
}

func TestStoreMigratesVersionThreeToTargetStateAndWritesProtectedBackup(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "state.json")
	active := Target{FileID: "active", URL: "url", ReviewSetID: "set", Generation: 2, OperationID: "operation", PublishStatus: "ready"}
	prior := Target{FileID: "prior", URL: "prior-url", ReviewSetID: "old-set", Generation: 1, OperationID: "old-operation", PublishStatus: "ready"}
	legacy := map[string]any{"version": 3, "workspace_id": "workspace", "profile": "work", "folders": map[string]any{}, "documents": map[string]any{"docs/a.md": Document{SourceHash: "source", RenderHash: "render", ActiveTarget: &active, PriorTargets: []Target{prior}}}}
	data, _ := json.Marshal(legacy)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	store := NewStore(path)
	if err := store.Acquire(); err != nil {
		t.Fatal(err)
	}
	defer store.Release()
	loaded, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	record := loaded.Targets["source:docs/a.md"]
	if record.TargetKey != "source:docs/a.md" || record.Source != "docs/a.md" || record.ActiveTarget.FileID != "active" || len(record.PriorTargets) != 1 || loaded.Documents["docs/a.md"].ActiveTarget.FileID != "active" {
		t.Fatalf("state = %#v", loaded)
	}
	if err := store.Save(loaded); err != nil {
		t.Fatal(err)
	}
	backup, err := os.ReadFile(store.PreV4BackupPath())
	if err != nil || !bytes.Equal(backup, data) {
		t.Fatalf("pre-v4 backup error = %v, data = %s", err, backup)
	}
	if info, err := os.Stat(store.PreV4BackupPath()); err != nil || runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("backup info = %#v, error = %v", info, err)
	}
	written, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(written, []byte(`"documents"`)) || !bytes.Contains(written, []byte(`"targets"`)) || !bytes.Contains(written, []byte(`"source:docs/a.md"`)) {
		t.Fatalf("version 4 state = %s", written)
	}
}

func TestStoreNeverOverwritesPreV4Backup(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "state.json")
	legacy := []byte(`{"version":3,"workspace_id":"workspace","profile":"work","folders":{},"documents":{}}`)
	if err := os.WriteFile(path, legacy, 0o600); err != nil {
		t.Fatal(err)
	}
	store := NewStore(path)
	sentinel := []byte("existing backup")
	if err := os.WriteFile(store.PreV4BackupPath(), sentinel, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := store.Acquire(); err != nil {
		t.Fatal(err)
	}
	defer store.Release()
	loaded, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(loaded); err != nil {
		t.Fatal(err)
	}
	backup, err := os.ReadFile(store.PreV4BackupPath())
	if err != nil || !bytes.Equal(backup, sentinel) {
		t.Fatalf("backup = %q, error = %v", backup, err)
	}
}

func TestStoreMigratesVersionFourWithoutInventingReviewSnapshots(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	active := Target{FileID: "active", URL: "url", ReviewSetID: "set", Generation: 2, OperationID: "operation", PublishStatus: "ready"}
	legacy := map[string]any{
		"version": 4, "workspace_id": "workspace", "profile": "work", "folders": map[string]any{},
		"targets": map[string]any{"publication:report": Document{TargetKey: "publication:report", PublicationID: "report", PublicationKind: "bundle", ActiveTarget: &active}},
	}
	data, _ := json.Marshal(legacy)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := NewStore(path).Load()
	if err != nil {
		t.Fatal(err)
	}
	target := loaded.Targets["publication:report"].ActiveTarget
	if loaded.Version != CurrentVersion || target == nil || target.ReviewSnapshot != nil {
		t.Fatalf("migrated state = %#v", loaded)
	}
}

func TestStoreReviewSnapshotReferenceRoundTripsWithoutContent(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "state.json"))
	if err := store.Acquire(); err != nil {
		t.Fatal(err)
	}
	defer store.Release()
	value := New("workspace", "work")
	active := Target{
		FileID: "active", URL: "url", ReviewSetID: "set", Generation: 1, OperationID: "operation", PublishStatus: "ready",
		ReviewSnapshot: &ReviewSnapshotRef{Version: 1, ManifestHash: strings.Repeat("a", 64), BaselineHash: strings.Repeat("b", 64), CapturedRevision: "capture", ActivatedRevision: "activation"},
	}
	value.Targets["publication:report"] = Document{TargetKey: "publication:report", PublicationID: "report", PublicationKind: "bundle", ActiveTarget: &active}
	if err := store.Save(value); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(store.Path())
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte("full baseline content")) || bytes.Contains(data, []byte("full source content")) {
		t.Fatal("state contains snapshot content")
	}
	loaded, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Targets["publication:report"].ActiveTarget.ReviewSnapshot.ManifestHash != strings.Repeat("a", 64) {
		t.Fatalf("state = %#v", loaded)
	}
}

func TestStateAcceptsCurrentReviewSnapshotReferenceVersion(t *testing.T) {
	value := New("workspace", "work")
	active := Target{
		FileID: "active", ReviewSetID: "set", Generation: 1, OperationID: "operation",
		ReviewSnapshot: &ReviewSnapshotRef{Version: 2, ManifestHash: strings.Repeat("a", 64), BaselineHash: strings.Repeat("b", 64), CapturedRevision: "capture", ActivatedRevision: "activation"},
	}
	value.Targets["publication:report"] = Document{TargetKey: "publication:report", PublicationID: "report", PublicationKind: "bundle", ActiveTarget: &active}
	if err := value.Validate(); err != nil {
		t.Fatal(err)
	}
	active.ReviewSnapshot.Version = 3
	if err := value.Validate(); err == nil {
		t.Fatal("accepted an unknown review snapshot reference version")
	}
}

func TestStateRejectsIncompleteReviewSnapshotReference(t *testing.T) {
	value := New("workspace", "work")
	active := Target{FileID: "active", ReviewSetID: "set", Generation: 1, OperationID: "operation", ReviewSnapshot: &ReviewSnapshotRef{Version: 1}}
	value.Targets["publication:report"] = Document{TargetKey: "publication:report", PublicationID: "report", PublicationKind: "bundle", ActiveTarget: &active}
	if err := value.Validate(); err == nil || !strings.Contains(err.Error(), "review snapshot") {
		t.Fatalf("error = %v", err)
	}
}

func TestPublicationTargetsKeepIdentityAcrossSourceChangesAndIDChanges(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "state.json"))
	if err := store.Acquire(); err != nil {
		t.Fatal(err)
	}
	defer store.Release()
	value := New("workspace", "work")
	active := Target{FileID: "file", URL: "url", ReviewSetID: "set", Generation: 1, OperationID: "operation", PublishStatus: "ready"}
	value.Targets["publication:report"] = Document{TargetKey: "publication:report", PublicationID: "report", PublicationKind: "source", Source: "old.md", ActiveTarget: &active}
	if err := store.Save(value); err != nil {
		t.Fatal(err)
	}
	value.Targets["publication:report"] = Document{TargetKey: "publication:report", PublicationID: "report", PublicationKind: "source", Source: "new.md", ActiveTarget: &active}
	value.Targets["publication:renamed"] = Document{TargetKey: "publication:renamed", PublicationID: "renamed", PublicationKind: "source", Source: "new.md"}
	if err := store.Save(value); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Targets["publication:report"].ActiveTarget.FileID != "file" || loaded.Targets["publication:report"].Source != "new.md" || loaded.Targets["publication:renamed"].ActiveTarget != nil {
		t.Fatalf("targets = %#v", loaded.Targets)
	}
}

func TestBundleJournalInputsRoundTrip(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "state.json"))
	if err := store.Acquire(); err != nil {
		t.Fatal(err)
	}
	defer store.Release()
	key := "publication:report"
	journal := &Journal{WorkspaceID: "workspace", Profile: "work", OperationID: "operation", ReviewSetID: "set", StagingID: "staging", ReviewID: "review", Sources: []string{key}, Entries: map[string]JournalEntry{key: {SourceKey: key, Generation: 1, SourceHash: "source", RenderHash: "render", DOCXHash: "docx", ArtifactHash: "artifact", PublicationID: "report", PublicationKind: "bundle", Members: []JournalMember{{SourceKey: "a.md", SourceHash: "a"}, {SourceKey: "b.md", SourceHash: "b"}}, FieldsHash: "fields", LayoutHash: "layout", TopologyHash: "topology", Stage: StagePlanned}}}
	if err := store.WriteJournal(journal); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.LoadJournal()
	if err != nil {
		t.Fatal(err)
	}
	entry := loaded.Entries[key]
	if entry.PublicationKind != "bundle" || len(entry.Members) != 2 || entry.LayoutHash != "layout" || entry.TopologyHash != "topology" || entry.ArtifactHash != "artifact" {
		t.Fatalf("entry = %#v", entry)
	}
}

func TestReviewJournalRecordsCandidateAndSealHashes(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "state.json"))
	if err := store.Acquire(); err != nil {
		t.Fatal(err)
	}
	defer store.Release()
	key := "publication:report"
	journal := &Journal{
		WorkspaceID: "workspace", Profile: "work", OperationID: "operation", ReviewSetID: "set", StagingID: "staging", ReviewID: "review", Sources: []string{key},
		Entries: map[string]JournalEntry{key: {SourceKey: key, Generation: 1, SourceHash: "source", RenderHash: "render", DOCXHash: "docx", PublicationID: "report", PublicationKind: "source", ReviewPullEnabled: true, ReviewReader: "gfm", Stage: StageLinksFixed}},
	}
	if err := store.WriteJournal(journal); err != nil {
		t.Fatal(err)
	}
	if err := store.AdvanceJournal(journal, key, StageCapturingReview, "file"); err != nil {
		t.Fatal(err)
	}
	candidate := strings.Repeat("a", 64)
	if err := store.AdvanceReviewJournal(journal, key, StageReviewCaptured, candidate); err != nil {
		t.Fatal(err)
	}
	for _, stage := range []Stage{StageMarkingReady, StageReady, StageMoving, StageMoved, StageSealingReview} {
		if err := store.AdvanceJournal(journal, key, stage, "file"); err != nil {
			t.Fatal(err)
		}
	}
	sealed := strings.Repeat("b", 64)
	if err := store.AdvanceReviewJournal(journal, key, StageReviewSealed, sealed); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.LoadJournal()
	if err != nil {
		t.Fatal(err)
	}
	entry := loaded.Entries[key]
	if entry.ReviewCandidateHash != candidate || entry.ReviewSealedHash != sealed || entry.Stage != StageReviewSealed {
		t.Fatalf("entry = %#v", entry)
	}
}

func TestStoreRemapJournalIsDurableAndOrdered(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "state.json"))
	if err := store.Acquire(); err != nil {
		t.Fatal(err)
	}
	defer store.Release()
	journal := &RemapJournal{OperationID: "remap-operation", WorkspaceID: "workspace", Profile: "work", From: "docs/old.md", To: "docs/new.md", Targets: []string{"file"}, Entries: map[string]RemapJournalEntry{"file": {FileID: "file", Generation: 1, Stage: RemapStagePlanned}}}
	if err := store.WriteRemapJournal(journal); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.LoadRemapJournal()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AdvanceRemapJournal(loaded, "file", RemapStageUpdating); err != nil {
		t.Fatal(err)
	}
	if err := store.AdvanceRemapJournal(loaded, "file", RemapStageUpdated); err != nil {
		t.Fatal(err)
	}
	if err := store.AdvanceRemapJournal(loaded, "file", RemapStagePlanned); err == nil {
		t.Fatal("expected backward remap transition to fail")
	}
	if err := store.ClearRemapJournal(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LoadRemapJournal(); !errors.Is(err, ErrNotFound) {
		t.Fatalf("journal error = %v", err)
	}
}

func TestStoreMigratesVersionOneRemapJournal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	legacy := &RemapJournal{
		Version: 1, OperationID: "operation", WorkspaceID: "workspace", Profile: "work",
		From: "docs/cafe\u0301.md", To: "docs/the\u0301.md", Targets: []string{"file"},
		Entries: map[string]RemapJournalEntry{"file": {FileID: "file", Generation: 1, Stage: RemapStagePlanned}},
	}
	data, _ := json.Marshal(legacy)
	if err := os.WriteFile(path+".remap-journal", data, 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := NewStore(path).LoadRemapJournal()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Version != CurrentRemapJournalVersion || loaded.From != "docs/caf\u00e9.md" || loaded.To != "docs/th\u00e9.md" {
		t.Fatalf("journal = %#v", loaded)
	}
}

func TestAtomicWriteInterruptionKeepsLastValidFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(path, []byte("last valid"), 0o600); err != nil {
		t.Fatal(err)
	}
	injected := errors.New("interrupted before rename")
	err := atomicWriteWithHook(path, []byte("next"), 0o600, func(string) error { return injected })
	if !errors.Is(err, injected) {
		t.Fatalf("error = %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "last valid" {
		t.Fatalf("state = %q", data)
	}
}

func TestDefaultPathIsProfileSpecific(t *testing.T) {
	first, err := DefaultPath("workspace", "work")
	if err != nil {
		t.Fatal(err)
	}
	second, err := DefaultPath("workspace", "client")
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("profiles share a state path")
	}
}
