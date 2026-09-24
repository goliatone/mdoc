package reviewsync

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildPatchIsDeterministicAndApplies(t *testing.T) {
	current := []byte("one\ntwo\nthree\n")
	proposed := []byte("one\nchanged\nthree\n")
	files := []FileProposal{{SourceKey: "docs/a.md", Current: current, Proposed: proposed, Changed: true}}
	first, err := BuildPatch(files)
	if err != nil {
		t.Fatal(err)
	}
	second, _ := BuildPatch(files)
	if !bytes.Equal(first, second) || !bytes.Contains(first, []byte("--- a/docs/a.md\n+++ b/docs/a.md\n")) {
		t.Fatalf("patch = %q", first)
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	root := t.TempDir()
	path := filepath.Join(root, "docs", "a.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, current, 0o600); err != nil {
		t.Fatal(err)
	}
	patchPath := filepath.Join(root, "change.patch")
	if err := os.WriteFile(patchPath, first, 0o600); err != nil {
		t.Fatal(err)
	}
	command := exec.Command("git", "apply", "--whitespace=nowarn", patchPath)
	command.Dir = root
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git apply: %v: %s\n%s", err, output, first)
	}
	got, _ := os.ReadFile(path)
	if !bytes.Equal(got, proposed) {
		t.Fatalf("applied = %q, want %q", got, proposed)
	}
}

func TestBuildPatchContextBoundsPathologicalLineDiff(t *testing.T) {
	before := bytes.Repeat([]byte("before\n"), 2000)
	after := bytes.Repeat([]byte("after\n"), 2000)
	_, err := BuildPatchContext(context.Background(), []FileProposal{{SourceKey: "docs/a.md", Current: before, Proposed: after, Changed: true}})
	if !errors.Is(err, ErrComparisonComplexity) {
		t.Fatalf("patch complexity error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = BuildPatchContext(ctx, []FileProposal{{SourceKey: "docs/a.md", Current: []byte("before\n"), Proposed: []byte("after\n"), Changed: true}})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("patch cancellation error = %v", err)
	}
}

func TestBuildPatchPreservesCRLFAndMarksMissingFinalNewline(t *testing.T) {
	patch, err := BuildPatch([]FileProposal{{SourceKey: "docs/a.md", Current: []byte("one\r\ntwo"), Proposed: []byte("one\r\nchanged"), Changed: true}})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(patch, []byte(" one\r\n")) || bytes.Count(patch, []byte("\\ No newline at end of file\n")) != 2 {
		t.Fatalf("patch = %q", patch)
	}
}

func TestBuildPatchQuotesPathsAndRejectsUnsafeInputs(t *testing.T) {
	patch, err := BuildPatch([]FileProposal{{SourceKey: "docs/a\tb.md", Current: []byte("a\n"), Proposed: []byte("b\n"), Changed: true}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(patch), `--- "a/docs/a\tb.md"`) {
		t.Fatalf("quoted patch = %q", patch)
	}
	for _, file := range []FileProposal{
		{SourceKey: "../outside.md", Current: []byte("a"), Proposed: []byte("b"), Changed: true},
		{SourceKey: "docs/a.md", Current: []byte{'a', 0}, Proposed: []byte("b"), Changed: true},
		{SourceKey: "docs/a.md", Current: []byte("a"), Proposed: []byte{0xff}, Changed: true},
	} {
		if _, err := BuildPatch([]FileProposal{file}); err == nil {
			t.Fatalf("accepted unsafe file %#v", file)
		}
	}
}
