package bundle

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/goliatone/mdoc/internal/config"
)

func validateTOCHeadingSelectors(publicationID string, toc config.TOCConfig, members []preparedMember) error {
	available := map[string]map[string]int{}
	for _, member := range members {
		ids := map[string]int{}
		for _, ref := range member.refs {
			if ref.originalID != "" {
				ids[ref.originalID]++
			}
		}
		available[member.member.SourceKey] = ids
	}
	for _, selector := range toc.Members {
		ids, exists := available[selector.Source]
		if !exists {
			return fmt.Errorf("publication %q table of contents selects source %q, which is not a bundle member", publicationID, selector.Source)
		}
		for _, headingID := range append(append([]string(nil), selector.IncludeHeadings...), selector.ExcludeHeadings...) {
			matches := ids[headingID]
			if matches == 0 {
				return fmt.Errorf("publication %q table of contents selects unknown heading ID %q in member %q", publicationID, headingID, selector.Source)
			}
			if matches > 1 {
				return fmt.Errorf("publication %q table of contents selects ambiguous heading ID %q in member %q; found %d retained headings", publicationID, headingID, selector.Source, matches)
			}
		}
	}
	return nil
}

func tocHeadingIncluded(toc config.TOCConfig, sourceKey string, heading headingRef) bool {
	if !toc.Enabled {
		return false
	}
	depth := toc.Depth
	var selector *config.TOCMemberConfig
	for index := range toc.Members {
		if toc.Members[index].Source == sourceKey {
			selector = &toc.Members[index]
			break
		}
	}
	if selector != nil {
		if selector.Enabled != nil && !*selector.Enabled {
			return false
		}
		if selector.Depth > 0 {
			depth = selector.Depth
		}
		if len(selector.IncludeHeadings) > 0 && !containsString(selector.IncludeHeadings, heading.originalID) {
			return false
		}
		if containsString(selector.ExcludeHeadings, heading.originalID) {
			return false
		}
	}
	return heading.level <= depth
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func Finalize(topology Topology, layout config.LayoutConfig, filtered []byte) ([]byte, error) {
	if err := VerifyTopology(topology, filtered); err != nil {
		return nil, err
	}
	root, err := decodeDocument(filtered)
	if err != nil {
		return nil, err
	}
	root["blocks"] = pruneTemporaryMarkers(root["blocks"])
	if layout.TableOfContents.Enabled {
		replacement := tocBlock(topology.TOCHeadings, tocTitle(layout.TableOfContents))
		count := 0
		root["blocks"] = replaceTOCMarker(root["blocks"], replacement, &count)
		if count != 1 {
			return nil, fmt.Errorf("reserved table of contents marker count is %d, expected 1", count)
		}
	}
	encoded, err := json.Marshal(root)
	if err != nil {
		return nil, fmt.Errorf("encode finalized table of contents: %w", err)
	}
	return encoded, nil
}

func tocBlock(headings []Heading, title string) map[string]any {
	blocks := []any{styledParagraph("TOC Heading", title)}
	for _, heading := range headings {
		link := map[string]any{"t": "Link", "c": []any{pandocAttribute("", nil, nil), textInlines(heading.Text), []any{"#" + heading.ID, ""}}}
		blocks = append(blocks, map[string]any{"t": "Div", "c": []any{pandocAttribute("", nil, map[string]string{"custom-style": fmt.Sprintf("TOC %d", heading.Level)}), []any{map[string]any{"t": "Para", "c": []any{link}}}}})
	}
	return map[string]any{"t": "Div", "c": []any{pandocAttribute("", []string{"mdoc-toc-content"}, nil), blocks}}
}

func tocTitle(value config.TOCConfig) string {
	if strings.TrimSpace(value.Title) != "" {
		return value.Title
	}
	return "Table of Contents"
}

func replaceTOCMarker(value any, replacement map[string]any, count *int) any {
	switch current := value.(type) {
	case map[string]any:
		if current["t"] == "Div" {
			content, _ := current["c"].([]any)
			if len(content) > 0 {
				id, classes, attributes := attributes(content[0])
				if id == "mdoc_toc" && contains(classes, "mdoc-protected") && attributes["data-mdoc-kind"] == MarkerTOC {
					*count = *count + 1
					return replacement
				}
			}
		}
		for key, child := range current {
			current[key] = replaceTOCMarker(child, replacement, count)
		}
		return current
	case []any:
		for index, child := range current {
			current[index] = replaceTOCMarker(child, replacement, count)
		}
		return current
	default:
		return current
	}
}

func pruneTemporaryMarkers(value any) any {
	switch current := value.(type) {
	case map[string]any:
		for key, child := range current {
			current[key] = pruneTemporaryMarkers(child)
		}
		return current
	case []any:
		result := make([]any, 0, len(current))
		for _, child := range current {
			if isProtectedMarker(child, MarkerCover) {
				continue
			}
			result = append(result, pruneTemporaryMarkers(child))
		}
		return result
	default:
		return current
	}
}

func isProtectedMarker(value any, kind string) bool {
	node, ok := value.(map[string]any)
	if !ok || node["t"] != "Div" {
		return false
	}
	content, _ := node["c"].([]any)
	if len(content) == 0 {
		return false
	}
	_, classes, attributes := attributes(content[0])
	return contains(classes, "mdoc-protected") && attributes["data-mdoc-kind"] == kind
}
