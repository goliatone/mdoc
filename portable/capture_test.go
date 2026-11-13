package portable

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type roundTripper func(*http.Request) (*http.Response, error)

func (f roundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func response(code int, b string) *http.Response {
	return &http.Response{StatusCode: code, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(b))}
}

type materializerFunc func(context.Context, *captureDraft) (materializedAssets, error)

func (f materializerFunc) materialize(c context.Context, d *captureDraft) (materializedAssets, error) {
	return f(c, d)
}
func TestCaptureProductionAdapterAttempts(t *testing.T) {
	for _, mode := range []string{"stable", "drift", "expired", "wrong_attempt", "denied", "rotating"} {
		t.Run(mode, func(t *testing.T) {
			reads := 0
			materializations := 0
			client := &http.Client{Transport: roundTripper(func(r *http.Request) (*http.Response, error) {
				if r.Method != "GET" {
					t.Fatal("remote write")
				}
				if strings.Contains(r.URL.Path, "/drive/") {
					if mode == "denied" {
						return response(403, "{}"), nil
					}
					return response(200, `{"id":"DOC","name":"Doc","mimeType":"application/vnd.google-apps.document","version":"1"}`), nil
				}
				reads++
				revision := "one"
				if mode == "drift" {
					revision = fmt.Sprint(reads)
				}
				uri := "https://lh3.googleusercontent.com/private-" + fmt.Sprint(reads)
				raw := fmt.Sprintf(`{"documentId":"DOC","revisionId":%q,"body":{"content":[{"paragraph":{"elements":[{"textRun":{"content":"Hello"}}]}}]},"inlineObjects":{"unused":{"inlineObjectProperties":{"embeddedObject":{"imageProperties":{"contentUri":%q}}}}}}`, revision, uri)
				return response(200, raw), nil
			})}
			s, e := New(Options{HTTPClient: client})
			if e != nil {
				t.Fatal(e)
			}
			c := DefaultConfig()
			mat := materializerFunc(func(_ context.Context, d *captureDraft) (materializedAssets, error) {
				materializations++
				if mode == "expired" && materializations == 1 {
					return materializedAssets{}, fail("image_unavailable", "retry")
				}
				a := materializedAssets{attempt: d.attempt}
				if mode == "wrong_attempt" {
					a.attempt = &attemptID{1}
				}
				return a, nil
			})
			got, e := s.capture(context.Background(), "DOC", c, &budget{max: c.Limits.TotalBytes}, mat)
			bad := mode == "drift" || mode == "wrong_attempt" || mode == "denied"
			if (e != nil) != bad {
				t.Fatalf("%s: %v", mode, e)
			}
			if bad && got.seal != nil {
				t.Fatal("failed capture finalized")
			}
			if mode == "drift" && materializations != 3 {
				t.Fatal(materializations)
			}
			if mode == "expired" && materializations != 2 {
				t.Fatal(materializations)
			}
			b, _ := json.Marshal(got.evidence)
			if strings.Contains(string(b), "private-") {
				t.Fatal("capability leak")
			}
		})
	}
}
func TestNativeUnsupportedAndTable(t *testing.T) {
	s, _ := New(Options{})
	for _, field := range []string{`"equation":{}`, `"person":{}`, `"inlineObjectElement":{"inlineObjectId":"missing"}`} {
		raw := []byte(`{"documentId":"D","body":{"content":[{"paragraph":{"elements":[{` + field + `}]}}]}}`)
		if _, _, _, _, _, _, e := s.native(context.Background(), raw, SourceRef{FileID: "D"}); e == nil {
			t.Fatal(field)
		}
	}
}

