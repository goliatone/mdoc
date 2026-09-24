package bundle

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type Asset struct {
	SourceKey string `json:"source_key"`
	Original  string `json:"original"`
	Path      string `json:"path"`
}

func resolveAsset(sourceKey, memberPath, target string, allowedRoots []string) (Asset, error) {
	if filepath.IsAbs(target) {
		return Asset{}, fmt.Errorf("asset %q uses an absolute path", target)
	}
	path := filepath.Clean(filepath.Join(filepath.Dir(memberPath), filepath.FromSlash(target)))
	if !pathAllowed(path, allowedRoots, filepath.Dir(memberPath)) {
		return Asset{}, fmt.Errorf("asset %q escapes approved roots", target)
	}
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return Asset{}, fmt.Errorf("asset %q does not exist", target)
	}
	real, err := filepath.EvalSymlinks(path)
	if err != nil || !pathAllowed(real, allowedRoots, filepath.Dir(memberPath)) {
		return Asset{}, fmt.Errorf("asset %q resolves outside approved roots", target)
	}
	return Asset{SourceKey: sourceKey, Original: target, Path: real}, nil
}

func pathAllowed(path string, roots []string, fallback string) bool {
	if len(roots) == 0 {
		roots = []string{fallback}
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	for _, root := range roots {
		absoluteRoot, rootErr := filepath.Abs(root)
		if rootErr != nil {
			continue
		}
		if realRoot, realErr := filepath.EvalSymlinks(absoluteRoot); realErr == nil {
			absoluteRoot = realRoot
		}
		candidate := absolute
		if realPath, realErr := filepath.EvalSymlinks(absolute); realErr == nil {
			candidate = realPath
		}
		relative, relErr := filepath.Rel(absoluteRoot, candidate)
		if relErr == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return true
		}
	}
	return false
}
