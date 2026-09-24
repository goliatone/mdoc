package sourcekey

import (
	"errors"
	"path/filepath"
	"strings"

	"golang.org/x/text/unicode/norm"
)

// Normalize returns the canonical workspace-relative form of a source key.
func Normalize(value string) (string, error) {
	if filepath.IsAbs(value) {
		return "", errors.New("source key must be workspace relative")
	}
	clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(value)))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || strings.Contains(clean, "\\") {
		return "", errors.New("source key must stay inside the workspace")
	}
	return norm.NFC.String(clean), nil
}

// FromPath returns the canonical source key for a path inside root.
func FromPath(root, path string) (string, error) {
	if !filepath.IsAbs(path) {
		path = filepath.Join(root, filepath.FromSlash(path))
	}
	relative, err := filepath.Rel(root, path)
	if err != nil {
		return "", err
	}
	return Normalize(relative)
}
