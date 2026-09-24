package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadResolvesPathsAndDefaults(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, ".mdoc.yaml")
	data := strings.Replace(DefaultYAML, "max_table_columns: 5", "max_table_columns: 0", 1)
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	profile, err := cfg.Select("work")
	if err != nil {
		t.Fatal(err)
	}
	if profile.MaxTableColumns != 5 {
		t.Fatalf("max columns = %d", profile.MaxTableColumns)
	}
	if !filepath.IsAbs(profile.Entry) || !strings.HasPrefix(profile.Entry, directory) {
		t.Fatalf("entry = %q", profile.Entry)
	}
	if !filepath.IsAbs(profile.Sources[0]) || !strings.HasPrefix(profile.ReferenceDOCX, directory) {
		t.Fatalf("paths were not resolved: %#v", profile)
	}
}

func TestLoadRejectsUnknownFieldAndVersion(t *testing.T) {
	for name, data := range map[string]string{
		"unknown": DefaultYAML + "unknown: true\n",
		"version": strings.Replace(DefaultYAML, "version: 1", "version: 99", 1),
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), ".mdoc.yaml")
			if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(path); err == nil {
				t.Fatal("expected load failure")
			}
		})
	}
}

func TestLoadRejectsUnsafePaths(t *testing.T) {
	data := strings.Replace(DefaultYAML, "docs/*.md", "../*.md", 1)
	path := filepath.Join(t.TempDir(), ".mdoc.yaml")
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("expected unsafe path failure")
	}
}

func TestLoadRejectsProfilesSharingCustomStateFile(t *testing.T) {
	data := `version: 1
workspace_id: workspace
profiles:
  first:
    sources: [docs/*.md]
    entry: docs/a.md
    review_folder_name: Review
    staging_folder_name: Staging
    reference_docx: reference.docx
    state_file: private/../private/STATE.json
  second:
    sources: [docs/*.md]
    entry: docs/a.md
    review_folder_name: Other Review
    staging_folder_name: Other Staging
    reference_docx: reference.docx
    state_file: private/state.json
`
	path := filepath.Join(t.TempDir(), ".mdoc.yaml")
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "same state_file") {
		t.Fatalf("error = %v", err)
	}
}

func TestWriteDefaultDoesNotOverwriteWithoutForce(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".mdoc.yaml")
	if err := WriteDefault(path, false); err != nil {
		t.Fatal(err)
	}
	if err := WriteDefault(path, false); err == nil {
		t.Fatal("expected existing config error")
	}
	if err := WriteDefault(path, true); err != nil {
		t.Fatal(err)
	}
}
