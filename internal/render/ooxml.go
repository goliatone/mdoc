package render

import (
	"fmt"
	"path"
	"sort"
	"strconv"
	"strings"

	"github.com/beevik/etree"
)

const (
	wordNamespace         = "http://schemas.openxmlformats.org/wordprocessingml/2006/main"
	relationshipNamespace = "http://schemas.openxmlformats.org/officeDocument/2006/relationships"
)

var repairablePackageContentTypes = map[string]string{
	"png":   "image/png",
	"ttf":   "application/x-font-ttf",
	"otf":   "application/vnd.ms-opentype",
	"woff":  "font/woff",
	"woff2": "font/woff2",
}

var sectionPropertyOrder = elementOrder(
	"headerReference", "footerReference", "footnotePr", "endnotePr", "type", "pgSz", "pgMar", "paperSrc", "pgBorders", "lnNumType", "pgNumType", "cols", "formProt", "vAlign", "noEndnote", "titlePg", "textDirection", "bidi", "rtlGutter", "docGrid", "printerSettings", "footnoteColumns", "sectPrChange",
)

var tablePropertyOrder = elementOrder(
	"tblStyle", "tblpPr", "tblOverlap", "bidiVisual", "tblStyleRowBandSize", "tblStyleColBandSize", "tblW", "jc", "tblCellSpacing", "tblInd", "tblBorders", "shd", "tblLayout", "tblCellMar", "tblLook", "tblCaption", "tblDescription", "tblPrChange",
)

var tableCellPropertyOrder = elementOrder(
	"cnfStyle", "tcW", "gridSpan", "hMerge", "vMerge", "tcBorders", "shd", "noWrap", "tcMar", "textDirection", "tcFitText", "vAlign", "hideMark", "cellIns", "cellDel", "cellMerge", "tcPrChange",
)

var tableRowPropertyOrder = elementOrder(
	"cnfStyle", "divId", "gridBefore", "gridAfter", "wBefore", "wAfter", "cantSplit", "trHeight", "tblHeader", "tblCellSpacing", "jc", "hidden", "ins", "del", "trPrChange",
)

var paragraphPropertyOrder = elementOrder(
	"pStyle", "keepNext", "keepLines", "pageBreakBefore", "framePr", "widowControl", "numPr", "suppressLineNumbers", "pBdr", "shd", "tabs", "suppressAutoHyphens", "kinsoku", "wordWrap", "overflowPunct", "topLinePunct", "autoSpaceDE", "autoSpaceDN", "bidi", "adjustRightInd", "snapToGrid", "spacing", "ind", "contextualSpacing", "mirrorIndents", "suppressOverlap", "jc", "textDirection", "textAlignment", "textboxTightWrap", "outlineLvl", "divId", "cnfStyle", "rPr", "sectPr", "pPrChange",
)

func elementOrder(tags ...string) map[string]int {
	result := make(map[string]int, len(tags))
	for index, tag := range tags {
		result[tag] = index
	}
	return result
}

func parseOOXML(data []byte) (*etree.Document, error) {
	document := etree.NewDocument()
	document.ReadSettings.Permissive = false
	if err := document.ReadFromBytes(data); err != nil {
		return nil, err
	}
	return document, nil
}

func writeOOXML(document *etree.Document) ([]byte, error) {
	document.WriteSettings.CanonicalAttrVal = true
	return document.WriteToBytes()
}

func parseWordFragment(source string) (*etree.Document, *etree.Element, error) {
	wrapper := `<mdoc xmlns:w="` + wordNamespace + `" xmlns:r="` + relationshipNamespace + `">` + source + `</mdoc>`
	document, err := parseOOXML([]byte(wrapper))
	if err != nil {
		return nil, nil, err
	}
	root := document.Root()
	for _, child := range root.ChildElements() {
		return document, child, nil
	}
	return nil, nil, fmt.Errorf("OOXML fragment has no root element")
}

