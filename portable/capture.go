package portable

import (
	"context"
	"encoding/json"
	"errors"
	"google.golang.org/api/drive/v3"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
)

type budget struct {
	used int64
	max  int64
}

func (b *budget) add(n int64) error {
	if n < 0 || n > b.max-b.used {
		return fail("limit_exceeded", "cumulative transfer budget exceeded")
	}
	b.used += n
	return nil
}
func fileEvidence(f *drive.File, ref SourceRef) Evidence {
	parents := append([]string(nil), f.Parents...)
	sort.Strings(parents)
	return Evidence{Source: ref, CanonicalURL: canonical(ref), Name: f.Name, MIME: f.MimeType, Version: f.Version, Parents: parents}
}
func validateFile(f *drive.File, id string) error {
	if f == nil || !idPattern.MatchString(id) || f.Id != id || f.Trashed {
		return fail("access_denied", "source metadata unavailable")
	}
	if f.DriveId != "" || f.MimeType == "application/vnd.google-apps.shortcut" {
		return fail("unsupported_content", "Shared Drives and shortcuts are unsupported")
	}
	if f.Capabilities != nil && !f.Capabilities.CanDownload && f.MimeType != folderMIME {
		return fail("access_denied", "source download is not permitted")
	}
	return nil
}
func sameEvidence(a, b Evidence) bool {
	return a.Source == b.Source && a.Name == b.Name && a.MIME == b.MIME && a.Version == b.Version && a.Revision == b.Revision && a.SourceDigest == b.SourceDigest && reflect.DeepEqual(a.Parents, b.Parents)
}
func (s *Service) readDraft(ctx context.Context, source string, c Config, transfer *budget) (*captureDraft, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	d := &captureDraft{attempt: &attemptID{1}, handles: map[string]string{}, headings: map[string]string{}}
	var raw []byte
	ref, e := ParseSource(source)
	if e != nil {
		if strings.Contains(source, "://") || !strings.EqualFold(filepath.Ext(source), ".md") {
			return nil, e
		}
		f, e := os.Open(source)
		if e != nil {
			return nil, fail("access_denied", "cannot read local index")
		}
		defer f.Close()
		st, e := f.Stat()
		if e != nil || !st.Mode().IsRegular() {
			return nil, fail("invalid_source", "local index must be a regular Markdown file")
		}
		raw, e = readBounded(f, c.Limits.SourceBytes)
		if e != nil {
			return nil, e
		}
		d.evidence = Evidence{Source: SourceRef{FileID: filepath.Base(source), Kind: "local"}, Name: filepath.Base(source), MIME: "text/markdown"}
	} else {
		if e = s.requireProvider(ctx); e != nil {
			return nil, e
		}
		s.sourceAccess(source)
		f, e := s.provider.File(ctx, ref.FileID)
		if e != nil {
			return nil, safeProviderError(e)
		}
		if e = validateFile(f, ref.FileID); e != nil {
			return nil, e
		}
		d.evidence = fileEvidence(f, ref)
		switch f.MimeType {
		case docMIME:
			raw, e = s.provider.Document(ctx, ref.FileID, c.Limits.SourceBytes)
		case "text/markdown", "text/x-markdown", "text/plain":
			if !strings.HasSuffix(strings.ToLower(f.Name), ".md") && f.MimeType == "text/plain" {
				return nil, fail("unsupported_content", "plain text source must have .md extension")
			}
			raw, e = s.provider.Download(ctx, ref.FileID, c.Limits.SourceBytes)
		default:
			return nil, fail("unsupported_content", "document must be native Docs or stored Markdown")
		}
		if e != nil {
			return nil, safeProviderError(e)
		}
	}
	if int64(len(raw)) > c.Limits.SourceBytes {
		return nil, fail("limit_exceeded", "source too large")
	}
	if e = transfer.add(int64(len(raw))); e != nil {
		return nil, e
	}
	if d.evidence.MIME == docMIME {
		m, h, heads, hash, rev, resolved, e := s.native(ctx, raw, ref)
		if e != nil {
			return nil, e
		}
		d.model = m
		d.handles = h
		d.headings = heads
		d.evidence.Source = resolved
		d.evidence.CanonicalURL = canonical(resolved)
		d.evidence.Revision = rev
		d.evidence.SourceDigest = hash
	} else {
		d.model, e = s.converter.Parse(ctx, raw)
		if e != nil {
			return nil, e
		}
		d.evidence.SourceDigest = digest(raw)
	}
	if e = checkModel(d.model, c.Limits.Assets); e != nil {
		return nil, e
	}
	return d, nil
}
func checkModel(m map[string]any, maxImages int) error {
	var err error
	count := 0
	walk(m, func(n map[string]any) {
		switch n["t"] {
		case "RawInline", "RawBlock", "Note", "Math":
			err = fail("unsupported_content", "raw embeds, footnotes and math are unsupported")
		case "Image":
			count++
		}
	})
	if count > maxImages {
		return fail("limit_exceeded", "image occurrence limit exceeded")
	}
	return err
}
func (s *Service) capture(ctx context.Context, source string, c Config, transfer *budget, mat materializer) (finalizedCapture, error) {
	var last error
	for range c.Limits.Attempts {
		start := s.now().UTC()
		d, e := s.readDraft(ctx, source, c, transfer)
		if e != nil {
			last = e
			if retryCapture(e) {
				continue
			}
			return finalizedCapture{}, e
		}
		d.evidence.CapturedFrom = start
		a, e := mat.materialize(ctx, d)
		if e != nil {
			last = e
			if retryCapture(e) {
				continue
			}
			return finalizedCapture{}, e
		}
		if a.attempt != d.attempt {
			return finalizedCapture{}, fail("capture_integrity", "materializer returned a different attempt")
		}
		if e = validateMaterialization(d, a); e != nil {
			return finalizedCapture{}, e
		}
		after, e := s.readDraft(ctx, source, c, transfer)
		if e != nil {
			last = e
			if retryCapture(e) {
				continue
			}
			return finalizedCapture{}, e
		}
		if !sameEvidence(d.evidence, after.evidence) {
			last = fail("unstable_source", "source changed during capture")
			continue
		}
		drift := false
		for _, asset := range a.assets {
			if asset.evidence.Source.Kind != "drive" {
				continue
			}
			f, e := s.provider.File(ctx, asset.evidence.Source.FileID)
			if e != nil {
				return finalizedCapture{}, safeProviderError(e)
			}
			if e = validateFile(f, asset.evidence.Source.FileID); e != nil {
				return finalizedCapture{}, e
			}
			ev := fileEvidence(f, asset.evidence.Source)
			ev.SourceDigest = asset.evidence.SourceDigest
			if !sameEvidence(ev, asset.evidence) {
				drift = true
				break
			}
		}
		if drift {
			last = fail("unstable_source", "asset changed during capture")
			continue
		}
		if e = ctx.Err(); e != nil {
			return finalizedCapture{}, e
		}
		d.evidence.CapturedTo = s.now().UTC()
		for i := range a.assets {
			if a.assets[i].evidence.Source.Kind == "embedded" {
				a.assets[i].evidence.CapturedTo = d.evidence.CapturedTo
			}
		}
		// Finalized image references are opaque bindings, never retrieval URLs.
		// Do this only after validating the original model and all source evidence.
		position := 0
		walk(d.model, func(n map[string]any) {
			if n["t"] == "Image" {
				setTarget(n, a.occurrences[position].ObjectID)
				position++
			}
		})
		return finalizedCapture{seal: d.attempt, evidence: d.evidence, model: d.model, assets: a.assets, occurrences: a.occurrences, headings: d.headings}, nil
	}
	if last == nil {
		last = fail("unstable_source", "capture attempts exhausted")
	}
	return finalizedCapture{}, last
}
func validateMaterialization(d *captureDraft, a materializedAssets) error {
	expected := []Occurrence{}
	identities := map[string]string{}
	walk(d.model, func(n map[string]any) {
		if n["t"] == "Image" {
			c := arr(n["c"])
			caption := ""
			if t := arr(c[2]); len(t) > 1 {
				caption = str(t[1])
			}
			expected = append(expected, Occurrence{ObjectID: imageIdentity(target(n), len(expected), identities), Alt: textOf(c[1]), Caption: caption})
		}
	})
	if len(a.occurrences) != len(expected) {
		return fail("capture_integrity", "required image mapping is incomplete")
	}
	assets := map[string]capturedAsset{}
	for _, v := range a.assets {
		if len(v.bytes) == 0 || digest(v.bytes) != v.digest || v.mime == "" || v.objectID == "" {
			return fail("capture_integrity", "invalid materialized bytes")
		}
		if _, exists := assets[v.objectID]; exists {
			return fail("capture_integrity", "duplicate materialized identity")
		}
		assets[v.objectID] = v
	}
	used := map[string]bool{}
	for i, o := range a.occurrences {
		if o.Position != i || o.SourceDigest != d.evidence.SourceDigest || o.DocumentID != d.evidence.Source.FileID || o.ObjectID != expected[i].ObjectID || o.Alt != expected[i].Alt || o.Caption != expected[i].Caption {
			return fail("capture_integrity", "invalid image occurrence")
		}
		asset, ok := assets[o.ObjectID]
		if !ok {
			return fail("capture_integrity", "image mapping has no bytes")
		}
		if strings.HasPrefix(o.ObjectID, "mdoc-object:") && (asset.evidence.Source.FileID != d.evidence.Source.FileID || asset.evidence.Source.ObjectID != strings.TrimPrefix(o.ObjectID, "mdoc-object:")) {
			return fail("capture_integrity", "native image belongs to another source")
		}
		used[o.ObjectID] = true
	}
	if len(used) != len(assets) {
		return fail("capture_integrity", "unreferenced materialization")
	}
	return nil
}

func retryCapture(e error) bool {
	var pe *Error
	return errors.As(e, &pe) && (pe.Code == "unstable_source" || pe.Code == "image_unavailable" || pe.Code == "transport_failure")
}
func modelDigest(m map[string]any) string { b, _ := json.Marshal(m); return digest(b) }
