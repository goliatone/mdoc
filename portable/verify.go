package portable

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path"
	"strings"
)

func (s *Service) Verify(ctx context.Context, r VerifyRequest) (Verification, error) {
	v := Verification{}
	root, e := os.OpenRoot(r.Directory)
	if e != nil {
		return v, fail("invalid_bundle", "cannot open bundle directory")
	}
	defer root.Close()
	if st, e := root.Lstat("manifest.json"); e != nil || !st.Mode().IsRegular() {
		return v, fail("invalid_bundle", "manifest must be a regular file")
	}
	f, e := openBundleFile(root, "manifest.json")
	if e != nil {
		return v, e
	}
	b, e := readBounded(f, 50<<20)
	f.Close()
	if e != nil {
		return v, e
	}
	var m Manifest
	decoder := json.NewDecoder(bytes.NewReader(b))
	decoder.DisallowUnknownFields()
	if e = decoder.Decode(&m); e != nil {
		return v, fail("invalid_bundle", "invalid manifest")
	}
	var trailing any
	if decoder.Decode(&trailing) != io.EOF {
		return v, fail("invalid_bundle", "manifest contains trailing data")
	}
	files := map[string][]byte{}
	var total int64
	e = fs.WalkDir(root.FS(), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if e := ctx.Err(); e != nil {
			return e
		}
		if d.Type()&os.ModeSymlink != 0 {
			return fail("path_escape", "symlinks are not allowed in bundles")
		}
		if d.Type() != 0 && !d.IsDir() {
			return fail("path_escape", "nonregular bundle file")
		}
		if d.IsDir() {
			return nil
		}
		if p == "manifest.json" {
			return nil
		}
		if len(files) >= 20000 {
			return fail("limit_exceeded", "too many bundle files")
		}
		f, e := openBundleFile(root, p)
		if e != nil {
			return e
		}
		defer f.Close()
		st, e := f.Stat()
		if e != nil || !st.Mode().IsRegular() {
			return fail("path_escape", "nonregular bundle file")
		}
		b, e := readBounded(f, (2<<30)-total)
		if e != nil {
			return e
		}
		total += int64(len(b))
		files[p] = b
		return nil
	})
	if e != nil {
		return v, e
	}
	v, e = s.verifyFiles(ctx, m, files)
	if e != nil {
		return v, e
	}
	if !m.LocalComplete || m.NoOnlineDependencies != v.NoOnlineDependencies {
		return Verification{}, fail("invalid_bundle", "manifest completeness assertions disagree with files")
	}
	return v, nil
}
func (s *Service) verifyFiles(ctx context.Context, m Manifest, files map[string][]byte) (Verification, error) {
	v := Verification{LocalComplete: true, NoOnlineDependencies: true}
	if m.SchemaVersion != 1 || m.ConversionVersion != ConversionVersion || m.Project == "" || len(m.Documents) == 0 {
		return Verification{}, fail("invalid_bundle", "unsupported or empty manifest")
	}
	expected := map[string]string{}
	markdownPaths := map[string]bool{}
	var reservations []Reservation
	add := func(p, hash string) error {
		if len(hash) != 64 || expected[p] != "" {
			return fail("invalid_bundle", "duplicate or missing file hash")
		}
		if e := reserve(&reservations, Reservation{Path: p, Kind: "file"}); e != nil {
			return e
		}
		expected[p] = hash
		return nil
	}
	for _, d := range append([]DocumentRecord{m.Index}, m.Documents...) {
		markdownPaths[d.Path] = true
		if e := add(d.Path, d.ContentDigest); e != nil {
			return Verification{}, e
		}
	}
	for _, f := range m.GeneratedFiles {
		markdownPaths[f.Path] = true
		if e := add(f.Path, f.ContentDigest); e != nil {
			return Verification{}, e
		}
	}
	for _, a := range m.Assets {
		if e := add(a.Path, a.Digest); e != nil {
			return Verification{}, e
		}
		b, ok := files[a.Path]
		if !ok || int64(len(b)) != a.Size {
			return Verification{}, fail("invalid_bundle", "missing asset or size mismatch at %s", a.Path)
		}
		if strings.HasPrefix(a.MIME, "image/") {
			if len(b) > 100<<20 {
				return Verification{}, fail("limit_exceeded", "image too large")
			}
			if _, e := validateImage(b, a.MIME); e != nil {
				return Verification{}, e
			}
			if !imagePathOK(a.Path, a.MIME) {
				return Verification{}, fail("invalid_bundle", "image extension mismatch")
			}
		}
	}
	if len(files) != len(expected) {
		return Verification{}, fail("invalid_bundle", "unexpected or missing bundle files")
	}
	models := map[string]map[string]any{}
	anchors := map[string]map[string]bool{}
	for p, b := range files {
		if digest(b) != expected[p] {
			return Verification{}, fail("invalid_bundle", "content hash mismatch at %s", p)
		}
		if markdownPaths[p] || strings.EqualFold(path.Ext(p), ".md") {
			if len(b) > 50<<20 {
				return Verification{}, fail("limit_exceeded", "Markdown file too large")
			}
			model, e := s.converter.Parse(ctx, b)
			if e != nil {
				return Verification{}, e
			}
			if e = checkModel(model, 10000); e != nil {
				return Verification{}, e
			}
			models[p] = model
			anchors[p] = map[string]bool{}
			walk(model, func(n map[string]any) {
				if n["t"] == "Header" {
					c := arr(n["c"])
					anchors[p][str(arr(c[1])[0])] = true
				}
			})
		}
	}
	for p, model := range models {
		var failure error
		walk(model, func(n map[string]any) {
			if failure != nil || n["t"] != "Link" && n["t"] != "Image" {
				return
			}
			t := target(n)
			u, e := url.Parse(t)
			if e != nil {
				failure = fail("invalid_bundle", "invalid link in %s", p)
				return
			}
			if u.IsAbs() || u.Host != "" {
				if n["t"] == "Image" {
					failure = fail("missing_asset", "online image in %s", p)
					return
				}
				v.NoOnlineDependencies = false
				v.Diagnostics = append(v.Diagnostics, Diagnostic{Code: "online_reference", Severity: "info", DocumentID: p, Reference: t, Message: "Navigation requires an online resource."})
				return
			}
			if strings.HasPrefix(u.Path, "/") || strings.Contains(u.Path, "\\") || u.RawQuery != "" {
				failure = fail("path_escape", "unsafe local link in %s", p)
				return
			}
			dest := p
			if u.Path != "" {
				dest = path.Clean(path.Join(path.Dir(p), u.Path))
			}
			if e = safePath(dest); e != nil {
				failure = e
				return
			}
			b, ok := files[dest]
			if !ok {
				failure = fail("missing_asset", "broken local link from %s to %s", p, dest)
				return
			}
			if u.Fragment != "" && !anchors[dest][u.Fragment] {
				failure = fail("unresolved_fragment", "missing local anchor in %s", dest)
				return
			}
			if n["t"] == "Image" {
				declared := ""
				for _, a := range m.Assets {
					if a.Path == dest {
						declared = a.MIME
						break
					}
				}
				if _, e := validateImage(b, declared); e != nil {
					failure = e
				}
			}
		})
		if failure != nil {
			return Verification{}, failure
		}
	}
	return v, nil
}