func writeWordFragment(element *etree.Element) (string, error) {
	document := etree.NewDocument()
	document.AddChild(element.Copy())
	data, err := writeOOXML(document)
	return strings.TrimSpace(string(data)), err
}

func wordElement(tag string) *etree.Element {
	return etree.NewElement("w:" + tag)
}

func directElements(parent *etree.Element, tag string) []*etree.Element {
	result := []*etree.Element{}
	if parent == nil {
		return result
	}
	for _, child := range parent.ChildElements() {
		if child.Space == "w" && child.Tag == tag {
			result = append(result, child)
		}
	}
	return result
}

func directElement(parent *etree.Element, tag string) *etree.Element {
	if parent == nil {
		return nil
	}
	for _, child := range parent.ChildElements() {
		if child.Space == "w" && child.Tag == tag {
			return child
		}
	}
	return nil
}

func descendantElements(parent *etree.Element, tag string) []*etree.Element {
	result := []*etree.Element{}
	if parent == nil {
		return result
	}
	var visit func(*etree.Element)
	visit = func(element *etree.Element) {
		for _, child := range element.ChildElements() {
			if child.Space == "w" && child.Tag == tag {
				result = append(result, child)
			}
			visit(child)
		}
	}
	visit(parent)
	return result
}

func wordAttr(element *etree.Element, name string) string {
	if element == nil {
		return ""
	}
	return element.SelectAttrValue("w:"+name, "")
}

func setWordAttr(element *etree.Element, name, value string) {
	element.CreateAttr("w:"+name, value)
}

func intWordAttr(element *etree.Element, name string, fallback int64) int64 {
	if element == nil {
		return fallback
	}
	value, err := strconv.ParseInt(wordAttr(element, name), 10, 64)
	if err != nil {
		return fallback
	}
	return value
}

func removeDirectElements(parent *etree.Element, tag string) {
	for _, child := range directElements(parent, tag) {
		parent.RemoveChild(child)
	}
}

func upsertOrderedChild(parent, child *etree.Element, order map[string]int) {
	removeDirectElements(parent, child.Tag)
	parent.AddChild(child)
	sortOrderedChildren(parent, order)
}

func sortOrderedChildren(parent *etree.Element, order map[string]int) {
	positions := []int{}
	elements := []*etree.Element{}
	for index, token := range parent.Child {
		child, ok := token.(*etree.Element)
		if !ok {
			continue
		}
		positions = append(positions, index)
		elements = append(elements, child)
	}
	sort.SliceStable(elements, func(left, right int) bool {
		leftOrder, leftKnown := order[elements[left].Tag]
		rightOrder, rightKnown := order[elements[right].Tag]
		switch {
		case leftKnown && rightKnown:
			return leftOrder < rightOrder
		case leftKnown:
			return true
		case rightKnown:
			return false
		default:
			return false
		}
	})
	for index, position := range positions {
		parent.Child[position] = elements[index]
	}
	parent.ReindexChildren()
}

func ensureDirectChild(parent *etree.Element, tag string, order map[string]int) *etree.Element {
	if child := directElement(parent, tag); child != nil {
		return child
	}
	child := wordElement(tag)
	upsertOrderedChild(parent, child, order)
	return child
}

func previousSiblingElement(element *etree.Element) *etree.Element {
	parent := element.Parent()
	if parent == nil {
		return nil
	}
	for index := element.Index() - 1; index >= 0; index-- {
		if sibling, ok := parent.Child[index].(*etree.Element); ok {
			return sibling
		}
	}
	return nil
}

func nextSiblingElement(element *etree.Element) *etree.Element {
	parent := element.Parent()
	if parent == nil {
		return nil
	}
	for index := element.Index() + 1; index < len(parent.Child); index++ {
		if sibling, ok := parent.Child[index].(*etree.Element); ok {
			return sibling
		}
	}
	return nil
}

func topLevelBodyChild(element, body *etree.Element) *etree.Element {
	for element != nil && element.Parent() != body {
		element = element.Parent()
	}
	return element
}

