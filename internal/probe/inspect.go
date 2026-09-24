package probe

import (
	"strings"

	docs "google.golang.org/api/docs/v1"
)

type linkRange struct {
	Start int64
	End   int64
}

type documentInspection struct {
	PlaceholderRanges   []linkRange
	NamedStyles         map[string]int
	TableCount          int
	CodeTextPresent     bool
	SuggestionsDetected bool
}

func inspectDocument(document *docs.Document) documentInspection {
	result := documentInspection{NamedStyles: map[string]int{}}
	if document == nil {
		return result
	}
	result.SuggestionsDetected = len(document.SuggestedDocumentStyleChanges) > 0 || len(document.SuggestedNamedStylesChanges) > 0
	if document.Body != nil {
		walkStructuralElements(document.Body.Content, &result)
	}
	return result
}

func documentHasLink(document *docs.Document, target string) bool {
	inspection := linkInspection(document, target)
	return len(inspection) > 0
}

func linkInspection(document *docs.Document, target string) []linkRange {
	ranges := []linkRange{}
	if document == nil || document.Body == nil {
		return ranges
	}
	walkLinks(document.Body.Content, target, &ranges)
	return ranges
}

func walkStructuralElements(elements []*docs.StructuralElement, result *documentInspection) {
	for _, structural := range elements {
		if structural == nil {
			continue
		}
		if paragraph := structural.Paragraph; paragraph != nil {
			var paragraphText strings.Builder
			if paragraph.ParagraphStyle != nil && paragraph.ParagraphStyle.NamedStyleType != "" {
				result.NamedStyles[paragraph.ParagraphStyle.NamedStyleType]++
			}
			if len(paragraph.SuggestedBulletChanges) > 0 || len(paragraph.SuggestedParagraphStyleChanges) > 0 || len(paragraph.SuggestedPositionedObjectIds) > 0 {
				result.SuggestionsDetected = true
			}
			for _, element := range paragraph.Elements {
				if element == nil {
					continue
				}
				if run := element.TextRun; run != nil {
					paragraphText.WriteString(run.Content)
					if len(run.SuggestedInsertionIds) > 0 || len(run.SuggestedDeletionIds) > 0 || len(run.SuggestedTextStyleChanges) > 0 {
						result.SuggestionsDetected = true
					}
					if run.TextStyle != nil && run.TextStyle.Link != nil && run.TextStyle.Link.Url == placeholderURL {
						result.PlaceholderRanges = append(result.PlaceholderRanges, linkRange{Start: element.StartIndex, End: element.EndIndex})
					}
				}
				if object := element.InlineObjectElement; object != nil {
					if len(object.SuggestedInsertionIds) > 0 || len(object.SuggestedDeletionIds) > 0 || len(object.SuggestedTextStyleChanges) > 0 {
						result.SuggestionsDetected = true
					}
				}
			}
			if strings.Contains(paragraphText.String(), "func ready() bool") {
				result.CodeTextPresent = true
			}
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
					walkStructuralElements(cell.Content, result)
				}
			}
		}
		if tableOfContents := structural.TableOfContents; tableOfContents != nil {
			walkStructuralElements(tableOfContents.Content, result)
		}
	}
}

func walkLinks(elements []*docs.StructuralElement, target string, ranges *[]linkRange) {
	for _, structural := range elements {
		if structural == nil {
			continue
		}
		if paragraph := structural.Paragraph; paragraph != nil {
			for _, element := range paragraph.Elements {
				if element != nil && element.TextRun != nil && element.TextRun.TextStyle != nil && element.TextRun.TextStyle.Link != nil && element.TextRun.TextStyle.Link.Url == target {
					*ranges = append(*ranges, linkRange{Start: element.StartIndex, End: element.EndIndex})
				}
			}
		}
		if table := structural.Table; table != nil {
			for _, row := range table.TableRows {
				if row == nil {
					continue
				}
				for _, cell := range row.TableCells {
					if cell != nil {
						walkLinks(cell.Content, target, ranges)
					}
				}
			}
		}
		if tableOfContents := structural.TableOfContents; tableOfContents != nil {
			walkLinks(tableOfContents.Content, target, ranges)
		}
	}
}
