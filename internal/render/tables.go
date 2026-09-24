package render

import (
	"fmt"
	"html"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/beevik/etree"
	"github.com/goliatone/mdoc/internal/config"
)

// These patterns remain available to tests and compatibility helpers. Production
// mutations use the namespace-aware element tree below.
var tableElementPattern = regexp.MustCompile(`(?s)<w:tbl\b.*?</w:tbl>`)
var tableGridPattern = regexp.MustCompile(`(?s)<w:tblGrid\b.*?</w:tblGrid>`)
var gridColumnPattern = regexp.MustCompile(`<w:gridCol\b[^>]*/>`)
var tableRowPattern = regexp.MustCompile(`(?s)<w:tr\b.*?</w:tr>`)
var tableCellPattern = regexp.MustCompile(`(?s)<w:tc\b.*?</w:tc>`)
var tablePropertiesPattern = regexp.MustCompile(`(?s)<w:tblPr\b[^>]*>.*?</w:tblPr>|<w:tblPr\b[^>]*/>`)
var tableWidthPattern = regexp.MustCompile(`<w:tblW\b[^>]*/>`)
var tableIndentPattern = regexp.MustCompile(`<w:tblInd\b[^>]*/>`)
var tableLayoutPattern = regexp.MustCompile(`<w:tblLayout\b[^>]*/>`)
var cellPropertiesPattern = regexp.MustCompile(`(?s)<w:tcPr\b[^>]*>.*?</w:tcPr>|<w:tcPr\b[^>]*/>`)
var cellWidthPattern = regexp.MustCompile(`<w:tcW\b[^>]*/>`)
var cellTextPattern = regexp.MustCompile(`(?s)<w:t\b[^>]*>(.*?)</w:t>`)
var gridSpanPattern = regexp.MustCompile(`<w:gridSpan\b[^>]*\bw:val="(\d+)"[^>]*/>`)

func applyFlowAndTables(documentXML []byte, plan LayoutPlan) ([]byte, []string, error) {
	document, err := parseOOXML(documentXML)
	if err != nil {
		return nil, nil, fmt.Errorf("parse word/document.xml: %w", err)
	}
	applyKeepWithNextElements(document.Root(), plan.Headings.KeepWithNext, plan.Headings.CaptionStyles)
	removeKeepWithNextBeforeTableElements(document.Root())
	warnings := []string{}
	for index, table := range descendantElements(document.Root(), "tbl") {
		if plan.Tables.RepeatHeaderRows {
			repeatFirstTableRowElement(table)
		}
		columns := tableColumnCountElement(table)
		wide := plan.Tables.WideTableColumns > 0 && columns > plan.Tables.WideTableColumns
		if !wide {
			continue
		}
		message := fmt.Sprintf("table %d has %d columns; configured wide table threshold is %d", index+1, columns, plan.Tables.WideTableColumns)
		switch plan.Tables.WideTable {
		case "error":
			return nil, nil, fmt.Errorf("%s", message)
		case "warning":
			warnings = append(warnings, message)
		}
	}
	if plan.Tables.WideTable == "landscape_section" && plan.Tables.WideTableColumns > 0 {
		if err := wrapWideTablesInLandscapeDocument(document, plan.Tables.WideTableColumns); err != nil {
			return nil, nil, err
		}
	}
	if shouldNormalizeBodyTables(plan) {
		if err := normalizeDocumentTableElements(document, plan.Tables.PreserveWidthStyles); err != nil {
			return nil, nil, err
		}
	}
	data, err := writeOOXML(document)
	if err != nil {
		return nil, nil, err
	}
	return data, warnings, nil
}

func shouldNormalizeBodyTables(plan LayoutPlan) bool {
	return hasPageGeometrySettings(plan.Page) || len(plan.Boundaries) > 0 || plan.Tables.RepeatHeaderRows || plan.Tables.WideTableColumns > 0 || len(plan.Tables.PreserveWidthStyles) > 0
}

func repeatFirstTableRow(table string) string {
	_, element, err := parseWordFragment(table)
	if err != nil {
		return table
	}
	repeatFirstTableRowElement(element)
	result, err := writeWordFragment(element)
	if err != nil {
		return table
	}
	return result
}

