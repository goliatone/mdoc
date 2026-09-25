package render

import (
	"bytes"
	"context"
	_ "embed"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

const RendererVersion = "7"

//go:embed filters/title-style.lua
var titleStyleFilter []byte

type Request struct {
	SourcePath         string
	PandocJSON         []byte
	FinalizeJSON       func([]byte) ([]byte, error)
	ReferenceDOCX      string
	ResourcePaths      []string
	Assets             []string
	ProfileFingerprint []byte
	LinkTargets        map[string]string
	Reader             string
	HeadingFilter      string
	Filters            []string
	HashParts          []HashPart
	Layout             *LayoutPlan
}

type Result struct {
	DOCX          []byte
	RenderHash    string
	PandocVersion string
	Warnings      []string
}

type Renderer struct {
	Pandoc        string
	Filter        string
	TempDir       func(string, string) (string, error)
	SVGRasterizer SVGRasterizer
}

func (r Renderer) ValidatePandoc(ctx context.Context) error {
	binary := r.Pandoc
	if binary == "" {
		binary = "pandoc"
	}
	version, err := pandocVersion(ctx, binary)
	if err != nil {
		return err
	}
	return checkVersion(version)
}

func (r Renderer) ValidateStyle(ctx context.Context, referencePath string) error {
	binary := r.Pandoc
	if binary == "" {
		binary = "pandoc"
	}
	if err := r.ValidatePandoc(ctx); err != nil {
		return err
	}
	defaultReference, err := exec.CommandContext(ctx, binary, "--print-default-data-file", "reference.docx").Output()
	if err != nil {
		return fmt.Errorf("read Pandoc default reference: %w", err)
	}
	return ValidateReference(referencePath, defaultReference)
}

func (r Renderer) Fingerprint(ctx context.Context, request Request) (string, string, error) {
	input, err := r.HashInput(ctx, request)
	if err != nil {
		return "", "", err
	}
	return ComputeHash(input), input.PandocVersion, nil
}

func (r Renderer) HashInput(ctx context.Context, request Request) (HashInput, error) {
	binary := r.Pandoc
	if binary == "" {
		binary = "pandoc"
	}
	version, err := pandocVersion(ctx, binary)
	if err != nil {
		return HashInput{}, err
	}
	if err := checkVersion(version); err != nil {
		return HashInput{}, err
	}
	input, err := loadHashInput(request, version)
	if err != nil {
		return HashInput{}, err
	}
	return input, nil
}

func (r Renderer) Render(ctx context.Context, request Request) (Result, error) {
	binary := r.Pandoc
	if binary == "" {
		binary = "pandoc"
	}
	version, err := pandocVersion(ctx, binary)
	if err != nil {
		return Result{}, err
	}
	if err := checkVersion(version); err != nil {
		return Result{}, err
	}
	hashInput, err := loadHashInput(request, version)
	if err != nil {
		return Result{}, err
	}
	tempDir := r.TempDir
	if tempDir == nil {
		tempDir = os.MkdirTemp
	}
	directory, err := tempDir("", "mdoc-render-*")
	if err != nil {
		return Result{}, err
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		os.RemoveAll(directory)
		return Result{}, err
	}
	defer os.RemoveAll(directory)
	rawOutput := filepath.Join(directory, "raw.docx")
	layoutOutput := filepath.Join(directory, "layout.docx")
	normalizedOutput := filepath.Join(directory, "document.docx")
	resourcePaths := request.ResourcePaths
	if len(resourcePaths) == 0 {
		resourcePaths = []string{filepath.Dir(request.SourcePath)}
	}
	reader := request.Reader
	if reader == "" {
		reader = "gfm"
	}
	inputPath := request.SourcePath
	inputReader := reader
	if len(request.PandocJSON) > 0 {
		inputPath = filepath.Join(directory, "assembled.json")
		if err := os.WriteFile(inputPath, request.PandocJSON, 0o600); err != nil {
			return Result{}, fmt.Errorf("write assembled Pandoc JSON: %w", err)
		}
		inputReader = "json"
	}
	args := []string{"--from=" + inputReader, "--standalone", "--resource-path=" + strings.Join(resourcePaths, string(filepath.ListSeparator))}
	if request.ReferenceDOCX != "" {
		defaultReference, err := exec.CommandContext(ctx, binary, "--print-default-data-file", "reference.docx").Output()
		if err != nil {
			return Result{}, fmt.Errorf("read Pandoc default reference: %w", err)
		}
		preparedReference := filepath.Join(directory, "reference.docx")
		if err := SupplementReference(request.ReferenceDOCX, defaultReference, preparedReference); err != nil {
			return Result{}, err
		}
		args = append(args, "--reference-doc="+preparedReference)
	}
	headingFilter := request.HeadingFilter
	if headingFilter == "" {
		headingFilter = "title"
	}
	if headingFilter == "title" && len(request.PandocJSON) == 0 {
		filter := r.Filter
		if filter == "" {
			filter = filepath.Join(directory, "title-style.lua")
			if err := os.WriteFile(filter, titleStyleFilter, 0o600); err != nil {
				return Result{}, fmt.Errorf("write embedded title filter: %w", err)
			}
		}
		args = append(args, "--lua-filter="+filter)
	}
	filterArguments := []string{}
	for _, filter := range request.Filters {
		filterArguments = append(filterArguments, "--lua-filter="+filter)
	}
	if len(request.LinkTargets) > 0 {
		linkFilter := filepath.Join(directory, "link-targets.lua")
		if err := os.WriteFile(linkFilter, []byte(linkFilterSource(request.LinkTargets)), 0o600); err != nil {
			return Result{}, err
		}
		filterArguments = append(filterArguments, "--lua-filter="+linkFilter)
	}
	if len(request.PandocJSON) > 0 {
		filtered := request.PandocJSON
		if len(filterArguments) > 0 {
			filteredPath := filepath.Join(directory, "filtered.json")
			filterArgs := []string{"--from=json", "--to=json", "--output=" + filteredPath}
			filterArgs = append(filterArgs, filterArguments...)
			filterArgs = append(filterArgs, inputPath)
			filterCommand := exec.CommandContext(ctx, binary, filterArgs...)
			var filterStderr bytes.Buffer
			filterCommand.Stderr = &filterStderr
			if err := filterCommand.Run(); err != nil {
				return Result{}, fmt.Errorf("filter assembled Pandoc JSON: %w: %s", err, strings.TrimSpace(filterStderr.String()))
			}
			filtered, err = os.ReadFile(filteredPath)
			if err != nil {
				return Result{}, fmt.Errorf("read filtered Pandoc JSON: %w", err)
			}
		}
		if request.FinalizeJSON != nil {
			filtered, err = request.FinalizeJSON(filtered)
			if err != nil {
				return Result{}, fmt.Errorf("finalize assembled Pandoc JSON: %w", err)
			}
		}
		inputPath = filepath.Join(directory, "final.json")
		if err := os.WriteFile(inputPath, filtered, 0o600); err != nil {
			return Result{}, fmt.Errorf("write finalized Pandoc JSON: %w", err)
		}
	} else {
		args = append(args, filterArguments...)
	}
	args = append(args, "--output="+rawOutput, inputPath)
	command := exec.CommandContext(ctx, binary, args...)
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		return Result{}, fmt.Errorf("render %s with Pandoc: %w: %s", request.SourcePath, err, strings.TrimSpace(stderr.String()))
	}
	normalizeInput := rawOutput
	layoutWarnings := []string{}
	if request.Layout != nil {
		var layoutErr error
		layoutWarnings, layoutErr = ApplyLayoutWithReport(rawOutput, layoutOutput, *request.Layout)
		if layoutErr != nil {
			return Result{}, fmt.Errorf("apply DOCX layout: %w", layoutErr)
		}
		normalizeInput = layoutOutput
	}
	compatibilityOutput := filepath.Join(directory, "compatible.docx")
	rasterizer := r.SVGRasterizer
	if rasterizer == nil {
		rasterizer = CommandSVGRasterizer("rsvg-convert")
	}
	svgFallbacks, err := AddSVGFallbacks(ctx, normalizeInput, compatibilityOutput, rasterizer)
	if err != nil {
		return Result{}, fmt.Errorf("add SVG compatibility fallback: %w", err)
	}
	if svgFallbacks > 0 {
		normalizeInput = compatibilityOutput
	}
	if err := NormalizeDOCX(normalizeInput, normalizedOutput); err != nil {
		return Result{}, fmt.Errorf("normalize rendered DOCX: %w", err)
	}
	docx, err := os.ReadFile(normalizedOutput)
	if err != nil {
		return Result{}, err
	}
	hash := ComputeHash(hashInput)
	warnings := []string{}
	warnings = append(warnings, layoutWarnings...)
	for line := range strings.SplitSeq(strings.TrimSpace(stderr.String()), "\n") {
		if strings.TrimSpace(line) != "" {
			warnings = append(warnings, line)
		}
	}
	return Result{DOCX: docx, RenderHash: hash, PandocVersion: version, Warnings: warnings}, nil
}

