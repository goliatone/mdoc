package document

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/goliatone/mdoc/internal/config"
)

type fakeRunner struct{ ast map[string][]byte }

func (f fakeRunner) PandocJSON(_ context.Context, path string) ([]byte, error) {
	if value := f.ast[filepath.Base(path)]; value != nil {
		return value, nil
	}
	return nil, fmt.Errorf("missing AST for %s", path)
}

func TestParsePandocJSONCollectsStructureProblems(t *testing.T) {
	ast := []byte(`{"pandoc-api-version":[1,23,1],"meta":{},"blocks":[
		{"t":"Header","c":[1,["title",[],[]],[{"t":"Str","c":"Title"}]]},
		{"t":"Header","c":[3,["jump",[],[]],[{"t":"Str","c":"Jump"}]]},
		{"t":"RawBlock","c":["html","<div>x</div>"]},
		{"t":"Table","c":[["",[],[]],[null,[]],[[{"t":"AlignDefault"},{"t":"ColWidthDefault"}],[{"t":"AlignDefault"},{"t":"ColWidthDefault"}],[{"t":"AlignDefault"},{"t":"ColWidthDefault"}]],null,[],null]}
	]}`)
	document, issues, err := ParsePandocJSON("docs/a.md", "/tmp/a.md", []byte("# Title\n<OWNER>"), ast, 2)
	if err != nil {
		t.Fatal(err)
	}
	if document.Title != "Title" || len(document.Headings) != 2 || document.TableColumns[0] != 3 {
		t.Fatalf("document = %#v", document)
	}
	for _, code := range []string{"heading_jump", "raw_node", "wide_table"} {
		if !hasIssue(issues, code) {
			t.Fatalf("missing issue %s: %#v", code, issues)
		}
	}
	if hasIssue(issues, "angle_placeholder") {
		t.Fatalf("literal angle text must not be classified without a Pandoc raw node: %#v", issues)
	}
}

func TestDiscoverValidatesLinksAssetsTitlesAndEntry(t *testing.T) {
	root := t.TempDir()
	docs := filepath.Join(root, "docs")
	if err := os.MkdirAll(docs, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"a.md": "# Same\n[broken](missing.md)\n![missing](missing.png)\n",
		"b.md": "# Same\n[unsafe](../../outside.md)\n",
	} {
		if err := os.WriteFile(filepath.Join(docs, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	graph, err := Discover(context.Background(), root, config.SelectedProfile{Name: "work", Profile: config.Profile{
		Sources: []string{filepath.Join(docs, "*.md")}, Entry: filepath.Join(docs, "a.md"), MaxTableColumns: 5,
	}}, fakeRunner{ast: map[string][]byte{
		"a.md": astWithLinks("Same", []string{"missing.md"}, []string{"missing.png"}),
		"b.md": astWithLinks("Same", []string{"../../outside.md"}, nil),
	}})
	if err != nil {
		t.Fatal(err)
	}
	for _, code := range []string{"broken_link", "missing_asset", "unsafe_path", "duplicate_title"} {
		if !hasIssue(graph.Issues, code) {
			t.Fatalf("missing issue %s: %#v", code, graph.Issues)
		}
	}
	if graph.EntryKey != "docs/a.md" {
		t.Fatalf("entry = %q", graph.EntryKey)
	}
}

func TestDiscoverAcceptsSyntheticExternalEntryKey(t *testing.T) {
	root := t.TempDir()
	external := t.TempDir()
	path := filepath.Join(external, "entry.md")
	if err := os.WriteFile(path, []byte("# External\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	graph, err := Discover(context.Background(), root, config.SelectedProfile{Name: "work", Profile: config.Profile{
		Sources:       []string{path},
		ExternalRoots: []string{external},
		Entry:         "external/1/entry.md",
	}}, fakeRunner{ast: map[string][]byte{"entry.md": basicAST("External")}})
	if err != nil {
		t.Fatal(err)
	}
	if graph.EntryKey != "external/1/entry.md" || graph.HasErrors() {
		t.Fatalf("external entry graph = %#v issues=%#v", graph, graph.Issues)
	}
}

func TestSafeSourceKeyRejectsSymlinkEscape(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink permissions vary on Windows")
	}
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.md")
	if err := os.WriteFile(outside, []byte("# Outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link.md")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	if _, issue := safeSourceKey(root, link); issue == nil || issue.Code != "symlink_escape" {
		t.Fatalf("issue = %#v", issue)
	}
}

func TestCaseMatchesUsesExactDiskCase(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "Exact.md")
	if err := os.WriteFile(path, []byte("# Exact"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !caseMatches(root, path) {
		t.Fatal("exact path did not match")
	}
	if caseMatches(root, filepath.Join(root, "exact.md")) {
		t.Fatal("case changed path matched")
	}
}

func TestMomentumProfileDiscoversFourteenDocuments(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", "..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	runtimeConfig, err := config.Resolve(context.Background(), config.ResolverOptions{ProjectPath: filepath.Join(root, ".mdoc.yaml"), GlobalPath: filepath.Join(t.TempDir(), "missing.yaml"), Environment: map[string]string{}})
	if err != nil {
		t.Fatal(err)
	}
	cfg, profile := runtimeConfig.Legacy()
	graph, err := Discover(context.Background(), cfg.Root, profile, CommandRunner{})
	if err != nil {
		t.Fatal(err)
	}
	if len(graph.Documents) != 14 {
		t.Fatalf("documents = %d", len(graph.Documents))
	}
	if graph.EntryKey != "" || profile.EntryPublication != "engagement-report" {
		t.Fatalf("document entry = %q publication entry = %q", graph.EntryKey, profile.EntryPublication)
	}
}

func basicAST(title string) []byte {
	title = strings.ReplaceAll(title, `"`, `\"`)
	return []byte(`{"pandoc-api-version":[1,23,1],"meta":{},"blocks":[{"t":"Header","c":[1,["title",[],[]],[{"t":"Str","c":"` + title + `"}]]}]}`)
}

func astWithLinks(title string, links, images []string) []byte {
	blocks := []string{string(basicAST(title))}
	nodes := []string{}
	for _, target := range links {
		nodes = append(nodes, fmt.Sprintf(`{"t":"Link","c":[["",[],[]],[{"t":"Str","c":"link"}],[%q,""]]}`, target))
	}
	for _, target := range images {
		nodes = append(nodes, fmt.Sprintf(`{"t":"Image","c":[["",[],[]],[{"t":"Str","c":"image"}],[%q,""]]}`, target))
	}
	header := strings.TrimSuffix(strings.TrimPrefix(blocks[0], `{"pandoc-api-version":[1,23,1],"meta":{},"blocks":[`), `]}`)
	return []byte(`{"pandoc-api-version":[1,23,1],"meta":{},"blocks":[` + header + `,{"t":"Para","c":[` + strings.Join(nodes, ",") + `]}]}`)
}

func hasIssue(issues []Issue, code string) bool {
	for _, issue := range issues {
		if issue.Code == code {
			return true
		}
	}
	return false
}
