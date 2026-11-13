package portable

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func bundleFixture(t *testing.T) (*Service, Config) {
	t.Helper()
	root := t.TempDir()
	index := `# Project

## Bundle inventory

| ID | Document | Section | Path | Role |
| --- | --- | --- | --- | --- |
| PRD | [Product](https://docs.google.com/document/d/PRD/edit) | | requirements/product.md | document |
| TDD | [Design](https://drive.google.com/file/d/TDD/view) | | architecture/overview.md | document |
| TID | [Implementation](https://drive.google.com/file/d/TID/view) | | implementation.md | document |
| ADR | [Decision](https://drive.google.com/file/d/ADR/view) | | architecture/decisions/workflow.md | document |
| DIAGRAMS | [Diagrams](https://drive.google.com/drive/folders/TREE) | | architecture/diagrams | asset-tree |
| OLD | [Archive](https://docs.google.com/document/d/OLD/edit) | | | reference |
`
	if e := os.WriteFile(filepath.Join(root, "index.md"), []byte(index), 0600); e != nil {
		t.Fatal(e)
	}
	png := pngBytes()
	blobs := map[string]string{"TDD": "# Design\n\n[Product](https://docs.google.com/document/d/PRD/edit)\n\n![System](diagrams/system.png)\n\n![System detail](diagrams/system.png \"Detail\")\n", "TID": "# Implementation\n\n[Design](https://drive.google.com/file/d/TDD/view)\n", "ADR": "# Decision\n\n[Design](https://drive.google.com/file/d/TDD/view)\n", "README": "# Assets\n\n[System](system.png)\n", "MANIFEST": "{\"source\":true}", "PNG": string(png)}
	api := &http.Client{Transport: roundTripper(func(r *http.Request) (*http.Response, error) {
		if r.Method != "GET" {
			t.Fatal("remote write")
		}
		if r.URL.Host == "docs.googleapis.com" {
			return response(200, strings.ReplaceAll(nativeImageJSON("one", "https://lh3.googleusercontent.com/capability"), `"DOC"`, `"PRD"`)), nil
		}
		if strings.HasSuffix(r.URL.Path, "/files") {
			return response(200, `{"files":[{"id":"README","name":"README.md","mimeType":"text/markdown","version":"1","parents":["TREE"]},{"id":"MANIFEST","name":"manifest.json","mimeType":"application/json","version":"1","parents":["TREE"]},{"id":"PNG","name":"system.png","mimeType":"image/png","version":"1","parents":["TREE"]}]}`), nil
		}
		id := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		if r.URL.Query().Get("alt") == "media" {
			data, ok := blobs[id]
			if !ok {
				return response(404, "{}"), nil
			}
			return response(200, data), nil
		}
		mime, name, parent := "text/markdown", id+".md", "PARENT"
		switch id {
		case "PRD":
			mime = docMIME
		case "TREE":
			mime = folderMIME
			name = "diagrams"
		case "PNG":
			mime = "image/png"
			name = "system.png"
			parent = "TREE"
		case "README":
			name = "README.md"
			parent = "TREE"
		case "MANIFEST":
			mime = "application/json"
			name = "manifest.json"
			parent = "TREE"
		}
		return response(200, fmt.Sprintf(`{"id":%q,"name":%q,"mimeType":%q,"parents":[%q],"version":"1"}`, id, name, mime, parent)), nil
	})}
	images := &http.Client{Transport: roundTripper(func(r *http.Request) (*http.Response, error) {
		res := response(200, string(png))
		res.Header.Set("Content-Type", "image/png")
		return res, nil
	})}
	s, e := New(Options{HTTPClient: api, ImageHTTPClient: images})
	if e != nil {
		t.Fatal(e)
	}
	c := DefaultConfig()
	c.Project = "demo"
	c.Index.Source = filepath.Join(root, "index.md")
	return s, c
}
func TestExportMoveVerifyOffline(t *testing.T) {
	s, c := bundleFixture(t)
	ctx := context.Background()
	destination := filepath.Join(t.TempDir(), "bundle")
	r, e := s.Export(ctx, ExportRequest{c, destination})
	if e != nil {
		t.Fatal(e)
	}
	if !r.Manifest.LocalComplete || r.Manifest.NoOnlineDependencies {
		t.Fatal(r.Manifest)
	}
	foundDescriptions := false
	for _, a := range r.Manifest.Assets {
		if a.Path == "architecture/diagrams/system.png" {
			if len(a.Occurrences) != 2 || a.Occurrences[0].Alt != "System" || a.Occurrences[1].Alt != "System detail" {
				t.Fatal(a.Occurrences)
			}
			foundDescriptions = true
		}
	}
	if !foundDescriptions {
		t.Fatal("missing image occurrences")
	}
	if len(r.Manifest.Documents) != 4 {
		t.Fatal(r.Manifest.Documents)
	}
	readme, e := os.ReadFile(filepath.Join(destination, "architecture/diagrams/README.md"))
	if e != nil || string(readme) != "# Assets\n\n[System](system.png)\n" {
		t.Fatal(string(readme), e)
	}
	index, _ := os.ReadFile(filepath.Join(destination, c.Layout.Index))
	if !strings.Contains(string(index), "navigation/diagrams.md") {
		t.Fatal(string(index))
	}
	moved := destination + "-moved"
	if e = os.Rename(destination, moved); e != nil {
		t.Fatal(e)
	}
	offline, _ := New(Options{})
	v, e := offline.Verify(ctx, VerifyRequest{moved})
	if e != nil || !v.LocalComplete || v.NoOnlineDependencies {
		t.Fatal(v, e)
	}
	if _, e = s.Export(ctx, ExportRequest{c, moved}); e == nil {
		t.Fatal("overwrote existing output")
	}
	if e = os.WriteFile(filepath.Join(moved, "architecture/diagrams/system.png"), []byte("tamper"), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e = offline.Verify(ctx, VerifyRequest{moved}); e == nil {
		t.Fatal("tampered image accepted")
	}
}
func TestInstallCancellationAndSymlink(t *testing.T) {
	root := t.TempDir()
	store := LocalOutputStore{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	dest := filepath.Join(root, "out")
	if e := store.Install(ctx, dest, map[string][]byte{"a.md": []byte("a")}); e == nil {
		t.Fatal("cancellation ignored")
	}
	if _, e := os.Stat(dest); !os.IsNotExist(e) {
		t.Fatal(e)
	}
	names, _ := os.ReadDir(root)
	if len(names) != 0 {
		t.Fatal("staging leaked")
	}
	outside := t.TempDir()
	if e := os.Symlink(outside, dest); e != nil {
		t.Fatal(e)
	}
	if e := store.Install(context.Background(), dest, map[string][]byte{"a.md": []byte("a")}); e == nil {
		t.Fatal("existing symlink replaced")
	}
}

func TestExhaustedImagesNeverInstall(t *testing.T) {
	s, c := bundleFixture(t)
	s.options.ImageHTTPClient = &http.Client{Transport: roundTripper(func(r *http.Request) (*http.Response, error) { return response(403, "private capability"), nil })}
	dest := filepath.Join(t.TempDir(), "missing")
	r, e := s.Export(context.Background(), ExportRequest{c, dest})
	if e == nil || r.Status != "incomplete" {
		t.Fatal(r, e)
	}
	if _, e = os.Lstat(dest); !os.IsNotExist(e) {
		t.Fatal("incomplete output installed")
	}
}
func TestConflictingSharedSourceAndExclusiveRename(t *testing.T) {
	c := DefaultConfig()
	a := newAssembly(c, Evidence{})
	one := capturedAsset{bytes: pngBytes(), mime: "image/png", path: "one.png", evidence: Evidence{Source: SourceRef{FileID: "IMG", Kind: "drive"}, Version: 1}}
	one.digest = digest(one.bytes)
	if e := a.addAsset(one, nil); e != nil {
		t.Fatal(e)
	}
	two := one
	two.path = "two.png"
	two.evidence.Version = 2
	if e := a.addAsset(two, nil); e == nil {
		t.Fatal("mixed source versions accepted")
	}
	root := t.TempDir()
	os.Mkdir(filepath.Join(root, "stage"), 0700)
	os.Mkdir(filepath.Join(root, "dest"), 0700)
	os.WriteFile(filepath.Join(root, "dest", "human.txt"), []byte("keep"), 0600)
	fd, e := os.Open(root)
	if e != nil {
		t.Fatal(e)
	}
	defer fd.Close()
	if e = renameExclusive(int(fd.Fd()), "stage", "dest"); e == nil {
		t.Fatal("concurrent destination replaced")
	}
	if b, e := os.ReadFile(filepath.Join(root, "dest", "human.txt")); e != nil || string(b) != "keep" {
		t.Fatal(e)
	}
}