func repeatFirstTableRowElement(table *etree.Element) {
	rows := tableRows(table)
	if len(rows) == 0 {
		return
	}
	properties := ensureFirstChild(rows[0], "trPr")
	upsertOrderedChild(properties, wordElement("tblHeader"), tableRowPropertyOrder)
}

func tableColumnCount(table string) int {
	_, element, err := parseWordFragment(table)
	if err != nil {
		return 0
	}
	return tableColumnCountElement(element)
}

func tableColumnCountElement(table *etree.Element) int {
	grid := directElement(table, "tblGrid")
	if grid == nil {
		return 0
	}
	return len(directElements(grid, "gridCol"))
}

func normalizeDocumentTables(source string) (string, error) {
	document, err := parseOOXML([]byte(source))
	if err != nil {
		return "", err
	}
	if err := normalizeDocumentTableElements(document, nil); err != nil {
		return "", err
	}
	data, err := writeOOXML(document)
	return string(data), err
}

func normalizeDocumentTableElements(document *etree.Document, preserveStyles []string) error {
	root := document.Root()
	body := directElement(root, "body")
	if body == nil {
		return fmt.Errorf("word/document.xml has no body")
	}
	tables := descendantElements(body, "tbl")
	if len(tables) == 0 {
		return nil
	}
	if len(descendantElements(body, "sectPr")) == 0 {
		return fmt.Errorf("word/document.xml has tables but no section properties")
	}
	preserved := map[string]bool{"Cover Field Grid": true, "CoverFieldGrid": true}
	for _, style := range preserveStyles {
		preserved[strings.TrimSpace(style)] = true
	}
	for index, table := range tables {
		section := sectionForBodyElement(body, table)
		if section == nil {
			return fmt.Errorf("table %d has no containing section", index+1)
		}
		contentWidth, err := sectionContentWidthElement(section)
		if err != nil {
			return fmt.Errorf("table %d: %w", index+1, err)
		}
		nestedCell := closestAncestor(table, "tc")
		if nestedCell != nil {
			if properties := directElement(nestedCell, "tcPr"); properties != nil {
				if width := intWordAttr(directElement(properties, "tcW"), "w", 0); width > 0 && width < contentWidth {
					contentWidth = width
				}
			}
		}
		preserve := preserved[tableStyle(table)] || nestedCell != nil
		if err := normalizeTableGeometryElement(table, contentWidth, preserve); err != nil {
			return fmt.Errorf("table %d: %w", index+1, err)
		}
	}
	return nil
}

func normalizeTableGeometry(table string, contentWidth int64) (string, error) {
	_, element, err := parseWordFragment(table)
	if err != nil {
		return "", err
	}
	preserve := tableStyle(element) == "Cover Field Grid" || tableStyle(element) == "CoverFieldGrid" || tableHasAncestor(element, "tc")
	if err := normalizeTableGeometryElement(element, contentWidth, preserve); err != nil {
		return "", err
	}
	return writeWordFragment(element)
}

func normalizeTableGeometryElement(table *etree.Element, contentWidth int64, preserve bool) error {
	existing := tableGridWidthElements(table)
	if len(existing) == 0 {
		return fmt.Errorf("table has no grid columns")
	}
	targetWidth := contentWidth
	if preserve {
		if current := sumWidths(existing); current > 0 && current < targetWidth {
			targetWidth = current
		}
	}
	weights := append([]int64(nil), existing...)
	if !preserve && equalWidths(existing) {
		weights = tableContentWeightElements(table, len(existing))
	}
	widths := allocateWeightedWidths(weights, targetWidth)
	setTablePropertyElements(table, targetWidth, 0)
	replaceTableGridElement(table, widths)
	if err := setTableCellWidthElements(table, widths); err != nil {
		return err
	}
	return auditTableGeometryElement(table)
}

func tableGridWidths(table string) []int64 {
	_, element, err := parseWordFragment(table)
	if err != nil {
		return nil
	}
	return tableGridWidthElements(element)
}

func tableGridWidthElements(table *etree.Element) []int64 {
	grid := directElement(table, "tblGrid")
	result := []int64{}
	if grid == nil {
		return result
	}
	for _, column := range directElements(grid, "gridCol") {
		result = append(result, intWordAttr(column, "w", 0))
	}
	return result
}

