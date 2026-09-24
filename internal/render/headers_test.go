package render

import (
	"encoding/xml"
	"path/filepath"
	"strings"
	"testing"

	"github.com/goliatone/mdoc/internal/config"
)

func TestApplyLayoutCreatesHeadersFootersFieldsAndFirstPageParts(t *testing.T) {
	directory := t.TempDir()
	input := filepath.Join(directory, "input.docx")
	output := filepath.Join(directory, "output.docx")
	writeLayoutFixture(t, input, `<w:p><w:r><w:t>Body</w:t></w:r></w:p>`)
	start := 7
	plan := LayoutPlan{
		Page: config.PageConfig{PageNumberStart: &start}, PublicationID: "report",
		Fields: map[string]any{"name": "A < B & PAGE", "first": "First page"}, Computed: map[string]any{"review_set_id": "review-1"},
		Header: config.HeaderFooterConfig{Left: `{{ field "name" }}`, Center: `{{ mdoc "review_set_id" }}`, Right: `{{ page "number" }}`, FirstPage: &config.HeaderFooterContent{Center: `{{ field "first" }}`}},
		Footer: config.HeaderFooterConfig{Left: "Confidential", Right: `{{ page "count" }}`},
	}
	if err := ApplyLayout(input, output, plan); err != nil {
		t.Fatal(err)
	}
	parts, err := readZip(output)
	if err != nil {
		t.Fatal(err)
	}
	for _, prefix := range []string{"word/header_mdoc_default_", "word/header_mdoc_first_", "word/footer_mdoc_default_"} {
		name, data := generatedPart(parts, prefix)
		if len(data) == 0 {
			t.Fatalf("missing %s part", prefix)
		}
		var value any
		if err := xml.Unmarshal(data, &value); err != nil {
			t.Fatalf("invalid %s: %v\n%s", name, err, data)
		}
	}
	_, headerData := generatedPart(parts, "word/header_mdoc_default_")
	_, footerData := generatedPart(parts, "word/footer_mdoc_default_")
	header := string(headerData)
	footer := string(footerData)
	if !strings.Contains(header, "A &lt; B &amp; PAGE") || !strings.Contains(header, "review-1") || !strings.Contains(header, `w:instr="PAGE"`) {
		t.Fatalf("header = %s", header)
	}
	if !strings.Contains(footer, `w:instr="NUMPAGES"`) {
		t.Fatalf("footer = %s", footer)
	}
	if strings.Contains(header, `<w:tbl`) || !strings.Contains(header, `<w:tab w:val="center" w:pos="4680"/>`) || !strings.Contains(header, `<w:tab w:val="right" w:pos="9360"/>`) || strings.Count(header, `<w:r><w:tab/></w:r>`) != 2 {
		t.Fatalf("header geometry = %s", header)
	}
	documentXML := string(parts["word/document.xml"])
	if !strings.Contains(documentXML, `<w:titlePg/>`) || !strings.Contains(documentXML, `<w:pgNumType w:start="7"/>`) || !strings.Contains(documentXML, `r:id="rIdMdocHeaderDefault2"`) {
		t.Fatalf("document = %s", documentXML)
	}
	rels := string(parts["word/_rels/document.xml.rels"])
	if !strings.Contains(rels, headerRelationshipType) || !strings.Contains(rels, footerRelationshipType) || !strings.Contains(rels, `Id="rIdHeader"`) {
		t.Fatalf("relationships = %s", rels)
	}
	contentTypes := string(parts["[Content_Types].xml"])
	if !strings.Contains(contentTypes, `/word/header_mdoc_default_9360.xml`) || !strings.Contains(contentTypes, `/word/footer_mdoc_default_9360.xml`) {
		t.Fatalf("content types = %s", contentTypes)
	}
	if len(parts["word/header-reference.xml"]) == 0 {
		t.Fatal("unrelated reference header was removed")
	}
}