func nativeIndexJSON() string {
	rows := [][]string{{"ID", "Document", "Section", "Path", "Role"}, {"PRD", "Product", "", "docs/PRD.md", "document"}, {"DIAGRAMS", "Diagrams", "", "diagrams", "asset-tree"}}
	tableRows := []any{}
	for i, row := range rows {
		cells := []any{}
		for j, value := range row {
			tr := map[string]any{"content": value + "\n"}
			if i > 0 && j == 1 {
				u := "https://docs.google.com/document/d/PRD/edit"
				if i == 2 {
					u = "https://drive.google.com/drive/folders/TREE"
				}
				tr["textStyle"] = map[string]any{"link": map[string]any{"url": u}}
			}
			cells = append(cells, map[string]any{"content": []any{map[string]any{"paragraph": map[string]any{"elements": []any{map[string]any{"textRun": tr}}}}}})
		}
		tableRows = append(tableRows, map[string]any{"tableCells": cells})
	}
	root := map[string]any{"documentId": "INDEX", "revisionId": "rev1", "body": map[string]any{"content": []any{map[string]any{"paragraph": map[string]any{"paragraphStyle": map[string]any{"namedStyleType": "HEADING_1", "headingId": "h.inventory"}, "elements": []any{map[string]any{"textRun": map[string]any{"content": "Bundle inventory\n"}}}}}, map[string]any{"table": map[string]any{"columns": 5, "rows": 3, "tableRows": tableRows}}}}}
	b, _ := json.Marshal(root)
	return string(b)
}
func TestPlanNativeIndexAndPaginatedTree(t *testing.T) {
	pages := 0
	client := &http.Client{Transport: roundTripper(func(r *http.Request) (*http.Response, error) {
		if r.Method != "GET" {
			t.Fatal("write")
		}
		if r.URL.Host == "docs.googleapis.com" {
			return response(200, nativeIndexJSON()), nil
		}
		if strings.HasSuffix(r.URL.Path, "/files") {
			pages++
			if r.URL.Query().Get("pageToken") == "" {
				return response(200, `{"nextPageToken":"next","files":[{"id":"README","name":"README.md","mimeType":"text/markdown","parents":["TREE"]}]}`), nil
			}
			return response(200, `{"files":[{"id":"MANIFEST","name":"manifest.json","mimeType":"application/json","parents":["TREE"]},{"id":"PNG","name":"system.png","mimeType":"image/png","parents":["TREE"]}]}`), nil
		}
		id := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		mime := docMIME
		if id == "TREE" {
			mime = folderMIME
		}
		return response(200, fmt.Sprintf(`{"id":%q,"name":%q,"mimeType":%q,"version":"1"}`, id, id, mime)), nil
	})}
	s, _ := New(Options{HTTPClient: client})
	c := DefaultConfig()
	c.Project = "test"
	c.Index.Source = "INDEX"
	p, e := s.Plan(context.Background(), PlanRequest{c})
	if e != nil {
		t.Fatal(e)
	}
	if pages != 2 {
		t.Fatal(pages)
	}
	seen := map[string]bool{}
	for _, r := range p.Destinations {
		seen[r.Path] = true
	}
	for _, p := range []string{"navigation/diagrams.md", "diagrams/README.md", "diagrams/manifest.json", "diagrams/system.png"} {
		if !seen[p] {
			t.Fatal(p)
		}
	}
}

func TestIndexRetryDiscardsTentativeMembershipAndCancellation(t *testing.T) {
	ctx := context.Background()
	s, _ := New(Options{})
	c := DefaultConfig()
	source := filepath.Join(t.TempDir(), "index.md")
	if e := os.WriteFile(source, []byte(inventory), 0600); e != nil {
		t.Fatal(e)
	}
	calls := 0
	mat := materializerFunc(func(_ context.Context, d *captureDraft) (materializedAssets, error) {
		calls++
		if calls == 1 {
			updated := strings.ReplaceAll(inventory, "PRD", "NEW")
			if e := os.WriteFile(source, []byte(updated), 0600); e != nil {
				t.Fatal(e)
			}
		}
		return materializedAssets{attempt: d.attempt}, nil
	})
	got, e := s.capture(ctx, source, c, &budget{max: c.Limits.TotalBytes}, mat)
	if e != nil {
		t.Fatal(e)
	}
	entries, e := parseInventory(got.model, c)
	if e != nil || entries[0].ID != "NEW" || calls != 2 {
		t.Fatal(entries, e, calls)
	}
	canceled, cancel := context.WithCancel(ctx)
	mat = materializerFunc(func(_ context.Context, d *captureDraft) (materializedAssets, error) {
		cancel()
		return materializedAssets{attempt: d.attempt}, nil
	})
	got, e = s.capture(canceled, source, c, &budget{max: c.Limits.TotalBytes}, mat)
	if e == nil || got.seal != nil {
		t.Fatal("canceled attempt finalized")
	}
}
