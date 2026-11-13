package portable

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExportImageOccurrencePrivacy(t *testing.T) {
	for _, denied := range []bool{false, true} {
		t.Run(fmt.Sprintf("denied=%v", denied), func(t *testing.T) {
			s, c := bundleFixture(t)
			base := s.options.HTTPClient.Transport
			const secret = "private-image-key"
			sawAccess := false
			s.options.HTTPClient = &http.Client{Transport: roundTripper(func(r *http.Request) (*http.Response, error) {
				if strings.HasSuffix(r.URL.Path, "/TDD") && r.URL.Query().Get("alt") == "media" {
					return response(200, "# Design\n\n![First](https://drive.google.com/file/d/PNG/view?resourcekey="+secret+")\n\n![Second](https://drive.google.com/file/d/PNG/view?resourcekey="+secret+" \"Detail\")\n"), nil
				}
				if strings.HasSuffix(r.URL.Path, "/PNG") && r.URL.Query().Get("alt") == "media" {
					sawAccess = strings.Contains(r.Header.Get("X-Goog-Drive-Resource-Keys"), secret)
					if denied {
						return response(403, secret), nil
					}
				}
				return base.RoundTrip(r)
			})}
			dest := filepath.Join(t.TempDir(), "bundle")
			r, err := s.Export(context.Background(), ExportRequest{c, dest})
			if !sawAccess {
				t.Fatal("resource key was not used for the private download")
			}
			encoded, _ := json.Marshal(r)
			if strings.Contains(string(encoded), secret) || err != nil && strings.Contains(err.Error(), secret) {
				t.Fatal("private key exposed in result/error")
			}
			if denied {
				if err == nil {
					t.Fatal("denied image accepted")
				}
				if _, e := os.Stat(dest); !os.IsNotExist(e) {
					t.Fatal("failed export installed output", e)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			count := 0
			for _, a := range r.Manifest.Assets {
				for _, o := range a.Occurrences {
					if o.DocumentID == "TDD" {
						if o.ObjectID != "mdoc-image:0" || o.Position != count {
							t.Fatalf("invalid occurrence binding: %+v", o)
						}
						count++
					}
				}
			}
			if count != 2 {
				t.Fatalf("lost repeated occurrences: %d", count)
			}
			if err := filepath.WalkDir(dest, func(p string, d os.DirEntry, e error) error {
				if e != nil {
					return e
				}
				if d.IsDir() {
					return nil
				}
				b, e := os.ReadFile(p)
				if strings.Contains(string(b), secret) {
					t.Errorf("private key in %s", p)
				}
				return e
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestExportSupplementaryImageLimits(t *testing.T) {
	for _, tc := range []struct {
		name, source, role, path string
		limit                    int64
		reject                   bool
	}{
		{"selected_image", "PNG", "asset", "cover.png", 1, true},
		{"tree_image", "TREE", "asset-tree", "diagrams", 1, true},
		{"image_at_limit", "PNG", "asset", "cover.png", int64(len(pngBytes())), false},
		{"non_image", "MANIFEST", "asset", "source.json", 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, c := bundleFixture(t)
			c.Limits.ImageBytes = tc.limit
			index := fmt.Sprintf("## Bundle inventory\n\n| ID | Document | Section | Path | Role |\n| --- | --- | --- | --- | --- |\n| TDD | [Design](https://drive.google.com/file/d/TDD/view) | | design.md | document |\n| ASSET | [Asset](https://drive.google.com/file/d/%s/view) | | %s | %s |\n", tc.source, tc.path, tc.role)
			if e := os.WriteFile(c.Index.Source, []byte(index), 0600); e != nil {
				t.Fatal(e)
			}
			base := s.options.HTTPClient.Transport
			s.options.HTTPClient = &http.Client{Transport: roundTripper(func(r *http.Request) (*http.Response, error) {
				if strings.HasSuffix(r.URL.Path, "/TDD") && r.URL.Query().Get("alt") == "media" {
					return response(200, "# Design\n"), nil
				}
				return base.RoundTrip(r)
			})}
			dest := filepath.Join(t.TempDir(), "bundle")
			r, e := s.Export(context.Background(), ExportRequest{c, dest})
			if tc.reject {
				var pe *Error
				if !errors.As(e, &pe) || pe.Code != "limit_exceeded" {
					t.Fatalf("expected byte limit failure, got %v", e)
				}
				if _, e := os.Stat(dest); !os.IsNotExist(e) {
					t.Fatal("oversized image installed", e)
				}
			} else if e != nil || !r.Manifest.LocalComplete {
				t.Fatal(r, e)
			}
		})
	}
}

func TestExportParentRelativeSources(t *testing.T) {
	for _, mode := range []string{"stable", "denied", "moved", "cycle", "ambiguous"} {
		t.Run(mode, func(t *testing.T) {
			s, c := bundleFixture(t)
			base := s.options.HTTPClient.Transport
			folderReads := 0
			s.options.HTTPClient = &http.Client{Transport: roundTripper(func(r *http.Request) (*http.Response, error) {
				if strings.HasSuffix(r.URL.Path, "/ADR") {
					if r.URL.Query().Get("alt") == "media" {
						return response(200, "# Decision\n\n[Design](../TDD.md)\n\n![System](../diagrams/system.png)\n"), nil
					}
					return response(200, `{"id":"ADR","name":"ADR.md","mimeType":"text/markdown","parents":["SUBFOLDER"],"version":"1"}`), nil
				}
				if strings.HasSuffix(r.URL.Path, "/SUBFOLDER") {
					folderReads++
					if mode == "denied" {
						return response(403, "private provider failure"), nil
					}
					parents := `["PARENT"]`
					if mode == "moved" && folderReads > 1 {
						parents = `["OTHER"]`
					}
					if mode == "cycle" {
						parents = `["SUBFOLDER"]`
					}
					if mode == "ambiguous" {
						parents = `["PARENT","OTHER"]`
					}
					return response(200, `{"id":"SUBFOLDER","name":"decisions","mimeType":"application/vnd.google-apps.folder","parents":`+parents+`,"version":"1"}`), nil
				}
				return base.RoundTrip(r)
			})}
			dest := filepath.Join(t.TempDir(), "bundle")
			r, e := s.Export(context.Background(), ExportRequest{c, dest})
			if mode != "stable" {
				want := map[string]string{"denied": "access_denied", "moved": "unstable_source", "cycle": "ambiguous_target", "ambiguous": "ambiguous_target"}[mode]
				var pe *Error
				if !errors.As(e, &pe) || pe.Code != want {
					t.Fatalf("wanted %s, got %v", want, e)
				}
				if _, e := os.Stat(dest); !os.IsNotExist(e) {
					t.Fatal("invalid ancestry installed", e)
				}
				return
			}
			if e != nil {
				t.Fatal(e)
			}
			if folderReads != 2 {
				t.Fatalf("expected cached read and final check, got %d", folderReads)
			}
			if len(r.Manifest.Documents) != 4 {
				t.Fatal("ancestry expanded membership")
			}
			b, e := os.ReadFile(filepath.Join(dest, "architecture/decisions/workflow.md"))
			if e != nil || !strings.Contains(string(b), "../overview.md") || !strings.Contains(string(b), "../diagrams/system.png") {
				t.Fatal(string(b), e)
			}
			moved := dest + "-moved"
			if e = os.Rename(dest, moved); e != nil {
				t.Fatal(e)
			}
			offline, _ := New(Options{})
			v, e := offline.Verify(context.Background(), VerifyRequest{moved})
			if e != nil || !v.LocalComplete {
				t.Fatal(v, e)
			}
		})
	}
}

func TestExportEveryDeclaredAssetDestination(t *testing.T) {
	for _, tc := range []struct {
		name                   string
		outer, after, samePath bool
	}{
		{name: "direct_before_tree"},
		{name: "direct_after_tree", after: true},
		{name: "overlapping_tree_before", outer: true},
		{name: "overlapping_tree_after", outer: true, after: true},
		{name: "same_path_coalesces", samePath: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, c := bundleFixture(t)
			destPath := "cover.png"
			if tc.samePath {
				destPath = "architecture/diagrams/system.png"
			}
			row := fmt.Sprintf("| COVER | [Cover](https://drive.google.com/file/d/PNG/view) | | %s | asset |\n", destPath)
			if tc.outer {
				row = "| OUTER | [Parent](https://drive.google.com/drive/folders/PARENT) | | copies | asset-tree |\n"
				destPath = "copies/diagrams/system.png"
			}
			raw, e := os.ReadFile(c.Index.Source)
			if e != nil {
				t.Fatal(e)
			}
			marker := "| DIAGRAMS |"
			if tc.after {
				marker = "| OLD |"
			}
			if e = os.WriteFile(c.Index.Source, []byte(strings.Replace(string(raw), marker, row+marker, 1)), 0600); e != nil {
				t.Fatal(e)
			}
			base := s.options.HTTPClient.Transport
			downloads := 0
			s.options.HTTPClient = &http.Client{Transport: roundTripper(func(r *http.Request) (*http.Response, error) {
				if tc.outer && strings.HasSuffix(r.URL.Path, "/PARENT") {
					return response(200, `{"id":"PARENT","name":"parent","mimeType":"application/vnd.google-apps.folder","version":"1"}`), nil
				}
				if tc.outer && strings.HasSuffix(r.URL.Path, "/files") && strings.Contains(r.URL.Query().Get("q"), "'PARENT'") {
					return response(200, `{"files":[{"id":"TREE","name":"diagrams","mimeType":"application/vnd.google-apps.folder","parents":["PARENT"],"version":"1"}]}`), nil
				}
				if strings.HasSuffix(r.URL.Path, "/PNG") && r.URL.Query().Get("alt") == "media" {
					downloads++
				}
				return base.RoundTrip(r)
			})}
			ctx := context.Background()
			plan, e := s.Plan(ctx, PlanRequest{c})
			if e != nil {
				t.Fatal(e)
			}
			dest := filepath.Join(t.TempDir(), "bundle")
			result, e := s.Export(ctx, ExportRequest{c, dest})
			if e != nil {
				t.Fatal(e)
			}
			if result.Status != "complete" || !result.Manifest.LocalComplete {
				t.Fatal(result)
			}
			for _, p := range plan.Destinations {
				if _, e := os.Stat(filepath.Join(dest, filepath.FromSlash(p.Path))); e != nil {
					t.Fatalf("missing planned %s: %v", p.Path, e)
				}
			}
			for _, p := range []string{destPath, "architecture/diagrams/system.png"} {
				b, e := os.ReadFile(filepath.Join(dest, p))
				if e != nil || string(b) != string(pngBytes()) {
					t.Fatalf("lost bytes at %s: %v", p, e)
				}
				found := false
				for _, a := range result.Manifest.Assets {
					if a.Path == p {
						found = true
					}
				}
				if !found {
					t.Fatalf("missing manifest destination %s", p)
				}
			}
			if downloads != 2 {
				t.Fatalf("expected document capture plus one supplementary capture, got %d", downloads)
			}
			listing, e := os.ReadFile(filepath.Join(dest, "navigation/diagrams.md"))
			if e != nil || !strings.Contains(string(listing), "../architecture/diagrams/system.png") {
				t.Fatal(string(listing), e)
			}
			target := "../cover.png"
			if tc.outer || tc.samePath {
				target = "diagrams/system.png"
			}
			doc, e := os.ReadFile(filepath.Join(dest, "architecture/overview.md"))
			if e != nil || !strings.Contains(string(doc), target) {
				t.Fatal("wrong canonical navigation", string(doc), e)
			}
			if tc.outer {
				listing, e = os.ReadFile(filepath.Join(dest, "navigation/outer.md"))
				if e != nil || !strings.Contains(string(listing), "../copies/diagrams/system.png") {
					t.Fatal(string(listing), e)
				}
			}
			moved := dest + "-moved"
			if e = os.Rename(dest, moved); e != nil {
				t.Fatal(e)
			}
			offline, _ := New(Options{})
			v, e := offline.Verify(ctx, VerifyRequest{moved})
			if e != nil || !v.LocalComplete {
				t.Fatal(v, e)
			}
		})
	}
}

func TestAssemblyRequiresEveryPlannedDestination(t *testing.T) {
	a := newAssembly(DefaultConfig(), Evidence{})
	image := capturedAsset{bytes: pngBytes(), mime: "image/png", path: "tree/system.png"}
	image.digest = digest(image.bytes)
	if e := a.addAsset(image, nil); e != nil {
		t.Fatal(e)
	}
	p := Plan{Destinations: []Reservation{{Path: "tree/system.png", Kind: "asset"}, {Path: "cover.png", Kind: "asset"}}}
	if e := a.checkPlan(p); e == nil {
		t.Fatal("manifest and files both omitted a required copy but completeness passed")
	}
	image.path = "cover.png"
	if e := a.addAsset(image, nil); e != nil {
		t.Fatal(e)
	}
	if e := a.checkPlan(p); e != nil {
		t.Fatal(e)
	}
}
