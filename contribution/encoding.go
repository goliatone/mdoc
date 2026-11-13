package contribution

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"reflect"
	"regexp"
	"strings"
	"unicode/utf8"
)

var identifier = regexp.MustCompile(`^[A-Za-z0-9_-]{1,256}$`)
var operationID = regexp.MustCompile(`^[a-f0-9]{32,128}$`)

func NewOperationID() (string, error) {
	b := make([]byte, 16)
	if _, e := rand.Read(b); e != nil {
		return "", e
	}
	return hex.EncodeToString(b), nil
}
func hash(b []byte) string { d := sha256.Sum256(b); return hex.EncodeToString(d[:]) }
func digest(v any) string {
	b, _ := json.Marshal(v)
	return hash(append([]byte("mdoc.contribution.v1\x00"), b...))
}

// ParagraphDigest binds exact native paragraph text, excluding its final newline.
func ParagraphDigest(text string) string { return digest(text) }
func validMeta(m OperationMeta) error {
	if m.SchemaVersion != SchemaVersion || !identifier.MatchString(m.Namespace) || !operationID.MatchString(m.OperationID) || m.AccountBinding == "" || len(m.AccountBinding) > 256 || !utf8.ValidString(m.AccountBinding) || len(m.CorrelationID) > 256 || !utf8.ValidString(m.CorrelationID) {
		return fail(InvalidInput, "invalid operation metadata")
	}
	return nil
}

// DecodeJSON rejects unknown fields, duplicates at every depth, and trailing data.
func DecodeJSON(b []byte, v any) error {
	if len(b) > MaxPlan || !utf8.Valid(b) || !validEscapes(b) {
		return fail(InvalidPlan, "invalid or oversized JSON")
	}
	d := json.NewDecoder(bytes.NewReader(b))
	if err := scanValue(d); err != nil {
		return fail(InvalidPlan, "invalid or duplicate JSON fields")
	}
	if _, e := d.Token(); e != io.EOF {
		return fail(InvalidPlan, "trailing JSON")
	}
	if !exactShape(b, reflect.TypeOf(v)) {
		return fail(InvalidPlan, "invalid or unknown JSON fields")
	}
	d = json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if d.Decode(v) != nil {
		return fail(InvalidPlan, "invalid or unknown JSON fields")
	}
	return nil
}
func scanValue(d *json.Decoder) error {
	t, e := d.Token()
	if e != nil {
		return e
	}
	x, ok := t.(json.Delim)
	if !ok {
		return nil
	}
	switch x {
	case '{':
		seen := map[string]bool{}
		for d.More() {
			k, e := d.Token()
			if e != nil {
				return e
			}
			s, ok := k.(string)
			if !ok || seen[s] {
				return io.ErrUnexpectedEOF
			}
			seen[s] = true
			if e := scanValue(d); e != nil {
				return e
			}
		}
	case '[':
		for d.More() {
			if e := scanValue(d); e != nil {
				return e
			}
		}
	default:
		return io.ErrUnexpectedEOF
	}
	_, e = d.Token()
	return e
}
func createDigest(p CreatePlan) string {
	p.Digest = ""
	p.Input.Meta.CorrelationID = ""
	return digest(p)
}
func updateDigest(p UpdatePlan) string {
	p.Digest = ""
	p.Input.Meta.CorrelationID = ""
	return digest(p)
}
func replyDigest(p ReplyPlan) string {
	p.Digest = ""
	p.Input.Meta.CorrelationID = ""
	return digest(p)
}
func DecodeCreatePlan(b []byte) (CreatePlan, error) {
	var p CreatePlan
	e := DecodeJSON(b, &p)
	if e == nil {
		e = validateCreate(p)
	}
	return p, e
}
func DecodeUpdatePlan(b []byte) (UpdatePlan, error) {
	var p UpdatePlan
	e := DecodeJSON(b, &p)
	if e == nil {
		e = validateUpdate(p)
	}
	return p, e
}
func DecodeReplyPlan(b []byte) (ReplyPlan, error) {
	var p ReplyPlan
	e := DecodeJSON(b, &p)
	if e == nil {
		e = validateReply(p)
	}
	return p, e
}
func validateCreate(p CreatePlan) error {
	if validMeta(p.Input.Meta) != nil || p.Kind != CreateKind || p.Digest != createDigest(p) || len(p.Input.Markdown) > MaxMarkdown || !utf8.ValidString(p.Input.Markdown) || strings.TrimSpace(p.Input.Title) == "" || len(p.Input.Title) > 1024 || !utf8.ValidString(p.Input.Title) || !identifier.MatchString(p.Input.ParentID) || len(p.Artifact.DOCX) == 0 || len(p.Artifact.DOCX) > MaxDOCX || p.Artifact.Digest != hash(p.Artifact.DOCX) || p.Artifact.RendererVersion == "" || !utf8.ValidString(p.Artifact.RendererVersion) || len(p.Artifact.RendererVersion) > 1024 {
		return fail(InvalidPlan, "invalid create plan")
	}
	return nil
}
func validateUpdate(p UpdatePlan) error {
	if validMeta(p.Input.Meta) != nil || p.Kind != UpdateKind || p.Digest != updateDigest(p) || validReplacement(p.Input.Replacement) != nil || !validPlanSource(p.Input.Source) || p.Input.ExpectedRevision == "" || len(p.Input.ExpectedRevision) > 4096 || !utf8.ValidString(p.Input.ExpectedRevision) || p.Run.SelectionPolicy != SelectionPolicy || p.Run.Start < 1 || p.Run.Start >= p.Start || p.Start > p.End || p.End >= p.Run.End || !isDigest(p.Run.StyleDigest) || !isDigest(p.SnapshotDigest) || !isDigest(p.ContentDigest) || !utf8.ValidString(p.Before) || !utf8.ValidString(p.After) || len(p.Before) > maxResponse || len(p.After) > maxResponse || p.Before == p.After {
		return fail(InvalidPlan, "invalid update plan")
	}
	sel := p.Input.Selection
	r := []rune(p.Before)
	if sel.ParagraphStart < 1 || sel.Start < 0 || sel.End < sel.Start || sel.End > len(r) || sel.ParagraphDigest != ParagraphDigest(p.Before) || sel.ExpectedText != string(r[sel.Start:sel.End]) || p.Start != sel.ParagraphStart+units(string(r[:sel.Start])) || p.End != sel.ParagraphStart+units(string(r[:sel.End])) || p.After != string(r[:sel.Start])+p.Input.Replacement+string(r[sel.End:]) {
		return fail(InvalidPlan, "inconsistent selected text or indices")
	}
	return nil
}
func validateReply(p ReplyPlan) error {
	if validMeta(p.Input.Meta) != nil || p.Kind != ReplyKind || p.Digest != replyDigest(p) || validReplyText(p.Input.Content) != nil || !validPlanSource(p.Input.Source) || !identifier.MatchString(p.Input.CommentID) || !isDigest(p.Thread.Fingerprint) || len(p.Thread.ModifiedAt) > 1024 || !utf8.ValidString(p.Thread.ModifiedAt) {
		return fail(InvalidPlan, "invalid reply plan")
	}
	return nil
}
func isDigest(s string) bool {
	b, e := hex.DecodeString(s)
	return e == nil && len(b) == 32 && s == strings.ToLower(s)
}
