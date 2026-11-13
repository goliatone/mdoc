package contribution

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/goliatone/mdoc/workingdraft"
)

func nativeFixture(rev string, contents []string, styles []any) map[string]any {
	elements := []any{}
	pos := int64(1)
	for i, c := range contents {
		tr := map[string]any{"content": c}
		if i < len(styles) && styles[i] != nil {
			tr["textStyle"] = styles[i]
		}
		elements = append(elements, map[string]any{"startIndex": pos, "endIndex": pos + units(c), "textRun": tr})
		pos += units(c)
	}
	return map[string]any{"documentId": "doc", "title": "Draft", "revisionId": rev, "tabs": []any{map[string]any{"tabProperties": map[string]any{"tabId": "t.0"}, "documentTab": map[string]any{"body": map[string]any{"content": []any{map[string]any{"startIndex": 1, "endIndex": pos, "paragraph": map[string]any{"elements": elements}}}}}}}}
}
func TestUpdateNativeBoundary(t *testing.T) {
	ctx := context.Background()
	rev := "r1"
	contents := []string{"abcd\n"}
	var styles []any
	writes := 0
	var batch map[string]any
	s, e := New(Options{AccountBinding: "a", HTTPClient: &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method == "POST" {
			writes++
			b, _ := io.ReadAll(r.Body)
			if json.Unmarshal(b, &batch) != nil {
				t.Fatal("invalid batch")
			}
			return jsonResponse(map[string]any{"documentId": "doc"}), nil
		}
		if strings.Contains(r.URL.Host, "docs.") {
			return jsonResponse(nativeFixture(rev, contents, styles)), nil
		}
		return jsonResponse(map[string]any{"id": "doc", "name": "Draft", "version": "1", "mimeType": nativeDoc, "capabilities": map[string]bool{"canEdit": true}}), nil
	})}})
	if e != nil {
		t.Fatal(e)
	}
	input := func(a, b int, replacement string) UpdateInput {
		all := strings.TrimSuffix(strings.Join(contents, ""), "\n")
		return UpdateInput{Meta: testMeta(), Source: workingdraft.SourceRef{DocumentID: "doc"}, ExpectedRevision: "r1", Selection: TextSelection{ParagraphStart: 1, Start: a, End: b, ExpectedText: string([]rune(all)[a:b]), ParagraphDigest: ParagraphDigest(all)}, Replacement: replacement}
	}
	for _, sel := range [][2]int{{0, 4}, {0, 0}, {4, 4}} {
		if _, e := s.PrepareUpdate(ctx, input(sel[0], sel[1], "x")); e == nil {
			t.Fatalf("accepted edge %v", sel)
		}
	}
	for _, v := range []string{"\ue000", "\uf8ff", "\n", "\u2028", "\x01"} {
		if _, e := s.PrepareUpdate(ctx, input(1, 3, v)); e == nil {
			t.Fatalf("accepted %q", v)
		}
	}
	for _, v := range []string{"", "\ud7ff", "\uf900", "\t", "😀"} {
		p, e := s.PrepareUpdate(ctx, input(1, 3, v))
		if e != nil {
			t.Fatalf("%q: %v", v, e)
		}
		o, e := s.ApplyUpdate(ctx, UpdateRequest{p, p.Digest, Attempt{"apply", true}})
		if e != nil || o.Status != Committed {
			t.Fatalf("%+v %v", o, e)
		}
		req := batch["requests"].([]any)
		del := object(req[0].(map[string]any), "deleteContentRange")
		rg := object(del, "range")
		if number(rg, "startIndex") != 2 || number(rg, "endIndex") != 4 || text(rg, "tabId") != "t.0" {
			t.Fatal(batch)
		}
		if text(object(batch, "writeControl"), "requiredRevisionId") != "r1" {
			t.Fatal(batch)
		}
		if v != "" && text(object(req[1].(map[string]any), "insertText"), "text") != v {
			t.Fatal(batch)
		}
	}
	p, e := s.PrepareUpdate(ctx, input(2, 2, "insert"))
	if e != nil {
		t.Fatal(e)
	}
	before := writes
	rev = "r2"
	if o, e := s.ApplyUpdate(ctx, UpdateRequest{p, p.Digest, Attempt{"stale", true}}); e == nil || o.Status != Conflict || writes != before {
		t.Fatal("stale write")
	}
	rev = "r1"
	p.Run.StyleDigest = digest("tampered")
	p.Digest = updateDigest(p)
	if _, e := s.ApplyUpdate(ctx, UpdateRequest{p, p.Digest, Attempt{"bad", true}}); e == nil || writes != before {
		t.Fatal("trusted altered evidence")
	}
	p.Input.Replacement = "\ue000"
	p.Digest = updateDigest(p)
	if o, e := s.ApplyUpdate(ctx, UpdateRequest{p, p.Digest, Attempt{"bad", true}}); e == nil || o.Code != UnsupportedText || writes != before {
		t.Fatal("private-use apply")
	}
	contents = []string{"a ", "word", " z\n"}
	styles = []any{nil, map[string]any{"bold": true}, nil}
	for _, sel := range [][2]int{{2, 6}, {2, 2}, {6, 6}} {
		if _, e := s.PrepareUpdate(ctx, input(sel[0], sel[1], "x")); e == nil {
			t.Fatal("styled edge")
		}
	}
	if _, e := s.PrepareUpdate(ctx, input(3, 5, "x")); e != nil {
		t.Fatal(e)
	}
	styles[1] = map[string]any{"foregroundColor": map[string]any{"color": map[string]any{"rgbColor": map[string]any{"red": 1}}}}
	if _, e := s.PrepareUpdate(ctx, input(2, 6, "x")); e == nil {
		t.Fatal("color boundary")
	}
	styles[1] = map[string]any{"link": map[string]any{"url": "https://example.com"}}
	for _, sel := range [][2]int{{2, 2}, {6, 6}, {3, 5}} {
		if _, e := s.PrepareUpdate(ctx, input(sel[0], sel[1], "x")); e == nil {
			t.Fatal("link boundary")
		}
	}
	contents = []string{"ab", "cd\n"}
	styles = nil
	if _, e := s.PrepareUpdate(ctx, input(1, 3, "x")); e == nil {
		t.Fatal("merged adjacent original runs")
	}
	contents = []string{"a😀bcd\n"}
	styles = nil
	p, e = s.PrepareUpdate(ctx, input(2, 3, "x"))
	if e != nil || p.Start != 4 || p.End != 5 {
		t.Fatalf("astral indices: %+v %v", p, e)
	}
	contents = []string{"\n"}
	if _, e := s.PrepareUpdate(ctx, input(0, 0, "x")); e == nil {
		t.Fatal("empty paragraph")
	}
}
