package fields

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

type Origin struct {
	Kind string `json:"kind"`
	Path string `json:"path,omitempty"`
}

type FileInput struct {
	Path     string
	Identity string
	Kind     string
	Data     []byte
	Hash     string
}

type Summary struct {
	RootCount       int    `json:"root_count"`
	FileCount       int    `json:"file_count"`
	AssignmentCount int    `json:"assignment_count"`
	Hash            string `json:"hash"`
	Warning         string `json:"warning"`
}

type ResolveOptions struct {
	PublicationID     string
	Defaults          map[string]any
	ConfiguredFiles   []string
	PublicationFields map[string]any
	CommandFiles      []string
	Assignments       []string
	WorkingDir        string
	ProjectRoot       string
	AllowedRoots      []string
}

type Result struct {
	Values     map[string]any
	Origins    map[string]Origin
	Files      []FileInput
	ValuesHash string
	FilesHash  string
	Hash       string
	Summary    Summary
}

func Resolve(options ResolveOptions) (Result, error) {
	values := map[string]any{}
	origins := map[string]Origin{}
	defaults, err := normalizeMap(options.Defaults)
	if err != nil {
		return Result{}, fmt.Errorf("publication %q profile field defaults: %w", options.PublicationID, err)
	}
	if err := rejectReservedRoots(defaults); err != nil {
		return Result{}, fmt.Errorf("publication %q profile field defaults: %w", options.PublicationID, err)
	}
	merge(values, defaults, "", Origin{Kind: "profile_default"}, origins)

	allowed, err := canonicalRoots(options.AllowedRoots)
	if err != nil {
		return Result{}, fmt.Errorf("publication %q field roots: %w", options.PublicationID, err)
	}
	projectRoot := options.ProjectRoot
	if projectRoot != "" {
		projectRoot, err = canonicalCandidate(projectRoot)
		if err != nil {
			return Result{}, fmt.Errorf("publication %q project root: %w", options.PublicationID, err)
		}
	}
	files := []FileInput{}
	for _, configured := range options.ConfiguredFiles {
		loaded, fileErr := loadFile(configured, "publication_file", projectRoot, allowed)
		if fileErr != nil {
			return Result{}, fmt.Errorf("publication %q: %w", options.PublicationID, fileErr)
		}
		merge(values, loaded.values, "", Origin{Kind: loaded.input.Kind, Path: loaded.input.Identity}, origins)
		files = append(files, loaded.input)
	}

	publicationValues, err := normalizeMap(options.PublicationFields)
	if err != nil {
		return Result{}, fmt.Errorf("publication %q fields: %w", options.PublicationID, err)
	}
	if err := rejectReservedRoots(publicationValues); err != nil {
		return Result{}, fmt.Errorf("publication %q fields: %w", options.PublicationID, err)
	}
	merge(values, publicationValues, "", Origin{Kind: "publication"}, origins)

	workingDir := options.WorkingDir
	if workingDir == "" {
		workingDir, err = os.Getwd()
		if err != nil {
			return Result{}, fmt.Errorf("publication %q command fields: resolve working directory: %w", options.PublicationID, err)
		}
	}
	for _, commandPath := range options.CommandFiles {
		path := commandPath
		if !filepath.IsAbs(path) {
			path = filepath.Join(workingDir, filepath.FromSlash(path))
		}
		loaded, fileErr := loadFile(path, "command_file", projectRoot, allowed)
		if fileErr != nil {
			return Result{}, fmt.Errorf("publication %q: %w", options.PublicationID, fileErr)
		}
		merge(values, loaded.values, "", Origin{Kind: loaded.input.Kind, Path: loaded.input.Identity}, origins)
		files = append(files, loaded.input)
	}

	for index, assignment := range options.Assignments {
		pointer, value, parseErr := ParseAssignment(assignment)
		if parseErr != nil {
			return Result{}, fmt.Errorf("publication %q field assignment %d: %w", options.PublicationID, index+1, parseErr)
		}
		segments, pointerErr := parsePointer(pointer)
		if pointerErr != nil {
			return Result{}, fmt.Errorf("publication %q field assignment %d: %w", options.PublicationID, index+1, pointerErr)
		}
		if segments[0] == "mdoc" || segments[0] == "page" {
			return Result{}, fmt.Errorf("publication %q field assignment %d: reserved root %q is read only", options.PublicationID, index+1, segments[0])
		}
		updated, assignErr := assign(values, segments, value)
		if assignErr != nil {
			return Result{}, fmt.Errorf("publication %q field assignment %d pointer %q: %w", options.PublicationID, index+1, pointer, assignErr)
		}
		values = updated.(map[string]any)
		origins[pointer] = Origin{Kind: "command_assignment", Path: fmt.Sprintf("%d", index+1)}
	}

	canonical, err := CanonicalJSON(values)
	if err != nil {
		return Result{}, fmt.Errorf("publication %q fields: %w", options.PublicationID, err)
	}
	valuesHash := digest(canonical)
	filesHash := hashFiles(files)
	combined := hashParts([]hashPart{{"values", canonical}, {"files", []byte(filesHash)}})
	return Result{
		Values: values, Origins: origins, Files: files, ValuesHash: valuesHash, FilesHash: filesHash, Hash: combined,
		Summary: Summary{RootCount: len(values), FileCount: len(files), AssignmentCount: len(options.Assignments), Hash: combined, Warning: "field values become document content; do not use fields for secrets"},
	}, nil
}

