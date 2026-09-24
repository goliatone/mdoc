package googleapi

import (
	"testing"

	"google.golang.org/api/docs/v1"
)

func TestInspectDocumentFindsSuggestionsAndLinks(t *testing.T) {
	document := &docs.Document{
		DocumentId: "doc",
		RevisionId: "revision",
		Body: &docs.Body{Content: []*docs.StructuralElement{
			{Paragraph: &docs.Paragraph{Elements: []*docs.ParagraphElement{
				{StartIndex: 1, EndIndex: 5, TextRun: &docs.TextRun{
					Content: "link", SuggestedInsertionIds: []string{"suggestion"},
					TextStyle: &docs.TextStyle{Link: &docs.Link{Url: "https://example.test"}},
				}},
			}}},
		}},
	}
	result := inspectDocument(document)
	if !result.SuggestionsDetected || result.RevisionID != "revision" || len(result.Links["https://example.test"]) != 1 {
		t.Fatalf("result = %#v", result)
	}
}

func TestInspectDocumentCountsNativeStructureAndInternalLinks(t *testing.T) {
	document := &docs.Document{
		DocumentId:    "doc",
		InlineObjects: map[string]docs.InlineObject{"image": {}},
		Body: &docs.Body{Content: []*docs.StructuralElement{
			{Paragraph: &docs.Paragraph{
				ParagraphStyle: &docs.ParagraphStyle{HeadingId: "heading-1", NamedStyleType: "HEADING_1"},
				Elements:       []*docs.ParagraphElement{{TextRun: &docs.TextRun{TextStyle: &docs.TextStyle{Link: &docs.Link{HeadingId: "heading-1"}}}}},
			}},
			{Table: &docs.Table{}},
			{SectionBreak: &docs.SectionBreak{}},
		}},
	}
	result := inspectDocument(document)
	if result.InternalLinkCount != 1 || result.HeadingCount != 1 || result.InlineObjectCount != 1 || result.TableCount != 1 || result.SectionBreakCount != 1 {
		t.Fatalf("result = %#v", result)
	}
}

func TestInspectDocumentFindsSuggestionsAcrossContainersAndNestedTabs(t *testing.T) {
	tests := []struct {
		name string
		tab  *docs.DocumentTab
	}{
		{name: "header", tab: &docs.DocumentTab{Headers: map[string]docs.Header{"h": {Content: suggestedText()}}}},
		{name: "footer", tab: &docs.DocumentTab{Footers: map[string]docs.Footer{"f": {Content: suggestedText()}}}},
		{name: "footnote", tab: &docs.DocumentTab{Footnotes: map[string]docs.Footnote{"n": {Content: suggestedText()}}}},
		{name: "list", tab: &docs.DocumentTab{Lists: map[string]docs.List{"l": {SuggestedInsertionId: "s"}}}},
		{name: "inline object", tab: &docs.DocumentTab{InlineObjects: map[string]docs.InlineObject{"i": {SuggestedDeletionIds: []string{"s"}}}}},
		{name: "positioned object", tab: &docs.DocumentTab{PositionedObjects: map[string]docs.PositionedObject{"p": {SuggestedInsertionId: "s"}}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			document := &docs.Document{Tabs: []*docs.Tab{{ChildTabs: []*docs.Tab{{DocumentTab: test.tab}}}}}
			if result := inspectDocument(document); !result.SuggestionsDetected {
				t.Fatalf("suggestion was not detected: %#v", result)
			}
		})
	}
}

func TestInspectDocumentRecordsOrderedTabTopology(t *testing.T) {
	document := &docs.Document{DocumentId: "doc", Tabs: []*docs.Tab{{
		TabProperties: &docs.TabProperties{TabId: "root", Title: "Root", Index: 0},
		ChildTabs:     []*docs.Tab{{TabProperties: &docs.TabProperties{TabId: "child", Title: "Child", Index: 0, ParentTabId: "root"}}},
	}, {TabProperties: &docs.TabProperties{TabId: "second", Title: "Second", Index: 1}}}}
	result := inspectDocument(document)
	if len(result.Tabs) != 3 || result.Tabs[0].ID != "root" || result.Tabs[1].ParentID != "root" || result.Tabs[2].ID != "second" {
		t.Fatalf("tabs = %#v", result.Tabs)
	}
}

func TestInspectDocumentFindsEveryParagraphElementSuggestion(t *testing.T) {
	tests := []struct {
		name    string
		element *docs.ParagraphElement
	}{
		{name: "auto text", element: &docs.ParagraphElement{AutoText: &docs.AutoText{SuggestedTextStyleChanges: map[string]docs.SuggestedTextStyle{"s": {}}}}},
		{name: "column break", element: &docs.ParagraphElement{ColumnBreak: &docs.ColumnBreak{SuggestedInsertionIds: []string{"s"}}}},
		{name: "date", element: &docs.ParagraphElement{DateElement: &docs.DateElement{SuggestedDateElementPropertiesChanges: map[string]docs.SuggestedDateElementProperties{"s": {}}}}},
		{name: "equation", element: &docs.ParagraphElement{Equation: &docs.Equation{SuggestedDeletionIds: []string{"s"}}}},
		{name: "footnote reference", element: &docs.ParagraphElement{FootnoteReference: &docs.FootnoteReference{SuggestedInsertionIds: []string{"s"}}}},
		{name: "horizontal rule", element: &docs.ParagraphElement{HorizontalRule: &docs.HorizontalRule{SuggestedDeletionIds: []string{"s"}}}},
		{name: "inline object", element: &docs.ParagraphElement{InlineObjectElement: &docs.InlineObjectElement{SuggestedTextStyleChanges: map[string]docs.SuggestedTextStyle{"s": {}}}}},
		{name: "page break", element: &docs.ParagraphElement{PageBreak: &docs.PageBreak{SuggestedInsertionIds: []string{"s"}}}},
		{name: "person", element: &docs.ParagraphElement{Person: &docs.Person{SuggestedDeletionIds: []string{"s"}}}},
		{name: "rich link", element: &docs.ParagraphElement{RichLink: &docs.RichLink{SuggestedTextStyleChanges: map[string]docs.SuggestedTextStyle{"s": {}}}}},
		{name: "text run", element: &docs.ParagraphElement{TextRun: &docs.TextRun{SuggestedInsertionIds: []string{"s"}}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			document := &docs.Document{Body: &docs.Body{Content: []*docs.StructuralElement{{Paragraph: &docs.Paragraph{Elements: []*docs.ParagraphElement{test.element}}}}}}
			if result := inspectDocument(document); !result.SuggestionsDetected {
				t.Fatalf("suggestion was not detected: %#v", result)
			}
		})
	}
}

func TestInspectDocumentFindsSectionBreakSuggestion(t *testing.T) {
	document := &docs.Document{Body: &docs.Body{Content: []*docs.StructuralElement{{SectionBreak: &docs.SectionBreak{SuggestedInsertionIds: []string{"s"}}}}}}
	if result := inspectDocument(document); !result.SuggestionsDetected {
		t.Fatalf("suggestion was not detected: %#v", result)
	}
}

func suggestedText() []*docs.StructuralElement {
	return []*docs.StructuralElement{{Paragraph: &docs.Paragraph{Elements: []*docs.ParagraphElement{{TextRun: &docs.TextRun{SuggestedInsertionIds: []string{"s"}}}}}}}
}
