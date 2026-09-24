package render

import (
	"fmt"
	"html"
	"regexp"
	"strings"

	"github.com/beevik/etree"
	"github.com/goliatone/mdoc/internal/config"
	fielddata "github.com/goliatone/mdoc/internal/fields"
)

const (
	headerRelationshipType = "http://schemas.openxmlformats.org/officeDocument/2006/relationships/header"
	footerRelationshipType = "http://schemas.openxmlformats.org/officeDocument/2006/relationships/footer"
	headerContentType      = "application/vnd.openxmlformats-officedocument.wordprocessingml.header+xml"
	footerContentType      = "application/vnd.openxmlformats-officedocument.wordprocessingml.footer+xml"
)

type headerFooterPart struct {
	Kind             string
	Type             string
	Path             string
	Target           string
	RelationshipID   string
	RelationshipType string
	ContentType      string
	XML              []byte
}

func applyHeadersAndFooters(parts map[string][]byte, documentXML []byte, plan LayoutPlan) ([]byte, error) {
	headerChange := changesHeaderFooter(plan.Header)
	footerChange := changesHeaderFooter(plan.Footer)
	if !headerChange && !footerChange && plan.Page.PageNumberStart == nil {
		return documentXML, nil
	}
	document, err := parseOOXML(documentXML)
	if err != nil {
		return nil, fmt.Errorf("parse word/document.xml: %w", err)
	}
	sections := descendantElements(document.Root(), "sectPr")
	if len(sections) == 0 {
		return nil, fmt.Errorf("word/document.xml has no section properties for headers or footers")
	}
	relsPath := "word/_rels/document.xml.rels"
	rels := parts[relsPath]
	contentTypes := parts["[Content_Types].xml"]
	knownIDs := relationshipIDs(rels)
	created := []headerFooterPart{}
	createdByKey := map[string]headerFooterPart{}
	previousWidth := map[string]int64{}
	requests := []struct {
		kind  string
		value config.HeaderFooterConfig
	}{{"header", plan.Header}, {"footer", plan.Footer}}
	createPart := func(kind, partType string, content config.HeaderFooterContent, width int64) (headerFooterPart, error) {
		key := fmt.Sprintf("%s:%s:%d", kind, partType, width)
		if part, ok := createdByKey[key]; ok {
			return part, nil
		}
		if len(rels) == 0 {
			return headerFooterPart{}, fmt.Errorf("DOCX is missing %s", relsPath)
		}
		if len(contentTypes) == 0 {
			return headerFooterPart{}, fmt.Errorf("DOCX is missing [Content_Types].xml")
		}
		part, err := buildHeaderFooterPart(kind, partType, content, width, plan, knownIDs)
		if err != nil {
			return headerFooterPart{}, err
		}
		knownIDs[part.RelationshipID] = true
		createdByKey[key] = part
		created = append(created, part)
		return part, nil
	}
	for sectionIndex, section := range sections {
		width, widthErr := sectionContentWidthElement(section)
		if widthErr != nil {
			return nil, widthErr
		}
		for _, request := range requests {
			mode := headerFooterMode(request.value)
			switch mode {
			case "remove":
				removeHeaderFooterReferenceElement(section, request.kind, "")
			case "override":
				if hasDefaultHeaderFooterContent(request.value) {
					if sectionIndex == 0 || previousWidth[request.kind] != width {
						part, err := createPart(request.kind, "default", defaultHeaderFooterContent(request.value), width)
						if err != nil {
							return nil, err
						}
						setHeaderFooterReferenceElement(section, part)
					} else {
						removeHeaderFooterReferenceElement(section, request.kind, "default")
					}
					previousWidth[request.kind] = width
				}
				if sectionIndex == 0 && request.value.FirstPage != nil {
					part, err := createPart(request.kind, "first", *request.value.FirstPage, width)
					if err != nil {
						return nil, err
					}
					setHeaderFooterReferenceElement(section, part)
				}
			}
		}
		sortOrderedChildren(section, sectionPropertyOrder)
		if sectionIndex == 0 {
			if headerFooterMode(plan.Header) == "override" && plan.Header.FirstPage != nil || headerFooterMode(plan.Footer) == "override" && plan.Footer.FirstPage != nil {
				upsertOrderedChild(section, wordElement("titlePg"), sectionPropertyOrder)
			}
			if plan.Page.PageNumberStart != nil {
				setPageNumberStartElement(section, *plan.Page.PageNumberStart)
			}
		}
	}
	for _, part := range created {
		parts[part.Path] = part.XML
		rels, err = insertRelationship(rels, part)
		if err != nil {
			return nil, err
		}
		contentTypes, err = insertContentType(contentTypes, part)
		if err != nil {
			return nil, err
		}
	}
	if len(created) > 0 {
		parts[relsPath] = rels
		parts["[Content_Types].xml"] = contentTypes
	}
	return writeOOXML(document)
}

