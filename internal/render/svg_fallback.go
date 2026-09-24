package render

import (
	"bytes"
	"context"
	"fmt"
	"image/png"
	"os/exec"
	"path"
	"sort"
	"strings"

	"github.com/beevik/etree"
)

const imageRelationshipType = "http://schemas.openxmlformats.org/officeDocument/2006/relationships/image"

type SVGRasterizer func(context.Context, []byte) ([]byte, error)

const svgRasterizerProbe = `<svg xmlns="http://www.w3.org/2000/svg" width="1" height="1"><rect width="1" height="1" fill="#000"/></svg>`

func CommandSVGRasterizer(binary string) SVGRasterizer {
	return func(ctx context.Context, source []byte) ([]byte, error) {
		command := exec.CommandContext(ctx, binary, "--format=png")
		command.Stdin = bytes.NewReader(source)
		var output bytes.Buffer
		var stderr bytes.Buffer
		command.Stdout = &output
		command.Stderr = &stderr
		if err := command.Run(); err != nil {
			message := strings.TrimSpace(stderr.String())
			if message == "" {
				message = "install librsvg so rsvg-convert is available"
			}
			return nil, fmt.Errorf("run %s: %w: %s", binary, err, message)
		}
		if _, err := png.DecodeConfig(bytes.NewReader(output.Bytes())); err != nil {
			return nil, fmt.Errorf("%s returned invalid PNG data: %w", binary, err)
		}
		return output.Bytes(), nil
	}
}

func (r Renderer) ValidateSVGRasterizer(ctx context.Context) error {
	rasterizer := r.SVGRasterizer
	if rasterizer == nil {
		rasterizer = CommandSVGRasterizer("rsvg-convert")
	}
	result, err := rasterizer(ctx, []byte(svgRasterizerProbe))
	if err != nil {
		return fmt.Errorf("SVG assets require rsvg-convert; install librsvg and retry: %w", err)
	}
	if _, err := png.DecodeConfig(bytes.NewReader(result)); err != nil {
		return fmt.Errorf("SVG rasterizer returned invalid PNG data: %w", err)
	}
	return nil
}

func AddSVGFallbacks(ctx context.Context, inputPath, outputPath string, rasterize SVGRasterizer) (int, error) {
	parts, err := readZip(inputPath)
	if err != nil {
		return 0, err
	}
	documentXML := parts["word/document.xml"]
	relationshipsXML := parts["word/_rels/document.xml.rels"]
	if len(documentXML) == 0 || len(relationshipsXML) == 0 {
		return 0, nil
	}
	document, err := parseOOXML(documentXML)
	if err != nil {
		return 0, fmt.Errorf("parse document XML: %w", err)
	}
	relationships, err := parseOOXML(relationshipsXML)
	if err != nil {
		return 0, fmt.Errorf("parse document relationships: %w", err)
	}

	type svgRelationship struct {
		id     string
		target string
		part   string
	}
	byID := map[string]svgRelationship{}
	usedIDs := map[string]bool{}
	for _, relationship := range relationships.Root().ChildElements() {
		id := relationship.SelectAttrValue("Id", "")
		usedIDs[id] = true
		target := relationship.SelectAttrValue("Target", "")
		if relationship.SelectAttrValue("Type", "") != imageRelationshipType || !strings.EqualFold(path.Ext(target), ".svg") {
			continue
		}
		part := path.Clean(path.Join("word", target))
		if !strings.HasPrefix(part, "word/media/") || len(parts[part]) == 0 {
			return 0, fmt.Errorf("SVG relationship %s has invalid target %q", id, target)
		}
		byID[id] = svgRelationship{id: id, target: target, part: part}
	}
	if len(byID) == 0 {
		return 0, nil
	}
	if rasterize == nil {
		return 0, fmt.Errorf("SVG media requires a rasterizer")
	}

	elements := allElements(document.Root())
	added := 0
	orderedIDs := make([]string, 0, len(byID))
	for id := range byID {
		orderedIDs = append(orderedIDs, id)
	}
	sort.Strings(orderedIDs)
	for _, id := range orderedIDs {
		var blips []*etree.Element
		for _, element := range elements {
			if element.Space == "asvg" && element.Tag == "svgBlip" && element.SelectAttrValue("r:embed", "") == id {
				if blip := ancestor(element, "a", "blip"); blip != nil {
					blips = append(blips, blip)
				}
			}
		}
		if len(blips) == 0 {
			continue
		}
		pngData, err := rasterize(ctx, parts[byID[id].part])
		if err != nil {
			return 0, fmt.Errorf("rasterize %s: %w", byID[id].target, err)
		}
		if _, err := png.DecodeConfig(bytes.NewReader(pngData)); err != nil {
			return 0, fmt.Errorf("rasterize %s returned invalid PNG data: %w", byID[id].target, err)
		}
		fallbackID := nextRelationshipID(usedIDs)
		usedIDs[fallbackID] = true
		fallbackTarget := strings.TrimSuffix(byID[id].target, path.Ext(byID[id].target)) + ".fallback.png"
		parts[path.Clean(path.Join("word", fallbackTarget))] = pngData
		newRelationship := etree.NewElement("Relationship")
		newRelationship.CreateAttr("Id", fallbackID)
		newRelationship.CreateAttr("Type", imageRelationshipType)
		newRelationship.CreateAttr("Target", fallbackTarget)
		relationships.Root().AddChild(newRelationship)
		for _, blip := range blips {
			blip.CreateAttr("r:embed", fallbackID)
		}
		added++
	}
	if added == 0 {
		return 0, nil
	}
	parts["word/document.xml"], err = writeOOXML(document)
	if err != nil {
		return 0, fmt.Errorf("write document XML: %w", err)
	}
	parts["word/_rels/document.xml.rels"], err = writeOOXML(relationships)
	if err != nil {
		return 0, fmt.Errorf("write document relationships: %w", err)
	}
	if err := ensurePackageContentTypes(parts); err != nil {
		return 0, err
	}
	if err := writeZip(outputPath, parts); err != nil {
		return 0, err
	}
	return added, nil
}

func allElements(root *etree.Element) []*etree.Element {
	result := []*etree.Element{}
	var visit func(*etree.Element)
	visit = func(element *etree.Element) {
		result = append(result, element)
		for _, child := range element.ChildElements() {
			visit(child)
		}
	}
	visit(root)
	return result
}

func ancestor(element *etree.Element, space, tag string) *etree.Element {
	for current := element.Parent(); current != nil; current = current.Parent() {
		if current.Space == space && current.Tag == tag {
			return current
		}
	}
	return nil
}

func nextRelationshipID(used map[string]bool) string {
	for index := 1; ; index++ {
		candidate := fmt.Sprintf("rIdMdocSvgFallback%d", index)
		if !used[candidate] {
			return candidate
		}
	}
}