func ParseAssignment(input string) (string, any, error) {
	separator := strings.IndexByte(input, '=')
	if separator <= 0 {
		return "", nil, errors.New("must use <json-pointer>=<json-value>")
	}
	pointer := input[:separator]
	if _, err := parsePointer(pointer); err != nil {
		return "", nil, err
	}
	decoder := json.NewDecoder(strings.NewReader(input[separator+1:]))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return "", nil, fmt.Errorf("invalid JSON value: %w", err)
	}
	if err := ensureEOF(decoder); err != nil {
		return "", nil, err
	}
	normalized, err := normalizeValue(value)
	if err != nil {
		return "", nil, err
	}
	return pointer, normalized, nil
}

func CanonicalJSON(value any) ([]byte, error) {
	normalized, err := normalizeValue(value)
	if err != nil {
		return nil, err
	}
	return json.Marshal(normalized)
}

type loadedFile struct {
	input  FileInput
	values map[string]any
}

func loadFile(path, kind, projectRoot string, allowedRoots []string) (loadedFile, error) {
	canonical, err := canonicalCandidate(path)
	if err != nil {
		return loadedFile{}, fmt.Errorf("field file %q is unavailable: %w", path, err)
	}
	if !withinAny(canonical, allowedRoots) {
		return loadedFile{}, fmt.Errorf("field file %q resolves outside approved roots", path)
	}
	info, err := os.Stat(canonical)
	if err != nil {
		return loadedFile{}, fmt.Errorf("field file %q is unavailable: %w", path, err)
	}
	if info.IsDir() {
		return loadedFile{}, fmt.Errorf("field file %q is a directory", path)
	}
	data, err := os.ReadFile(canonical)
	if err != nil {
		return loadedFile{}, fmt.Errorf("read field file %q: %w", path, err)
	}
	values, err := decodeMap(canonical, data)
	if err != nil {
		return loadedFile{}, err
	}
	if err := rejectReservedRoots(values); err != nil {
		return loadedFile{}, fmt.Errorf("field file %q: %w", path, err)
	}
	identity := filepath.ToSlash(canonical)
	if projectRoot != "" {
		if relative, relErr := filepath.Rel(projectRoot, canonical); relErr == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			identity = filepath.ToSlash(relative)
		}
	}
	return loadedFile{input: FileInput{Path: canonical, Identity: identity, Kind: kind, Data: data, Hash: digest(data)}, values: values}, nil
}