func TestApplyLayoutHeaderFooterModesPreserveOverrideAndRemove(t *testing.T) {
	tests := []struct {
		name       string
		config     config.HeaderFooterConfig
		wantRef    bool
		wantNew    bool
		wantOldRef bool
	}{
		{name: "inherit", config: config.HeaderFooterConfig{Mode: "inherit"}, wantRef: true, wantOldRef: true},
		{name: "override", config: config.HeaderFooterConfig{Mode: "override", Right: "New"}, wantRef: true, wantNew: true},
		{name: "remove", config: config.HeaderFooterConfig{Mode: "remove"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			input := filepath.Join(directory, "input.docx")
			output := filepath.Join(directory, "output.docx")
			writeLayoutFixture(t, input, `<w:p><w:r><w:t>Body</w:t></w:r></w:p>`)
			if err := ApplyLayout(input, output, LayoutPlan{Page: config.PageConfig{Size: "letter"}, Header: test.config}); err != nil {
				t.Fatal(err)
			}
			parts, err := readZip(output)
			if err != nil {
				t.Fatal(err)
			}
			document := string(parts["word/document.xml"])
			if strings.Contains(document, `<w:headerReference`) != test.wantRef {
				t.Fatalf("header reference = %s", document)
			}
			if strings.Contains(document, `r:id="rIdHeader"`) != test.wantOldRef {
				t.Fatalf("reference ownership = %s", document)
			}
			_, generated := generatedPart(parts, "word/header_mdoc_default_")
			if (len(generated) > 0) != test.wantNew {
				t.Fatalf("generated header = %s", generated)
			}
		})
	}
}

func TestApplyLayoutCreatesSectionWidthHeaderPartsAndUsesInheritance(t *testing.T) {
	directory := t.TempDir()
	input := filepath.Join(directory, "input.docx")
	output := filepath.Join(directory, "output.docx")
	body := `<w:p><w:r><w:t>Cover</w:t></w:r></w:p><w:bookmarkStart w:id="1" w:name="m_landscape"/><w:p><w:r><w:t>Wide</w:t></w:r></w:p><w:bookmarkStart w:id="2" w:name="m_portrait"/><w:p><w:r><w:t>Back</w:t></w:r></w:p>`
	writeLayoutFixture(t, input, body)
	plan := LayoutPlan{
		Page: config.PageConfig{Size: "letter", Margins: config.MarginsConfig{Left: "0.75in", Right: "0.75in"}},
		Boundaries: []SectionBoundary{
			{BookmarkID: "m_landscape", SourceKey: "wide.md", Start: "new_section", Section: config.SectionConfig{Orientation: "landscape"}},
			{BookmarkID: "m_portrait", SourceKey: "back.md", Start: "new_section", Section: config.SectionConfig{Orientation: "portrait"}},
		},
		Header: config.HeaderFooterConfig{Left: "Left", Right: "Right"},
	}
	if err := ApplyLayout(input, output, plan); err != nil {
		t.Fatal(err)
	}
	parts, err := readZip(output)
	if err != nil {
		t.Fatal(err)
	}
	if len(parts["word/header_mdoc_default_10080.xml"]) == 0 || len(parts["word/header_mdoc_default_13680.xml"]) == 0 {
		t.Fatalf("section header parts = %#v", parts)
	}
	document := string(parts["word/document.xml"])
	if strings.Count(document, `<w:headerReference`) != 3 || strings.Contains(document, `r:id="rIdHeader"`) {
		t.Fatalf("section header references = %s", document)
	}
}

func TestApplyLayoutPreservesReferenceHeaderWithoutOverride(t *testing.T) {
	directory := t.TempDir()
	input := filepath.Join(directory, "input.docx")
	output := filepath.Join(directory, "output.docx")
	writeLayoutFixture(t, input, `<w:p><w:r><w:t>Body</w:t></w:r></w:p>`)
	if err := ApplyLayout(input, output, LayoutPlan{Page: config.PageConfig{Size: "a4"}}); err != nil {
		t.Fatal(err)
	}
	parts, err := readZip(output)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(parts["word/document.xml"]), `r:id="rIdHeader"`) || !strings.Contains(string(parts["word/header-reference.xml"]), "Reference Header") {
		t.Fatal("reference header was not preserved")
	}
}

