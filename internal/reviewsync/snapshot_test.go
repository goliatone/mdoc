package reviewsync

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
	"time"

	"github.com/goliatone/mdoc/internal/googleapi"
)

func TestSnapshotCandidateAndSealRoundTrip(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "private", "state.json")
	store, err := NewSnapshotStore(statePath)
	if err != nil {
		t.Fatal(err)
	}
	input := testCandidateInput()
	candidate, err := store.WriteCandidate(input)
	if err != nil {
		t.Fatal(err)
	}
	if candidate.Status != SnapshotCandidate || candidate.Activation != nil || !validHash(candidate.IntegrityHash) {
		t.Fatalf("candidate = %#v", candidate)
	}
	loaded, err := store.Load(input.TargetKey, input.Generation, false)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(loaded.Baseline, input.Baseline) || !bytes.Equal(loaded.Sources["docs/a.md"], input.Sources[0].Content) {
		t.Fatalf("loaded snapshot differs: %#v", loaded)
	}
	if _, err := store.Load(input.TargetKey, input.Generation, true); !errors.Is(err, ErrSnapshotUnsealed) {
		t.Fatalf("unsealed error = %v", err)
	}
	seal := testActivation()
	sealed, err := store.Seal(input.TargetKey, input.Generation, seal)
	if err != nil {
		t.Fatal(err)
	}
	if sealed.Status != SnapshotSealed || sealed.Activation == nil || sealed.IntegrityHash == candidate.IntegrityHash {
		t.Fatalf("sealed manifest = %#v", sealed)
	}
	loaded, err = store.Load(input.TargetKey, input.Generation, true)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Manifest.Activation.DriveVersion != "3" || loaded.Manifest.Capture.DriveVersion != "1" {
		t.Fatalf("capture and activation versions were not kept separate: %#v", loaded.Manifest)
	}
	if again, err := store.Seal(input.TargetKey, input.Generation, seal); err != nil || again.IntegrityHash != sealed.IntegrityHash {
		t.Fatalf("idempotent seal = %#v, %v", again, err)
	}
	assertSnapshotPermissions(t, store, input)
}

func TestSnapshotCandidateIsIdempotentButRejectsDifferentContent(t *testing.T) {
	store, _ := NewSnapshotStore(filepath.Join(t.TempDir(), "state.json"))
	input := testCandidateInput()
	first, err := store.WriteCandidate(input)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.WriteCandidate(input)
	if err != nil || second.IntegrityHash != first.IntegrityHash {
		t.Fatalf("second candidate = %#v, %v", second, err)
	}
	input.Baseline = []byte("changed baseline")
	if _, err := store.WriteCandidate(input); !errors.Is(err, ErrSnapshotDamaged) {
		t.Fatalf("different candidate error = %v", err)
	}
}

func TestSnapshotCandidateRejectsDuplicateAndUnsafeSourceKeys(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*CandidateInput)
	}{
		{name: "duplicate", change: func(input *CandidateInput) { input.Sources = append(input.Sources, input.Sources[0]) }},
		{name: "path traversal", change: func(input *CandidateInput) { input.Sources[0].SourceKey = "../outside.md" }},
		{name: "non canonical Unicode", change: func(input *CandidateInput) { input.Sources[0].SourceKey = "docs/cafe\u0301.md" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, _ := NewSnapshotStore(filepath.Join(t.TempDir(), "state.json"))
			input := testCandidateInput()
			test.change(&input)
			if _, err := store.WriteCandidate(input); err == nil {
				t.Fatal("expected unsafe source rejection")
			}
		})
	}
}

func TestConcurrentSnapshotCandidatesLeaveOneValidGeneration(t *testing.T) {
	store, _ := NewSnapshotStore(filepath.Join(t.TempDir(), "state.json"))
	input := testCandidateInput()
	const writers = 8
	start := make(chan struct{})
	errorsSeen := make(chan error, writers)
	var group sync.WaitGroup
	for range writers {
		group.Go(func() {
			<-start
			_, err := store.WriteCandidate(input)
			errorsSeen <- err
		})
	}
	close(start)
	group.Wait()
	close(errorsSeen)
	successes := 0
	for err := range errorsSeen {
		if err == nil {
			successes++
		}
	}
	if successes == 0 {
		t.Fatal("no concurrent writer completed")
	}
	if _, err := store.Load(input.TargetKey, input.Generation, false); err != nil {
		t.Fatalf("concurrent writes damaged the visible snapshot: %v", err)
	}
}

func TestSnapshotSealRejectsRevisionAndTopologyMismatch(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*ActivationSeal)
	}{
		{name: "revision", change: func(value *ActivationSeal) { value.DocsRevision = "other" }},
		{name: "tab title", change: func(value *ActivationSeal) { value.Tabs[0].Title = "Renamed" }},
		{name: "parent", change: func(value *ActivationSeal) { value.ReviewParent = "" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, _ := NewSnapshotStore(filepath.Join(t.TempDir(), "state.json"))
			input := testCandidateInput()
			if _, err := store.WriteCandidate(input); err != nil {
				t.Fatal(err)
			}
			seal := testActivation()
			test.change(&seal)
			if _, err := store.Seal(input.TargetKey, input.Generation, seal); err == nil {
				t.Fatal("expected seal rejection")
			}
		})
	}
}

