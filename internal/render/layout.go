package render

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/beevik/etree"
	"github.com/goliatone/mdoc/internal/config"
)

type LayoutPlan struct {
	Page          config.PageConfig
	Boundaries    []SectionBoundary
	Header        config.HeaderFooterConfig
	Footer        config.HeaderFooterConfig
	PublicationID string
	Fields        map[string]any
	Computed      map[string]any
	Headings      config.HeadingLayoutConfig
	Tables        config.TableLayoutConfig
}

type SectionBoundary struct {
	BookmarkID string
	SourceKey  string
	Start      string
	Section    config.SectionConfig
}

var sectionPattern = regexp.MustCompile(`(?s)<w:sectPr\b.*?</w:sectPr>`)
var pageSizePattern = regexp.MustCompile(`<w:pgSz\b[^>]*/>`)
var pageMarginPattern = regexp.MustCompile(`<w:pgMar\b[^>]*/>`)
var paragraphPattern = regexp.MustCompile(`<w:(?:p|tbl)\b`)

func ApplyLayout(inputPath, outputPath string, plan LayoutPlan) error {
	_, err := ApplyLayoutWithReport(inputPath, outputPath, plan)
	return err
}

func ApplyLayoutWithReport(inputPath, outputPath string, plan LayoutPlan) ([]string, error) {
	if err := config.ValidatePageLayout(plan.Page); err != nil {
		return nil, err
	}
	if err := config.ValidateHeaderFooterLayout("header", plan.Header); err != nil {
		return nil, err
	}
	if err := config.ValidateHeaderFooterLayout("footer", plan.Footer); err != nil {
		return nil, err
	}
	if !hasLayoutSettings(plan) {
		data, err := os.ReadFile(inputPath)
		if err != nil {
			return nil, err
		}
		return nil, os.WriteFile(outputPath, data, 0o600)
	}
	parts, err := readZip(inputPath)
	if err != nil {
		return nil, err
	}
	if err := ensurePackageContentTypes(parts); err != nil {
		return nil, err
	}
	documentXML := parts["word/document.xml"]
	if len(documentXML) == 0 {
		return nil, errors.New("DOCX is missing word/document.xml")
	}
	updated := documentXML
	if hasPageSettings(plan.Page) || len(plan.Boundaries) > 0 {
		updated, err = applyDocumentLayout(documentXML, plan)
		if err != nil {
			return nil, err
		}
	}
	updated, warnings, err := applyFlowAndTables(updated, plan)
	if err != nil {
		return nil, err
	}
	updated, err = applyHeadersAndFooters(parts, updated, plan)
	if err != nil {
		return nil, err
	}
	if shouldNormalizeBodyTables(plan) {
		if err := auditDocumentTableGeometry(string(updated)); err != nil {
			return nil, err
		}
	}
	for name, data := range parts {
		if (strings.HasPrefix(name, "word/header_mdoc") || strings.HasPrefix(name, "word/footer_mdoc")) && len(data) > 0 {
			if err := auditDocumentTableGeometry(string(data)); err != nil {
				return nil, fmt.Errorf("%s: %w", name, err)
			}
		}
	}
	if err := auditDOCXConformance(parts, updated); err != nil {
		return nil, fmt.Errorf("DOCX conformance audit: %w", err)
	}
	parts["word/document.xml"] = updated
	return warnings, writeZip(outputPath, parts)
}

func applyDocumentLayout(documentXML []byte, plan LayoutPlan) ([]byte, error) {
	document, err := parseOOXML(documentXML)
	if err != nil {
		return nil, fmt.Errorf("parse word/document.xml: %w", err)
	}
	root := document.Root()
	body := directElement(root, "body")
	if body == nil {
		return nil, errors.New("word/document.xml has no body")
	}
	sections := descendantElements(root, "sectPr")
	if len(sections) == 0 {
		return nil, errors.New("word/document.xml has no final section properties")
	}
	finalSection := sections[len(sections)-1]
	base := finalSection.Copy()
	global := base.Copy()
	if err := applyPageConfigElement(global, plan.Page); err != nil {
		return nil, err
	}
	continuation := stripFirstSectionSettingsElement(base.Copy())
	if err := applyPageConfigElement(continuation, plan.Page); err != nil {
		return nil, err
	}
	current := global
	for index, boundary := range plan.Boundaries {
		if boundary.BookmarkID == "" {
			return nil, fmt.Errorf("section boundary %d has no bookmark", index+1)
		}
		if boundary.Start == "after_cover" && index != 0 {
			return nil, fmt.Errorf("member %q uses after_cover outside the first member", boundary.SourceKey)
		}
		if boundary.Start == "continuous" && boundary.Section.Orientation != "" {
			return nil, fmt.Errorf("member %q cannot change orientation in a continuous section", boundary.SourceKey)
		}
		targetPage := mergeSection(plan.Page, boundary.Section)
		if err := config.ValidatePageLayout(targetPage); err != nil {
			return nil, fmt.Errorf("member %q section: %w", boundary.SourceKey, err)
		}
		target := continuation.Copy()
		if err := applyPageConfigElement(target, targetPage); err != nil {
			return nil, err
		}
		marker := findBookmark(root, boundary.BookmarkID)
		if marker == nil {
			return nil, fmt.Errorf("member %q bookmark %q was not found in DOCX", boundary.SourceKey, boundary.BookmarkID)
		}
		markerBlock := topLevelBodyChild(marker, body)
		if markerBlock == nil {
			return nil, fmt.Errorf("member %q bookmark %q is outside the document body", boundary.SourceKey, boundary.BookmarkID)
		}
		if !hasBodyBlockBefore(body, markerBlock) {
			current = target
			continue
		}
		if boundary.Start == "new_page" && boundary.Section == (config.SectionConfig{}) && sectionGeometryKey(target) == sectionGeometryKey(current) {
			setPageBreakBefore(markerBlock)
			continue
		}
		breakType := sectionType(boundary.Start, boundary.Section)
		if breakType == "" && sectionGeometryKey(target) == sectionGeometryKey(current) {
			continue
		}
		properties := current.Copy()
		setSectionTypeElement(properties, breakType)
		body.InsertChildAt(markerBlock.Index(), sectionBreakParagraph(properties))
		current = target
	}
	parent := finalSection.Parent()
	position := finalSection.Index()
	parent.RemoveChild(finalSection)
	parent.InsertChildAt(position, current)
	return writeOOXML(document)
}