func TestApplyLayoutRejectsMissingHeaderFieldBeforeWriting(t *testing.T) {
	directory := t.TempDir()
	input := filepath.Join(directory, "input.docx")
	output := filepath.Join(directory, "output.docx")
	writeLayoutFixture(t, input, `<w:p><w:r><w:t>Body</w:t></w:r></w:p>`)
	err := ApplyLayout(input, output, LayoutPlan{PublicationID: "report", Header: config.HeaderFooterConfig{Left: `{{ field "missing" }}`}})
	if err == nil || !strings.Contains(err.Error(), `field "missing" is missing`) {
		t.Fatalf("error = %v", err)
	}
}

func TestApplyLayoutRejectsInvalidPageNumberStart(t *testing.T) {
	directory := t.TempDir()
	input := filepath.Join(directory, "input.docx")
	output := filepath.Join(directory, "output.docx")
	writeLayoutFixture(t, input, `<w:p><w:r><w:t>Body</w:t></w:r></w:p>`)
	start := 0
	err := ApplyLayout(input, output, LayoutPlan{Page: config.PageConfig{PageNumberStart: &start}})
	if err == nil || !strings.Contains(err.Error(), "between 1 and 32767") {
		t.Fatalf("error = %v", err)
	}
}

func TestApplyLayoutCreatesExplicitBlankDefaultHeader(t *testing.T) {
	directory := t.TempDir()
	input := filepath.Join(directory, "input.docx")
	output := filepath.Join(directory, "output.docx")
	writeLayoutFixture(t, input, `<w:p><w:r><w:t>Body</w:t></w:r></w:p>`)
	plan := LayoutPlan{Header: config.HeaderFooterConfig{Mode: "override", Default: &config.HeaderFooterContent{}}}
	if err := ApplyLayout(input, output, plan); err != nil {
		t.Fatal(err)
	}
	parts, err := readZip(output)
	if err != nil {
		t.Fatal(err)
	}
	_, header := generatedPart(parts, "word/header_mdoc_default_")
	if len(header) == 0 || strings.Contains(string(header), "Reference Header") {
		t.Fatalf("blank default header = %s", header)
	}
	if err := auditDOCXConformance(parts, parts["word/document.xml"]); err != nil {
		t.Fatal(err)
	}
}

func TestApplyLayoutRejectsAmbiguousEmptyOverride(t *testing.T) {
	directory := t.TempDir()
	input := filepath.Join(directory, "input.docx")
	output := filepath.Join(directory, "output.docx")
	writeLayoutFixture(t, input, `<w:p><w:r><w:t>Body</w:t></w:r></w:p>`)
	err := ApplyLayout(input, output, LayoutPlan{Header: config.HeaderFooterConfig{Mode: "override"}})
	if err == nil || !strings.Contains(err.Error(), "requires default") {
		t.Fatalf("error = %v", err)
	}
}

func TestHeaderOnlyLayoutDoesNotNormalizeBodyTables(t *testing.T) {
	directory := t.TempDir()
	input := filepath.Join(directory, "input.docx")
	output := filepath.Join(directory, "output.docx")
	body := `<w:tbl><w:tblPr><w:tblW w:w="2500" w:type="dxa"/></w:tblPr><w:tblGrid><w:gridCol w:w="2500"/></w:tblGrid><w:tr><w:tc><w:tcPr><w:tcW w:w="2500" w:type="dxa"/></w:tcPr><w:p/></w:tc></w:tr></w:tbl>`
	writeLayoutFixture(t, input, body)
	if err := ApplyLayout(input, output, LayoutPlan{Header: config.HeaderFooterConfig{Left: "New"}}); err != nil {
		t.Fatal(err)
	}
	parts, err := readZip(output)
	if err != nil {
		t.Fatal(err)
	}
	document := string(parts["word/document.xml"])
	if !strings.Contains(document, `<w:tblW w:w="2500" w:type="dxa"/>`) || strings.Contains(document, `<w:tblInd`) {
		t.Fatalf("body table geometry changed: %s", document)
	}
}

func generatedPart(parts map[string][]byte, prefix string) (string, []byte) {
	for name, data := range parts {
		if strings.HasPrefix(name, prefix) {
			return name, data
		}
	}
	return "", nil
}
