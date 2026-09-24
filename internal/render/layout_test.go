package render

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/goliatone/mdoc/internal/config"
)

func TestLayoutTwipConversionsUseHalfUpRounding(t *testing.T) {
	tests := map[string]int64{"1in": 1440, "72pt": 1440, "2.54cm": 1440, "25.4mm": 1440, "0.01mm": 1}
	for input, expected := range tests {
		actual, err := config.LayoutTwips(input)
		if err != nil || actual != expected {
			t.Fatalf("%s = %d, %v; want %d", input, actual, err, expected)
		}
	}
}

func TestApplyLayoutWritesPageGeometryAndSectionStarts(t *testing.T) {
	directory := t.TempDir()
	input := filepath.Join(directory, "input.docx")
	output := filepath.Join(directory, "output.docx")
	writeLayoutFixture(t, input, `<w:p><w:r><w:t>Cover</w:t></w:r></w:p><w:bookmarkStart w:id="1" w:name="m_one"/><w:p><w:r><w:t>One</w:t></w:r></w:p><w:bookmarkEnd w:id="1"/><w:p><w:pPr><w:pStyle w:val="Heading1"/><w:pageBreakBefore/></w:pPr><w:bookmarkStart w:id="2" w:name="m_two"/><w:r><w:t>Two</w:t></w:r><w:bookmarkEnd w:id="2"/></w:p>`)
	plan := LayoutPlan{Page: config.PageConfig{Size: "letter", Orientation: "landscape", Margins: config.MarginsConfig{Top: "1in", Right: "1in", Bottom: "1in", Left: "1in"}}, Boundaries: []SectionBoundary{
		{BookmarkID: "m_one", SourceKey: "one.md", Start: "after_cover"},
		{BookmarkID: "m_two", SourceKey: "two.md", Start: "odd_page", Section: config.SectionConfig{Orientation: "portrait", Margins: config.MarginsConfig{Left: "0.5in"}}},
	}}
	if err := ApplyLayout(input, output, plan); err != nil {
		t.Fatal(err)
	}
	xml := string(readLayoutDocument(t, output))
	if strings.Count(xml, "<w:sectPr") != 3 || !strings.Contains(xml, `<w:type w:val="nextPage"/>`) || !strings.Contains(xml, `<w:type w:val="oddPage"/>`) {
		t.Fatalf("section starts = %s", xml)
	}
	if !strings.Contains(xml, `<w:pgSz w:w="15840" w:h="12240" w:orient="landscape"/>`) || !strings.Contains(xml, `<w:pgMar w:top="1440" w:right="1440" w:bottom="1440" w:left="1440"`) {
		t.Fatalf("global geometry = %s", xml)
	}
	if !strings.Contains(xml, `<w:pgSz w:w="12240" w:h="15840" w:orient="portrait"/>`) || !strings.Contains(xml, `w:left="720"`) {
		t.Fatalf("member geometry = %s", xml)
	}
	if strings.Count(xml, `r:id="rIdHeader"`) != 3 {
		t.Fatalf("reference section properties were not retained: %s", xml)
	}
}

func TestApplyLayoutDoesNotCopyFirstSectionPageNumbering(t *testing.T) {
	directory := t.TempDir()
	input := filepath.Join(directory, "input.docx")
	output := filepath.Join(directory, "output.docx")
	writeLayoutFixture(t, input, `<w:p><w:r><w:t>Cover</w:t></w:r></w:p><w:bookmarkStart w:id="1" w:name="m_one"/><w:p><w:r><w:t>One</w:t></w:r></w:p><w:bookmarkEnd w:id="1"/><w:bookmarkStart w:id="2" w:name="m_two"/><w:p><w:r><w:t>Two</w:t></w:r></w:p><w:bookmarkEnd w:id="2"/>`)
	parts, err := readZip(input)
	if err != nil {
		t.Fatal(err)
	}
	parts["word/document.xml"] = []byte(strings.Replace(string(parts["word/document.xml"]), "<w:pgSz", `<w:titlePg/><w:pgNumType w:start="3"/><w:pgSz`, 1))
	if err := writeZip(input, parts); err != nil {
		t.Fatal(err)
	}
	plan := LayoutPlan{Page: config.PageConfig{Size: "letter"}, Boundaries: []SectionBoundary{
		{BookmarkID: "m_one", SourceKey: "one.md", Start: "after_cover"},
		{BookmarkID: "m_two", SourceKey: "two.md", Start: "new_page"},
	}}
	if err := ApplyLayout(input, output, plan); err != nil {
		t.Fatal(err)
	}
	xml := string(readLayoutDocument(t, output))
	if strings.Count(xml, `<w:pgNumType w:start="3"/>`) != 1 || strings.Count(xml, `<w:titlePg/>`) != 1 {
		t.Fatalf("first-section settings were copied: %s", xml)
	}
}