func bookmarkPattern(name string) *regexp.Regexp {
	return regexp.MustCompile(`<w:bookmarkStart\b[^>]*\bw:name="` + regexp.QuoteMeta(name) + `"[^>]*/>`)
}

func findBookmark(root *etree.Element, name string) *etree.Element {
	for _, marker := range descendantElements(root, "bookmarkStart") {
		if wordAttr(marker, "name") == name {
			return marker
		}
	}
	return nil
}

func hasBodyBlockBefore(body, target *etree.Element) bool {
	for _, child := range body.ChildElements() {
		if child == target {
			return false
		}
		if child.Space == "w" && (child.Tag == "p" || child.Tag == "tbl") {
			return true
		}
	}
	return false
}

func sectionBreakParagraph(section *etree.Element) *etree.Element {
	paragraph := wordElement("p")
	properties := wordElement("pPr")
	properties.AddChild(section)
	paragraph.AddChild(properties)
	return paragraph
}

func setPageBreakBefore(block *etree.Element) {
	paragraph := block
	if paragraph.Space != "w" || paragraph.Tag != "p" {
		paragraph = nextSiblingElement(block)
	}
	if paragraph == nil || paragraph.Space != "w" || paragraph.Tag != "p" {
		return
	}
	properties := directElement(paragraph, "pPr")
	if properties == nil {
		properties = ensureFirstChild(paragraph, "pPr")
	}
	pageBreak := wordElement("pageBreakBefore")
	setWordAttr(pageBreak, "val", "1")
	upsertOrderedChild(properties, pageBreak, paragraphPropertyOrder)
}

func sectionType(start string, section config.SectionConfig) string {
	switch start {
	case "continuous":
		return "continuous"
	case "odd_page":
		return "oddPage"
	case "after_cover", "new_page", "new_section":
		return "nextPage"
	case "":
		if section.Orientation != "" || section.Margins != (config.MarginsConfig{}) {
			return "continuous"
		}
	}
	return ""
}

func setSectionType(section, value string) string {
	_, element, err := parseWordFragment(section)
	if err != nil {
		return section
	}
	setSectionTypeElement(element, value)
	result, err := writeWordFragment(element)
	if err != nil {
		return section
	}
	return result
}

func setSectionTypeElement(section *etree.Element, value string) {
	removeDirectElements(section, "type")
	if value == "" {
		sortOrderedChildren(section, sectionPropertyOrder)
		return
	}
	element := wordElement("type")
	setWordAttr(element, "val", value)
	upsertOrderedChild(section, element, sectionPropertyOrder)
}

func mergeSection(page config.PageConfig, section config.SectionConfig) config.PageConfig {
	if section.Orientation != "" {
		page.Orientation = section.Orientation
	}
	if section.Margins.Top != "" {
		page.Margins.Top = section.Margins.Top
	}
	if section.Margins.Right != "" {
		page.Margins.Right = section.Margins.Right
	}
	if section.Margins.Bottom != "" {
		page.Margins.Bottom = section.Margins.Bottom
	}
	if section.Margins.Left != "" {
		page.Margins.Left = section.Margins.Left
	}
	return page
}

func applyPageConfig(section string, page config.PageConfig) (string, error) {
	_, element, err := parseWordFragment(section)
	if err != nil {
		return "", err
	}
	if err := applyPageConfigElement(element, page); err != nil {
		return "", err
	}
	return writeWordFragment(element)
}

