package workingdraft_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	wd "github.com/goliatone/mdoc/workingdraft"
	"google.golang.org/api/docs/v1"
	"google.golang.org/api/drive/v3"
)

type provider struct {
	doc      *docs.Document
	comments *drive.CommentList
	err      error
	calls    int
}

func (p *provider) File(context.Context, string) (*drive.File, error) {
	if p.err != nil {
		return nil, p.err
	}
	return &drive.File{Id: "doc", Name: p.doc.Title, MimeType: "application/vnd.google-apps.document", Version: 1}, nil
}
func (p *provider) Document(context.Context, string) (json.RawMessage, error) {
	return json.Marshal(p.doc)
}
func (p *provider) Comments(context.Context, string, string) (*drive.CommentList, error) {
	p.calls++
	return p.comments, p.err
}
func document(text string) *docs.Document {
	return &docs.Document{DocumentId: "doc", Title: "Draft", RevisionId: "r1", Tabs: []*docs.Tab{{TabProperties: &docs.TabProperties{TabId: "t.0"}, DocumentTab: &docs.DocumentTab{Body: &docs.Body{Content: []*docs.StructuralElement{{SectionBreak: &docs.SectionBreak{}}, {Paragraph: &docs.Paragraph{Elements: []*docs.ParagraphElement{{TextRun: &docs.TextRun{Content: text + "\n"}}}}}}}}}}}
}

type memoryStore map[string][]byte