func TestApplyLayoutUsesPageBreakForNewPageWithoutGeometryChange(t *testing.T) {
	directory := t.TempDir()
	input := filepath.Join(directory, "input.docx")
	output := filepath.Join(directory, "output.docx")
	writeLayoutFixture(t, input, `<w:p><w:r><w:t>Cover</w:t></w:r></w:p><w:bookmarkStart w:id="1" w:name="m_one"/><w:p><w:r><w:t>One</w:t></w:r></w:p><w:bookmarkEnd w:id="1"/><w:bookmarkStart w:id="2" w:name="m_two"/><w:p><w:r><w:t>Two</w:t></w:r></w:p><w:bookmarkEnd w:id="2"/>`)
	plan := LayoutPlan{Page: config.PageConfig{Size: "letter"}, Boundaries: []SectionBoundary{
		{BookmarkID: "m_one", SourceKey: "one.md", Start: "after_cover"},
		{BookmarkID: "m_two", SourceKey: "two.md", Start: "new_page"},
	}}
	if err := ApplyLayout(input, output, plan); err != nil {
		t.Fatal(err)
	}
	xml := string(readLayoutDocument(t, output))
	if strings.Count(xml, "<w:sectPr") != 2 {
		t.Fatalf("plain new_page created a redundant section: %s", xml)
	}
	if strings.Contains(xml, `<w:br w:type="page"/>`) {
		t.Fatalf("plain new_page created a standalone page break paragraph: %s", xml)
	}
	if !strings.Contains(xml, `<w:pageBreakBefore w:val="1"/>`) {
		t.Fatalf("plain new_page did not apply the break to the member heading: %s", xml)
	}
}

func TestApplyLayoutPreservesPackageWhenNoSettingsExist(t *testing.T) {
	directory := t.TempDir()
	input := filepath.Join(directory, "input.docx")
	output := filepath.Join(directory, "output.docx")
	writeLayoutFixture(t, input, `<w:p><w:r><w:t>Body</w:t></w:r></w:p>`)
	before, err := os.ReadFile(input)
	if err != nil {
		t.Fatal(err)
	}
	if err := ApplyLayout(input, output, LayoutPlan{}); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("empty layout changed the reference package")
	}
}

func TestApplyLayoutRejectsUnsupportedContinuousOrientation(t *testing.T) {
	directory := t.TempDir()
	input := filepath.Join(directory, "input.docx")
	output := filepath.Join(directory, "output.docx")
	writeLayoutFixture(t, input, `<w:p><w:r><w:t>Before</w:t></w:r></w:p><w:bookmarkStart w:id="1" w:name="m_one"/><w:p><w:r><w:t>One</w:t></w:r></w:p>`)
	err := ApplyLayout(input, output, LayoutPlan{Boundaries: []SectionBoundary{{BookmarkID: "m_one", SourceKey: "one.md", Start: "continuous", Section: config.SectionConfig{Orientation: "landscape"}}}})
	if err == nil || !strings.Contains(err.Error(), "continuous") {
		t.Fatalf("error = %v", err)
	}
}

func TestApplyLayoutRechecksUsableAreaAgainstReferencePage(t *testing.T) {
	directory := t.TempDir()
	input := filepath.Join(directory, "input.docx")
	output := filepath.Join(directory, "output.docx")
	writeLayoutFixture(t, input, `<w:p><w:r><w:t>Body</w:t></w:r></w:p>`)
	err := ApplyLayout(input, output, LayoutPlan{Page: config.PageConfig{Margins: config.MarginsConfig{Left: "6in", Right: "6in"}}})
	if err == nil || !strings.Contains(err.Error(), "usable width") {
		t.Fatalf("error = %v", err)
	}
}

func TestApplyLayoutDoesNotCreateEmptyLeadingSection(t *testing.T) {
	directory := t.TempDir()
	input := filepath.Join(directory, "input.docx")
	output := filepath.Join(directory, "output.docx")
	writeLayoutFixture(t, input, `<w:bookmarkStart w:id="1" w:name="m_one"/><w:p><w:r><w:t>One</w:t></w:r></w:p>`)
	if err := ApplyLayout(input, output, LayoutPlan{Page: config.PageConfig{Size: "a4"}, Boundaries: []SectionBoundary{{BookmarkID: "m_one", SourceKey: "one.md", Start: "new_page"}}}); err != nil {
		t.Fatal(err)
	}
	xml := string(readLayoutDocument(t, output))
	if strings.Count(xml, "<w:sectPr") != 1 {
		t.Fatalf("leading empty section was created: %s", xml)
	}
}