func loadHashInput(request Request, version string) (HashInput, error) {
	source := request.PandocJSON
	var err error
	if len(source) == 0 {
		source, err = os.ReadFile(request.SourcePath)
		if err != nil {
			return HashInput{}, fmt.Errorf("read render source: %w", err)
		}
	}
	var reference []byte
	if request.ReferenceDOCX != "" {
		reference, err = os.ReadFile(request.ReferenceDOCX)
		if err != nil {
			return HashInput{}, fmt.Errorf("read reference DOCX: %w", err)
		}
	}
	assets := map[string][]byte{}
	for _, path := range request.Assets {
		data, err := os.ReadFile(path)
		if err != nil {
			return HashInput{}, fmt.Errorf("read render asset %s: %w", path, err)
		}
		assets[filepath.ToSlash(path)] = data
	}
	filters := make([]FilterInput, 0, len(request.Filters))
	for _, path := range request.Filters {
		data, err := os.ReadFile(path)
		if err != nil {
			return HashInput{}, fmt.Errorf("read Lua filter %s: %w", path, err)
		}
		filters = append(filters, FilterInput{Path: filepath.ToSlash(path), Data: data})
	}
	reader := request.Reader
	if reader == "" {
		reader = "gfm"
	}
	headingFilter := request.HeadingFilter
	if headingFilter == "" {
		headingFilter = "title"
	}
	return HashInput{Source: source, Assets: assets, ReferenceDOCX: reference, ProfileFingerprint: request.ProfileFingerprint, LinkTargets: request.LinkTargets, PandocVersion: version, RendererVersion: RendererVersion, Reader: reader, HeadingFilter: headingFilter, Filters: filters, Parts: append([]HashPart(nil), request.HashParts...)}, nil
}

