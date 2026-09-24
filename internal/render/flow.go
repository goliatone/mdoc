package render

import (
	"regexp"
	"strings"

	"github.com/beevik/etree"
)

var paragraphElementPattern = regexp.MustCompile(`(?s)<w:p\b.*?</w:p>`)
var keepNextPattern = regexp.MustCompile(`<w:keepNext\b[^>]*/>`)

func applyKeepWithNext(document string, enabled bool, captionStyles []string) string {
	parsed, err := parseOOXML([]byte(document))
	if err != nil {
		return document
	}
	applyKeepWithNextElements(parsed.Root(), enabled, captionStyles)
	data, err := writeOOXML(parsed)
	if err != nil {
		return document
	}
	return string(data)
}

func applyKeepWithNextElements(root *etree.Element, enabled bool, captionStyles []string) {
	if !enabled && len(captionStyles) == 0 {
		return
	}
	captions := map[string]bool{}
	for _, style := range captionStyles {
		captions[style] = true
	}
	for _, paragraph := range descendantElements(root, "p") {
		properties := directElement(paragraph, "pPr")
		style := wordAttr(directElement(properties, "pStyle"), "val")
		keep := captions[style]
		if enabled && strings.HasPrefix(style, "Heading") {
			level := strings.TrimPrefix(style, "Heading")
			keep = len(level) == 1 && level[0] >= '1' && level[0] <= '6'
		}
		if !keep {
			continue
		}
		if properties == nil {
			properties = ensureFirstChild(paragraph, "pPr")
		}
		if directElement(properties, "keepNext") == nil {
			upsertOrderedChild(properties, wordElement("keepNext"), paragraphPropertyOrder)
		}
	}
}

func removeKeepWithNextBeforeTables(document string) string {
	parsed, err := parseOOXML([]byte(document))
	if err != nil {
		return document
	}
	removeKeepWithNextBeforeTableElements(parsed.Root())
	data, err := writeOOXML(parsed)
	if err != nil {
		return document
	}
	return string(data)
}

func removeKeepWithNextBeforeTableElements(root *etree.Element) {
	for _, table := range descendantElements(root, "tbl") {
		paragraph := previousSiblingElement(table)
		if paragraph == nil || paragraph.Space != "w" || paragraph.Tag != "p" {
			continue
		}
		properties := directElement(paragraph, "pPr")
		style := wordAttr(directElement(properties, "pStyle"), "val")
		if !strings.HasPrefix(style, "Heading") {
			continue
		}
		if properties == nil {
			properties = ensureFirstChild(paragraph, "pPr")
		}
		keep := wordElement("keepNext")
		setWordAttr(keep, "val", "0")
		upsertOrderedChild(properties, keep, paragraphPropertyOrder)
	}
}