func sumWidths(widths []int64) int64 {
	var total int64
	for _, width := range widths {
		total += width
	}
	return total
}

func equalWidths(widths []int64) bool {
	if len(widths) < 2 {
		return true
	}
	minimum, maximum := widths[0], widths[0]
	for _, width := range widths[1:] {
		if width < minimum {
			minimum = width
		}
		if width > maximum {
			maximum = width
		}
	}
	// Pandoc and Word may distribute remainder twips across otherwise equal
	// columns. Treat a one-twip difference as an equal default grid.
	return maximum-minimum <= 1
}

func tableContentWeights(table string, columns int) []int64 {
	_, element, err := parseWordFragment(table)
	if err != nil {
		return nil
	}
	return tableContentWeightElements(element, columns)
}

func tableContentWeightElements(table *etree.Element, columns int) []int64 {
	weights := make([]int64, columns)
	for index := range weights {
		weights[index] = 8
	}
	for _, row := range tableRows(table) {
		column := rowGridBefore(row)
		for _, cell := range rowCells(row) {
			span := cellGridSpan(cell)
			if span == 1 && column < len(weights) {
				length := int64(utf8.RuneCountInString(strings.TrimSpace(cellText(cell))))
				if length < 8 {
					length = 8
				}
				if length > 48 {
					length = 48
				}
				if length > weights[column] {
					weights[column] = length
				}
			}
			column += span
		}
	}
	return weights
}

func allocateEqualWidths(total int64, count int) []int64 {
	weights := make([]int64, count)
	for index := range weights {
		weights[index] = 1
	}
	return allocateWeightedWidths(weights, total)
}

func allocateWeightedWidths(weights []int64, total int64) []int64 {
	if len(weights) == 0 || total <= 0 {
		return nil
	}
	minimum := int64(720)
	if minimum*int64(len(weights)) > total {
		minimum = total / int64(len(weights)*2)
	}
	if minimum < 1 {
		minimum = 1
	}
	remaining := total - minimum*int64(len(weights))
	weightTotal := sumWidths(weights)
	if weightTotal <= 0 {
		weightTotal = int64(len(weights))
		for index := range weights {
			weights[index] = 1
		}
	}
	result := make([]int64, len(weights))
	used := int64(0)
	for index := range weights {
		if index == len(weights)-1 {
			result[index] = total - used
			break
		}
		result[index] = minimum + remaining*weights[index]/weightTotal
		used += result[index]
	}
	return result
}

func setTableProperties(table string, width, indent int64) string {
	_, element, err := parseWordFragment(table)
	if err != nil {
		return table
	}
	setTablePropertyElements(element, width, indent)
	result, err := writeWordFragment(element)
	if err != nil {
		return table
	}
	return result
}

func setTablePropertyElements(table *etree.Element, width, indent int64) {
	properties := ensureFirstChild(table, "tblPr")
	tableWidth := wordElement("tblW")
	setWordAttr(tableWidth, "w", strconv.FormatInt(width, 10))
	setWordAttr(tableWidth, "type", "dxa")
	upsertOrderedChild(properties, tableWidth, tablePropertyOrder)
	tableIndent := wordElement("tblInd")
	setWordAttr(tableIndent, "w", strconv.FormatInt(indent, 10))
	setWordAttr(tableIndent, "type", "dxa")
	upsertOrderedChild(properties, tableIndent, tablePropertyOrder)
	tableLayout := wordElement("tblLayout")
	setWordAttr(tableLayout, "type", "fixed")
	upsertOrderedChild(properties, tableLayout, tablePropertyOrder)
}

func replaceTableGrid(table string, widths []int64) string {
	_, element, err := parseWordFragment(table)
	if err != nil {
		return table
	}
	replaceTableGridElement(element, widths)
	result, err := writeWordFragment(element)
	if err != nil {
		return table
	}
	return result
}