func decodeMap(path string, data []byte) (map[string]any, error) {
	var raw map[string]any
	switch strings.ToLower(filepath.Ext(path)) {
	case ".json":
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.UseNumber()
		if err := decoder.Decode(&raw); err != nil {
			return nil, fmt.Errorf("decode field file %q: %w", path, err)
		}
		if err := ensureEOF(decoder); err != nil {
			return nil, fmt.Errorf("decode field file %q: %w", path, err)
		}
	case ".yaml", ".yml":
		decoder := yaml.NewDecoder(bytes.NewReader(data))
		if err := decoder.Decode(&raw); err != nil {
			return nil, fmt.Errorf("decode field file %q: %w", path, err)
		}
		var extra any
		if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
			if err != nil {
				return nil, fmt.Errorf("decode field file %q: %w", path, err)
			}
			return nil, fmt.Errorf("decode field file %q: multiple documents are not supported", path)
		}
	default:
		return nil, fmt.Errorf("field file %q must use .yaml, .yml, or .json", path)
	}
	if raw == nil {
		return nil, fmt.Errorf("field file %q must contain a map", path)
	}
	normalized, err := normalizeMap(raw)
	if err != nil {
		return nil, fmt.Errorf("field file %q: %w", path, err)
	}
	return normalized, nil
}

func normalizeMap(input map[string]any) (map[string]any, error) {
	if input == nil {
		return map[string]any{}, nil
	}
	normalized, err := normalizeValue(input)
	if err != nil {
		return nil, err
	}
	return normalized.(map[string]any), nil
}

func normalizeValue(input any) (any, error) {
	switch value := input.(type) {
	case nil, string, bool, json.Number:
		return value, nil
	case int:
		return json.Number(strconv.FormatInt(int64(value), 10)), nil
	case int8:
		return json.Number(strconv.FormatInt(int64(value), 10)), nil
	case int16:
		return json.Number(strconv.FormatInt(int64(value), 10)), nil
	case int32:
		return json.Number(strconv.FormatInt(int64(value), 10)), nil
	case int64:
		return json.Number(strconv.FormatInt(value, 10)), nil
	case uint:
		return json.Number(strconv.FormatUint(uint64(value), 10)), nil
	case uint8:
		return json.Number(strconv.FormatUint(uint64(value), 10)), nil
	case uint16:
		return json.Number(strconv.FormatUint(uint64(value), 10)), nil
	case uint32:
		return json.Number(strconv.FormatUint(uint64(value), 10)), nil
	case uint64:
		return json.Number(strconv.FormatUint(value, 10)), nil
	case float32:
		return json.Number(strconv.FormatFloat(float64(value), 'g', -1, 32)), nil
	case float64:
		return json.Number(strconv.FormatFloat(value, 'g', -1, 64)), nil
	case []any:
		result := make([]any, len(value))
		for index := range value {
			normalized, err := normalizeValue(value[index])
			if err != nil {
				return nil, err
			}
			result[index] = normalized
		}
		return result, nil
	case map[string]any:
		result := make(map[string]any, len(value))
		for key, item := range value {
			normalized, err := normalizeValue(item)
			if err != nil {
				return nil, fmt.Errorf("field %q: %w", key, err)
			}
			result[key] = normalized
		}
		return result, nil
	default:
		return nil, fmt.Errorf("unsupported field value type %T", input)
	}
}

func merge(target, source map[string]any, pointer string, origin Origin, origins map[string]Origin) {
	keys := make([]string, 0, len(source))
	for key := range source {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		path := pointer + "/" + escapePointer(key)
		sourceMap, sourceIsMap := source[key].(map[string]any)
		targetMap, targetIsMap := target[key].(map[string]any)
		if sourceIsMap && targetIsMap {
			merge(targetMap, sourceMap, path, origin, origins)
			continue
		}
		if sourceIsMap {
			clone := map[string]any{}
			merge(clone, sourceMap, path, origin, origins)
			target[key] = clone
		} else {
			target[key] = source[key]
			origins[path] = origin
		}
	}
}