func TestSnapshotLoadRejectsDamageAndUnsafePaths(t *testing.T) {
	tests := []struct {
		name   string
		damage func(*testing.T, *SnapshotStore, CandidateInput)
	}{
		{name: "baseline", damage: func(t *testing.T, store *SnapshotStore, input CandidateInput) {
			path, _ := store.generationPath(input.TargetKey, input.Generation)
			if err := os.WriteFile(filepath.Join(path, "baseline.md"), []byte("damage"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "source", damage: func(t *testing.T, store *SnapshotStore, input CandidateInput) {
			path, _ := store.generationPath(input.TargetKey, input.Generation)
			if err := os.WriteFile(filepath.Join(path, "sources", hashString("docs/a.md")+".md"), []byte("damage"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "manifest", damage: func(t *testing.T, store *SnapshotStore, input CandidateInput) {
			path, _ := store.generationPath(input.TargetKey, input.Generation)
			data, err := os.ReadFile(filepath.Join(path, "manifest.json"))
			if err != nil {
				t.Fatal(err)
			}
			data = bytes.Replace(data, []byte(`"profile": "work"`), []byte(`"profile": "evil"`), 1)
			if err := os.WriteFile(filepath.Join(path, "manifest.json"), data, 0o600); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store, _ := NewSnapshotStore(filepath.Join(t.TempDir(), "state.json"))
			input := testCandidateInput()
			if _, err := store.WriteCandidate(input); err != nil {
				t.Fatal(err)
			}
			test.damage(t, store, input)
			if _, err := store.Load(input.TargetKey, input.Generation, false); !errors.Is(err, ErrSnapshotDamaged) {
				t.Fatalf("damage error = %v", err)
			}
		})
	}
	if runtime.GOOS != "windows" {
		t.Run("symlink target directory", func(t *testing.T) {
			root := t.TempDir()
			store, _ := NewSnapshotStore(filepath.Join(root, "state.json"))
			input := testCandidateInput()
			if err := os.Mkdir(store.Root(), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(t.TempDir(), filepath.Join(store.Root(), hashString(input.TargetKey))); err != nil {
				t.Fatal(err)
			}
			if _, err := store.WriteCandidate(input); err == nil {
				t.Fatal("expected symlink path rejection")
			}
		})
	}
}

func TestSnapshotStoreUsesOnlyEffectiveStatePath(t *testing.T) {
	directory := t.TempDir()
	first, _ := NewSnapshotStore(filepath.Join(directory, "first.json"))
	second, _ := NewSnapshotStore(filepath.Join(directory, "second.json"))
	if first.Root() == second.Root() {
		t.Fatal("custom state files share a snapshot root")
	}
	input := testCandidateInput()
	if _, err := first.WriteCandidate(input); err != nil {
		t.Fatal(err)
	}
	if _, err := second.Load(input.TargetKey, input.Generation, false); !errors.Is(err, ErrSnapshotNotFound) {
		t.Fatalf("moved state path searched a fallback: %v", err)
	}
	path, _ := first.generationPath("../../unsafe", 1)
	if !strings.HasPrefix(path, first.Root()+string(filepath.Separator)) || strings.Contains(filepath.ToSlash(strings.TrimPrefix(path, first.Root())), "../") {
		t.Fatalf("target key escaped snapshot root: %s", path)
	}
}

func TestSnapshotAtomicFailuresKeepLastValidState(t *testing.T) {
	injected := errors.New("injected before rename")
	store, _ := NewSnapshotStore(filepath.Join(t.TempDir(), "state.json"))
	input := testCandidateInput()
	store.beforeRename = func(string) error { return injected }
	if _, err := store.WriteCandidate(input); !errors.Is(err, injected) {
		t.Fatalf("candidate error = %v", err)
	}
	if _, err := store.Load(input.TargetKey, input.Generation, false); !errors.Is(err, ErrSnapshotNotFound) {
		t.Fatalf("failed candidate became visible: %v", err)
	}
	store.beforeRename = nil
	candidate, err := store.WriteCandidate(input)
	if err != nil {
		t.Fatal(err)
	}
	store.beforeRename = func(string) error { return injected }
	if _, err := store.Seal(input.TargetKey, input.Generation, testActivation()); !errors.Is(err, injected) {
		t.Fatalf("seal error = %v", err)
	}
	loaded, err := store.Load(input.TargetKey, input.Generation, false)
	if err != nil || loaded.Manifest.Status != SnapshotCandidate || loaded.Manifest.IntegrityHash != candidate.IntegrityHash {
		t.Fatalf("failed seal replaced candidate: %#v, %v", loaded.Manifest, err)
	}
}

func TestSnapshotOrphansAreReportedAndRetained(t *testing.T) {
	store, _ := NewSnapshotStore(filepath.Join(t.TempDir(), "state.json"))
	first := testCandidateInput()
	firstManifest, err := store.WriteCandidate(first)
	if err != nil {
		t.Fatal(err)
	}
	second := testCandidateInput()
	second.TargetKey = "publication:other"
	second.PublicationID = "other"
	second.Generation = 2
	if _, err := store.WriteCandidate(second); err != nil {
		t.Fatal(err)
	}
	orphans, err := store.Orphans([]SnapshotReference{{TargetKey: first.TargetKey, Generation: first.Generation, ManifestHash: firstManifest.IntegrityHash}})
	if err != nil {
		t.Fatal(err)
	}
	if len(orphans) != 1 || orphans[0].TargetKey != second.TargetKey || orphans[0].Reason != "not referenced by state" {
		t.Fatalf("orphans = %#v", orphans)
	}
	if _, err := store.Load(second.TargetKey, second.Generation, false); err != nil {
		t.Fatalf("orphan was not retained: %v", err)
	}
}

func TestSnapshotOrphanScanRejectsUnsafePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("directory permission bits are not portable on Windows")
	}
	store, _ := NewSnapshotStore(filepath.Join(t.TempDir(), "state.json"))
	input := testCandidateInput()
	if _, err := store.WriteCandidate(input); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(store.Root(), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Orphans(nil); err == nil || !strings.Contains(err.Error(), "not private") {
		t.Fatalf("orphan permission error = %v", err)
	}
}

func TestSnapshotManifestDoesNotStoreFullContent(t *testing.T) {
	store, _ := NewSnapshotStore(filepath.Join(t.TempDir(), "state.json"))
	input := testCandidateInput()
	manifest, err := store.WriteCandidate(input)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, input.Baseline) || bytes.Contains(data, input.Sources[0].Content) {
		t.Fatalf("manifest contains full content: %s", data)
	}
}

func testCandidateInput() CandidateInput {
	source := []byte("# Title\n\nBody.\n")
	return CandidateInput{
		WorkspaceID: "workspace", Profile: "work", TargetKey: "publication:report", PublicationID: "report", PublicationKind: "bundle", Reader: "gfm",
		FileID: "file", Generation: 1, ReviewSetID: "set", OperationID: "operation",
		Capture:  CaptureTuple{FileID: "file", DriveVersion: "1", DocsRevision: "revision", Tabs: []googleapi.TabTopology{{ID: "tab", Title: "Tab", Index: 0}}},
		Baseline: []byte("# Cover\n\n# Title\n\nBody.\n"),
		Sources:  []SnapshotSource{{SourceKey: "docs/a.md", SourceHash: hashBytes(source), Content: source}},
		SourceMap: SourceMap{Version: SourceMapVersion, Entries: []SourceMapEntry{
			{BaselineIndex: 0, Kind: BlockHeading, BaselineKind: BlockHeading, Fingerprint: "heading:Cover", Signature: blockSignature(Block{Kind: BlockHeading, Fingerprint: "heading:Cover"}, true), VisibleText: "Cover", Generated: true},
			{BaselineIndex: 1, Member: "docs/a.md", Kind: BlockHeading, BaselineKind: BlockHeading, Fingerprint: "heading:Title", Signature: blockSignature(Block{Kind: BlockHeading, Fingerprint: "heading:Title"}, true), VisibleText: "Title", SourceRange: ByteRange{Start: 0, End: 8}, Occurrence: 1, Eligible: true},
			{BaselineIndex: 2, Member: "docs/a.md", Kind: BlockParagraph, BaselineKind: BlockParagraph, Fingerprint: "paragraph:Body.", Signature: blockSignature(Block{Kind: BlockParagraph, Fingerprint: "paragraph:Body."}, true), VisibleText: "Body.", SourceRange: ByteRange{Start: 9, End: len(source)}, Occurrence: 1, Eligible: true},
		}},
		CapturedAt: time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC),
	}
}

func testActivation() ActivationSeal {
	return ActivationSeal{
		FileID: "file", DriveVersion: "3", DocsRevision: "revision", ReviewParent: "review",
		ReadyMetadata: map[string]string{"mdoc_status": "ready"}, Tabs: []googleapi.TabTopology{{ID: "tab", Title: "Tab", Index: 0}},
		ActivatedAt: time.Date(2026, 8, 3, 12, 1, 0, 0, time.UTC),
	}
}

func assertSnapshotPermissions(t *testing.T, store *SnapshotStore, input CandidateInput) {
	t.Helper()
	if runtime.GOOS == "windows" {
		return
	}
	path, _ := store.generationPath(input.TargetKey, input.Generation)
	for _, directory := range []string{store.Root(), filepath.Dir(path), path, filepath.Join(path, "sources")} {
		info, err := os.Stat(directory)
		if err != nil || info.Mode().Perm() != 0o700 {
			t.Fatalf("directory %s mode = %#o, error = %v", directory, info.Mode().Perm(), err)
		}
	}
	for _, file := range []string{filepath.Join(path, "manifest.json"), filepath.Join(path, "baseline.md"), filepath.Join(path, "sources", hashString("docs/a.md")+".md")} {
		info, err := os.Stat(file)
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("file %s mode = %#o, error = %v", file, info.Mode().Perm(), err)
		}
	}
}