func validateOrderedChildren(parent *etree.Element, order map[string]int) error {
	last := -1
	lastTag := ""
	for _, child := range parent.ChildElements() {
		value, known := order[child.Tag]
		if !known {
			continue
		}
		if value < last {
			return fmt.Errorf("w:%s appears after w:%s", child.Tag, lastTag)
		}
		last = value
		lastTag = child.Tag
	}
	return nil
}

func auditOOXMLOrder(document *etree.Document) error {
	root := document.Root()
	checks := []struct {
		tag   string
		order map[string]int
	}{
		{"sectPr", sectionPropertyOrder},
		{"tblPr", tablePropertyOrder},
		{"tcPr", tableCellPropertyOrder},
		{"trPr", tableRowPropertyOrder},
		{"pPr", paragraphPropertyOrder},
	}
	for _, check := range checks {
		for index, element := range descendantElements(root, check.tag) {
			if err := validateOrderedChildren(element, check.order); err != nil {
				return fmt.Errorf("w:%s %d has invalid child order: %w", check.tag, index+1, err)
			}
		}
	}
	return nil
}

func auditDOCXConformance(parts map[string][]byte, documentXML []byte) error {
	document, err := parseOOXML(documentXML)
	if err != nil {
		return fmt.Errorf("word/document.xml: %w", err)
	}
	if err := auditOOXMLOrder(document); err != nil {
		return fmt.Errorf("word/document.xml: %w", err)
	}
	relationships, err := parseOOXML(parts["word/_rels/document.xml.rels"])
	if err != nil {
		return fmt.Errorf("word/_rels/document.xml.rels: %w", err)
	}
	relationshipByID := map[string]*etree.Element{}
	for _, relationship := range relationships.Root().ChildElements() {
		if relationship.Tag == "Relationship" {
			relationshipByID[relationship.SelectAttrValue("Id", "")] = relationship
		}
	}
	for _, kind := range []string{"header", "footer"} {
		for index, reference := range descendantElements(document.Root(), kind+"Reference") {
			id := reference.SelectAttrValue("r:id", "")
			relationship := relationshipByID[id]
			if relationship == nil {
				return fmt.Errorf("w:%sReference %d uses missing relationship %q", kind, index+1, id)
			}
			if !strings.HasSuffix(relationship.SelectAttrValue("Type", ""), "/"+kind) {
				return fmt.Errorf("w:%sReference %d relationship %q has the wrong type", kind, index+1, id)
			}
			targetValue := relationship.SelectAttrValue("Target", "")
			var target string
			if after, ok := strings.CutPrefix(targetValue, "/"); ok {
				target = path.Clean(after)
			} else {
				target = path.Clean(path.Join("word", targetValue))
			}
			if len(parts[target]) == 0 {
				return fmt.Errorf("w:%sReference %d relationship %q uses missing part %q", kind, index+1, id, target)
			}
		}
	}
	contentTypes, err := parseOOXML(parts["[Content_Types].xml"])
	if err != nil {
		return fmt.Errorf("[Content_Types].xml: %w", err)
	}
	overrides := map[string]bool{}
	for _, override := range contentTypes.Root().ChildElements() {
		if override.Tag == "Override" {
			overrides[override.SelectAttrValue("PartName", "")] = true
		}
	}
	if err := auditPackageContentTypes(parts, contentTypes); err != nil {
		return err
	}
	if err := auditPackageRelationships(parts); err != nil {
		return err
	}
	for name, data := range parts {
		if !strings.HasPrefix(name, "word/header_mdoc") && !strings.HasPrefix(name, "word/footer_mdoc") {
			continue
		}
		part, err := parseOOXML(data)
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		if err := auditOOXMLOrder(part); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		if !overrides["/"+name] {
			return fmt.Errorf("%s has no content type override", name)
		}
	}
	return nil
}