func replaceTableGridElement(table *etree.Element, widths []int64) {
	grid := directElement(table, "tblGrid")
	if grid == nil {
		grid = wordElement("tblGrid")
		properties := directElement(table, "tblPr")
		position := 0
		if properties != nil {
			position = properties.Index() + 1
		}
		table.InsertChildAt(position, grid)
	}
	for _, column := range directElements(grid, "gridCol") {
		grid.RemoveChild(column)
	}
	for _, width := range widths {
		column := wordElement("gridCol")
		setWordAttr(column, "w", strconv.FormatInt(width, 10))
		grid.AddChild(column)
	}
}

func setTableCellWidths(table string, widths []int64) (string, error) {
	_, element, err := parseWordFragment(table)
	if err != nil {
		return "", err
	}
	if err := setTableCellWidthElements(element, widths); err != nil {
		return "", err
	}
	return writeWordFragment(element)
}

func setTableCellWidthElements(table *etree.Element, widths []int64) error {
	for rowIndex, row := range tableRows(table) {
		column := rowGridBefore(row)
		for _, cell := range rowCells(row) {
			span := cellGridSpan(cell)
			if column+span > len(widths) {
				return fmt.Errorf("row %d exceeds the table grid", rowIndex+1)
			}
			setCellWidthElement(cell, sumWidths(widths[column:column+span]))
			column += span
		}
		column += rowGridAfter(row)
		if column != len(widths) {
			return fmt.Errorf("row %d covers %d columns, expected %d", rowIndex+1, column, len(widths))
		}
	}
	return nil
}

func setCellWidth(cell string, width int64) string {
	_, element, err := parseWordFragment(cell)
	if err != nil {
		return cell
	}
	setCellWidthElement(element, width)
	result, err := writeWordFragment(element)
	if err != nil {
		return cell
	}
	return result
}

func setCellWidthElement(cell *etree.Element, width int64) {
	properties := ensureFirstChild(cell, "tcPr")
	value := wordElement("tcW")
	setWordAttr(value, "w", strconv.FormatInt(width, 10))
	setWordAttr(value, "type", "dxa")
	upsertOrderedChild(properties, value, tableCellPropertyOrder)
}

func auditDocumentTableGeometry(source string) error {
	document, err := parseOOXML([]byte(source))
	if err != nil {
		return err
	}
	for index, table := range descendantElements(document.Root(), "tbl") {
		if err := auditTableGeometryElement(table); err != nil {
			return fmt.Errorf("table %d geometry: %w", index+1, err)
		}
	}
	return nil
}

func auditTableGeometry(table string) error {
	_, element, err := parseWordFragment(table)
	if err != nil {
		return err
	}
	return auditTableGeometryElement(element)
}

func auditTableGeometryElement(table *etree.Element) error {
	properties := directElement(table, "tblPr")
	tableWidthElement := directElement(properties, "tblW")
	if tableWidthElement == nil || wordAttr(tableWidthElement, "type") != "dxa" {
		return fmt.Errorf("table width must use DXA")
	}
	tableWidth := intWordAttr(tableWidthElement, "w", 0)
	if tableWidth <= 0 {
		return fmt.Errorf("table width must be positive")
	}
	indent := directElement(properties, "tblInd")
	if indent == nil || wordAttr(indent, "type") != "dxa" {
		return fmt.Errorf("table indent must use DXA")
	}
	layout := directElement(properties, "tblLayout")
	if layout == nil || wordAttr(layout, "type") != "fixed" {
		return fmt.Errorf("table layout must be fixed")
	}
	grid := tableGridWidthElements(table)
	if len(grid) == 0 || sumWidths(grid) != tableWidth {
		return fmt.Errorf("grid width %d does not match table width %d", sumWidths(grid), tableWidth)
	}
	for rowIndex, row := range tableRows(table) {
		column := rowGridBefore(row)
		rowWidth := sumWidths(grid[:column])
		for _, cell := range rowCells(row) {
			properties := directElement(cell, "tcPr")
			widthElement := directElement(properties, "tcW")
			if widthElement == nil || wordAttr(widthElement, "type") != "dxa" {
				return fmt.Errorf("row %d has a cell without DXA width", rowIndex+1)
			}
			span := cellGridSpan(cell)
			if column+span > len(grid) {
				return fmt.Errorf("row %d exceeds the table grid", rowIndex+1)
			}
			expected := sumWidths(grid[column : column+span])
			actual := intWordAttr(widthElement, "w", 0)
			if actual != expected {
				return fmt.Errorf("row %d cell width %d does not match grid width %d", rowIndex+1, actual, expected)
			}
			rowWidth += actual
			column += span
		}
		after := rowGridAfter(row)
		if column+after > len(grid) {
			return fmt.Errorf("row %d omitted columns exceed the table grid", rowIndex+1)
		}
		rowWidth += sumWidths(grid[column : column+after])
		if rowWidth != tableWidth {
			return fmt.Errorf("row %d width %d does not match table width %d", rowIndex+1, rowWidth, tableWidth)
		}
	}
	return nil
}

