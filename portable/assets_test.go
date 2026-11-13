package portable

import (
	"bytes"
	"context"
	"fmt"
	"google.golang.org/api/drive/v3"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"strings"
	"testing"
)

func pngBytes() []byte {
	im := image.NewRGBA(image.Rect(0, 0, 2, 2))
	im.Set(0, 0, color.RGBA{20, 80, 140, 255})
	var b bytes.Buffer
	png.Encode(&b, im)
	return b.Bytes()
}
func nativeImageJSON(rev, capability string) string {
	return fmt.Sprintf(`{"documentId":"DOC","revisionId":%q,"body":{"content":[{"paragraph":{"elements":[{"inlineObjectElement":{"inlineObjectId":"image"}},{"inlineObjectElement":{"inlineObjectId":"image"}}]}}]},"inlineObjects":{"image":{"inlineObjectProperties":{"embeddedObject":{"description":"Architecture","title":"Figure one","imageProperties":{"contentUri":%q}}}}}}`, rev, capability)
}
func TestNativeImageFinalization(t *testing.T) {
	for _, mode := range []string{"stable", "expired", "drift", "missing"} {
		t.Run(mode, func(t *testing.T) {
			reads, fetches := 0, 0
			raw := pngBytes()
			api := &http.Client{Transport: roundTripper(func(r *http.Request) (*http.Response, error) {
				if r.URL.Host == "docs.googleapis.com" {
					reads++
					rev := "one"
					if mode == "drift" && reads == 2 {
						rev = "two"
					}
					return response(200, nativeImageJSON(rev, fmt.Sprintf("https://lh3.googleusercontent.com/secret-%d", reads))), nil
				}
				return response(200, `{"id":"DOC","name":"Doc","mimeType":"application/vnd.google-apps.document","version":"1"}`), nil
			})}
			images := &http.Client{Transport: roundTripper(func(r *http.Request) (*http.Response, error) {
				fetches++
				if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
					t.Fatal("credentials leaked")
				}
				if mode == "expired" && fetches == 1 || mode == "missing" {
					return response(403, "secret"), nil
				}
				res := response(200, string(raw))
				res.Header.Set("Content-Type", "image/png")
				return res, nil
			})}
			s, _ := New(Options{HTTPClient: api, ImageHTTPClient: images})
			c := DefaultConfig()
			b := &budget{max: c.Limits.TotalBytes}
			mat := &assetMaterializer{s: s, c: c, cat: newCatalog(), transfer: b}
			got, e := s.capture(context.Background(), "DOC", c, b, mat)
			if mode == "missing" {
				if e == nil || got.seal != nil || fetches != 3 {
					t.Fatal(e, fetches)
				}
				if strings.Contains(e.Error(), "secret") {
					t.Fatal("capability leak")
				}
				return
			}
			if e != nil {
				t.Fatal(e)
			}
			if len(got.assets) != 1 || len(got.occurrences) != 2 {
				t.Fatal(got)
			}
			if mode == "stable" && fetches != 1 || mode != "stable" && fetches != 2 {
				t.Fatal(fetches)
			}
			p, e := assetPath(c, "docs/doc.md", got.assets[0])
			if e != nil || !strings.HasPrefix(p, "docs/assets/doc/image-") {
				t.Fatal(p, e)
			}
			if got.occurrences[0].Caption != "Figure one" {
				t.Fatal(got.occurrences)
			}
		})
	}
}
func TestRelativeAssetAndValidation(t *testing.T) {
	cat := newCatalog()
	cat.files["TREE"] = &drive.File{Id: "TREE", Name: "diagrams", Parents: []string{"PARENT"}}
	cat.files["PNG"] = &drive.File{Id: "PNG", Name: "system.png", Parents: []string{"TREE"}}
	cat.linkPaths["PNG"] = "relocated/system.png"
	source := Evidence{Source: SourceRef{FileID: "MD", Kind: "drive"}, Parents: []string{"PARENT"}}
	s, _ := New(Options{})
	ref, e := s.resolveAssetReference(context.Background(), "diagrams/system.png", source, cat)
	if e != nil || ref.FileID != "PNG" {
		t.Fatal(e, ref)
	}
	cat.files["OTHER"] = &drive.File{Id: "OTHER", Name: "system.png", Parents: []string{"TREE"}}
	if _, e = s.resolveAssetReference(context.Background(), "diagrams/system.png", source, cat); e == nil {
		t.Fatal("ambiguous image accepted")
	}
	if _, e = validateImage(pngBytes(), "image/jpeg"); e == nil {
		t.Fatal("wrong MIME")
	}
	for _, u := range []string{"http://lh3.googleusercontent.com/x", "https://evil.googleusercontent.com.evil/x", "https://127.0.0.1/x"} {
		if imageURL(u) == nil {
			t.Fatal(u)
		}
	}
	if _, e = validateImage([]byte(`<svg xmlns="http://www.w3.org/2000/svg" width="2" height="2"><script>alert(1)</script></svg>`), "image/svg+xml"); e == nil {
		t.Fatal("active SVG")
	}
}
func TestAssetCollisionUsesFullIdentity(t *testing.T) {
	a := capturedAsset{bytes: pngBytes(), mime: "image/png", path: "image.png"}
	a.digest = digest(a.bytes)
	b := a
	if e := coalesceAsset(a, b); e != nil {
		t.Fatal(e)
	}
	b.bytes = []byte("different")
	b.digest = digest(b.bytes)
	if coalesceAsset(a, b) == nil {
		t.Fatal("different bytes coalesced")
	}
	b = a
	b.bytes = append([]byte(nil), a.bytes...)
	b.bytes[0]++
	if coalesceAsset(a, b) == nil {
		t.Fatal("equal declared hash bypassed byte equality")
	}
	c := DefaultConfig()
	c.Layout.Assets = "assets"
	b = a
	b.digest = a.digest[:8] + strings.Repeat("0", 56)
	a.path = ""
	b.path = ""
	p, _ := assetPath(c, "a.md", a)
	q, _ := assetPath(c, "b.md", b)
	if p != q || coalesceAsset(a, b) == nil {
		t.Fatal("short hash collision not detected")
	}
}

func TestSVGRejectsIndirectExternalReferences(t *testing.T) {
	for _, body := range []string{`<use href="#a" xml:base="https://example.test/"/>`, `<path style="fill:u\72l(https://example.test/image)"/>`, `<path fill="url(https://example.test/image)"/>`} {
		raw := []byte(`<svg xmlns="http://www.w3.org/2000/svg" width="2" height="2">` + body + `</svg>`)
		if validateSVG(raw) == nil {
			t.Fatal(body)
		}
	}
}
