package fields

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestResolveAppliesDocumentedPrecedenceAndTypes(t *testing.T) {
	root := t.TempDir()
	configuredOne := filepath.Join(root, "one.yaml")
	configuredTwo := filepath.Join(root, "two.json")
	commandOne := filepath.Join(root, "command.yaml")
	writeFieldFile(t, configuredOne, "name: file-one\nnested:\n  first: 1\n  keep: true\nlist: [one]\nnullable: value\n")
	writeFieldFile(t, configuredTwo, `{"name":"file-two","nested":{"second":2},"list":["two"]}`)
	writeFieldFile(t, commandOne, "name: command-file\nnested:\n  third: 3\n")

	result, err := Resolve(ResolveOptions{
		PublicationID:     "report",
		Defaults:          map[string]any{"name": "default", "nested": map[string]any{"default": true}, "default_only": false},
		ConfiguredFiles:   []string{configuredOne, configuredTwo},
		PublicationFields: map[string]any{"name": "publication", "nested": map[string]any{"publication": true}},
		CommandFiles:      []string{commandOne},
		Assignments:       []string{`/name="assignment-one"`, `/name="assignment-two"`, `/nested/a~1b="slash"`, `/nested/t~0n=true`, `/list=[1,2]`, `/list/0=3`, `/nullable=null`},
		WorkingDir:        root, ProjectRoot: root, AllowedRoots: []string{root},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Values["name"] != "assignment-two" || result.Values["nullable"] != nil || result.Values["default_only"] != false {
		t.Fatalf("resolved scalars = %#v", result.Values)
	}
	nested := result.Values["nested"].(map[string]any)
	for _, key := range []string{"default", "first", "keep", "second", "publication", "third", "a/b", "t~n"} {
		if _, exists := nested[key]; !exists {
			t.Fatalf("missing nested key %q in %#v", key, nested)
		}
	}
	list := result.Values["list"].([]any)
	if number, ok := list[0].(json.Number); !ok || number.String() != "3" || len(list) != 2 {
		t.Fatalf("list = %#v", list)
	}
	if result.Summary.RootCount == 0 || result.Summary.FileCount != 3 || result.Summary.AssignmentCount != 7 || result.Summary.Hash == "" {
		t.Fatalf("summary = %#v", result.Summary)
	}
	canonicalConfiguredOne, canonicalErr := filepath.EvalSymlinks(configuredOne)
	if canonicalErr != nil {
		t.Fatal(canonicalErr)
	}
	if result.Files[0].Path != canonicalConfiguredOne || result.Files[0].Identity != "one.yaml" {
		t.Fatalf("field file path=%q identity=%q", result.Files[0].Path, result.Files[0].Identity)
	}
}

func TestAssignmentsRejectInvalidPointersValuesAndTraversal(t *testing.T) {
	tests := []struct {
		assignment string
		message    string
	}{
		{"name=1", "start with /"},
		{"/bad~2name=1", "escape"},
		{"/name=plain", "invalid JSON"},
		{"/mdoc/name=1", "reserved"},
		{"/page/count=1", "reserved"},
	}
	for _, test := range tests {
		t.Run(test.assignment, func(t *testing.T) {
			_, err := Resolve(ResolveOptions{PublicationID: "report", Assignments: []string{test.assignment}, AllowedRoots: []string{t.TempDir()}})
			if err == nil || !strings.Contains(err.Error(), test.message) {
				t.Fatalf("error = %v", err)
			}
		})
	}
	_, err := Resolve(ResolveOptions{PublicationID: "report", Defaults: map[string]any{"scalar": true}, Assignments: []string{"/scalar/nested=1"}, AllowedRoots: []string{t.TempDir()}})
	if err == nil || !strings.Contains(err.Error(), "cannot traverse scalar") {
		t.Fatalf("scalar traversal error = %v", err)
	}
}

func TestResolveRejectsUnsafeMissingAndReservedFieldFiles(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	outsideFile := filepath.Join(outside, "fields.yaml")
	writeFieldFile(t, outsideFile, "name: outside\n")
	for name, test := range map[string]struct {
		path    string
		message string
	}{
		"outside": {outsideFile, "outside approved roots"},
		"missing": {filepath.Join(root, "missing.yaml"), "unavailable"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Resolve(ResolveOptions{PublicationID: "report", ConfiguredFiles: []string{test.path}, ProjectRoot: root, AllowedRoots: []string{root}})
			if err == nil || !strings.Contains(err.Error(), test.message) {
				t.Fatalf("error = %v", err)
			}
		})
	}
	reserved := filepath.Join(root, "reserved.yaml")
	writeFieldFile(t, reserved, "mdoc:\n  project: bad\n")
	_, err := Resolve(ResolveOptions{PublicationID: "report", ConfiguredFiles: []string{reserved}, ProjectRoot: root, AllowedRoots: []string{root}})
	if err == nil || !strings.Contains(err.Error(), "reserved root") {
		t.Fatalf("reserved file error = %v", err)
	}

	if runtime.GOOS != "windows" {
		linked := filepath.Join(root, "linked.yaml")
		if err := os.Symlink(outsideFile, linked); err != nil {
			t.Fatal(err)
		}
		_, err = Resolve(ResolveOptions{PublicationID: "report", ConfiguredFiles: []string{linked}, ProjectRoot: root, AllowedRoots: []string{root}})
		if err == nil || !strings.Contains(err.Error(), "outside approved roots") {
			t.Fatalf("symlink error = %v", err)
		}
	}
}

func TestFieldHashesAreStableAndOrderSensitive(t *testing.T) {
	root := t.TempDir()
	one := filepath.Join(root, "one.yaml")
	two := filepath.Join(root, "two.yaml")
	writeFieldFile(t, one, "a: 1\n")
	writeFieldFile(t, two, "b: 2\n")
	base := ResolveOptions{PublicationID: "report", Defaults: map[string]any{"z": true, "a": map[string]any{"y": 2, "x": 1}}, ConfiguredFiles: []string{one, two}, ProjectRoot: root, AllowedRoots: []string{root}}
	first, err := Resolve(base)
	if err != nil {
		t.Fatal(err)
	}
	base.Defaults = map[string]any{"a": map[string]any{"x": 1, "y": 2}, "z": true}
	second, err := Resolve(base)
	if err != nil {
		t.Fatal(err)
	}
	if first.Hash != second.Hash || first.ValuesHash != second.ValuesHash {
		t.Fatalf("map order changed hashes: %s %s", first.Hash, second.Hash)
	}
	base.ConfiguredFiles = []string{two, one}
	reordered, err := Resolve(base)
	if err != nil {
		t.Fatal(err)
	}
	if reordered.FilesHash == first.FilesHash || reordered.Hash == first.Hash {
		t.Fatal("field file order did not change hashes")
	}
}

func TestSummaryDoesNotContainFieldValues(t *testing.T) {
	result, err := Resolve(ResolveOptions{PublicationID: "report", Defaults: map[string]any{"token": "super-secret-value"}, AllowedRoots: []string{t.TempDir()}})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(result.Summary)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "super-secret-value") {
		t.Fatalf("summary leaked value: %s", encoded)
	}
}

func writeFieldFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