func stringAttribute(element, name string) string {
	pattern := regexp.MustCompile(regexp.QuoteMeta(name) + `="([^"]+)"`)
	match := pattern.FindStringSubmatch(element)
	if len(match) != 2 {
		return ""
	}
	return match[1]
}

func wrapWideTablesInLandscape(source string, threshold int) (string, error) {
	document, err := parseOOXML([]byte(source))
	if err != nil {
		return "", err
	}
	if err := wrapWideTablesInLandscapeDocument(document, threshold); err != nil {
		return "", err
	}
	data, err := writeOOXML(document)
	return string(data), err
}

type wideTableGroup struct {
	first   *etree.Element
	last    *etree.Element
	section *etree.Element
}

func wrapWideTablesInLandscapeDocument(document *etree.Document, threshold int) error {
	body := directElement(document.Root(), "body")
	if body == nil {
		return fmt.Errorf("word/document.xml has no body")
	}
	groups := []wideTableGroup{}
	for child := firstBodyElement(body); child != nil; {
		if child.Space != "w" || child.Tag != "tbl" || tableColumnCountElement(child) <= threshold {
			child = nextSiblingElement(child)
			continue
		}
		group := wideTableGroup{first: child, last: child, section: sectionForBodyElement(body, child)}
		for next := nextSiblingElement(group.last); next != nil && next.Space == "w" && next.Tag == "tbl" && tableColumnCountElement(next) > threshold; next = nextSiblingElement(group.last) {
			group.last = next
		}
		if group.section == nil {
			return fmt.Errorf("wide table has no containing section")
		}
		groups = append(groups, group)
		child = nextSiblingElement(group.last)
	}
	if len(groups) == 0 {
		return nil
	}
	bySection := map[*etree.Element][]wideTableGroup{}
	for _, group := range groups {
		bySection[group.section] = append(bySection[group.section], group)
	}
	for section, sectionGroups := range bySection {
		original := section.Copy()
		stripFirstSectionSettingsElement(section)
		for index := len(sectionGroups) - 1; index >= 0; index-- {
			group := sectionGroups[index]
			firstInSection := index == 0
			beforeSection := section.Copy()
			if firstInSection {
				beforeSection = original.Copy()
			}
			landscape := section.Copy()
			if err := applyPageConfigElement(landscape, config.PageConfig{Orientation: "landscape"}); err != nil {
				return err
			}
			setSectionTypeElement(landscape, "nextPage")
			if hasBodyBlockBeforeInSection(body, group.first) {
				setSectionTypeElement(beforeSection, "nextPage")
				body.InsertChildAt(group.first.Index(), sectionBreakParagraph(beforeSection))
			} else if firstInSection {
				landscape = original.Copy()
				if err := applyPageConfigElement(landscape, config.PageConfig{Orientation: "landscape"}); err != nil {
					return err
				}
				setSectionTypeElement(landscape, "nextPage")
			}
			body.InsertChildAt(group.last.Index()+1, sectionBreakParagraph(landscape))
		}
	}
	return nil
}

func stripFirstSectionSettings(section string) string {
	_, element, err := parseWordFragment(section)
	if err != nil {
		return section
	}
	stripFirstSectionSettingsElement(element)
	result, err := writeWordFragment(element)
	if err != nil {
		return section
	}
	return result
}