func buildHeaderFooterPart(kind, partType string, content config.HeaderFooterContent, width int64, plan LayoutPlan, known map[string]bool) (headerFooterPart, error) {
	context := fielddata.TemplateContext{PublicationID: plan.PublicationID, Fields: plan.Fields, Computed: plan.Computed, AllowPageFields: true}
	values := [3][]fielddata.Segment{}
	for index, item := range []struct{ name, source string }{{"left", content.Left}, {"center", content.Center}, {"right", content.Right}} {
		if item.source == "" {
			continue
		}
		parsed, err := fielddata.ParseTemplate(kind+" "+partType+" "+item.name, item.source)
		if err != nil {
			return headerFooterPart{}, err
		}
		segments, err := parsed.Evaluate(context)
		if err != nil {
			return headerFooterPart{}, err
		}
		values[index] = segments
	}
	capitalized := strings.ToUpper(kind[:1]) + kind[1:]
	baseID := "rIdMdoc" + capitalized + strings.ToUpper(partType[:1]) + partType[1:]
	relationshipID := allocateRelationshipID(baseID, known)
	filename := fmt.Sprintf("%s_mdoc_%s_%d.xml", kind, partType, width)
	root := "hdr"
	if kind == "footer" {
		root = "ftr"
	}
	// Header and footer content uses paragraph tab stops instead of a table.
	// This keeps page furniture independent from body table pagination in Word,
	// Google Docs, and importers such as Pages.
	xml := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><w:` + root + ` xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:p><w:pPr><w:tabs>`
	xml += `<w:tab w:val="center" w:pos="` + fmt.Sprint(width/2) + `"/><w:tab w:val="right" w:pos="` + fmt.Sprint(width) + `"/>`
	xml += `</w:tabs></w:pPr>` + segmentsXML(values[0]) + `<w:r><w:tab/></w:r>` + segmentsXML(values[1]) + `<w:r><w:tab/></w:r>` + segmentsXML(values[2]) + `</w:p></w:` + root + `>`
	relationshipType := headerRelationshipType
	contentType := headerContentType
	if kind == "footer" {
		relationshipType = footerRelationshipType
		contentType = footerContentType
	}
	return headerFooterPart{Kind: kind, Type: partType, Path: "word/" + filename, Target: filename, RelationshipID: relationshipID, RelationshipType: relationshipType, ContentType: contentType, XML: []byte(xml)}, nil
}

func segmentsXML(segments []fielddata.Segment) string {
	var result strings.Builder
	for _, segment := range segments {
		switch segment.Kind {
		case fielddata.SegmentText:
			result.WriteString(`<w:r><w:t xml:space="preserve">` + html.EscapeString(segment.Text) + `</w:t></w:r>`)
		case fielddata.SegmentPage:
			result.WriteString(`<w:fldSimple w:instr="PAGE"><w:r><w:t>1</w:t></w:r></w:fldSimple>`)
		case fielddata.SegmentPageCount:
			result.WriteString(`<w:fldSimple w:instr="NUMPAGES"><w:r><w:t>1</w:t></w:r></w:fldSimple>`)
		}
	}
	return result.String()
}

func relationshipIDs(data []byte) map[string]bool {
	result := map[string]bool{}
	pattern := regexp.MustCompile(`\bId="([^"]+)"`)
	for _, match := range pattern.FindAllSubmatch(data, -1) {
		result[string(match[1])] = true
	}
	return result
}

