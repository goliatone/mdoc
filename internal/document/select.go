package document

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/goliatone/mdoc/internal/config"
	"github.com/goliatone/mdoc/internal/sourcekey"
)

type selectedSource struct {
	Path string
	Key  string
}

type SourceRef struct {
	Path string `json:"path"`
	Key  string `json:"key"`
}

func SelectSources(root string, profile config.SelectedProfile) ([]SourceRef, error) {
	selected, err := selectProfileSources(root, profile)
	if err != nil {
		return nil, err
	}
	result := make([]SourceRef, 0, len(selected))
	for _, source := range selected {
		result = append(result, SourceRef(source))
	}
	return result, nil
}

// ResolveSelectedSourceKey maps a configured key or a local path to the
// canonical key assigned by source selection. It cannot approve a path that is
// absent from the selected source set.
func ResolveSelectedSourceKey(root string, sources []SourceRef, value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("source is required")
	}
	if !filepath.IsAbs(value) {
		if key, err := sourcekey.Normalize(value); err == nil {
			for _, source := range sources {
				if source.Key == key {
					return source.Key, nil
				}
			}
		}
		value = filepath.Join(root, filepath.FromSlash(value))
	}
	candidate, err := canonicalPath(value)
	if err != nil {
		return "", fmt.Errorf("resolve source %q: %w", value, err)
	}
	for _, source := range sources {
		selected, err := canonicalPath(source.Path)
		if err != nil {
			return "", fmt.Errorf("resolve configured source %q: %w", source.Path, err)
		}
		if filepath.Clean(candidate) == filepath.Clean(selected) {
			return source.Key, nil
		}
	}
	return "", fmt.Errorf("source %q is not in the configured set", value)
}

// ResolveEntrySourceKey resolves an explicit entry through the selected source
// map. An omitted entry is inferred only when source selection found one file.
func ResolveEntrySourceKey(root string, sources []SourceRef, value string) (string, error) {
	if strings.TrimSpace(value) == "" {
		if len(sources) == 1 {
			return sources[0].Key, nil
		}
		return "", nil
	}
	return ResolveSelectedSourceKey(root, sources, value)
}

func selectProfileSources(root string, profile config.SelectedProfile) ([]selectedSource, error) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	allowedRoots := []string{absRoot}
	for _, value := range profile.ExternalRoots {
		abs, err := filepath.Abs(value)
		if err != nil {
			return nil, fmt.Errorf("resolve external root %q: %w", value, err)
		}
		allowedRoots = append(allowedRoots, abs)
	}

	candidates := []string{}
	for _, source := range profile.Sources {
		matches, err := expandSource(source)
		if err != nil {
			return nil, err
		}
		candidates = append(candidates, matches...)
	}
	sort.Strings(candidates)
	byRealPath := map[string]selectedSource{}
	byKey := map[string]string{}
	for _, candidate := range candidates {
		if excludedSource(candidate, profile.Excludes) {
			continue
		}
		if !strings.EqualFold(filepath.Ext(candidate), ".md") {
			continue
		}
		abs, err := filepath.Abs(candidate)
		if err != nil {
			return nil, err
		}
		real, err := filepath.EvalSymlinks(abs)
		if err != nil {
			return nil, fmt.Errorf("resolve source %q: %w", candidate, err)
		}
		rootIndex := containingRoot(allowedRoots, real)
		if rootIndex < 0 {
			return nil, fmt.Errorf("source %q resolves outside the project root and approved external roots", candidate)
		}
		if !caseMatches(allowedRoots[rootIndex], abs) {
			return nil, fmt.Errorf("source %q does not match on disk casing", candidate)
		}
		key, err := selectedSourceKey(absRoot, allowedRoots, rootIndex, abs)
		if err != nil {
			return nil, err
		}
		canonical := filepath.Clean(real)
		if existing, ok := byRealPath[canonical]; ok {
			if existing.Key != key {
				return nil, fmt.Errorf("source %q has conflicting keys %q and %q", candidate, existing.Key, key)
			}
			continue
		}
		if prior, exists := byKey[key]; exists && prior != canonical {
			return nil, fmt.Errorf("source paths %q and %q have the same canonical key %q", prior, canonical, key)
		}
		selected := selectedSource{Path: abs, Key: key}
		byRealPath[canonical] = selected
		byKey[key] = canonical
	}
	result := make([]selectedSource, 0, len(byRealPath))
	for _, selected := range byRealPath {
		result = append(result, selected)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Key < result[j].Key })
	return result, nil
}

func expandSource(source string) ([]string, error) {
	info, err := os.Stat(source)
	if err == nil {
		if !info.IsDir() {
			return []string{source}, nil
		}
		result := []string{}
		err = filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() {
				return nil
			}
			if strings.EqualFold(filepath.Ext(path), ".md") {
				result = append(result, path)
			}
			return nil
		})
		return result, err
	}
	if !os.IsNotExist(err) {
		return nil, fmt.Errorf("inspect source %q: %w", source, err)
	}
	matches, err := doublestar.FilepathGlob(source)
	if err != nil {
		return nil, fmt.Errorf("invalid source pattern %q: %w", source, err)
	}
	result := []string{}
	for _, match := range matches {
		info, err := os.Stat(match)
		if err != nil {
			return nil, fmt.Errorf("inspect source match %q: %w", match, err)
		}
		if info.IsDir() {
			nested, err := expandSource(match)
			if err != nil {
				return nil, err
			}
			result = append(result, nested...)
			continue
		}
		result = append(result, match)
	}
	return result, nil
}

func excludedSource(path string, patterns []string) bool {
	clean := filepath.ToSlash(filepath.Clean(path))
	for _, pattern := range patterns {
		patternClean := filepath.ToSlash(filepath.Clean(pattern))
		if clean == patternClean || strings.HasPrefix(clean, strings.TrimSuffix(patternClean, "/")+"/") {
			return true
		}
		matched, err := doublestar.Match(patternClean, clean)
		if err == nil && matched {
			return true
		}
	}
	return false
}

func containingRoot(roots []string, path string) int {
	realPath, err := canonicalPath(path)
	if err != nil {
		return -1
	}
	for index, root := range roots {
		realRoot, err := canonicalPath(root)
		if err == nil && within(realRoot, realPath) {
			return index
		}
	}
	return -1
}

func canonicalPath(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	candidate := abs
	suffix := []string{}
	var lastErr error
	for {
		real, evalErr := filepath.EvalSymlinks(candidate)
		if evalErr == nil {
			parts := append([]string{real}, suffix...)
			return filepath.Join(parts...), nil
		}
		lastErr = evalErr
		parent := filepath.Dir(candidate)
		if parent == candidate {
			return "", fmt.Errorf("resolve path %s: %w", abs, lastErr)
		}
		suffix = append([]string{filepath.Base(candidate)}, suffix...)
		candidate = parent
	}
}

func selectedSourceKey(projectRoot string, roots []string, rootIndex int, path string) (string, error) {
	base := roots[rootIndex]
	relative, err := filepath.Rel(base, path)
	if err != nil {
		return "", err
	}
	if rootIndex > 0 {
		relative = filepath.Join("external", fmt.Sprintf("%d", rootIndex), relative)
	} else if base != projectRoot {
		relative, err = filepath.Rel(projectRoot, path)
		if err != nil {
			return "", err
		}
	}
	return sourcekey.Normalize(relative)
}
