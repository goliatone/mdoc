package reviewsync

import (
	"bytes"
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/goliatone/mdoc/internal/document"
	"github.com/goliatone/mdoc/internal/googleapi"
)

func TestBuildProposalsCleanAndNonOverlappingThreeWayMerge(t *testing.T) {
	requirePandoc(t)
	base := []byte("# Title\n\nFirst.\n\nSecond.\n\nThird.\n")
	snapshot, sourceMap := sealedTestSnapshot(t, base)
	review := []byte("# Title\n\nFirst changed.\n\nSecond.\n\nThird.\n")
	blocks, _ := ParseBaseline(context.Background(), PandocParser{}, "gfm", review)
	classification, err := ClassifyReview(sourceMap, blocks)
	if err != nil {
		t.Fatal(err)
	}
	clean, err := BuildProposals(snapshot, classification, map[string][]byte{"docs/a.md": base})
	if err != nil {
		t.Fatal(err)
	}
	if len(clean.Conflicts) != 0 || len(clean.Files) != 1 || !bytes.Contains(clean.Files[0].Proposed, []byte("First changed.")) {
		t.Fatalf("clean proposal = %#v", clean)
	}
	local := bytes.Replace(base, []byte("Third."), []byte("Third local."), 1)
	merged, err := BuildProposals(snapshot, classification, map[string][]byte{"docs/a.md": local})
	if err != nil {
		t.Fatal(err)
	}
	if len(merged.Conflicts) != 0 || !bytes.Contains(merged.Files[0].Proposed, []byte("First changed.")) || !bytes.Contains(merged.Files[0].Proposed, []byte("Third local.")) {
		t.Fatalf("merged proposal = %#v", merged)
	}
}

func TestBuildPortableProposalPreservesInlineMarkdown(t *testing.T) {
	requirePandoc(t)
	base := []byte("**Outcome:** Move `main` using [Guide](guide.md) now.\n")
	snapshot, sourceMap := sealedTestSnapshot(t, base)
	review := []byte("Outcome: Move main using Guide later.\n")
	blocks, err := ParseBaseline(context.Background(), PandocParser{}, "gfm", review)
	if err != nil {
		t.Fatal(err)
	}
	classification, err := ClassifyPortableReviewContext(context.Background(), sourceMap, blocks)
	if err != nil {
		t.Fatal(err)
	}
	result, err := BuildProposals(snapshot, classification, map[string][]byte{"docs/a.md": base})
	if err != nil {
		t.Fatal(err)
	}
	want := []byte("**Outcome:** Move `main` using [Guide](guide.md) later.\n")
	if len(result.Conflicts) != 0 || len(result.Files) != 1 || !bytes.Equal(result.Files[0].Proposed, want) {
		t.Fatalf("portable proposal = %#v, bytes=%q, want=%q", result, result.Files[0].Proposed, want)
	}
}

func TestBuildProposalsReportsOverlapAndPreservesBytesOutsideRange(t *testing.T) {
	requirePandoc(t)
	base := []byte("# Title\r\n\r\nFirst.\r\n\r\nSecond.\r\n")
	snapshot, sourceMap := sealedTestSnapshot(t, base)
	review := []byte("# Title\n\nFirst remote.\n\nSecond.\n")
	blocks, _ := ParseBaseline(context.Background(), PandocParser{}, "gfm", review)
	classification, _ := ClassifyReview(sourceMap, blocks)
	local := bytes.Replace(base, []byte("First."), []byte("First local."), 1)
	result, err := BuildProposals(snapshot, classification, map[string][]byte{"docs/a.md": local})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Conflicts) != 1 || result.Conflicts[0].Reason != "local and remote changes overlap" {
		t.Fatalf("result = %#v", result)
	}
	clean, err := BuildProposals(snapshot, classification, map[string][]byte{"docs/a.md": base})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(clean.Files[0].Proposed, []byte("First remote.\r\n\r\nSecond.\r\n")) {
		t.Fatalf("CRLF proposal = %q", clean.Files[0].Proposed)
	}
}

