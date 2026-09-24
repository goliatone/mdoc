package googleapi

import (
	"context"
	"strings"

	"google.golang.org/api/docs/v1"
)

type DocsClient struct{ service *docs.Service }

func NewDocsClient(service *docs.Service) *DocsClient { return &DocsClient{service: service} }

func (c *DocsClient) GetDocument(ctx context.Context, documentID string) (Document, error) {
	call := c.service.Documents.Get(documentID).SuggestionsViewMode("SUGGESTIONS_INLINE").IncludeTabsContent(true).Context(ctx)
	document, err := retryValue(ctx, "read Google Doc", func() (*docs.Document, error) { return call.Do() })
	if err != nil {
		return Document{}, err
	}
	return inspectDocument(document), nil
}

func (c *DocsClient) UpdateLinks(ctx context.Context, documentID, requiredRevisionID string, updates []LinkUpdate) (Document, error) {
	requests := make([]*docs.Request, 0, len(updates))
	for _, update := range updates {
		requests = append(requests, &docs.Request{UpdateTextStyle: &docs.UpdateTextStyleRequest{
			Range:     &docs.Range{StartIndex: update.StartIndex, EndIndex: update.EndIndex},
			TextStyle: &docs.TextStyle{Link: &docs.Link{Url: update.URL}},
			Fields:    "link",
		}})
	}
	if len(requests) > 0 {
		_, err := c.service.Documents.BatchUpdate(documentID, &docs.BatchUpdateDocumentRequest{
			Requests:     requests,
			WriteControl: &docs.WriteControl{RequiredRevisionId: requiredRevisionID},
		}).Context(ctx).Do()
		if err != nil {
			return Document{}, classify("update pending Google Doc links", err)
		}
	}
	return c.GetDocument(ctx, documentID)
}

func inspectDocument(document *docs.Document) Document {
	result := Document{ID: document.DocumentId, RevisionID: document.RevisionId, Links: map[string][]TextRange{}}
	if len(document.Tabs) > 0 {
		for _, tab := range document.Tabs {
			inspectTab(tab, &result)
		}
		return result
	}
	inspectDocumentTab(&docs.DocumentTab{
		Body: document.Body, DocumentStyle: document.DocumentStyle, Footers: document.Footers,
		Footnotes: document.Footnotes, Headers: document.Headers, InlineObjects: document.InlineObjects,
		Lists: document.Lists, NamedStyles: document.NamedStyles, PositionedObjects: document.PositionedObjects,
		SuggestedDocumentStyleChanges: document.SuggestedDocumentStyleChanges,
		SuggestedNamedStylesChanges:   document.SuggestedNamedStylesChanges,
	}, &result)
	return result
}

func inspectTab(tab *docs.Tab, result *Document) {
	if tab == nil {
		return
	}
	if tab.TabProperties != nil {
		result.Tabs = append(result.Tabs, TabTopology{
			ID: tab.TabProperties.TabId, Title: tab.TabProperties.Title, Index: tab.TabProperties.Index, ParentID: tab.TabProperties.ParentTabId,
		})
	}
	inspectDocumentTab(tab.DocumentTab, result)
	for _, child := range tab.ChildTabs {
		inspectTab(child, result)
	}
}

func inspectDocumentTab(tab *docs.DocumentTab, result *Document) {
	if tab == nil {
		return
	}
	if len(tab.SuggestedDocumentStyleChanges) > 0 || len(tab.SuggestedNamedStylesChanges) > 0 {
		result.SuggestionsDetected = true
	}
	result.InlineObjectCount += len(tab.InlineObjects)
	if tab.Body != nil {
		inspectElements(tab.Body.Content, result)
	}
	for _, header := range tab.Headers {
		inspectElements(header.Content, result)
	}
	for _, footer := range tab.Footers {
		inspectElements(footer.Content, result)
	}
	for _, footnote := range tab.Footnotes {
		inspectElements(footnote.Content, result)
	}
	for _, list := range tab.Lists {
		if list.SuggestedInsertionId != "" || len(list.SuggestedDeletionIds) > 0 || len(list.SuggestedListPropertiesChanges) > 0 {
			result.SuggestionsDetected = true
		}
	}
	for _, object := range tab.InlineObjects {
		if object.SuggestedInsertionId != "" || len(object.SuggestedDeletionIds) > 0 || len(object.SuggestedInlineObjectPropertiesChanges) > 0 {
			result.SuggestionsDetected = true
		}
	}
	for _, object := range tab.PositionedObjects {
		if object.SuggestedInsertionId != "" || len(object.SuggestedDeletionIds) > 0 || len(object.SuggestedPositionedObjectPropertiesChanges) > 0 {
			result.SuggestionsDetected = true
		}
	}
}

