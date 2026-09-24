package bundle

import (
	"fmt"
	"net/url"
	"path/filepath"
	"strings"

	"github.com/goliatone/mdoc/internal/document"
	"github.com/goliatone/mdoc/internal/publication"
)

const protectedDependencyClass = "mdoc-protected-dependency"

type Dependency struct {
	SourceKey string `json:"source_key"`
	Kind      string `json:"kind"`
	Target    string `json:"target"`
}

type ExternalLink struct {
	SourceKey    string `json:"source_key"`
	TargetSource string `json:"target_source"`
	Target       string `json:"target"`
	HadFragment  bool   `json:"had_fragment"`
}

type RewriteOptions struct {
	FragmentPolicy  string
	AllowedRoots    []string
	PathTargets     map[string]string
	ExternalTargets map[string]string
}

type RewriteResult struct {
	Dependencies  []Dependency
	Assets        []Asset
	ExternalLinks []ExternalLink
	Issues        []document.Issue
}

type memberIndex struct {
	byPath   map[string]string
	starts   map[string]string
	headings map[string]map[string][]string
}

func newMemberIndex(items []preparedMember, bookmarks map[string]string) memberIndex {
	index := memberIndex{byPath: map[string]string{}, starts: map[string]string{}, headings: map[string]map[string][]string{}}
	for _, item := range items {
		index.byPath[canonicalPath(item.member.Document.Path)] = item.member.SourceKey
		index.starts[item.member.SourceKey] = bookmarks[memberStartLogical(item.member.SourceKey)]
		index.headings[item.member.SourceKey] = map[string][]string{}
		for _, ref := range item.refs {
			if ref.originalID != "" {
				index.headings[item.member.SourceKey][ref.originalID] = append(index.headings[item.member.SourceKey][ref.originalID], bookmarks[ref.logical])
			}
		}
	}
	return index
}

func rewriteMember(member publication.Member, blocks []any, index memberIndex, options RewriteOptions) (RewriteResult, error) {
	result := RewriteResult{}
	policy := options.FragmentPolicy
	if policy == "" {
		policy = "error"
	}
	var rewriteErr error
	walk(blocks, func(node map[string]any) {
		if rewriteErr != nil {
			return
		}
		typeName, _ := node["t"].(string)
		if typeName != "Link" && typeName != "Image" {
			return
		}
		target, ok := nodeTarget(node)
		if !ok {
			return
		}
		parsed, err := url.Parse(target)
		if err != nil {
			rewriteErr = fmt.Errorf("invalid target %q", target)
			return
		}
		if typeName == "Image" {
			if parsed.Scheme != "" || strings.HasPrefix(target, "//") || parsed.Path == "" {
				rewriteErr = fmt.Errorf("image %q must use a local relative path", target)
				return
			}
			path, decodeErr := url.PathUnescape(parsed.Path)
			if decodeErr != nil {
				rewriteErr = fmt.Errorf("decode asset path %q: %w", target, decodeErr)
				return
			}
			asset, assetErr := resolveAsset(member.SourceKey, member.Document.Path, path, options.AllowedRoots)
			if assetErr != nil {
				rewriteErr = assetErr
				return
			}
			resolved := filepath.ToSlash(asset.Path)
			setNodeTarget(node, resolved)
			protectDependency(node, "image")
			result.Assets = append(result.Assets, asset)
			result.Dependencies = append(result.Dependencies, Dependency{SourceKey: member.SourceKey, Kind: "image", Target: resolved})
			return
		}
		if parsed.Scheme != "" || strings.HasPrefix(target, "//") {
			return
		}
		targetSource := member.SourceKey
		if parsed.Path != "" {
			decoded, decodeErr := url.PathUnescape(parsed.Path)
			if decodeErr != nil {
				rewriteErr = fmt.Errorf("decode link path %q: %w", target, decodeErr)
				return
			}
			resolved := canonicalPath(filepath.Join(filepath.Dir(member.Document.Path), filepath.FromSlash(decoded)))
			if source, exists := index.byPath[resolved]; exists {
				targetSource = source
			} else {
				if len(options.AllowedRoots) > 0 && !pathAllowed(resolved, options.AllowedRoots, filepath.Dir(member.Document.Path)) {
					rewriteErr = fmt.Errorf("Markdown target %q escapes approved roots", target)
					return
				}
				externalSource := lookupPathTarget(options.PathTargets, resolved)
				externalURL := options.ExternalTargets[externalSource]
				if externalSource == "" || externalURL == "" {
					rewriteErr = fmt.Errorf("Markdown target %q has no publication mapping", target)
					return
				}
				setNodeTarget(node, externalURL)
				protectDependency(node, "publication")
				result.Dependencies = append(result.Dependencies, Dependency{SourceKey: member.SourceKey, Kind: "publication", Target: externalURL})
				result.ExternalLinks = append(result.ExternalLinks, ExternalLink{SourceKey: member.SourceKey, TargetSource: externalSource, Target: externalURL, HadFragment: parsed.Fragment != ""})
				return
			}
		}
		bookmark := index.starts[targetSource]
		if parsed.Fragment != "" {
			fragment, decodeErr := url.PathUnescape(parsed.Fragment)
			if decodeErr != nil {
				fragment = parsed.Fragment
			}
			matches := index.headings[targetSource][fragment]
			if len(matches) == 1 {
				bookmark = matches[0]
			} else {
				switch policy {
				case "warning_root":
					message := fmt.Sprintf("fragment %q in target %q did not resolve uniquely; linked to member root", fragment, target)
					result.Issues = append(result.Issues, document.Issue{Severity: document.SeverityWarning, Code: "bundle_fragment_root", Source: member.SourceKey, Message: message})
				case "preserve":
					protectDependency(node, "preserved")
					result.Dependencies = append(result.Dependencies, Dependency{SourceKey: member.SourceKey, Kind: "preserved", Target: target})
					return
				default:
					rewriteErr = fmt.Errorf("fragment %q in target %q did not resolve uniquely", fragment, target)
					return
				}
			}
		}
		internalTarget := "#" + bookmark
		setNodeTarget(node, internalTarget)
		protectDependency(node, "internal")
		result.Dependencies = append(result.Dependencies, Dependency{SourceKey: member.SourceKey, Kind: "internal", Target: internalTarget})
	})
	return result, rewriteErr
}

