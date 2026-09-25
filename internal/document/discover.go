package document

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/goliatone/mdoc/internal/config"
	"github.com/goliatone/mdoc/internal/sourcekey"
)

type Runner interface {
	PandocJSON(context.Context, string) ([]byte, error)
}

type CommandRunner struct {
	Binary string
	Reader string
}

func (r CommandRunner) PandocJSON(ctx context.Context, path string) ([]byte, error) {
	binary := r.Binary
	if binary == "" {
		binary = "pandoc"
	}
	reader := r.Reader
	if reader == "" {
		reader = "gfm"
	}
	command := exec.CommandContext(ctx, binary, "--from="+reader, "--to=json", path)
	output, err := command.Output()
	if err != nil {
		if exit, ok := errors.AsType[*exec.ExitError](err); ok {
			return nil, fmt.Errorf("pandoc failed for %s: %s", path, strings.TrimSpace(string(exit.Stderr)))
		}
		return nil, fmt.Errorf("run pandoc for %s: %w", path, err)
	}
	return output, nil
}

func Discover(ctx context.Context, root string, profile config.SelectedProfile, runner Runner) (*Graph, error) {
	if runner == nil {
		runner = CommandRunner{}
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	selected, err := selectProfileSources(root, profile)
	if err != nil {
		return nil, err
	}
	policy := policyFromProfile(profile)
	allowedRoots := []string{root}
	allowedRoots = append(allowedRoots, profile.ExternalRoots...)
	graph := &Graph{Root: root, ByKey: map[string]*Document{}, Issues: []Issue{}, Policy: policy, AllowedRoots: allowedRoots, PathKeys: map[string]string{}}
	keyPaths := map[string]string{}
	if len(selected) == 0 {
		graph.Issues = append(graph.Issues, Issue{Severity: SeverityError, Code: "no_sources", Message: "source patterns matched no files"})
		return graph, nil
	}
	selectedRefs := make([]SourceRef, 0, len(selected))
	for _, source := range selected {
		selectedRefs = append(selectedRefs, SourceRef(source))
	}
	for _, selectedSource := range selected {
		path := selectedSource.Path
		key := selectedSource.Key
		if priorPath, exists := keyPaths[key]; exists {
			graph.Issues = append(graph.Issues, Issue{
				Severity: SeverityError,
				Code:     "source_key_collision",
				Source:   key,
				Message:  fmt.Sprintf("source paths %q and %q have the same canonical key", priorPath, path),
			})
			continue
		}
		keyPaths[key] = path
		graph.PathKeys[filepath.Clean(path)] = key
		if real, realErr := filepath.EvalSymlinks(path); realErr == nil {
			graph.PathKeys[filepath.Clean(real)] = key
		}
		source, err := os.ReadFile(path)
		if err != nil {
			graph.Issues = append(graph.Issues, Issue{Severity: SeverityError, Code: "source_read", Source: key, Message: err.Error()})
			continue
		}
		ast, err := runner.PandocJSON(ctx, path)
		if err != nil {
			graph.Issues = append(graph.Issues, Issue{Severity: SeverityError, Code: "pandoc", Source: key, Message: err.Error()})
			continue
		}
		document, issues, err := ParsePandocJSONWithPolicy(key, path, source, ast, policy)
		if err != nil {
			graph.Issues = append(graph.Issues, Issue{Severity: SeverityError, Code: "pandoc_json", Source: key, Message: err.Error()})
			continue
		}
		graph.Documents = append(graph.Documents, document)
		graph.ByKey[key] = document
		graph.Issues = append(graph.Issues, issues...)
	}
	entryKey, entryErr := ResolveEntrySourceKey(root, selectedRefs, profile.Entry)
	if entryErr != nil {
		graph.Issues = append(graph.Issues, Issue{Severity: SeverityError, Code: "entry_invalid", Message: entryErr.Error()})
	} else {
		graph.EntryKey = entryKey
	}
	if graph.EntryKey != "" && graph.ByKey[graph.EntryKey] == nil {
		graph.Issues = append(graph.Issues, Issue{Severity: SeverityError, Code: "entry_missing", Source: graph.EntryKey, Message: "configured entry is not in the source set"})
	}
	validateGraph(graph)
	graph.SortIssues()
	return graph, nil
}

func policyFromProfile(profile config.SelectedProfile) Policy {
	titlePolicy := profile.TitlePolicy
	if titlePolicy == "" {
		titlePolicy = "required"
	}
	headingJumps := profile.HeadingJumps
	if headingJumps == "" {
		headingJumps = "error"
	}
	linkPolicy := profile.LinkPolicy
	if linkPolicy == "" {
		linkPolicy = "error"
	}
	namingSource := profile.NamingSource
	if namingSource == "" {
		namingSource = "h1"
	}
	imageTypes := map[string]bool{}
	for _, value := range profile.ImageTypes {
		imageTypes[strings.TrimPrefix(strings.ToLower(strings.TrimSpace(value)), ".")] = true
	}
	if len(imageTypes) == 0 {
		for _, value := range []string{"png", "jpg", "jpeg", "svg"} {
			imageTypes[value] = true
		}
	}
	return Policy{
		Title: titlePolicy, HeadingJumps: headingJumps,
		UnpublishedMarkdownLinks: linkPolicy, NamingSource: namingSource,
		NameMapping: profile.NameMapping, ImageTypes: imageTypes, MaxTableColumns: profile.MaxTableColumns,
	}
}

func validateGraph(graph *Graph) {
	titles := map[string][]string{}
	for _, document := range graph.Documents {
		if document.Title != "" {
			titles[document.Title] = append(titles[document.Title], document.SourceKey)
		}
		for _, link := range document.Links {
			validateLink(graph, document, link)
		}
	}
	for title, sources := range titles {
		if len(sources) > 1 {
			sort.Strings(sources)
			for _, source := range sources {
				graph.Issues = append(graph.Issues, Issue{Severity: SeverityWarning, Code: "duplicate_title", Source: source, Message: fmt.Sprintf("title %q is also used by %s", title, strings.Join(sources, ", "))})
			}
		}
	}
}

func validateLink(graph *Graph, document *Document, link Link) {
	target := strings.TrimSpace(link.Target)
	parsed, err := url.Parse(target)
	if err != nil {
		graph.Issues = append(graph.Issues, Issue{Severity: SeverityError, Code: "invalid_link", Source: document.SourceKey, Message: fmt.Sprintf("invalid link %q", target)})
		return
	}
	if link.Image && (parsed.Scheme != "" || strings.HasPrefix(target, "//")) {
		graph.Issues = append(graph.Issues, Issue{Severity: SeverityError, Code: "unsafe_asset_source", Source: document.SourceKey, Message: fmt.Sprintf("image %q must use an approved local path", target)})
		return
	}
	if parsed.Scheme != "" || strings.HasPrefix(target, "//") {
		return
	}
	decodedPath, err := url.PathUnescape(parsed.Path)
	if err != nil {
		decodedPath = parsed.Path
	}
	if decodedPath == "" {
		if parsed.Fragment != "" && !hasHeading(document, parsed.Fragment) {
			graph.Issues = append(graph.Issues, Issue{Severity: SeverityWarning, Code: "missing_fragment", Source: document.SourceKey, Message: fmt.Sprintf("heading fragment %q was not found", parsed.Fragment)})
		}
		return
	}
	if filepath.IsAbs(decodedPath) {
		graph.Issues = append(graph.Issues, Issue{Severity: SeverityError, Code: "unsafe_path", Source: document.SourceKey, Message: fmt.Sprintf("absolute local target %q is not allowed", target)})
		return
	}
	resolved := filepath.Clean(filepath.Join(filepath.Dir(document.Path), filepath.FromSlash(decodedPath)))
	allowedRoots := graph.approvedRoots()
	rootIndex := containingRoot(allowedRoots, resolved)
	if rootIndex < 0 {
		graph.Issues = append(graph.Issues, Issue{Severity: SeverityError, Code: "unsafe_path", Source: document.SourceKey, Message: fmt.Sprintf("target %q escapes the workspace", target)})
		return
	}
	info, err := os.Stat(resolved)
	if err != nil || info.IsDir() {
		code := "broken_link"
		if link.Image {
			code = "missing_asset"
		}
		graph.Issues = append(graph.Issues, Issue{Severity: SeverityError, Code: code, Source: document.SourceKey, Message: fmt.Sprintf("target %q does not exist", target)})
		return
	}
	real, err := filepath.EvalSymlinks(resolved)
	if err != nil || containingRoot(allowedRoots, real) < 0 {
		graph.Issues = append(graph.Issues, Issue{Severity: SeverityError, Code: "symlink_escape", Source: document.SourceKey, Message: fmt.Sprintf("target %q resolves outside the workspace", target)})
		return
	}
	if !caseMatches(allowedRoots[rootIndex], resolved) {
		graph.Issues = append(graph.Issues, Issue{Severity: SeverityError, Code: "case_mismatch", Source: document.SourceKey, Message: fmt.Sprintf("target %q does not match on disk casing", target)})
	}
	if link.Image {
		extension := strings.TrimPrefix(strings.ToLower(filepath.Ext(resolved)), ".")
		if !graph.Policy.ImageTypes[extension] {
			graph.Issues = append(graph.Issues, Issue{Severity: SeverityError, Code: "unsupported_asset", Source: document.SourceKey, Message: fmt.Sprintf("image %q has unsupported type", target)})
		}
		return
	}
	key := graph.keyForPath(resolved)
	if strings.EqualFold(filepath.Ext(resolved), ".md") {
		targetDocument := graph.ByKey[key]
		if targetDocument == nil {
			if graph.Policy.UnpublishedMarkdownLinks != "preserve" {
				severity := SeverityError
				if graph.Policy.UnpublishedMarkdownLinks == "warning" {
					severity = SeverityWarning
				}
				graph.Issues = append(graph.Issues, Issue{Severity: severity, Code: "unpublished_link", Source: document.SourceKey, Message: fmt.Sprintf("Markdown target %q is outside the publish set", target)})
			}
		} else if parsed.Fragment != "" && !hasHeading(targetDocument, parsed.Fragment) {
			graph.Issues = append(graph.Issues, Issue{Severity: SeverityWarning, Code: "missing_fragment", Source: document.SourceKey, Message: fmt.Sprintf("heading fragment %q was not found in %s", parsed.Fragment, key)})
		}
	}
}

func safeSourceKey(root, path string) (string, *Issue) {
	abs, err := filepath.Abs(path)
	if err != nil {
		issue := Issue{Severity: SeverityError, Code: "unsafe_path", Message: err.Error()}
		return "", &issue
	}
	if !within(root, abs) {
		issue := Issue{Severity: SeverityError, Code: "unsafe_path", Message: fmt.Sprintf("path %q escapes the workspace", path)}
		return "", &issue
	}
	real, err := filepath.EvalSymlinks(abs)
	realRoot, rootErr := filepath.EvalSymlinks(root)
	if err == nil && rootErr == nil && !within(realRoot, real) {
		issue := Issue{Severity: SeverityError, Code: "symlink_escape", Message: fmt.Sprintf("path %q resolves outside the workspace", path)}
		return "", &issue
	}
	relative, err := filepath.Rel(root, abs)
	if err != nil {
		issue := Issue{Severity: SeverityError, Code: "unsafe_path", Message: err.Error()}
		return "", &issue
	}
	key, err := sourcekey.Normalize(relative)
	if err != nil {
		issue := Issue{Severity: SeverityError, Code: "unsafe_path", Message: err.Error()}
		return "", &issue
	}
	return key, nil
}

func within(root, path string) bool {
	relative, err := filepath.Rel(root, path)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func caseMatches(root, path string) bool {
	relative, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	current := root
	for part := range strings.SplitSeq(relative, string(filepath.Separator)) {
		entries, err := os.ReadDir(current)
		if err != nil {
			return false
		}
		found := false
		for _, entry := range entries {
			if entry.Name() == part {
				found = true
				break
			}
		}
		if !found {
			return false
		}
		current = filepath.Join(current, part)
	}
	return true
}

func hasHeading(document *Document, fragment string) bool {
	fragment, _ = url.PathUnescape(fragment)
	for _, heading := range document.Headings {
		if heading.ID == fragment {
			return true
		}
	}
	return false
}

func (g *Graph) ResolveTarget(source *Document, target string) (sourceKey, fragment string, local bool) {
	parsed, err := url.Parse(strings.TrimSpace(target))
	if err != nil || parsed.Scheme != "" || strings.HasPrefix(target, "//") || parsed.Path == "" {
		return "", parsed.Fragment, false
	}
	decoded, err := url.PathUnescape(parsed.Path)
	if err != nil {
		decoded = parsed.Path
	}
	resolved := filepath.Clean(filepath.Join(filepath.Dir(source.Path), filepath.FromSlash(decoded)))
	key := g.keyForPath(resolved)
	if key == "" || g.ByKey[key] == nil {
		return "", parsed.Fragment, false
	}
	return key, parsed.Fragment, true
}

func (g *Graph) keyForPath(path string) string {
	clean := filepath.Clean(path)
	if key := g.PathKeys[clean]; key != "" {
		return key
	}
	real, err := filepath.EvalSymlinks(clean)
	if err == nil {
		return g.PathKeys[filepath.Clean(real)]
	}
	return ""
}

func (g *Graph) approvedRoots() []string {
	if len(g.AllowedRoots) == 0 {
		return []string{g.Root}
	}
	return g.AllowedRoots
}

func (g *Graph) AssetPaths(source *Document) []string {
	result := []string{}
	seen := map[string]bool{}
	for _, link := range source.Links {
		if !link.Image {
			continue
		}
		parsed, err := url.Parse(link.Target)
		if err != nil || parsed.Scheme != "" || strings.HasPrefix(strings.TrimSpace(link.Target), "//") || parsed.Path == "" {
			continue
		}
		decoded, err := url.PathUnescape(parsed.Path)
		if err != nil {
			decoded = parsed.Path
		}
		path := filepath.Clean(filepath.Join(filepath.Dir(source.Path), filepath.FromSlash(decoded)))
		if !seen[path] {
			seen[path] = true
			result = append(result, path)
		}
	}
	sort.Strings(result)
	return result
}
