package render

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/beevik/etree"
	"github.com/goliatone/mdoc/internal/config"
)

func TestApplyLayoutAddsFlowAndRepeatHeaderControls(t *testing.T) {
	directory := t.TempDir()
	input := filepath.Join(directory, "input.docx")
	output := filepath.Join(directory, "output.docx")
	body := paragraphWithStyle("Heading1", "Heading") + paragraphWithStyle("Caption", "Caption") + paragraphWithStyle("BodyText", "Body") + tableXML(3)
	writeLayoutFixture(t, input, body)
	warnings, err := ApplyLayoutWithReport(input, output, LayoutPlan{Headings: config.HeadingLayoutConfig{KeepWithNext: true, CaptionStyles: []string{"Caption"}}, Tables: config.TableLayoutConfig{RepeatHeaderRows: true, WideTableColumns: 2, WideTable: "warning"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "3 columns") {
		t.Fatalf("warnings = %#v", warnings)
	}
	xml := string(readLayoutDocument(t, output))
	if strings.Count(xml, "<w:keepNext/>") != 2 || !strings.Contains(xml, "<w:tblHeader/>") {
		t.Fatalf("flow controls = %s", xml)
	}
	if strings.Contains(xml, "<w:trHeight") {
		t.Fatalf("fixed row height was added: %s", xml)
	}
}

func TestHeadingBeforeTableDoesNotKeepWholeTableWithHeading(t *testing.T) {
	directory := t.TempDir()
	input := filepath.Join(directory, "input.docx")
	output := filepath.Join(directory, "output.docx")
	body := paragraphWithStyle("Heading2", "Prose heading") + paragraphWithStyle("BodyText", "Body") + paragraphWithStyle("Heading3", "Table heading") + tableXML(3)
	writeLayoutFixture(t, input, body)
	if err := ApplyLayout(input, output, LayoutPlan{Headings: config.HeadingLayoutConfig{KeepWithNext: true}}); err != nil {
		t.Fatal(err)
	}
	xml := string(readLayoutDocument(t, output))
	if !strings.Contains(xml, `<w:pStyle w:val="Heading2"/><w:keepNext/>`) {
		t.Fatalf("prose heading lost keep-next: %s", xml)
	}
	tableHeading := paragraphElementPattern.FindAllString(xml, -1)[2]
	if !strings.Contains(tableHeading, `<w:keepNext w:val="0"/>`) {
		t.Fatalf("table heading did not override style keep-next: %s", tableHeading)
	}
}

func TestWideTableThresholdAndPolicies(t *testing.T) {
	document := []byte(`<w:document><w:body>` + tableXML(3) + testSectionXML() + `</w:body></w:document>`)
	tests := []struct {
		name      string
		threshold int
		policy    string
		warnings  int
		fails     bool
	}{
		{"below", 4, "warning", 0, false}, {"equal", 3, "warning", 0, false}, {"above", 2, "warning", 1, false}, {"disabled", 0, "error", 0, false}, {"error", 2, "error", 0, true}, {"normal", 2, "normal_flow", 0, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, warnings, err := applyFlowAndTables(document, LayoutPlan{Tables: config.TableLayoutConfig{WideTableColumns: test.threshold, WideTable: test.policy}})
			if (err != nil) != test.fails || len(warnings) != test.warnings {
				t.Fatalf("warnings = %#v, error = %v", warnings, err)
			}
		})
	}
}

func TestLandscapeWideTableReturnsToPortrait(t *testing.T) {
	directory := t.TempDir()
	input := filepath.Join(directory, "input.docx")
	output := filepath.Join(directory, "output.docx")
	writeLayoutFixture(t, input, paragraphWithStyle("BodyText", "Before")+tableXML(4)+paragraphWithStyle("BodyText", "After"))
	plan := LayoutPlan{Page: config.PageConfig{Size: "letter", Orientation: "portrait"}, Tables: config.TableLayoutConfig{WideTableColumns: 3, WideTable: "landscape_section"}}
	if err := ApplyLayout(input, output, plan); err != nil {
		t.Fatal(err)
	}
	xml := string(readLayoutDocument(t, output))
	if strings.Count(xml, "<w:sectPr") != 3 || strings.Count(xml, `w:orient="landscape"`) != 1 || strings.Count(xml, `w:orient="portrait"`) != 2 {
		t.Fatalf("landscape table sections = %s", xml)
	}
	tablePosition := strings.Index(xml, "<w:tbl>")
	landscapePosition := strings.Index(xml, `w:orient="landscape"`)
	beforePosition := strings.Index(xml, "Before")
	afterPosition := strings.Index(xml, "After")
	if !(beforePosition < tablePosition && tablePosition < landscapePosition && landscapePosition < afterPosition) {
		t.Fatalf("section order = %s", xml)
	}
}

func TestLandscapeWideTableDoesNotRestartPageNumbers(t *testing.T) {
	directory := t.TempDir()
	input := filepath.Join(directory, "input.docx")
	output := filepath.Join(directory, "output.docx")
	writeLayoutFixture(t, input, tableXML(4))
	start := 5
	plan := LayoutPlan{Page: config.PageConfig{Size: "letter", Orientation: "portrait", PageNumberStart: &start}, Header: config.HeaderFooterConfig{FirstPage: &config.HeaderFooterContent{Center: "First"}}, Tables: config.TableLayoutConfig{WideTableColumns: 3, WideTable: "landscape_section"}}
	if err := ApplyLayout(input, output, plan); err != nil {
		t.Fatal(err)
	}
	xml := string(readLayoutDocument(t, output))
	if strings.Count(xml, `w:start="5"`) != 1 || strings.Count(xml, `<w:titlePg/>`) != 1 {
		t.Fatalf("first-section settings were copied: %s", xml)
	}
}

func TestWideTableErrorDoesNotWriteOutput(t *testing.T) {
	directory := t.TempDir()
	input := filepath.Join(directory, "input.docx")
	output := filepath.Join(directory, "output.docx")
	writeLayoutFixture(t, input, tableXML(4))
	err := ApplyLayout(input, output, LayoutPlan{Tables: config.TableLayoutConfig{WideTableColumns: 3, WideTable: "error"}})
	if err == nil || !strings.Contains(err.Error(), "table 1") {
		t.Fatalf("error = %v", err)
	}
	if _, statErr := os.Stat(output); !os.IsNotExist(statErr) {
		t.Fatalf("output exists after error: %v", statErr)
	}
}

func TestNormalizeTableGeometryUsesSectionWidthAndContentWeights(t *testing.T) {
	table := tableXMLWithRows([][]string{
		{"ID", "Finding", "Rating", "Root", "Response"},
		{"E02-F01", "Static file path traversal permits arbitrary file reads", "High; Immediate; Confirmed", "E08-F02", "Intake owner must add route checks and proof"},
	})
	document := `<w:document><w:body>` + table + `<w:sectPr><w:pgSz w:w="12240" w:h="15840"/><w:pgMar w:left="1080" w:right="1080" w:top="1152" w:bottom="1152" w:gutter="0"/></w:sectPr></w:body></w:document>`
	normalized, err := normalizeDocumentTables(document)
	if err != nil {
		t.Fatal(err)
	}
	table = tableElementPattern.FindString(normalized)
	if !strings.Contains(table, `<w:tblW w:w="10080" w:type="dxa"/>`) || !strings.Contains(table, `<w:tblInd w:w="0" w:type="dxa"/>`) || !strings.Contains(table, `<w:tblLayout w:type="fixed"/>`) {
		t.Fatalf("table properties = %s", table)
	}
	widths := tableGridWidths(table)
	if len(widths) != 5 || sumWidths(widths) != 10080 || widths[1] <= widths[0] || widths[4] <= widths[3] {
		t.Fatalf("content widths = %#v", widths)
	}
	if err := auditTableGeometry(table); err != nil {
		t.Fatal(err)
	}
}

func TestNormalizeCoverGridKeepsDeliberateCompactWidth(t *testing.T) {
	table := strings.Replace(tableXML(2), `<w:tblPr/>`, `<w:tblPr><w:tblStyle w:val="Cover Field Grid"/></w:tblPr>`, 1)
	document := `<w:document><w:body>` + table + testSectionXML() + `</w:body></w:document>`
	normalized, err := normalizeDocumentTables(document)
	if err != nil {
		t.Fatal(err)
	}
	table = tableElementPattern.FindString(normalized)
	if !strings.Contains(table, `<w:tblW w:w="2000" w:type="dxa"/>`) {
		t.Fatalf("cover width = %s", table)
	}
}

func TestNormalizeTablePropertiesUseSchemaOrder(t *testing.T) {
	table := `<w:tbl><w:tblPr><w:tblStyle w:val="Table"/><w:tblLook w:val="04A0"/><w:tblW w:w="0" w:type="auto"/></w:tblPr><w:tblGrid><w:gridCol w:w="1000"/><w:gridCol w:w="1000"/></w:tblGrid><w:tr><w:tc><w:tcPr><w:gridSpan w:val="2"/><w:shd w:fill="EEEEEE"/><w:tcW w:w="0" w:type="auto"/></w:tcPr><w:p><w:r><w:t>Spanned</w:t></w:r></w:p></w:tc></w:tr></w:tbl>`
	document := `<w:document xmlns:w="` + wordNamespace + `"><w:body>` + table + testSectionXML() + `</w:body></w:document>`
	normalized, err := normalizeDocumentTables(document)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := parseOOXML([]byte(normalized))
	if err != nil {
		t.Fatal(err)
	}
	if err := auditOOXMLOrder(parsed); err != nil {
		t.Fatal(err)
	}
	properties := descendantElements(parsed.Root(), "tblPr")[0]
	if got := childTagSequence(properties); got != "tblStyle,tblW,tblInd,tblLayout,tblLook" {
		t.Fatalf("table property order = %s", got)
	}
	cellProperties := descendantElements(parsed.Root(), "tcPr")[0]
	if got := childTagSequence(cellProperties); got != "tcW,gridSpan,shd" {
		t.Fatalf("cell property order = %s", got)
	}
}

func TestNormalizeSupportsOmittedGridColumns(t *testing.T) {
	table := `<w:tbl><w:tblPr/><w:tblGrid><w:gridCol w:w="1000"/><w:gridCol w:w="1000"/><w:gridCol w:w="1000"/></w:tblGrid><w:tr><w:trPr><w:gridBefore w:val="1"/><w:gridAfter w:val="1"/></w:trPr><w:tc><w:p><w:r><w:t>Center</w:t></w:r></w:p></w:tc></w:tr></w:tbl>`
	document := `<w:document xmlns:w="` + wordNamespace + `"><w:body>` + table + testSectionXML() + `</w:body></w:document>`
	normalized, err := normalizeDocumentTables(document)
	if err != nil {
		t.Fatal(err)
	}
	if err := auditDocumentTableGeometry(normalized); err != nil {
		t.Fatal(err)
	}
}

func TestNormalizeSupportsNestedTablesWithoutExpandingThem(t *testing.T) {
	nested := `<w:tbl><w:tblPr/><w:tblGrid><w:gridCol w:w="800"/><w:gridCol w:w="1200"/></w:tblGrid><w:tr><w:tc><w:p/></w:tc><w:tc><w:p/></w:tc></w:tr></w:tbl>`
	outer := `<w:tbl><w:tblPr/><w:tblGrid><w:gridCol w:w="3000"/></w:tblGrid><w:tr><w:tc><w:p/>` + nested + `<w:p/></w:tc></w:tr></w:tbl>`
	document := `<w:document xmlns:w="` + wordNamespace + `"><w:body>` + outer + testSectionXML() + `</w:body></w:document>`
	normalized, err := normalizeDocumentTables(document)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := parseOOXML([]byte(normalized))
	if err != nil {
		t.Fatal(err)
	}
	tables := descendantElements(parsed.Root(), "tbl")
	if len(tables) != 2 {
		t.Fatalf("tables = %d", len(tables))
	}
	if got := sumWidths(tableGridWidthElements(tables[0])); got != 9360 {
		t.Fatalf("outer width = %d", got)
	}
	if got := sumWidths(tableGridWidthElements(tables[1])); got != 2000 {
		t.Fatalf("nested width = %d", got)
	}
	if err := auditDocumentTableGeometry(normalized); err != nil {
		t.Fatal(err)
	}
}

func TestNormalizeNestedTableUsesContainingCellWidth(t *testing.T) {
	nested := `<w:tbl><w:tblPr/><w:tblGrid><w:gridCol w:w="3000"/><w:gridCol w:w="3000"/></w:tblGrid><w:tr><w:tc><w:p/></w:tc><w:tc><w:p/></w:tc></w:tr></w:tbl>`
	outer := `<w:tbl><w:tblPr/><w:tblGrid><w:gridCol w:w="3000"/><w:gridCol w:w="3000"/></w:tblGrid><w:tr><w:tc><w:p/>` + nested + `<w:p/></w:tc><w:tc><w:p/></w:tc></w:tr></w:tbl>`
	document := `<w:document xmlns:w="` + wordNamespace + `"><w:body>` + outer + testSectionXML() + `</w:body></w:document>`
	normalized, err := normalizeDocumentTables(document)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := parseOOXML([]byte(normalized))
	if err != nil {
		t.Fatal(err)
	}
	tables := descendantElements(parsed.Root(), "tbl")
	outerWidths := tableGridWidthElements(tables[0])
	if got := sumWidths(tableGridWidthElements(tables[1])); got != outerWidths[0] {
		t.Fatalf("nested width = %d, containing cell width = %d", got, outerWidths[0])
	}
}

func TestPageNumberingDoesNotNormalizeBodyTables(t *testing.T) {
	directory := t.TempDir()
	input := filepath.Join(directory, "input.docx")
	output := filepath.Join(directory, "output.docx")
	writeLayoutFixture(t, input, tableXML(2))
	start := 3
	if err := ApplyLayout(input, output, LayoutPlan{Page: config.PageConfig{PageNumberStart: &start}}); err != nil {
		t.Fatal(err)
	}
	xml := string(readLayoutDocument(t, output))
	table := tableElementPattern.FindString(xml)
	if got := sumWidths(tableGridWidths(table)); got != 2000 {
		t.Fatalf("table width = %d, want unchanged width 2000", got)
	}
}

func TestConfiguredLayoutTableStylePreservesWidth(t *testing.T) {
	table := strings.Replace(tableXML(2), `<w:tblPr/>`, `<w:tblPr><w:tblStyle w:val="Metadata Grid"/></w:tblPr>`, 1)
	document := []byte(`<w:document xmlns:w="` + wordNamespace + `"><w:body>` + table + testSectionXML() + `</w:body></w:document>`)
	updated, _, err := applyFlowAndTables(document, LayoutPlan{Page: config.PageConfig{Size: "letter"}, Tables: config.TableLayoutConfig{PreserveWidthStyles: []string{"Metadata Grid"}}})
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := parseOOXML(updated)
	if err != nil {
		t.Fatal(err)
	}
	if got := sumWidths(tableGridWidthElements(descendantElements(parsed.Root(), "tbl")[0])); got != 2000 {
		t.Fatalf("preserved width = %d", got)
	}
}

func TestPreserveWidthStylesAloneRunsTableNormalization(t *testing.T) {
	directory := t.TempDir()
	input := filepath.Join(directory, "input.docx")
	output := filepath.Join(directory, "output.docx")
	body := strings.Replace(tableXML(2), `<w:tblPr/>`, `<w:tblPr><w:tblStyle w:val="Metadata Grid"/></w:tblPr>`, 1) + tableXML(2)
	writeLayoutFixture(t, input, body)
	plan := LayoutPlan{Tables: config.TableLayoutConfig{PreserveWidthStyles: []string{"Metadata Grid"}}}
	if err := ApplyLayout(input, output, plan); err != nil {
		t.Fatal(err)
	}
	parsed, err := parseOOXML(readLayoutDocument(t, output))
	if err != nil {
		t.Fatal(err)
	}
	tables := descendantElements(parsed.Root(), "tbl")
	if got := sumWidths(tableGridWidthElements(tables[0])); got != 2000 {
		t.Fatalf("preserved table width = %d", got)
	}
	if got := sumWidths(tableGridWidthElements(tables[1])); got != 9360 {
		t.Fatalf("normalized table width = %d", got)
	}
}

func TestLandscapeWideTablesUseActiveSectionAndGroupAdjacentTables(t *testing.T) {
	active := `<w:p><w:pPr><w:sectPr><w:pgSz w:w="12240" w:h="15840"/><w:pgMar w:left="1080" w:right="1080" w:top="1080" w:bottom="1080"/></w:sectPr></w:pPr></w:p>`
	final := `<w:sectPr><w:pgSz w:w="20160" w:h="15840"/><w:pgMar w:left="1440" w:right="1440" w:top="1440" w:bottom="1440"/></w:sectPr>`
	document := `<w:document xmlns:w="` + wordNamespace + `"><w:body>` + paragraphWithStyle("BodyText", "Before") + tableXML(4) + tableXML(4) + active + paragraphWithStyle("BodyText", "After") + final + `</w:body></w:document>`
	updated, err := wrapWideTablesInLandscape(document, 3)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := parseOOXML([]byte(updated))
	if err != nil {
		t.Fatal(err)
	}
	sections := descendantElements(parsed.Root(), "sectPr")
	if len(sections) != 4 {
		t.Fatalf("sections = %d, want 4", len(sections))
	}
	landscapeCount := 0
	for _, section := range sections {
		size := directElement(section, "pgSz")
		if wordAttr(size, "orient") == "landscape" {
			landscapeCount++
			if intWordAttr(size, "w", 0) != 15840 {
				t.Fatalf("landscape used wrong section width: %s", wordAttr(size, "w"))
			}
		}
	}
	if landscapeCount != 1 {
		t.Fatalf("landscape sections = %d", landscapeCount)
	}
}

func childTagSequence(parent *etree.Element) string {
	tags := []string{}
	for _, child := range parent.ChildElements() {
		tags = append(tags, child.Tag)
	}
	return strings.Join(tags, ",")
}

func paragraphWithStyle(style, text string) string {
	return `<w:p><w:pPr><w:pStyle w:val="` + style + `"/></w:pPr><w:r><w:t>` + text + `</w:t></w:r></w:p>`
}

func tableXML(columns int) string {
	var grid strings.Builder
	var cells strings.Builder
	for range columns {
		grid.WriteString(`<w:gridCol w:w="1000"/>`)
		cells.WriteString(`<w:tc><w:p><w:r><w:t>Cell</w:t></w:r></w:p></w:tc>`)
	}
	return `<w:tbl><w:tblPr/><w:tblGrid>` + grid.String() + `</w:tblGrid><w:tr>` + cells.String() + `</w:tr><w:tr>` + cells.String() + `</w:tr></w:tbl>`
}

func tableXMLWithRows(rows [][]string) string {
	var grid strings.Builder
	for range rows[0] {
		grid.WriteString(`<w:gridCol w:w="1000"/>`)
	}
	var body strings.Builder
	for _, row := range rows {
		body.WriteString("<w:tr>")
		for _, value := range row {
			body.WriteString(`<w:tc><w:tcPr/><w:p><w:r><w:t>` + value + `</w:t></w:r></w:p></w:tc>`)
		}
		body.WriteString("</w:tr>")
	}
	return `<w:tbl><w:tblPr/><w:tblGrid>` + grid.String() + `</w:tblGrid>` + body.String() + `</w:tbl>`
}

func testSectionXML() string {
	return `<w:sectPr><w:pgSz w:w="12240" w:h="15840"/><w:pgMar w:left="1440" w:right="1440" w:top="1440" w:bottom="1440" w:gutter="0"/></w:sectPr>`
}