func TestBuildProposalsInsertionAndDeletion(t *testing.T) {
	requirePandoc(t)
	base := []byte("# Title\n\nFirst.\n\nSecond.\n\nThird.\n")
	snapshot, sourceMap := sealedTestSnapshot(t, base)
	for _, test := range []struct {
		name   string
		review []byte
		want   []byte
		gone   []byte
	}{
		{name: "insert", review: []byte("# Title\n\nFirst.\n\nInserted.\n\nSecond.\n\nThird.\n"), want: []byte("First.\n\nInserted.\n\nSecond.")},
		{name: "delete", review: []byte("# Title\n\nFirst.\n\nThird.\n"), want: []byte("First.\n\nThird."), gone: []byte("Second.")},
	} {
		t.Run(test.name, func(t *testing.T) {
			blocks, _ := ParseBaseline(context.Background(), PandocParser{}, "gfm", test.review)
			classification, _ := ClassifyReview(sourceMap, blocks)
			result, err := BuildProposals(snapshot, classification, map[string][]byte{"docs/a.md": base})
			if err != nil {
				t.Fatal(err)
			}
			if len(result.Conflicts) != 0 || !bytes.Contains(result.Files[0].Proposed, test.want) || len(test.gone) > 0 && bytes.Contains(result.Files[0].Proposed, test.gone) {
				t.Fatalf("proposal = %#v, bytes=%q", result, result.Files[0].Proposed)
			}
		})
	}
}

func TestBuildProposalsDeletionPreservesUnsupportedNeighbor(t *testing.T) {
	requirePandoc(t)
	base := []byte("# Title\n\nFirst.\n\n---\n\nSecond.\n")
	snapshot, sourceMap := sealedTestSnapshot(t, base)
	review := []byte("# Title\n\n---\n\nSecond.\n")
	blocks, err := ParseBaseline(context.Background(), PandocParser{}, "gfm", review)
	if err != nil {
		t.Fatal(err)
	}
	classification, err := ClassifyReview(sourceMap, blocks)
	if err != nil {
		t.Fatal(err)
	}
	result, err := BuildProposals(snapshot, classification, map[string][]byte{"docs/a.md": base})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Conflicts) != 0 || len(result.Files) != 1 {
		t.Fatalf("proposal = %#v", result)
	}
	want := []byte("# Title\n\n---\n\nSecond.\n")
	if !bytes.Equal(result.Files[0].Proposed, want) {
		t.Fatalf("proposal = %q, want %q", result.Files[0].Proposed, want)
	}
}

func TestBuildProposalsDeletionPreservesFollowingIndentation(t *testing.T) {
	requirePandoc(t)
	base := []byte("# Title\n\nDelete me.\n\n    indented code\n\nAfter.\n")
	snapshot, sourceMap := sealedTestSnapshot(t, base)
	review := []byte("# Title\n\n    indented code\n\nAfter.\n")
	blocks, err := ParseBaseline(context.Background(), PandocParser{}, "gfm", review)
	if err != nil {
		t.Fatal(err)
	}
	classification, err := ClassifyReview(sourceMap, blocks)
	if err != nil {
		t.Fatal(err)
	}
	result, err := BuildProposals(snapshot, classification, map[string][]byte{"docs/a.md": base})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Conflicts) != 0 || len(result.Files) != 1 || !bytes.Contains(result.Files[0].Proposed, []byte("    indented code")) {
		t.Fatalf("indented proposal = %#v, bytes=%q", result, result.Files[0].Proposed)
	}
}

