package document

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/goliatone/mdoc/internal/config"
)

func TestSelectProfileSourcesRecursesExcludesAndDeduplicates(t *testing.T) {
	root := t.TempDir()
	writeSourceFile(t, filepath.Join(root, "README.md"))
	writeSourceFile(t, filepath.Join(root, "docs", "one.md"))
	writeSourceFile(t, filepath.Join(root, "docs", "nested", "two.MD"))
	writeSourceFile(t, filepath.Join(root, "docs", "drafts", "skip.md"))
	writeSourceFile(t, filepath.Join(root, "docs", "not-markdown.txt"))
	profile := config.SelectedProfile{Name: "review", Profile: config.Profile{
		Sources:  []string{filepath.Join(root, "README.md"), filepath.Join(root, "docs"), filepath.Join(root, "docs", "**", "*.md")},
		Excludes: []string{filepath.Join(root, "docs", "drafts", "**")},
	}}
	selected, err := selectProfileSources(root, profile)
	if err != nil {
		t.Fatal(err)
	}
	keys := make([]string, 0, len(selected))
	for _, source := range selected {
		keys = append(keys, source.Key)
	}
	if got := strings.Join(keys, ","); got != "README.md,docs/nested/two.MD,docs/one.md" {
		t.Fatalf("selected keys = %q", got)
	}
}

func TestSelectProfileSourcesRejectsSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	external := t.TempDir()
	target := filepath.Join(external, "outside.md")
	writeSourceFile(t, target)
	link := filepath.Join(root, "outside.md")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	_, err := selectProfileSources(root, config.SelectedProfile{Profile: config.Profile{Sources: []string{link}}})
	if err == nil || !strings.Contains(err.Error(), "outside the project root") {
		t.Fatalf("symlink escape error = %v", err)
	}
}

func TestSelectProfileSourcesAllowsExplicitExternalRoot(t *testing.T) {
	root := t.TempDir()
	external := t.TempDir()
	path := filepath.Join(external, "notes", "outside.md")
	writeSourceFile(t, path)
	selected, err := selectProfileSources(root, config.SelectedProfile{Profile: config.Profile{Sources: []string{external}, ExternalRoots: []string{external}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(selected) != 1 || selected[0].Key != "external/1/notes/outside.md" {
		t.Fatalf("external selection = %#v", selected)
	}
}

func TestResolveSelectedSourceKeyAcceptsExternalPathAndSyntheticKey(t *testing.T) {
	root := t.TempDir()
	external := t.TempDir()
	path := filepath.Join(external, "notes", "outside.md")
	writeSourceFile(t, path)
	sources, err := SelectSources(root, config.SelectedProfile{Profile: config.Profile{Sources: []string{external}, ExternalRoots: []string{external}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{path, "external/1/notes/outside.md"} {
		key, err := ResolveSelectedSourceKey(root, sources, value)
		if err != nil {
			t.Fatalf("resolve %q: %v", value, err)
		}
		if key != "external/1/notes/outside.md" {
			t.Fatalf("resolve %q = %q", value, key)
		}
	}
	if _, err := ResolveSelectedSourceKey(root, sources, filepath.Join(t.TempDir(), "outside.md")); err == nil {
		t.Fatal("unselected external path was accepted")
	}
}

func TestResolveEntrySourceKeyInfersFromDeduplicatedSelection(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "docs", "only.md")
	writeSourceFile(t, path)
	sources, err := SelectSources(root, config.SelectedProfile{Profile: config.Profile{
		Sources: []string{path, filepath.Join(root, "docs"), filepath.Join(root, "docs", "*.md")},
	}})
	if err != nil {
		t.Fatal(err)
	}
	key, err := ResolveEntrySourceKey(root, sources, "")
	if err != nil {
		t.Fatal(err)
	}
	if key != "docs/only.md" {
		t.Fatalf("entry key = %q", key)
	}

	writeSourceFile(t, filepath.Join(root, "docs", "second.md"))
	sources, err = SelectSources(root, config.SelectedProfile{Profile: config.Profile{Sources: []string{filepath.Join(root, "docs")}}})
	if err != nil {
		t.Fatal(err)
	}
	key, err = ResolveEntrySourceKey(root, sources, "")
	if err != nil {
		t.Fatal(err)
	}
	if key != "" {
		t.Fatalf("multi-source entry key = %q", key)
	}
}

func TestApprovedExternalSourceCanUseLocalLinksAndAssets(t *testing.T) {
	root := t.TempDir()
	external := t.TempDir()
	sourcePath := filepath.Join(external, "source.md")
	targetPath := filepath.Join(external, "target.md")
	assetPath := filepath.Join(external, "diagram.png")
	for _, path := range []string{sourcePath, targetPath, assetPath} {
		writeSourceFile(t, path)
	}
	source := &Document{SourceKey: "external/1/source.md", Path: sourcePath}
	target := &Document{SourceKey: "external/1/target.md", Path: targetPath}
	graph := &Graph{
		Root: root, AllowedRoots: []string{root, external},
		ByKey:    map[string]*Document{source.SourceKey: source, target.SourceKey: target},
		PathKeys: map[string]string{sourcePath: source.SourceKey, targetPath: target.SourceKey},
		Policy:   Policy{UnpublishedMarkdownLinks: "error", ImageTypes: map[string]bool{"png": true}},
	}
	validateLink(graph, source, Link{Target: "target.md"})
	validateLink(graph, source, Link{Target: "diagram.png", Image: true})
	if len(graph.Issues) != 0 {
		t.Fatalf("approved external links produced issues: %#v", graph.Issues)
	}
	outside := filepath.Join(t.TempDir(), "outside.md")
	writeSourceFile(t, outside)
	validateLink(graph, source, Link{Target: outside})
	if !hasIssueCode(graph.Issues, "unsafe_path") {
		t.Fatalf("outside link was not rejected: %#v", graph.Issues)
	}
}

func TestContainingRootKeepsMissingNestedTargetInsideWorkspace(t *testing.T) {
	root := t.TempDir()
	missing := filepath.Join(root, "missing", "nested", "target.md")
	if index := containingRoot([]string{root}, missing); index != 0 {
		t.Fatalf("missing nested target root index = %d", index)
	}
}

func writeSourceFile(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("# Title\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}
