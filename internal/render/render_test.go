package render

import (
	"archive/zip"
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRenderProducesDeterministicStyledDOCX(t *testing.T) {
	root := repoRoot(t)
	fixture := filepath.Join(root, "tools", "mdoc", "testdata", "feasibility", "google-import.md")
	assets := []string{
		filepath.Join(filepath.Dir(fixture), "synthetic-diagram.svg"),
		filepath.Join(filepath.Dir(fixture), "synthetic-diagram.png"),
		filepath.Join(filepath.Dir(fixture), "synthetic-diagram.jpg"),
	}
	request := Request{
		SourcePath: fixture, ReferenceDOCX: filepath.Join(root, "document-styles", "work-reference.docx"),
		ResourcePaths: []string{filepath.Dir(fixture)}, Assets: assets,
		ProfileFingerprint: []byte("work|max-columns=5"), LinkTargets: map[string]string{"pending": "https://link-01.mdoc.invalid/pending"},
	}
	renderer := Renderer{SVGRasterizer: testSVGRasterizer}
	first, err := renderer.Render(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	second, err := renderer.Render(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first.DOCX, second.DOCX) {
		t.Fatal("normalized DOCX output changed between identical renders")
	}
	parts := readDOCX(t, first.DOCX)
	documentXML := string(parts["word/document.xml"])
	for _, style := range []string{"Title", "Heading1", "Heading2", "Heading3", "Heading4", "Heading5"} {
		if !strings.Contains(documentXML, `w:val="`+style+`"`) {
			t.Fatalf("document is missing %s", style)
		}
	}
	if strings.Contains(documentXML, "w:bookmarkStart") {
		t.Fatal("rendered headings contain automatic bookmarks")
	}
	stylesXML := string(parts["word/styles.xml"])
	for _, style := range []string{"Table1", "BlockText", "Caption", "SourceCode", "VerbatimChar", "Hyperlink"} {
		if !strings.Contains(stylesXML, `w:styleId="`+style+`"`) {
			t.Fatalf("styles are missing %s", style)
		}
	}
	relationshipsXML := string(parts["word/_rels/document.xml.rels"])
	if !strings.Contains(relationshipsXML, ".fallback.png") || !strings.Contains(documentXML, "asvg:svgBlip") || !strings.Contains(documentXML, `a:blip r:embed="rIdMdocSvgFallback`) {
		t.Fatal("SVG media is missing its PNG compatibility fallback")
	}
	media := 0
	for name := range parts {
		if strings.HasPrefix(name, "word/media/") {
			media++
		}
	}
	if media < 3 {
		t.Fatalf("media files = %d", media)
	}
	if first.RenderHash == "" || first.PandocVersion == "" {
		t.Fatalf("result = %#v", first)
	}
}

func TestValidateSVGRasterizerExercisesConfiguredRasterizer(t *testing.T) {
	called := false
	renderer := Renderer{SVGRasterizer: func(_ context.Context, source []byte) ([]byte, error) {
		called = true
		if !bytes.Contains(source, []byte("<svg")) {
			t.Fatal("rasterizer probe is not SVG")
		}
		return testSVGRasterizer(context.Background(), source)
	}}
	if err := renderer.ValidateSVGRasterizer(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("configured SVG rasterizer was not called")
	}
}

func TestValidateSVGRasterizerRejectsInvalidOutput(t *testing.T) {
	renderer := Renderer{SVGRasterizer: func(context.Context, []byte) ([]byte, error) {
		return []byte("not a PNG"), nil
	}}
	if err := renderer.ValidateSVGRasterizer(context.Background()); err == nil || !strings.Contains(err.Error(), "invalid PNG") {
		t.Fatalf("error = %v", err)
	}
}

func TestRenderUsesArgumentArrayAndRemovesPrivateTempDirectory(t *testing.T) {
	root := repoRoot(t)
	directory := t.TempDir()
	source := filepath.Join(directory, "source;touch SHOULD_NOT_EXIST.md")
	if err := os.WriteFile(source, []byte("# Safe\n\nContent."), 0o600); err != nil {
		t.Fatal(err)
	}
	var renderDirectory string
	renderer := Renderer{TempDir: func(base, pattern string) (string, error) {
		path, err := os.MkdirTemp(base, pattern)
		renderDirectory = path
		return path, err
	}}
	_, err := renderer.Render(context.Background(), Request{SourcePath: source, ReferenceDOCX: filepath.Join(root, "document-styles", "work-reference.docx")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(renderDirectory); !os.IsNotExist(err) {
		t.Fatalf("render temp directory still exists: %v", err)
	}
	if _, err := os.Stat(filepath.Join(directory, "SHOULD_NOT_EXIST.md")); !os.IsNotExist(err) {
		t.Fatal("source filename was interpreted by a shell")
	}
}

func TestRenderChangesConfiguredLocalLinksAndKeepsExternalLinks(t *testing.T) {
	root := repoRoot(t)
	directory := t.TempDir()
	source := filepath.Join(directory, "links.md")
	if err := os.WriteFile(source, []byte("# Links\n\n[Local](b.md) [External](https://example.com/path)"), 0o600); err != nil {
		t.Fatal(err)
	}
	target := "https://0123456789abcdef.mdoc.invalid/pending"
	result, err := (Renderer{}).Render(context.Background(), Request{SourcePath: source, ReferenceDOCX: filepath.Join(root, "document-styles", "work-reference.docx"), LinkTargets: map[string]string{"b.md": target}})
	if err != nil {
		t.Fatal(err)
	}
	relationships := string(readDOCX(t, result.DOCX)["word/_rels/document.xml.rels"])
	if !strings.Contains(relationships, target) || !strings.Contains(relationships, "https://example.com/path") || strings.Contains(relationships, `Target="b.md"`) {
		t.Fatalf("relationships = %s", relationships)
	}
}

func TestRenderUsesNeutralPandocStyleWithoutReference(t *testing.T) {
	directory := t.TempDir()
	source := filepath.Join(directory, "neutral.md")
	if err := os.WriteFile(source, []byte("# Neutral\n\nContent."), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := (Renderer{}).Render(context.Background(), Request{SourcePath: source, HeadingFilter: "none", Reader: "gfm"})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.DOCX) == 0 || result.RenderHash == "" {
		t.Fatalf("neutral result = %#v", result)
	}
}

func TestRenderAndHashUseTheSourceDirectoryAssetWhenRootNameDuplicates(t *testing.T) {
	root := t.TempDir()
	sourceDir := filepath.Join(root, "docs")
	if err := os.MkdirAll(sourceDir, 0o700); err != nil {
		t.Fatal(err)
	}
	rootAsset := filepath.Join(root, "image.png")
	sourceAsset := filepath.Join(sourceDir, "image.png")
	writeTestPNG(t, rootAsset, color.RGBA{R: 255, A: 255})
	writeTestPNG(t, sourceAsset, color.RGBA{B: 255, A: 255})
	source := filepath.Join(sourceDir, "source.md")
	if err := os.WriteFile(source, []byte("# Source\n\n![](image.png)\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	request := Request{SourcePath: source, ResourcePaths: []string{sourceDir}, Assets: []string{sourceAsset}, HeadingFilter: "none"}
	first, err := (Renderer{}).Render(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	media := renderedMedia(t, first.DOCX)
	decoded, _, err := image.Decode(bytes.NewReader(media))
	if err != nil {
		t.Fatal(err)
	}
	if got := color.RGBAModel.Convert(decoded.At(0, 0)).(color.RGBA); got.B != 255 || got.R != 0 {
		t.Fatalf("rendered duplicate asset color = %#v", got)
	}

	writeTestPNG(t, sourceAsset, color.RGBA{G: 255, A: 255})
	secondHash, _, err := (Renderer{}).Fingerprint(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if secondHash == first.RenderHash {
		t.Fatal("render hash did not change with the selected source asset")
	}
}

func TestHashInputReadsButDoesNotExecuteCustomFilter(t *testing.T) {
	directory := t.TempDir()
	source := filepath.Join(directory, "source.md")
	filter := filepath.Join(directory, "trusted.lua")
	marker := filepath.Join(directory, "executed")
	if err := os.WriteFile(source, []byte("# Title\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	filterSource := "os.execute(" + luaQuote("touch "+marker) + ")\n"
	if err := os.WriteFile(filter, []byte(filterSource), 0o600); err != nil {
		t.Fatal(err)
	}
	input, err := (Renderer{}).HashInput(context.Background(), Request{SourcePath: source, HeadingFilter: "none", Filters: []string{filter}})
	if err != nil {
		t.Fatal(err)
	}
	if len(input.Filters) != 1 || !bytes.Equal(input.Filters[0].Data, []byte(filterSource)) {
		t.Fatalf("filter hash input = %#v", input.Filters)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("hash inspection executed custom filter: %v", err)
	}
}

func TestRenderAcceptsAssembledPandocJSONAndFinalizesAfterFilters(t *testing.T) {
	directory := t.TempDir()
	filter := filepath.Join(directory, "change.lua")
	if err := os.WriteFile(filter, []byte(`function Str(el) if el.text == "Before" then el.text = "After" end return el end`), 0o600); err != nil {
		t.Fatal(err)
	}
	assembled := []byte(`{"pandoc-api-version":[1,23,1],"meta":{},"blocks":[{"t":"Header","c":[1,["title",[],[]],[{"t":"Str","c":"Title"}]]},{"t":"Para","c":[{"t":"Str","c":"Before"}]}]}`)
	finalizerCalled := false
	request := Request{PandocJSON: assembled, ResourcePaths: []string{directory}, HeadingFilter: "none", Filters: []string{filter}, FinalizeJSON: func(filtered []byte) ([]byte, error) {
		finalizerCalled = true
		if !bytes.Contains(filtered, []byte("After")) || bytes.Contains(filtered, []byte("Before")) {
			t.Fatalf("finalizer input = %s", filtered)
		}
		return filtered, nil
	}}
	if _, _, err := (Renderer{}).Fingerprint(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if finalizerCalled {
		t.Fatal("fingerprint executed the finalizer")
	}
	result, err := (Renderer{}).Render(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if !finalizerCalled || len(result.DOCX) == 0 {
		t.Fatalf("result=%#v finalizer=%t", result, finalizerCalled)
	}
	documentXML := readDOCX(t, result.DOCX)["word/document.xml"]
	if !bytes.Contains(documentXML, []byte("After")) || bytes.Contains(documentXML, []byte("Before")) {
		t.Fatalf("document XML = %s", documentXML)
	}
}

func TestRenderHashChangesForEveryInputClass(t *testing.T) {
	base := HashInput{Source: []byte("source"), Assets: map[string][]byte{"a.png": []byte("asset")}, ReferenceDOCX: []byte("reference"), ProfileFingerprint: []byte("profile"), LinkTargets: map[string]string{"a.md": "url"}, PandocVersion: "3.10.1", RendererVersion: "1", Reader: "gfm", HeadingFilter: "title", Filters: []FilterInput{{Path: "one.lua", Data: []byte("one")}, {Path: "two.lua", Data: []byte("two")}}}
	baseline := ComputeHash(base)
	mutations := []HashInput{
		{Source: []byte("changed"), Assets: base.Assets, ReferenceDOCX: base.ReferenceDOCX, ProfileFingerprint: base.ProfileFingerprint, LinkTargets: base.LinkTargets, PandocVersion: base.PandocVersion, RendererVersion: base.RendererVersion},
		{Source: base.Source, Assets: map[string][]byte{"a.png": []byte("changed")}, ReferenceDOCX: base.ReferenceDOCX, ProfileFingerprint: base.ProfileFingerprint, LinkTargets: base.LinkTargets, PandocVersion: base.PandocVersion, RendererVersion: base.RendererVersion},
		{Source: base.Source, Assets: base.Assets, ReferenceDOCX: []byte("changed"), ProfileFingerprint: base.ProfileFingerprint, LinkTargets: base.LinkTargets, PandocVersion: base.PandocVersion, RendererVersion: base.RendererVersion},
		{Source: base.Source, Assets: base.Assets, ReferenceDOCX: base.ReferenceDOCX, ProfileFingerprint: []byte("changed"), LinkTargets: base.LinkTargets, PandocVersion: base.PandocVersion, RendererVersion: base.RendererVersion},
		{Source: base.Source, Assets: base.Assets, ReferenceDOCX: base.ReferenceDOCX, ProfileFingerprint: base.ProfileFingerprint, LinkTargets: map[string]string{"a.md": "changed"}, PandocVersion: base.PandocVersion, RendererVersion: base.RendererVersion},
		{Source: base.Source, Assets: base.Assets, ReferenceDOCX: base.ReferenceDOCX, ProfileFingerprint: base.ProfileFingerprint, LinkTargets: base.LinkTargets, PandocVersion: "3.11.0", RendererVersion: base.RendererVersion},
		{Source: base.Source, Assets: base.Assets, ReferenceDOCX: base.ReferenceDOCX, ProfileFingerprint: base.ProfileFingerprint, LinkTargets: base.LinkTargets, PandocVersion: base.PandocVersion, RendererVersion: base.RendererVersion, Reader: "markdown", HeadingFilter: base.HeadingFilter, Filters: base.Filters},
		{Source: base.Source, Assets: base.Assets, ReferenceDOCX: base.ReferenceDOCX, ProfileFingerprint: base.ProfileFingerprint, LinkTargets: base.LinkTargets, PandocVersion: base.PandocVersion, RendererVersion: base.RendererVersion, Reader: base.Reader, HeadingFilter: "none", Filters: base.Filters},
		{Source: base.Source, Assets: base.Assets, ReferenceDOCX: base.ReferenceDOCX, ProfileFingerprint: base.ProfileFingerprint, LinkTargets: base.LinkTargets, PandocVersion: base.PandocVersion, RendererVersion: base.RendererVersion, Reader: base.Reader, HeadingFilter: base.HeadingFilter, Filters: []FilterInput{base.Filters[1], base.Filters[0]}},
	}
	for index, input := range mutations {
		if ComputeHash(input) == baseline {
			t.Fatalf("mutation %d did not change hash", index)
		}
	}
}

func TestArtifactHashIsCanonicalAndCoversVisibleOutput(t *testing.T) {
	first, err := ComputeArtifactHash("sha256:render", map[string]any{"b": 2, "a": 1}, []byte("docx"))
	if err != nil {
		t.Fatal(err)
	}
	repeated, err := ComputeArtifactHash("sha256:render", map[string]any{"a": 1, "b": 2}, []byte("docx"))
	if err != nil || repeated != first {
		t.Fatalf("repeated = %q, error = %v", repeated, err)
	}
	changedValues, _ := ComputeArtifactHash("sha256:render", map[string]any{"a": 2, "b": 2}, []byte("docx"))
	changedDOCX, _ := ComputeArtifactHash("sha256:render", map[string]any{"a": 1, "b": 2}, []byte("changed"))
	changedRender, _ := ComputeArtifactHash("sha256:changed", map[string]any{"a": 1, "b": 2}, []byte("docx"))
	if changedValues == first || changedDOCX == first || changedRender == first {
		t.Fatal("artifact hash omitted a required input")
	}
}

func TestDescribeHashInputReturnsOnlyNamesAndDigests(t *testing.T) {
	input := HashInput{Source: []byte("secret source"), Parts: []HashPart{{Name: "resolved_fields", Data: []byte("secret field")}}}
	parts := DescribeHashInput(input)
	if len(parts) < 2 || parts[len(parts)-1].Name != "part:0:resolved_fields" || strings.Contains(parts[len(parts)-1].Hash, "secret") {
		t.Fatalf("parts = %#v", parts)
	}
}

func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", "..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func readDOCX(t *testing.T, data []byte) map[string][]byte {
	t.Helper()
	archive, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	parts := map[string][]byte{}
	for _, file := range archive.File {
		reader, err := file.Open()
		if err != nil {
			t.Fatal(err)
		}
		value, err := io.ReadAll(reader)
		reader.Close()
		if err != nil {
			t.Fatal(err)
		}
		parts[file.Name] = value
	}
	return parts
}

func renderedMedia(t *testing.T, docx []byte) []byte {
	t.Helper()
	for name, data := range readDOCX(t, docx) {
		if strings.HasPrefix(name, "word/media/") {
			return data
		}
	}
	t.Fatal("rendered DOCX has no media")
	return nil
}

func writeTestPNG(t *testing.T, path string, value color.RGBA) {
	t.Helper()
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	canvas := image.NewRGBA(image.Rect(0, 0, 1, 1))
	canvas.SetRGBA(0, 0, value)
	encodeErr := png.Encode(file, canvas)
	closeErr := file.Close()
	if encodeErr != nil {
		t.Fatal(encodeErr)
	}
	if closeErr != nil {
		t.Fatal(closeErr)
	}
}

func testSVGRasterizer(context.Context, []byte) ([]byte, error) {
	var output bytes.Buffer
	canvas := image.NewRGBA(image.Rect(0, 0, 2, 2))
	canvas.SetRGBA(0, 0, color.RGBA{R: 255, A: 255})
	if err := png.Encode(&output, canvas); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}