func TestApplyLayoutProducesDeterministicPackages(t *testing.T) {
	directory := t.TempDir()
	input := filepath.Join(directory, "input.docx")
	first := filepath.Join(directory, "first.docx")
	second := filepath.Join(directory, "second.docx")
	writeLayoutFixture(t, input, `<w:p><w:r><w:t>Body</w:t></w:r></w:p>`)
	plan := LayoutPlan{Page: config.PageConfig{Size: "legal", Margins: config.MarginsConfig{Top: "1cm"}}}
	if err := ApplyLayout(input, first, plan); err != nil {
		t.Fatal(err)
	}
	if err := ApplyLayout(input, second, plan); err != nil {
		t.Fatal(err)
	}
	firstData, _ := os.ReadFile(first)
	secondData, _ := os.ReadFile(second)
	if !bytes.Equal(firstData, secondData) {
		t.Fatal("layout package changed between equal runs")
	}
}

func TestNormalizeDOCXRepairsEmbeddedFontContentType(t *testing.T) {
	directory := t.TempDir()
	input := filepath.Join(directory, "input.docx")
	output := filepath.Join(directory, "output.docx")
	writeLayoutFixture(t, input, `<w:p><w:r><w:t>Body</w:t></w:r></w:p>`)
	parts, err := readZip(input)
	if err != nil {
		t.Fatal(err)
	}
	parts["word/fonts/example.ttf"] = []byte("font")
	if err := writeZip(input, parts); err != nil {
		t.Fatal(err)
	}
	if err := NormalizeDOCX(input, output); err != nil {
		t.Fatal(err)
	}
	parts, err = readZip(output)
	if err != nil {
		t.Fatal(err)
	}
	contentTypes, err := parseOOXML(parts["[Content_Types].xml"])
	if err != nil {
		t.Fatal(err)
	}
	if err := auditPackageContentTypes(parts, contentTypes); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(parts["[Content_Types].xml"]), `Extension="ttf" ContentType="application/x-font-ttf"`) {
		t.Fatalf("content types = %s", parts["[Content_Types].xml"])
	}
}

func TestNormalizeDOCXRejectsBrokenPackageRelationship(t *testing.T) {
	directory := t.TempDir()
	input := filepath.Join(directory, "input.docx")
	output := filepath.Join(directory, "output.docx")
	writeLayoutFixture(t, input, `<w:p><w:r><w:t>Body</w:t></w:r></w:p>`)
	parts, err := readZip(input)
	if err != nil {
		t.Fatal(err)
	}
	delete(parts, "word/header-reference.xml")
	if err := writeZip(input, parts); err != nil {
		t.Fatal(err)
	}
	err = NormalizeDOCX(input, output)
	if err == nil || !strings.Contains(err.Error(), "missing part") {
		t.Fatalf("error = %v", err)
	}
	if _, statErr := os.Stat(output); !os.IsNotExist(statErr) {
		t.Fatalf("output exists after failed audit: %v", statErr)
	}
}

func writeLayoutFixture(t *testing.T, path, body string) {
	t.Helper()
	document := `<?xml version="1.0" encoding="UTF-8"?><w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><w:body>` + body + `<w:sectPr><w:headerReference w:type="default" r:id="rIdHeader"/><w:pgSz w:w="12240" w:h="15840"/><w:pgMar w:top="1440" w:right="1440" w:bottom="1440" w:left="1440" w:header="720" w:footer="720" w:gutter="0"/></w:sectPr></w:body></w:document>`
	parts := map[string][]byte{
		"[Content_Types].xml":          []byte(`<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="xml" ContentType="application/xml"/><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/><Override PartName="/word/header-reference.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.header+xml"/></Types>`),
		"word/document.xml":            []byte(document),
		"word/_rels/document.xml.rels": []byte(`<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rIdHeader" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/header" Target="header-reference.xml"/><Relationship Id="rIdMdocHeaderDefault" Type="urn:existing" Target="existing.xml"/></Relationships>`),
		"word/header-reference.xml":    []byte(`<w:hdr xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:p><w:r><w:t>Reference Header</w:t></w:r></w:p></w:hdr>`),
		"word/existing.xml":            []byte(`<existing/>`),
	}
	if err := writeZip(path, parts); err != nil {
		t.Fatal(err)
	}
}

func readLayoutDocument(t *testing.T, path string) []byte {
	t.Helper()
	parts, err := readZip(path)
	if err != nil {
		t.Fatal(err)
	}
	return parts["word/document.xml"]
}