func assign(node any, segments []string, value any) (any, error) {
	if len(segments) == 0 {
		return value, nil
	}
	switch current := node.(type) {
	case map[string]any:
		child, exists := current[segments[0]]
		if !exists {
			child = map[string]any{}
		}
		updated, err := assign(child, segments[1:], value)
		if err != nil {
			return nil, err
		}
		current[segments[0]] = updated
		return current, nil
	case []any:
		index, err := strconv.Atoi(segments[0])
		if err != nil || index < 0 || index >= len(current) {
			return nil, fmt.Errorf("array index %q is out of range", segments[0])
		}
		updated, err := assign(current[index], segments[1:], value)
		if err != nil {
			return nil, err
		}
		current[index] = updated
		return current, nil
	default:
		return nil, fmt.Errorf("cannot traverse scalar value at segment %q", segments[0])
	}
}

func parsePointer(pointer string) ([]string, error) {
	if pointer == "" || pointer[0] != '/' {
		return nil, errors.New("JSON Pointer must be non-empty and start with /")
	}
	raw := strings.Split(pointer[1:], "/")
	segments := make([]string, len(raw))
	for index, segment := range raw {
		decoded, err := unescapePointer(segment)
		if err != nil {
			return nil, err
		}
		segments[index] = decoded
	}
	return segments, nil
}

func escapePointer(value string) string {
	return strings.ReplaceAll(strings.ReplaceAll(value, "~", "~0"), "/", "~1")
}

func unescapePointer(value string) (string, error) {
	var result strings.Builder
	for index := 0; index < len(value); index++ {
		if value[index] != '~' {
			result.WriteByte(value[index])
			continue
		}
		if index+1 >= len(value) || (value[index+1] != '0' && value[index+1] != '1') {
			return "", fmt.Errorf("invalid JSON Pointer escape in %q", value)
		}
		index++
		if value[index] == '0' {
			result.WriteByte('~')
		} else {
			result.WriteByte('/')
		}
	}
	return result.String(), nil
}

func rejectReservedRoots(values map[string]any) error {
	for _, root := range []string{"mdoc", "page"} {
		if _, exists := values[root]; exists {
			return fmt.Errorf("reserved root %q is read only", root)
		}
	}
	return nil
}

func ensureEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err != nil {
			return fmt.Errorf("invalid trailing JSON: %w", err)
		}
		return errors.New("JSON value must contain exactly one value")
	}
	return nil
}

func canonicalRoots(roots []string) ([]string, error) {
	result := make([]string, 0, len(roots))
	for _, root := range roots {
		canonical, err := canonicalCandidate(root)
		if err != nil {
			return nil, fmt.Errorf("resolve approved root %q: %w", root, err)
		}
		result = append(result, canonical)
	}
	return result, nil
}

func canonicalCandidate(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	candidate := absolute
	suffix := []string{}
	for {
		resolved, resolveErr := filepath.EvalSymlinks(candidate)
		if resolveErr == nil {
			return filepath.Join(append([]string{resolved}, suffix...)...), nil
		}
		parent := filepath.Dir(candidate)
		if parent == candidate {
			return "", resolveErr
		}
		suffix = append([]string{filepath.Base(candidate)}, suffix...)
		candidate = parent
	}
}

func withinAny(path string, roots []string) bool {
	for _, root := range roots {
		relative, err := filepath.Rel(root, path)
		if err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

type hashPart struct {
	name string
	data []byte
}

func hashFiles(files []FileInput) string {
	parts := make([]hashPart, 0, len(files)*2)
	for index, file := range files {
		parts = append(parts, hashPart{fmt.Sprintf("file:%d:path", index), []byte(file.Identity)}, hashPart{fmt.Sprintf("file:%d:data", index), file.Data})
	}
	return hashParts(parts)
}

func hashParts(parts []hashPart) string {
	hash := sha256.New()
	for _, part := range parts {
		fmt.Fprintf(hash, "%d:%s:%d:", len(part.name), part.name, len(part.data))
		_, _ = hash.Write(part.data)
	}
	return fmt.Sprintf("sha256:%x", hash.Sum(nil))
}

func digest(data []byte) string {
	sum := sha256.Sum256(data)
	return fmt.Sprintf("sha256:%x", sum[:])
}