func allocateRelationshipID(base string, known map[string]bool) string {
	if !known[base] {
		return base
	}
	for index := 2; ; index++ {
		candidate := fmt.Sprintf("%s%d", base, index)
		if !known[candidate] {
			return candidate
		}
	}
}

func insertRelationship(data []byte, part headerFooterPart) ([]byte, error) {
	document, err := parseOOXML(data)
	if err != nil {
		return nil, fmt.Errorf("parse word/_rels/document.xml.rels: %w", err)
	}
	root := document.Root()
	if root == nil || root.Tag != "Relationships" {
		return nil, fmt.Errorf("word/_rels/document.xml.rels has no Relationships root")
	}
	for _, relationship := range root.ChildElements() {
		if relationship.Tag == "Relationship" && relationship.SelectAttrValue("Id", "") == part.RelationshipID {
			return nil, fmt.Errorf("relationship %s already exists", part.RelationshipID)
		}
	}
	relationship := etree.NewElement("Relationship")
	relationship.CreateAttr("Id", part.RelationshipID)
	relationship.CreateAttr("Type", part.RelationshipType)
	relationship.CreateAttr("Target", part.Target)
	root.AddChild(relationship)
	return writeOOXML(document)
}

func insertContentType(data []byte, part headerFooterPart) ([]byte, error) {
	document, err := parseOOXML(data)
	if err != nil {
		return nil, fmt.Errorf("parse [Content_Types].xml: %w", err)
	}
	root := document.Root()
	if root == nil || root.Tag != "Types" {
		return nil, fmt.Errorf("[Content_Types].xml has no Types root")
	}
	partName := "/" + part.Path
	for _, override := range root.ChildElements() {
		if override.Tag == "Override" && override.SelectAttrValue("PartName", "") == partName {
			return data, nil
		}
	}
	override := etree.NewElement("Override")
	override.CreateAttr("PartName", partName)
	override.CreateAttr("ContentType", part.ContentType)
	root.AddChild(override)
	return writeOOXML(document)
}

func setHeaderFooterReferenceElement(section *etree.Element, part headerFooterPart) {
	tag := part.Kind + "Reference"
	for _, reference := range directElements(section, tag) {
		if wordAttr(reference, "type") == part.Type {
			section.RemoveChild(reference)
		}
	}
	reference := wordElement(tag)
	setWordAttr(reference, "type", part.Type)
	reference.CreateAttr("r:id", part.RelationshipID)
	section.AddChild(reference)
	sortOrderedChildren(section, sectionPropertyOrder)
}

func removeHeaderFooterReferenceElement(section *etree.Element, kind, partType string) {
	for _, reference := range directElements(section, kind+"Reference") {
		if partType == "" || wordAttr(reference, "type") == partType {
			section.RemoveChild(reference)
		}
	}
	sortOrderedChildren(section, sectionPropertyOrder)
}

func setPageNumberStartElement(section *etree.Element, start int) {
	element := wordElement("pgNumType")
	setWordAttr(element, "start", fmt.Sprint(start))
	upsertOrderedChild(section, element, sectionPropertyOrder)
}

func headerFooterMode(value config.HeaderFooterConfig) string {
	if value.Mode != "" {
		return value.Mode
	}
	if hasDefaultHeaderFooterContent(value) || value.FirstPage != nil {
		return "override"
	}
	return "inherit"
}

func hasDefaultHeaderFooterContent(value config.HeaderFooterConfig) bool {
	return value.Default != nil || value.Left != "" || value.Center != "" || value.Right != ""
}

func defaultHeaderFooterContent(value config.HeaderFooterConfig) config.HeaderFooterContent {
	if value.Default != nil {
		return *value.Default
	}
	return config.HeaderFooterContent{Left: value.Left, Center: value.Center, Right: value.Right}
}

func changesHeaderFooter(value config.HeaderFooterConfig) bool {
	mode := headerFooterMode(value)
	return mode == "remove" || mode == "override"
}