func lookupPathTarget(values map[string]string, path string) string {
	if value := values[path]; value != "" {
		return value
	}
	return values[filepath.Clean(path)]
}

func canonicalPath(path string) string {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return filepath.Clean(path)
	}
	if real, realErr := filepath.EvalSymlinks(absolute); realErr == nil {
		return filepath.Clean(real)
	}
	return filepath.Clean(absolute)
}

func nodeTarget(node map[string]any) (string, bool) {
	content, _ := node["c"].([]any)
	if len(content) < 3 {
		return "", false
	}
	target, _ := content[2].([]any)
	if len(target) < 1 {
		return "", false
	}
	value, ok := target[0].(string)
	return value, ok
}

func setNodeTarget(node map[string]any, value string) {
	content, _ := node["c"].([]any)
	if len(content) < 3 {
		return
	}
	target, _ := content[2].([]any)
	if len(target) > 0 {
		target[0] = value
	}
}

func protectDependency(node map[string]any, kind string) {
	content, _ := node["c"].([]any)
	if len(content) < 1 {
		return
	}
	attribute, _ := content[0].([]any)
	if len(attribute) < 3 {
		return
	}
	classes, _ := attribute[1].([]any)
	if !containsStringAny(classes, protectedDependencyClass) {
		classes = append(classes, protectedDependencyClass)
	}
	attribute[1] = classes
	attributes, _ := attribute[2].([]any)
	attributes = append(attributes, []any{"data-mdoc-dependency", kind})
	attribute[2] = attributes
}

func extractDependency(node map[string]any, sourceKey string) (Dependency, bool) {
	content, _ := node["c"].([]any)
	if len(content) < 3 {
		return Dependency{}, false
	}
	target, ok := nodeTarget(node)
	if !ok {
		return Dependency{}, false
	}
	_, classes, values := attributes(content[0])
	if contains(classes, protectedDependencyClass) {
		return Dependency{SourceKey: sourceKey, Kind: values["data-mdoc-dependency"], Target: target}, true
	}
	if node["t"] == "Image" {
		return Dependency{SourceKey: sourceKey, Kind: "image", Target: target}, true
	}
	parsed, err := url.Parse(target)
	if err == nil && parsed.Scheme == "" && !strings.HasPrefix(target, "//") {
		return Dependency{SourceKey: sourceKey, Kind: "local", Target: target}, true
	}
	return Dependency{}, false
}

func collectDependencies(blocks []any, sourceKey string) []Dependency {
	result := []Dependency{}
	walk(blocks, func(node map[string]any) {
		if dependency, ok := extractDependency(node, sourceKey); ok {
			result = append(result, dependency)
		}
	})
	return result
}

func containsStringAny(values []any, target string) bool {
	for _, value := range values {
		if text, ok := value.(string); ok && text == target {
			return true
		}
	}
	return false
}