func ensurePackageContentTypes(parts map[string][]byte) error {
	data := parts["[Content_Types].xml"]
	if len(data) == 0 {
		return fmt.Errorf("DOCX is missing [Content_Types].xml")
	}
	document, err := parseOOXML(data)
	if err != nil {
		return fmt.Errorf("[Content_Types].xml: %w", err)
	}
	root := document.Root()
	defaults := map[string]bool{}
	for _, child := range root.ChildElements() {
		if child.Tag == "Default" {
			defaults[strings.ToLower(child.SelectAttrValue("Extension", ""))] = true
		}
	}
	needed := map[string]string{}
	for name := range parts {
		extension := strings.TrimPrefix(strings.ToLower(path.Ext(name)), ".")
		contentType, repairable := repairablePackageContentTypes[extension]
		if repairable && !defaults[extension] {
			needed[extension] = contentType
		}
	}
	if len(needed) == 0 {
		return nil
	}
	extensions := make([]string, 0, len(needed))
	for extension := range needed {
		extensions = append(extensions, extension)
	}
	sort.Strings(extensions)
	position := len(root.Child)
	for index, token := range root.Child {
		if element, ok := token.(*etree.Element); ok && element.Tag == "Override" {
			position = index
			break
		}
	}
	for _, extension := range extensions {
		element := etree.NewElement("Default")
		element.CreateAttr("Extension", extension)
		element.CreateAttr("ContentType", needed[extension])
		root.InsertChildAt(position, element)
		position++
	}
	updated, err := writeOOXML(document)
	if err != nil {
		return fmt.Errorf("write [Content_Types].xml: %w", err)
	}
	parts["[Content_Types].xml"] = updated
	return nil
}

func auditPackageContentTypes(parts map[string][]byte, contentTypes *etree.Document) error {
	defaults := map[string]bool{}
	overrides := map[string]bool{}
	for _, child := range contentTypes.Root().ChildElements() {
		switch child.Tag {
		case "Default":
			defaults[strings.ToLower(child.SelectAttrValue("Extension", ""))] = true
		case "Override":
			overrides[path.Clean(strings.TrimPrefix(child.SelectAttrValue("PartName", ""), "/"))] = true
		}
	}
	names := make([]string, 0, len(parts))
	for name := range parts {
		if name != "[Content_Types].xml" {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	for _, name := range names {
		extension := strings.TrimPrefix(strings.ToLower(path.Ext(name)), ".")
		if !overrides[path.Clean(name)] && !defaults[extension] {
			return fmt.Errorf("package part %q has no content type", name)
		}
	}
	return nil
}

func auditPackageRelationships(parts map[string][]byte) error {
	paths := make([]string, 0)
	for name := range parts {
		if strings.HasSuffix(name, ".rels") {
			paths = append(paths, name)
		}
	}
	sort.Strings(paths)
	for _, relationshipPath := range paths {
		document, err := parseOOXML(parts[relationshipPath])
		if err != nil {
			return fmt.Errorf("%s: %w", relationshipPath, err)
		}
		base := relationshipSourceDirectory(relationshipPath)
		for _, relationship := range document.Root().ChildElements() {
			if relationship.Tag != "Relationship" || strings.EqualFold(relationship.SelectAttrValue("TargetMode", ""), "External") {
				continue
			}
			targetValue := relationship.SelectAttrValue("Target", "")
			var target string
			if after, ok := strings.CutPrefix(targetValue, "/"); ok {
				target = path.Clean(after)
			} else {
				target = path.Clean(path.Join(base, targetValue))
			}
			if len(parts[target]) == 0 {
				return fmt.Errorf("%s relationship %q uses missing part %q", relationshipPath, relationship.SelectAttrValue("Id", ""), target)
			}
		}
	}
	return nil
}

func relationshipSourceDirectory(relationshipPath string) string {
	if relationshipPath == "_rels/.rels" {
		return ""
	}
	marker := "/_rels/"
	index := strings.LastIndex(relationshipPath, marker)
	if index < 0 {
		return path.Dir(relationshipPath)
	}
	source := relationshipPath[:index+1] + strings.TrimSuffix(relationshipPath[index+len(marker):], ".rels")
	return path.Dir(source)
}