func (s memoryStore) Put(_ context.Context, k string, v []byte) error {
	s[k] = append([]byte(nil), v...)
	return nil
}
func (s memoryStore) Get(_ context.Context, k string) ([]byte, error) {
	return append([]byte(nil), s[k]...), nil
}
func TestCaptureIntegrityAndContentIdentity(t *testing.T) {
	ctx := context.Background()
	p := &provider{doc: document("Hello *literal*")}
	store := memoryStore{}
	now := time.Date(2026, 9, 24, 0, 0, 0, 0, time.UTC)
	service, err := wd.New(wd.Options{Provider: p, Store: store, Now: func() time.Time { now = now.Add(time.Second); return now }})
	if err != nil {
		t.Fatal(err)
	}
	first, err := service.Capture(ctx, wd.SourceRef{DocumentID: "doc"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.Capture(ctx, wd.SourceRef{DocumentID: "doc"})
	if err != nil {
		t.Fatal(err)
	}
	if !first.BodyUsable || first.ContentDigest != second.ContentDigest || first.SnapshotDigest == second.SnapshotDigest {
		t.Fatal("content and capture identities were not separated")
	}
	loaded, err := wd.LoadSnapshot(ctx, store, first.SnapshotDigest)
	if err != nil || loaded.SnapshotDigest != first.SnapshotDigest {
		t.Fatalf("load: %v", err)
	}
	bytes, err := json.MarshalIndent(first, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = wd.DecodeSnapshot(bytes); err != nil {
		t.Fatalf("formatted saved bytes: %v", err)
	}
	loaded.NormalizedContent.Body = "tampered"
	if wd.VerifySnapshot(loaded) == nil {
		t.Fatal("accepted corrupt content")
	}
	if p.calls != 0 {
		t.Fatal("capture queried discussion")
	}
}
func TestUnsupportedAndSuggestedBodyRetainsEvidence(t *testing.T) {
	for _, kind := range []string{"suggestion", "table", "tabs", "rich", "footnote"} {
		t.Run(kind, func(t *testing.T) {
			d := document("draft")
			tab := d.Tabs[0].DocumentTab
			switch kind {
			case "suggestion":
				tab.Body.Content[1].Paragraph.Elements[0].TextRun.SuggestedInsertionIds = []string{"s"}
			case "table":
				tab.Body.Content = append(tab.Body.Content, &docs.StructuralElement{Table: &docs.Table{}})
			case "tabs":
				d.Tabs = append(d.Tabs, d.Tabs[0])
			case "rich":
				tab.Body.Content[1].Paragraph.Elements = append(tab.Body.Content[1].Paragraph.Elements, &docs.ParagraphElement{RichLink: &docs.RichLink{}})
			case "footnote":
				tab.Footnotes = map[string]docs.Footnote{"f": {}}
			}
			service, _ := wd.New(wd.Options{Provider: &provider{doc: d}})
			s, err := service.Capture(context.Background(), wd.SourceRef{DocumentID: "doc"})
			if err != nil {
				t.Fatal(err)
			}
			if s.BodyUsable || len(s.Diagnostics) == 0 || len(s.RawContent) == 0 || s.NormalizedContent != (wd.Content{}) {
				t.Fatal("ambiguous body became usable")
			}
			if err := wd.VerifySnapshot(s); err != nil {
				t.Fatal(err)
			}
		})
	}
}

type alternateConverter struct{}

func (alternateConverter) Convert(context.Context, *docs.Document) (wd.Content, []wd.Diagnostic, error) {
	return wd.Content{Title: "Draft", Body: "alternate\n"}, nil, nil
}
func TestConversionVersionCannotMixProposalSemantics(t *testing.T) {
	p := &provider{doc: document("body")}
	if _, err := wd.New(wd.Options{Provider: p, Converter: alternateConverter{}}); err == nil {
		t.Fatal("accepted unversioned converter")
	}
	standard, _ := wd.New(wd.Options{Provider: p})
	alternate, _ := wd.New(wd.Options{Provider: p, Converter: alternateConverter{}, ConversionVersion: "alternate-v1"})
	ctx := context.Background()
	base, err := standard.Capture(ctx, wd.SourceRef{DocumentID: "doc"})
	if err != nil {
		t.Fatal(err)
	}
	incoming, err := alternate.Capture(ctx, wd.SourceRef{DocumentID: "doc"})
	if err != nil {
		t.Fatal(err)
	}
	if incoming.ConversionVersion != "alternate-v1" {
		t.Fatal("lost converter identity")
	}
	if _, err := wd.Compare(ctx, wd.CompareInput{Baseline: base, Incoming: incoming, Current: base.NormalizedContent}); err == nil {
		t.Fatal("compared different conversion semantics")
	}
}

type guardedStore struct {
	memoryStore
	reads int
}

func (s *guardedStore) Get(ctx context.Context, key string) ([]byte, error) {
	s.reads++
	return s.memoryStore.Get(ctx, key)
}
func TestLoadRejectsUnsafeKeysBeforeStore(t *testing.T) {
	store := &guardedStore{memoryStore: memoryStore{}}
	if _, err := wd.LoadSnapshot(context.Background(), store, "../../credentials"); err == nil || store.reads != 0 {
		t.Fatal("unsafe key reached the store")
	}
}
func TestLiteralEntitiesAndEquivalentFormattingArePreserved(t *testing.T) {
	d := document("unused")
	d.Tabs[0].DocumentTab.Body.Content[1].Paragraph.Elements = []*docs.ParagraphElement{
		{TextRun: &docs.TextRun{Content: "&copy; "}},
		{TextRun: &docs.TextRun{Content: "bo", TextStyle: &docs.TextStyle{Bold: true, FontSize: &docs.Dimension{Magnitude: 10, Unit: "PT"}}}},
		{TextRun: &docs.TextRun{Content: "ld", TextStyle: &docs.TextStyle{Bold: true, FontSize: &docs.Dimension{Magnitude: 12, Unit: "PT"}}}},
		{TextRun: &docs.TextRun{Content: "\n"}},
	}
	s, _ := wd.New(wd.Options{Provider: &provider{doc: d}})
	snapshot, err := s.Capture(context.Background(), wd.SourceRef{DocumentID: "doc"})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.NormalizedContent.Body != "\\&copy; **bold**\n" {
		t.Fatalf("changed literal/formatting: %q", snapshot.NormalizedContent.Body)
	}
}

func TestLiteralIndentationCannotBecomeCode(t *testing.T) {
	s, _ := wd.New(wd.Options{Provider: &provider{doc: document("    prose, not code")}})
	snapshot, err := s.Capture(context.Background(), wd.SourceRef{DocumentID: "doc"})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.BodyUsable || len(snapshot.Diagnostics) == 0 {
		t.Fatal("ambiguous indentation became usable Markdown")
	}
}