func applyPageConfigElement(section *etree.Element, page config.PageConfig) error {
	if !hasPageSettings(page) {
		return nil
	}
	width, height := config.NamedPageTwips(page.Size)
	var err error
	if page.Width != "" {
		width, err = config.LayoutTwips(page.Width)
		if err != nil {
			return err
		}
	}
	if page.Height != "" {
		height, err = config.LayoutTwips(page.Height)
		if err != nil {
			return err
		}
	}
	existingSize := directElement(section, "pgSz")
	if width == 0 {
		width = intWordAttr(existingSize, "w", 12240)
	}
	if height == 0 {
		height = intWordAttr(existingSize, "h", 15840)
	}
	if page.Orientation == "landscape" && width < height {
		width, height = height, width
	}
	if page.Orientation == "portrait" && width > height {
		width, height = height, width
	}
	if page.Size != "" || page.Width != "" || page.Height != "" || page.Orientation != "" {
		size := wordElement("pgSz")
		setWordAttr(size, "w", strconv.FormatInt(width, 10))
		setWordAttr(size, "h", strconv.FormatInt(height, 10))
		if page.Orientation != "" {
			setWordAttr(size, "orient", page.Orientation)
		}
		upsertOrderedChild(section, size, sectionPropertyOrder)
	}
	if page.Margins != (config.MarginsConfig{}) {
		existing := directElement(section, "pgMar")
		values := map[string]int64{"top": intWordAttr(existing, "top", 1440), "right": intWordAttr(existing, "right", 1440), "bottom": intWordAttr(existing, "bottom", 1440), "left": intWordAttr(existing, "left", 1440), "header": intWordAttr(existing, "header", 720), "footer": intWordAttr(existing, "footer", 720), "gutter": intWordAttr(existing, "gutter", 0)}
		for name, raw := range map[string]string{"top": page.Margins.Top, "right": page.Margins.Right, "bottom": page.Margins.Bottom, "left": page.Margins.Left} {
			if raw == "" {
				continue
			}
			values[name], err = config.LayoutTwips(raw)
			if err != nil {
				return err
			}
		}
		if values["left"]+values["right"] > width-720 {
			return errors.New("page left and right margins must leave at least 0.5in of usable width")
		}
		if values["top"]+values["bottom"] > height-720 {
			return errors.New("page top and bottom margins must leave at least 0.5in of usable height")
		}
		margin := wordElement("pgMar")
		for _, name := range []string{"top", "right", "bottom", "left", "header", "footer", "gutter"} {
			setWordAttr(margin, name, strconv.FormatInt(values[name], 10))
		}
		upsertOrderedChild(section, margin, sectionPropertyOrder)
	}
	sortOrderedChildren(section, sectionPropertyOrder)
	return nil
}

func int64Attribute(element, name string, fallback int64) int64 {
	pattern := regexp.MustCompile(regexp.QuoteMeta(name) + `="(\d+)"`)
	match := pattern.FindStringSubmatch(element)
	if len(match) != 2 {
		return fallback
	}
	value, err := strconv.ParseInt(match[1], 10, 64)
	if err != nil {
		return fallback
	}
	return value
}

func sectionContentWidth(section string) (int64, error) {
	_, element, err := parseWordFragment(section)
	if err != nil {
		return 0, err
	}
	return sectionContentWidthElement(element)
}

func sectionContentWidthElement(section *etree.Element) (int64, error) {
	size := directElement(section, "pgSz")
	margins := directElement(section, "pgMar")
	width := intWordAttr(size, "w", 12240)
	left := intWordAttr(margins, "left", 1440)
	right := intWordAttr(margins, "right", 1440)
	gutter := intWordAttr(margins, "gutter", 0)
	content := width - left - right - gutter
	if content <= 0 {
		return 0, fmt.Errorf("section content width must be positive: page=%d left=%d right=%d gutter=%d", width, left, right, gutter)
	}
	return content, nil
}

func sectionGeometryKey(section *etree.Element) string {
	size := directElement(section, "pgSz")
	margins := directElement(section, "pgMar")
	return fmt.Sprintf("%d:%d:%s:%d:%d:%d:%d:%d", intWordAttr(size, "w", 12240), intWordAttr(size, "h", 15840), wordAttr(size, "orient"), intWordAttr(margins, "top", 1440), intWordAttr(margins, "right", 1440), intWordAttr(margins, "bottom", 1440), intWordAttr(margins, "left", 1440), intWordAttr(margins, "gutter", 0))
}

func hasPageSettings(page config.PageConfig) bool {
	return page.Size != "" || page.Width != "" || page.Height != "" || page.Orientation != "" || page.Margins != (config.MarginsConfig{}) || page.PageNumberStart != nil
}

func hasPageGeometrySettings(page config.PageConfig) bool {
	return page.Size != "" || page.Width != "" || page.Height != "" || page.Orientation != "" || page.Margins != (config.MarginsConfig{})
}

func hasLayoutSettings(plan LayoutPlan) bool {
	return hasPageSettings(plan.Page) || len(plan.Boundaries) > 0 || changesHeaderFooter(plan.Header) || changesHeaderFooter(plan.Footer) || plan.Headings.KeepWithNext || len(plan.Headings.CaptionStyles) > 0 || plan.Tables.RepeatHeaderRows || plan.Tables.WideTableColumns > 0 || len(plan.Tables.PreserveWidthStyles) > 0
}