func TestBuildProposalsRendersOrderedListInsertion(t *testing.T) {
	requirePandoc(t)
	base := []byte("Before.\n\n1. First\n2. Third\n\nAfter.\n")
	snapshot, sourceMap := sealedTestSnapshot(t, base)
	review := []byte("Before.\n\n1. First\n2. Inserted\n3. Third\n\nAfter.\n")
	blocks, err := ParseBaseline(context.Background(), PandocParser{}, "gfm", review)
	if err != nil {
		t.Fatal(err)
	}
	classification, err := ClassifyReview(sourceMap, blocks)
	if err != nil {
		t.Fatal(err)
	}
	result, err := BuildProposals(snapshot, classification, map[string][]byte{"docs/a.md": base})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Conflicts) != 0 || len(result.Files) != 1 || !bytes.Contains(result.Files[0].Proposed, []byte("1. Inserted")) {
		t.Fatalf("ordered insertion proposal = %#v, bytes=%q", result, result.Files[0].Proposed)
	}
}

func TestBuildProposalsInsertionUsesProtectedAnchor(t *testing.T) {
	requirePandoc(t)
	base := []byte("# Title\n\n---\n\nAfter.\n")
	snapshot, sourceMap := sealedTestSnapshot(t, base)
	review := []byte("# Title\n\nInserted.\n\n---\n\nAfter.\n")
	blocks, err := ParseBaseline(context.Background(), PandocParser{}, "gfm", review)
	if err != nil {
		t.Fatal(err)
	}
	classification, err := ClassifyReview(sourceMap, blocks)
	if err != nil {
		t.Fatal(err)
	}
	result, err := BuildProposals(snapshot, classification, map[string][]byte{"docs/a.md": base})
	if err != nil {
		t.Fatal(err)
	}
	want := []byte("# Title\n\nInserted.\n\n---\n\nAfter.\n")
	if len(result.Conflicts) != 0 || len(result.Files) != 1 || !bytes.Equal(result.Files[0].Proposed, want) {
		t.Fatalf("protected anchor proposal = %#v, bytes=%q", result, result.Files[0].Proposed)
	}
}

func TestBuildProposalsRejectsBinaryAndInvalidUTF8(t *testing.T) {
	snapshot := LoadedSnapshot{Manifest: SnapshotManifest{Status: SnapshotSealed, Members: []SnapshotMember{{SourceKey: "docs/a.md"}}}, Sources: map[string][]byte{"docs/a.md": []byte("base")}}
	for _, content := range [][]byte{{'a', 0, 'b'}, {0xff}} {
		if _, err := BuildProposals(snapshot, Classification{}, map[string][]byte{"docs/a.md": content}); err == nil {
			t.Fatalf("accepted unsafe content %v", content)
		}
	}
}

func sealedTestSnapshot(t *testing.T, source []byte) (LoadedSnapshot, SourceMap) {
	t.Helper()
	sourceMap, err := BuildSourceMap(context.Background(), PandocParser{}, "gfm", []MemberInput{{SourceKey: "docs/a.md", Content: source}}, source)
	if err != nil {
		t.Fatal(err)
	}
	store, _ := NewSnapshotStore(filepath.Join(t.TempDir(), "state.json"))
	input := CandidateInput{
		WorkspaceID: "workspace", Profile: "work", TargetKey: "publication:report", PublicationID: "report", PublicationKind: "source", Reader: "gfm",
		FileID: "file", Generation: 1, ReviewSetID: "set", OperationID: "operation",
		Capture:  CaptureTuple{FileID: "file", DriveVersion: "1", DocsRevision: "r1", Tabs: []googleapi.TabTopology{{ID: "tab", Title: "Tab", Index: 0}}},
		Baseline: source, Sources: []SnapshotSource{{SourceKey: "docs/a.md", SourceHash: document.SourceHash(source), Content: source}}, SourceMap: sourceMap,
		CapturedAt: time.Unix(1, 0).UTC(),
	}
	if _, err := store.WriteCandidate(input); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Seal(input.TargetKey, 1, ActivationSeal{FileID: "file", DriveVersion: "2", DocsRevision: "r1", ReviewParent: "review", ReadyMetadata: map[string]string{"mdoc_status": "ready"}, Tabs: input.Capture.Tabs, ActivatedAt: time.Unix(2, 0).UTC()}); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load(input.TargetKey, 1, true)
	if err != nil {
		t.Fatal(err)
	}
	return loaded, sourceMap
}