func stripFirstSectionSettingsElement(section *etree.Element) *etree.Element {
	removeDirectElements(section, "titlePg")
	if pageNumber := directElement(section, "pgNumType"); pageNumber != nil {
		pageNumber.RemoveAttr("w:start")
		if len(pageNumber.Attr) == 0 && len(pageNumber.Child) == 0 {
			section.RemoveChild(pageNumber)
		}
	}
	sortOrderedChildren(section, sectionPropertyOrder)
	return section
}

func pageConfigWithOrientation(_ string, orientation string) config.PageConfig {
	return config.PageConfig{Orientation: orientation}
}

func tableRows(table *etree.Element) []*etree.Element {
	return descendantsUntilNestedTable(table, "tr")
}

func rowCells(row *etree.Element) []*etree.Element {
	return descendantsUntilNestedTable(row, "tc")
}

func descendantsUntilNestedTable(root *etree.Element, tag string) []*etree.Element {
	result := []*etree.Element{}
	var visit func(*etree.Element)
	visit = func(parent *etree.Element) {
		for _, child := range parent.ChildElements() {
			if child != root && child.Space == "w" && child.Tag == "tbl" {
				continue
			}
			if child.Space == "w" && child.Tag == tag {
				result = append(result, child)
			}
			visit(child)
		}
	}
	visit(root)
	return result
}

func ensureFirstChild(parent *etree.Element, tag string) *etree.Element {
	if child := directElement(parent, tag); child != nil {
		return child
	}
	child := wordElement(tag)
	position := 0
	for position < len(parent.Child) {
		if _, ok := parent.Child[position].(*etree.Element); ok {
			break
		}
		position++
	}
	parent.InsertChildAt(position, child)
	return child
}

func tableStyle(table *etree.Element) string {
	properties := directElement(table, "tblPr")
	return wordAttr(directElement(properties, "tblStyle"), "val")
}

func tableHasAncestor(element *etree.Element, tag string) bool {
	return closestAncestor(element, tag) != nil
}

func closestAncestor(element *etree.Element, tag string) *etree.Element {
	for parent := element.Parent(); parent != nil; parent = parent.Parent() {
		if parent.Space == "w" && parent.Tag == tag {
			return parent
		}
	}
	return nil
}

func cellGridSpan(cell *etree.Element) int {
	properties := directElement(cell, "tcPr")
	span := int(intWordAttr(directElement(properties, "gridSpan"), "val", 1))
	if span < 1 {
		return 1
	}
	return span
}

func rowGridBefore(row *etree.Element) int {
	properties := directElement(row, "trPr")
	return int(intWordAttr(directElement(properties, "gridBefore"), "val", 0))
}

func rowGridAfter(row *etree.Element) int {
	properties := directElement(row, "trPr")
	return int(intWordAttr(directElement(properties, "gridAfter"), "val", 0))
}

func cellText(cell *etree.Element) string {
	var result strings.Builder
	var visit func(*etree.Element)
	visit = func(parent *etree.Element) {
		for _, child := range parent.ChildElements() {
			if child != cell && child.Space == "w" && child.Tag == "tbl" {
				continue
			}
			if child.Space == "w" && child.Tag == "t" {
				result.WriteString(html.UnescapeString(child.Text()))
			}
			visit(child)
		}
	}
	visit(cell)
	return result.String()
}

func sectionForBodyElement(body, element *etree.Element) *etree.Element {
	block := topLevelBodyChild(element, body)
	if block == nil {
		return nil
	}
	for current := block; current != nil; current = nextSiblingElement(current) {
		if current.Space == "w" && current.Tag == "sectPr" {
			return current
		}
		if sections := descendantElements(current, "sectPr"); len(sections) > 0 {
			return sections[0]
		}
	}
	return nil
}

func firstBodyElement(body *etree.Element) *etree.Element {
	for _, token := range body.Child {
		if element, ok := token.(*etree.Element); ok {
			return element
		}
	}
	return nil
}

func hasBodyBlockBeforeInSection(body, target *etree.Element) bool {
	for current := previousSiblingElement(target); current != nil; current = previousSiblingElement(current) {
		if current.Space != "w" {
			continue
		}
		if current.Tag == "sectPr" || len(descendantElements(current, "sectPr")) > 0 {
			return false
		}
		if current.Tag == "p" || current.Tag == "tbl" {
			return true
		}
	}
	return false
}