func linkFilterSource(targets map[string]string) string {
	keys := make([]string, 0, len(targets))
	for key := range targets {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var builder strings.Builder
	builder.WriteString("local targets = {\n")
	for _, key := range keys {
		fmt.Fprintf(&builder, "  [%s] = %s,\n", luaQuote(key), luaQuote(targets[key]))
	}
	builder.WriteString("}\nfunction Link(element)\n  if targets[element.target] ~= nil then element.target = targets[element.target] end\n  return element\nend\n")
	return builder.String()
}

func luaQuote(value string) string {
	value = strings.ReplaceAll(value, "\\", "\\\\")
	value = strings.ReplaceAll(value, "\"", "\\\"")
	value = strings.ReplaceAll(value, "\n", "\\n")
	value = strings.ReplaceAll(value, "\r", "\\r")
	return "\"" + value + "\""
}

var versionPattern = regexp.MustCompile(`(?m)^pandoc\s+(\d+)\.(\d+)(?:\.(\d+))?`)

func pandocVersion(ctx context.Context, binary string) (string, error) {
	output, err := exec.CommandContext(ctx, binary, "--version").Output()
	if err != nil {
		return "", fmt.Errorf("run Pandoc: %w; install Pandoc with `brew install pandoc`", err)
	}
	match := versionPattern.FindStringSubmatch(string(output))
	if len(match) == 0 {
		return "", errors.New("read Pandoc version")
	}
	return strings.TrimPrefix(match[0], "pandoc "), nil
}

func checkVersion(version string) error {
	parts := strings.Split(version, ".")
	major, _ := strconv.Atoi(parts[0])
	minor := 0
	if len(parts) > 1 {
		minor, _ = strconv.Atoi(parts[1])
	}
	if major != 3 || minor < 1 {
		return fmt.Errorf("unsupported Pandoc version %s; install Pandoc 3.1 or newer and below 4.0", version)
	}
	return nil
}
