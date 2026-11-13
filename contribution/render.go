package contribution

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"io"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"time"
	"unicode/utf8"
)

type pandocRenderer struct{}
type boundedBuffer struct {
	bytes.Buffer
	limit int
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	if b.Len()+len(p) > b.limit {
		return 0, fail(LimitExceeded, "rendered output exceeds limit")
	}
	return b.Buffer.Write(p)
}
func pandoc(ctx context.Context, dir string, input []byte, limit int, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	c := exec.CommandContext(ctx, "pandoc", args...)
	c.Dir = dir
	c.Stdin = bytes.NewReader(input)
	out := &boundedBuffer{limit: limit}
	c.Stdout = out
	c.Stderr = io.Discard
	if c.Run() != nil {
		return nil, fail(UnsupportedContent, "Pandoc failed or is unavailable")
	}
	return out.Bytes(), nil
}
func supportedMarkdown(ctx context.Context, dir, md string) ([]byte, error) {
	if dir == "" {
		private, e := os.MkdirTemp("", "mdoc-contribution-parse-*")
		if e != nil {
			return nil, fail(Unavailable, "cannot create private parsing directory")
		}
		defer os.RemoveAll(private)
		dir = private
	}
	if len(md) > MaxMarkdown || !utf8.ValidString(md) {
		return nil, fail(InvalidInput, "invalid or oversized Markdown")
	}
	ast, e := pandoc(ctx, dir, []byte(md), 8*MaxMarkdown, "--from=markdown", "--to=json")
	if e != nil {
		return nil, e
	}
	var tree map[string]any
	if json.Unmarshal(ast, &tree) != nil {
		return nil, fail(UnsupportedContent, "invalid Pandoc output")
	}
	if meta, ok := tree["meta"].(map[string]any); !ok || len(meta) != 0 {
		return nil, fail(UnsupportedContent, "Markdown metadata is not supported")
	}
	if e = checkAST(tree["blocks"]); e != nil {
		return nil, e
	}
	return ast, nil
}
func safeLink(s string) bool {
	u, e := url.Parse(s)
	return e == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Host != "" && u.User == nil
}
func checkAST(v any) error {
	switch n := v.(type) {
	case []any:
		for _, x := range n {
			if e := checkAST(x); e != nil {
				return e
			}
		}
	case map[string]any:
		if t, ok := n["t"].(string); ok {
			switch t {
			case "Para", "Plain", "Header", "BulletList", "OrderedList", "Str", "Space", "SoftBreak", "Emph", "Strong", "Link", "Decimal", "DefaultStyle", "DefaultDelim", "Period", "OneParen":
			default:
				return fail(UnsupportedContent, "Markdown contains an unsupported structure")
			}
			if t == "Link" {
				c, ok := n["c"].([]any)
				if !ok || len(c) != 3 {
					return fail(UnsupportedContent, "invalid link")
				}
				target, ok := c[2].([]any)
				if !ok || len(target) != 2 {
					return fail(UnsupportedContent, "invalid link")
				}
				s, ok := target[0].(string)
				if !ok || !safeLink(s) {
					return fail(UnsupportedContent, "only explicit http/https links are supported")
				}
			}
		}
		for _, x := range n {
			if e := checkAST(x); e != nil {
				return e
			}
		}
	}
	return nil
}
func (pandocRenderer) Render(ctx context.Context, md string) (Artifact, error) {
	dir, e := os.MkdirTemp("", "mdoc-contribution-*")
	if e != nil {
		return Artifact{}, fail(Unavailable, "cannot create private rendering directory")
	}
	defer os.RemoveAll(dir)
	ast, e := supportedMarkdown(ctx, dir, md)
	if e != nil {
		return Artifact{}, e
	}
	version, e := pandoc(ctx, dir, nil, 4096, "--version")
	if e != nil {
		return Artifact{}, e
	}
	b, e := pandoc(ctx, dir, ast, MaxDOCX, "--from=json", "--to=docx", "--sandbox")
	if e != nil {
		return Artifact{}, e
	}
	return Artifact{DOCX: b, Digest: hash(b), RendererVersion: strings.SplitN(string(version), "\n", 2)[0]}, nil
}
func validateArtifact(a Artifact) error {
	if len(a.DOCX) == 0 || len(a.DOCX) > MaxDOCX || a.Digest != hash(a.DOCX) || a.RendererVersion == "" {
		return fail(InvalidPlan, "invalid rendered artifact")
	}
	z, e := zip.NewReader(bytes.NewReader(a.DOCX), int64(len(a.DOCX)))
	if e != nil || len(z.File) > 1000 {
		return fail(UnsupportedContent, "invalid DOCX archive")
	}
	seen := map[string]bool{}
	total := uint64(0)
	for _, f := range z.File {
		if seen[f.Name] || strings.Contains(f.Name, "..") || strings.HasPrefix(f.Name, "/") {
			return fail(UnsupportedContent, "unsafe DOCX archive path")
		}
		seen[f.Name] = true
		total += f.UncompressedSize64
		if total > 40<<20 || f.UncompressedSize64 > 10<<20 {
			return fail(LimitExceeded, "expanded DOCX exceeds limit")
		}
		if strings.Contains(f.Name, "/media/") || strings.Contains(f.Name, "/embeddings/") || strings.Contains(strings.ToLower(f.Name), "vba") {
			return fail(UnsupportedContent, "DOCX contains embedded resources")
		}
		r, e := f.Open()
		if e != nil {
			return fail(UnsupportedContent, "invalid DOCX member")
		}
		b, e := io.ReadAll(io.LimitReader(r, 10<<20+1))
		r.Close()
		if e != nil || len(b) > 10<<20 {
			return fail(UnsupportedContent, "invalid DOCX member")
		}
		if strings.HasSuffix(f.Name, ".rels") {
			var rel struct {
				Items []struct {
					Type   string `xml:"Type,attr"`
					Target string `xml:"Target,attr"`
					Mode   string `xml:"TargetMode,attr"`
				} `xml:"Relationship"`
			}
			if xml.Unmarshal(b, &rel) != nil {
				return fail(UnsupportedContent, "invalid DOCX relationships")
			}
			for _, v := range rel.Items {
				if v.Mode != "" && v.Mode != "Internal" && v.Mode != "External" {
					return fail(UnsupportedContent, "unknown DOCX relationship mode")
				}
				if v.Mode != "External" {
					target, e := url.Parse(v.Target)
					if e != nil || target.IsAbs() || target.Host != "" || strings.HasPrefix(v.Target, "/") || strings.Contains(v.Target, "\\") {
						return fail(UnsupportedContent, "unsafe internal DOCX relationship")
					}
				}
				if v.Mode == "External" && (v.Type != "http://schemas.openxmlformats.org/officeDocument/2006/relationships/hyperlink" || !safeLink(v.Target)) {
					return fail(UnsupportedContent, "DOCX contains external resources")
				}
			}
		}
	}
	if !seen["word/document.xml"] || !seen["[Content_Types].xml"] {
		return fail(UnsupportedContent, "DOCX document parts missing")
	}
	return nil
}