func inspectElements(elements []*docs.StructuralElement, result *Document) {
	for _, structural := range elements {
		if structural == nil {
			continue
		}
		if paragraph := structural.Paragraph; paragraph != nil {
			if paragraph.ParagraphStyle != nil && (paragraph.ParagraphStyle.HeadingId != "" || strings.HasPrefix(paragraph.ParagraphStyle.NamedStyleType, "HEADING_")) {
				result.HeadingCount++
			}
			if len(paragraph.SuggestedBulletChanges) > 0 || len(paragraph.SuggestedParagraphStyleChanges) > 0 || len(paragraph.SuggestedPositionedObjectIds) > 0 {
				result.SuggestionsDetected = true
			}
			for _, element := range paragraph.Elements {
				inspectParagraphElement(element, result)
			}
		}
		if section := structural.SectionBreak; section != nil && (len(section.SuggestedInsertionIds) > 0 || len(section.SuggestedDeletionIds) > 0) {
			result.SuggestionsDetected = true
		}
		if structural.SectionBreak != nil {
			result.SectionBreakCount++
		}
		if table := structural.Table; table != nil {
			result.TableCount++
			if len(table.SuggestedInsertionIds) > 0 || len(table.SuggestedDeletionIds) > 0 {
				result.SuggestionsDetected = true
			}
			for _, row := range table.TableRows {
				if row == nil {
					continue
				}
				if len(row.SuggestedInsertionIds) > 0 || len(row.SuggestedDeletionIds) > 0 || len(row.SuggestedTableRowStyleChanges) > 0 {
					result.SuggestionsDetected = true
				}
				for _, cell := range row.TableCells {
					if cell == nil {
						continue
					}
					if len(cell.SuggestedInsertionIds) > 0 || len(cell.SuggestedDeletionIds) > 0 || len(cell.SuggestedTableCellStyleChanges) > 0 {
						result.SuggestionsDetected = true
					}
					inspectElements(cell.Content, result)
				}
			}
		}
		if toc := structural.TableOfContents; toc != nil {
			inspectElements(toc.Content, result)
		}
	}
}

func inspectParagraphElement(element *docs.ParagraphElement, result *Document) {
	if element == nil {
		return
	}
	if run := element.TextRun; run != nil {
		if hasSuggestedText(run.SuggestedInsertionIds, run.SuggestedDeletionIds, run.SuggestedTextStyleChanges) {
			result.SuggestionsDetected = true
		}
		if run.TextStyle != nil && run.TextStyle.Link != nil && run.TextStyle.Link.Url != "" {
			url := run.TextStyle.Link.Url
			result.Links[url] = append(result.Links[url], TextRange{StartIndex: element.StartIndex, EndIndex: element.EndIndex})
		}
		if run.TextStyle != nil && run.TextStyle.Link != nil {
			link := run.TextStyle.Link
			if link.Bookmark != nil || link.BookmarkId != "" || link.Heading != nil || link.HeadingId != "" {
				result.InternalLinkCount++
			}
		}
	}
	if value := element.AutoText; value != nil && hasSuggestedText(value.SuggestedInsertionIds, value.SuggestedDeletionIds, value.SuggestedTextStyleChanges) {
		result.SuggestionsDetected = true
	}
	if value := element.ColumnBreak; value != nil && hasSuggestedText(value.SuggestedInsertionIds, value.SuggestedDeletionIds, value.SuggestedTextStyleChanges) {
		result.SuggestionsDetected = true
	}
	if value := element.DateElement; value != nil && (hasSuggestedText(value.SuggestedInsertionIds, value.SuggestedDeletionIds, value.SuggestedTextStyleChanges) || len(value.SuggestedDateElementPropertiesChanges) > 0) {
		result.SuggestionsDetected = true
	}
	if value := element.Equation; value != nil && (len(value.SuggestedInsertionIds) > 0 || len(value.SuggestedDeletionIds) > 0) {
		result.SuggestionsDetected = true
	}
	if value := element.FootnoteReference; value != nil && hasSuggestedText(value.SuggestedInsertionIds, value.SuggestedDeletionIds, value.SuggestedTextStyleChanges) {
		result.SuggestionsDetected = true
	}
	if value := element.HorizontalRule; value != nil && hasSuggestedText(value.SuggestedInsertionIds, value.SuggestedDeletionIds, value.SuggestedTextStyleChanges) {
		result.SuggestionsDetected = true
	}
	if value := element.InlineObjectElement; value != nil && hasSuggestedText(value.SuggestedInsertionIds, value.SuggestedDeletionIds, value.SuggestedTextStyleChanges) {
		result.SuggestionsDetected = true
	}
	if value := element.PageBreak; value != nil && hasSuggestedText(value.SuggestedInsertionIds, value.SuggestedDeletionIds, value.SuggestedTextStyleChanges) {
		result.SuggestionsDetected = true
	}
	if value := element.Person; value != nil && hasSuggestedText(value.SuggestedInsertionIds, value.SuggestedDeletionIds, value.SuggestedTextStyleChanges) {
		result.SuggestionsDetected = true
	}
	if value := element.RichLink; value != nil && hasSuggestedText(value.SuggestedInsertionIds, value.SuggestedDeletionIds, value.SuggestedTextStyleChanges) {
		result.SuggestionsDetected = true
	}
}

func hasSuggestedText[T any](insertions, deletions []string, styles map[string]T) bool {
	return len(insertions) > 0 || len(deletions) > 0 || len(styles) > 0
}
