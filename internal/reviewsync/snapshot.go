package reviewsync

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/goliatone/mdoc/internal/googleapi"
	"github.com/goliatone/mdoc/internal/sourcekey"
)

const SnapshotVersion = 2

var (
	ErrSnapshotNotFound = errors.New("review snapshot not found")
	ErrSnapshotDamaged  = errors.New("review snapshot is damaged")
	ErrSnapshotUnsealed = errors.New("review snapshot is not sealed")
)

type SnapshotStatus string

const (
	SnapshotCandidate SnapshotStatus = "candidate"
	SnapshotSealed    SnapshotStatus = "sealed"
)

type CaptureTuple struct {
	FileID       string                  `json:"file_id"`
	DriveVersion string                  `json:"drive_version"`
	DocsRevision string                  `json:"docs_revision"`
	Tabs         []googleapi.TabTopology `json:"tabs"`
}

type ActivationSeal struct {
	FileID        string                  `json:"file_id"`
	DriveVersion  string                  `json:"drive_version"`
	DocsRevision  string                  `json:"docs_revision"`
	ReviewParent  string                  `json:"review_parent"`
	ReadyMetadata map[string]string       `json:"ready_metadata"`
	Tabs          []googleapi.TabTopology `json:"tabs"`
	ActivatedAt   time.Time               `json:"activated_at"`
}

type SnapshotMember struct {
	SourceKey    string `json:"source_key"`
	SourceHash   string `json:"source_hash"`
	SnapshotHash string `json:"snapshot_hash"`
	ByteCount    int    `json:"byte_count"`
	File         string `json:"file"`
}

type SnapshotManifest struct {
	Version         int              `json:"version"`
	Status          SnapshotStatus   `json:"status"`
	WorkspaceID     string           `json:"workspace_id"`
	Profile         string           `json:"profile"`
	TargetKey       string           `json:"target_key"`
	PublicationID   string           `json:"publication_id"`
	PublicationKind string           `json:"publication_kind"`
	Reader          string           `json:"reader"`
	FileID          string           `json:"file_id"`
	Generation      int              `json:"generation"`
	ReviewSetID     string           `json:"review_set_id"`
	OperationID     string           `json:"operation_id"`
	Capture         CaptureTuple     `json:"capture"`
	Activation      *ActivationSeal  `json:"activation,omitempty"`
	BaselineHash    string           `json:"baseline_hash"`
	BaselineBytes   int              `json:"baseline_bytes"`
	Members         []SnapshotMember `json:"members"`
	SourceMap       SourceMap        `json:"source_map"`
	CapturedAt      time.Time        `json:"captured_at"`
	CandidateHash   string           `json:"candidate_hash,omitempty"`
	IntegrityHash   string           `json:"integrity_hash"`
}

type SnapshotSource struct {
	SourceKey  string
	SourceHash string
	Content    []byte
}

type CandidateInput struct {
	WorkspaceID     string
	Profile         string
	TargetKey       string
	PublicationID   string
	PublicationKind string
	Reader          string
	FileID          string
	Generation      int
	ReviewSetID     string
	OperationID     string
	Capture         CaptureTuple
	Baseline        []byte
	Sources         []SnapshotSource
	SourceMap       SourceMap
	CapturedAt      time.Time
}

type LoadedSnapshot struct {
	Manifest SnapshotManifest
	Baseline []byte
	Sources  map[string][]byte
}

type SnapshotReference struct {
	TargetKey    string
	Generation   int
	ManifestHash string
}

type OrphanSnapshot struct {
	Path       string
	TargetKey  string
	Generation int
	Reason     string
}

type SnapshotStore struct {
	root         string
	beforeRename func(string) error
}

func NewSnapshotStore(statePath string) (*SnapshotStore, error) {
	if strings.TrimSpace(statePath) == "" {
		return nil, errors.New("effective state path is required")
	}
	absolute, err := filepath.Abs(filepath.Clean(statePath))
	if err != nil {
		return nil, fmt.Errorf("resolve effective state path: %w", err)
	}
	return &SnapshotStore{root: absolute + ".review-snapshots"}, nil
}

func (s *SnapshotStore) Root() string { return s.root }

func (s *SnapshotStore) WriteCandidate(input CandidateInput) (SnapshotManifest, error) {
	manifest, sourceData, err := buildCandidate(input)
	if err != nil {
		return SnapshotManifest{}, err
	}
	generationPath, err := s.generationPath(input.TargetKey, input.Generation)
	if err != nil {
		return SnapshotManifest{}, err
	}
	if _, err := os.Lstat(generationPath); err == nil {
		loaded, loadErr := s.Load(input.TargetKey, input.Generation, false)
		if loadErr != nil {
			return SnapshotManifest{}, loadErr
		}
		if loaded.Manifest.IntegrityHash != manifest.IntegrityHash {
			return SnapshotManifest{}, fmt.Errorf("%w: candidate identity differs from the existing generation", ErrSnapshotDamaged)
		}
		return loaded.Manifest, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return SnapshotManifest{}, fmt.Errorf("inspect snapshot generation: %w", err)
	}
	targetPath := filepath.Dir(generationPath)
	if err := ensurePrivateSnapshotDir(targetPath); err != nil {
		return SnapshotManifest{}, err
	}
	temporary, err := os.MkdirTemp(targetPath, ".candidate-*")
	if err != nil {
		return SnapshotManifest{}, fmt.Errorf("create candidate snapshot directory: %w", err)
	}
	defer os.RemoveAll(temporary)
	if err := os.Chmod(temporary, 0o700); err != nil {
		return SnapshotManifest{}, err
	}
	if err := writeSnapshotFile(filepath.Join(temporary, "baseline.md"), input.Baseline); err != nil {
		return SnapshotManifest{}, err
	}
	if err := ensurePrivateSnapshotDir(filepath.Join(temporary, "sources")); err != nil {
		return SnapshotManifest{}, err
	}
	for _, member := range manifest.Members {
		if err := writeSnapshotFile(filepath.Join(temporary, "sources", member.File), sourceData[member.SourceKey]); err != nil {
			return SnapshotManifest{}, err
		}
	}
	manifestData, err := encodeManifest(manifest)
	if err != nil {
		return SnapshotManifest{}, err
	}
	if err := writeSnapshotFile(filepath.Join(temporary, "manifest.json"), manifestData); err != nil {
		return SnapshotManifest{}, err
	}
	if err := syncSnapshotDir(filepath.Join(temporary, "sources")); err != nil {
		return SnapshotManifest{}, err
	}
	if err := syncSnapshotDir(temporary); err != nil {
		return SnapshotManifest{}, err
	}
	if s.beforeRename != nil {
		if err := s.beforeRename(temporary); err != nil {
			return SnapshotManifest{}, err
		}
	}
	if err := os.Rename(temporary, generationPath); err != nil {
		return SnapshotManifest{}, fmt.Errorf("activate candidate snapshot: %w", err)
	}
	if err := syncSnapshotDir(targetPath); err != nil {
		return SnapshotManifest{}, err
	}
	return manifest, nil
}

func (s *SnapshotStore) Seal(targetKey string, generation int, seal ActivationSeal) (SnapshotManifest, error) {
	seal.ActivatedAt = seal.ActivatedAt.UTC()
	seal.Tabs = append([]googleapi.TabTopology(nil), seal.Tabs...)
	seal.ReadyMetadata = cloneStringMap(seal.ReadyMetadata)
	loaded, err := s.Load(targetKey, generation, false)
	if err != nil {
		return SnapshotManifest{}, err
	}
	manifest := loaded.Manifest
	if manifest.Status == SnapshotSealed {
		if manifest.Activation != nil && activationEqual(*manifest.Activation, seal) {
			return manifest, nil
		}
		return SnapshotManifest{}, fmt.Errorf("%w: generation is already sealed with another activation", ErrSnapshotDamaged)
	}
	if err := validateActivation(manifest, seal); err != nil {
		return SnapshotManifest{}, err
	}
	manifest.Status = SnapshotSealed
	manifest.Activation = &seal
	manifest.CandidateHash = manifest.IntegrityHash
	manifest.IntegrityHash = ""
	manifest.IntegrityHash, err = manifestHash(manifest)
	if err != nil {
		return SnapshotManifest{}, err
	}
	data, err := encodeManifest(manifest)
	if err != nil {
		return SnapshotManifest{}, err
	}
	path, _ := s.generationPath(targetKey, generation)
	if err := atomicSnapshotWrite(filepath.Join(path, "manifest.json"), data, s.beforeRename); err != nil {
		return SnapshotManifest{}, err
	}
	return manifest, nil
}

func (s *SnapshotStore) Load(targetKey string, generation int, requireSealed bool) (LoadedSnapshot, error) {
	path, err := s.generationPath(targetKey, generation)
	if err != nil {
		return LoadedSnapshot{}, err
	}
	for _, directory := range []string{s.root, filepath.Dir(path), path} {
		if err := validatePrivateSnapshotDir(directory); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return LoadedSnapshot{}, ErrSnapshotNotFound
			}
			return LoadedSnapshot{}, fmt.Errorf("%w: %v", ErrSnapshotDamaged, err)
		}
	}
	return loadSnapshotAt(path, targetKey, generation, requireSealed)
}

func (s *SnapshotStore) Orphans(references []SnapshotReference) ([]OrphanSnapshot, error) {
	expected := map[string]string{}
	for _, reference := range references {
		path, err := s.generationPath(reference.TargetKey, reference.Generation)
		if err != nil {
			return nil, err
		}
		expected[path] = reference.ManifestHash
	}
	if err := validatePrivateSnapshotDir(s.root); errors.Is(err, os.ErrNotExist) {
		return nil, nil
	} else if err != nil {
		return nil, fmt.Errorf("inspect snapshot root: %w", err)
	}
	entries, err := os.ReadDir(s.root)
	if err != nil {
		return nil, fmt.Errorf("read snapshot root: %w", err)
	}
	result := []OrphanSnapshot{}
	for _, targetEntry := range entries {
		targetPath := filepath.Join(s.root, targetEntry.Name())
		if !targetEntry.IsDir() || targetEntry.Type()&os.ModeSymlink != 0 {
			result = append(result, OrphanSnapshot{Path: targetPath, Reason: "unexpected snapshot root entry"})
			continue
		}
		if err := validatePrivateSnapshotDir(targetPath); err != nil {
			result = append(result, OrphanSnapshot{Path: targetPath, Reason: "unsafe snapshot target directory"})
			continue
		}
		generations, readErr := os.ReadDir(targetPath)
		if readErr != nil {
			return nil, readErr
		}
		for _, generationEntry := range generations {
			generationPath := filepath.Join(targetPath, generationEntry.Name())
			if !generationEntry.IsDir() || generationEntry.Type()&os.ModeSymlink != 0 {
				result = append(result, OrphanSnapshot{Path: generationPath, Reason: "unexpected target entry"})
				continue
			}
			if err := validatePrivateSnapshotDir(generationPath); err != nil {
				result = append(result, OrphanSnapshot{Path: generationPath, Reason: "unsafe snapshot generation directory"})
				continue
			}
			manifest, readErr := readManifestFile(filepath.Join(generationPath, "manifest.json"))
			if readErr != nil {
				result = append(result, OrphanSnapshot{Path: generationPath, Reason: "missing or damaged manifest"})
				continue
			}
			expectedHash, referenced := expected[generationPath]
			if !referenced {
				result = append(result, OrphanSnapshot{Path: generationPath, TargetKey: manifest.TargetKey, Generation: manifest.Generation, Reason: "not referenced by state"})
			} else if expectedHash != manifest.IntegrityHash {
				result = append(result, OrphanSnapshot{Path: generationPath, TargetKey: manifest.TargetKey, Generation: manifest.Generation, Reason: "state manifest hash mismatch"})
			}
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Path < result[j].Path })
	return result, nil
}

func buildCandidate(input CandidateInput) (SnapshotManifest, map[string][]byte, error) {
	if input.WorkspaceID == "" || input.Profile == "" || input.TargetKey == "" || input.PublicationID == "" || input.Reader == "" || input.FileID == "" || input.Generation < 1 || input.ReviewSetID == "" || input.OperationID == "" {
		return SnapshotManifest{}, nil, errors.New("candidate snapshot identity is incomplete")
	}
	if input.PublicationKind != "source" && input.PublicationKind != "bundle" {
		return SnapshotManifest{}, nil, errors.New("candidate publication kind is invalid")
	}
	if input.Capture.FileID != input.FileID || input.Capture.DriveVersion == "" || input.Capture.DocsRevision == "" {
		return SnapshotManifest{}, nil, errors.New("candidate capture tuple is incomplete")
	}
	if err := googleapi.ValidateSingleRootTopology(input.Capture.Tabs); err != nil {
		return SnapshotManifest{}, nil, err
	}
	if len(input.Baseline) == 0 || len(input.Sources) == 0 || input.SourceMap.Version != SourceMapVersion || len(input.SourceMap.Entries) == 0 {
		return SnapshotManifest{}, nil, errors.New("candidate snapshot content is incomplete")
	}
	if err := validateSourceMapIdentity(input.SourceMap); err != nil {
		return SnapshotManifest{}, nil, fmt.Errorf("candidate source map: %w", err)
	}
	capturedAt := input.CapturedAt.UTC()
	if capturedAt.IsZero() {
		return SnapshotManifest{}, nil, errors.New("candidate capture time is required")
	}
	members := make([]SnapshotMember, 0, len(input.Sources))
	sourceData := make(map[string][]byte, len(input.Sources))
	for _, source := range input.Sources {
		if source.SourceKey == "" || len(source.Content) == 0 {
			return SnapshotManifest{}, nil, errors.New("candidate source snapshot is incomplete")
		}
		normalized, normalizeErr := sourcekey.Normalize(source.SourceKey)
		if normalizeErr != nil || normalized != source.SourceKey {
			return SnapshotManifest{}, nil, fmt.Errorf("candidate source key %q is not canonical", source.SourceKey)
		}
		if _, duplicate := sourceData[source.SourceKey]; duplicate {
			return SnapshotManifest{}, nil, fmt.Errorf("duplicate snapshot source %q", source.SourceKey)
		}
		snapshotHash := hashBytes(source.Content)
		sourceHash := source.SourceHash
		if sourceHash == "" {
			sourceHash = snapshotHash
		}
		file := hashString(source.SourceKey) + ".md"
		members = append(members, SnapshotMember{SourceKey: source.SourceKey, SourceHash: sourceHash, SnapshotHash: snapshotHash, ByteCount: len(source.Content), File: file})
		sourceData[source.SourceKey] = append([]byte(nil), source.Content...)
	}
	manifest := SnapshotManifest{
		Version: SnapshotVersion, Status: SnapshotCandidate, WorkspaceID: input.WorkspaceID, Profile: input.Profile,
		TargetKey: input.TargetKey, PublicationID: input.PublicationID, PublicationKind: input.PublicationKind, Reader: input.Reader,
		FileID: input.FileID, Generation: input.Generation, ReviewSetID: input.ReviewSetID, OperationID: input.OperationID,
		Capture: input.Capture, BaselineHash: hashBytes(input.Baseline), BaselineBytes: len(input.Baseline),
		Members: members, SourceMap: input.SourceMap, CapturedAt: capturedAt,
	}
	manifest.Capture.Tabs = append([]googleapi.TabTopology(nil), input.Capture.Tabs...)
	var err error
	manifest.IntegrityHash, err = manifestHash(manifest)
	return manifest, sourceData, err
}

func loadSnapshotAt(path, targetKey string, generation int, requireSealed bool) (LoadedSnapshot, error) {
	if err := validatePrivateSnapshotDir(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return LoadedSnapshot{}, ErrSnapshotNotFound
		}
		return LoadedSnapshot{}, fmt.Errorf("%w: %v", ErrSnapshotDamaged, err)
	}
	manifest, err := readManifestFile(filepath.Join(path, "manifest.json"))
	if errors.Is(err, os.ErrNotExist) {
		return LoadedSnapshot{}, ErrSnapshotNotFound
	}
	if err != nil {
		return LoadedSnapshot{}, fmt.Errorf("%w: %v", ErrSnapshotDamaged, err)
	}
	if manifest.TargetKey != targetKey || manifest.Generation != generation {
		return LoadedSnapshot{}, fmt.Errorf("%w: snapshot path identity does not match its manifest", ErrSnapshotDamaged)
	}
	if requireSealed && manifest.Status != SnapshotSealed {
		return LoadedSnapshot{}, ErrSnapshotUnsealed
	}
	baseline, err := readSnapshotFile(filepath.Join(path, "baseline.md"))
	if err != nil || len(baseline) != manifest.BaselineBytes || hashBytes(baseline) != manifest.BaselineHash {
		return LoadedSnapshot{}, fmt.Errorf("%w: baseline content does not match its manifest", ErrSnapshotDamaged)
	}
	sources := make(map[string][]byte, len(manifest.Members))
	memberBytes := make(map[string]int, len(manifest.Members))
	for _, member := range manifest.Members {
		if member.File != hashString(member.SourceKey)+".md" {
			return LoadedSnapshot{}, fmt.Errorf("%w: source file identity is unsafe", ErrSnapshotDamaged)
		}
		content, readErr := readSnapshotFile(filepath.Join(path, "sources", member.File))
		if readErr != nil || len(content) != member.ByteCount || hashBytes(content) != member.SnapshotHash {
			return LoadedSnapshot{}, fmt.Errorf("%w: source %q does not match its manifest", ErrSnapshotDamaged, member.SourceKey)
		}
		sources[member.SourceKey] = content
		memberBytes[member.SourceKey] = member.ByteCount
	}
	if err := validateSourceMapIdentity(manifest.SourceMap); err != nil {
		return LoadedSnapshot{}, fmt.Errorf("%w: %v", ErrSnapshotDamaged, err)
	}
	for _, entry := range manifest.SourceMap.Entries {
		if entry.Generated {
			if entry.Member != "" || entry.Eligible {
				return LoadedSnapshot{}, fmt.Errorf("%w: generated source map entry claims source ownership", ErrSnapshotDamaged)
			}
			continue
		}
		byteCount, exists := memberBytes[entry.Member]
		if !exists || entry.SourceRange.Start < 0 || entry.SourceRange.End < entry.SourceRange.Start || entry.SourceRange.End > byteCount || entry.Eligible && entry.SourceRange.End == entry.SourceRange.Start {
			return LoadedSnapshot{}, fmt.Errorf("%w: source map entry is outside member %q", ErrSnapshotDamaged, entry.Member)
		}
	}
	return LoadedSnapshot{Manifest: manifest, Baseline: baseline, Sources: sources}, nil
}

func validateSourceMapIdentity(sourceMap SourceMap) error {
	if sourceMap.Version != SourceMapVersion || len(sourceMap.Entries) == 0 {
		return errors.New("source map identity is incomplete")
	}
	for index, entry := range sourceMap.Entries {
		if entry.BaselineIndex != index || entry.Kind == "" || entry.BaselineKind == "" || entry.Fingerprint == "" || entry.Signature == "" {
			return fmt.Errorf("source map entry %d identity is incomplete", index)
		}
	}
	return nil
}

func readManifestFile(path string) (SnapshotManifest, error) {
	data, err := readSnapshotFile(path)
	if err != nil {
		return SnapshotManifest{}, err
	}
	var manifest SnapshotManifest
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return SnapshotManifest{}, err
	}
	if err := validateManifest(manifest); err != nil {
		return SnapshotManifest{}, err
	}
	return manifest, nil
}

func validateManifest(manifest SnapshotManifest) error {
	if manifest.Version != SnapshotVersion || manifest.Status != SnapshotCandidate && manifest.Status != SnapshotSealed {
		return errors.New("unsupported snapshot manifest")
	}
	if manifest.WorkspaceID == "" || manifest.Profile == "" || manifest.TargetKey == "" || manifest.PublicationID == "" || manifest.Reader == "" || manifest.FileID == "" || manifest.Generation < 1 || manifest.ReviewSetID == "" || manifest.OperationID == "" || manifest.CapturedAt.IsZero() {
		return errors.New("snapshot manifest identity is incomplete")
	}
	if manifest.Capture.FileID != manifest.FileID || manifest.Capture.DriveVersion == "" || manifest.Capture.DocsRevision == "" {
		return errors.New("snapshot capture tuple is incomplete")
	}
	if err := googleapi.ValidateSingleRootTopology(manifest.Capture.Tabs); err != nil {
		return err
	}
	if manifest.Status == SnapshotCandidate && manifest.Activation != nil || manifest.Status == SnapshotSealed && manifest.Activation == nil {
		return errors.New("snapshot seal does not match status")
	}
	if manifest.Status == SnapshotCandidate && manifest.CandidateHash != "" || manifest.Status == SnapshotSealed && !validHash(manifest.CandidateHash) {
		return errors.New("snapshot candidate hash does not match status")
	}
	if manifest.Activation != nil {
		if err := validateActivation(manifest, *manifest.Activation); err != nil {
			return err
		}
	}
	if !validHash(manifest.BaselineHash) || manifest.BaselineBytes < 1 || len(manifest.Members) == 0 || manifest.SourceMap.Version != SourceMapVersion || len(manifest.SourceMap.Entries) == 0 || !validHash(manifest.IntegrityHash) {
		return errors.New("snapshot manifest content is incomplete")
	}
	seenKeys := map[string]bool{}
	seenFiles := map[string]bool{}
	for _, member := range manifest.Members {
		if member.SourceKey == "" || !validSourceHash(member.SourceHash) || !validHash(member.SnapshotHash) || member.ByteCount < 1 || member.File != hashString(member.SourceKey)+".md" || seenKeys[member.SourceKey] || seenFiles[member.File] {
			return errors.New("snapshot member is invalid")
		}
		seenKeys[member.SourceKey] = true
		seenFiles[member.File] = true
	}
	want, err := manifestHash(manifest)
	if err != nil || want != manifest.IntegrityHash {
		return errors.New("snapshot manifest integrity hash does not match")
	}
	return nil
}

func validateActivation(manifest SnapshotManifest, seal ActivationSeal) error {
	if seal.FileID != manifest.FileID || seal.DriveVersion == "" || seal.DocsRevision != manifest.Capture.DocsRevision || seal.ReviewParent == "" || seal.ActivatedAt.IsZero() || seal.ReadyMetadata["mdoc_status"] != "ready" {
		return errors.New("activation seal does not match the capture tuple")
	}
	if err := googleapi.ValidateSingleRootTopology(seal.Tabs); err != nil {
		return err
	}
	if !sameTabTopology(manifest.Capture.Tabs, seal.Tabs) {
		return errors.New("activation tab topology does not match the capture tuple")
	}
	return nil
}

func (s *SnapshotStore) generationPath(targetKey string, generation int) (string, error) {
	if strings.TrimSpace(targetKey) == "" || generation < 1 {
		return "", errors.New("snapshot target and generation are required")
	}
	return filepath.Join(s.root, hashString(targetKey), strconv.Itoa(generation)), nil
}

func manifestHash(manifest SnapshotManifest) (string, error) {
	manifest.IntegrityHash = ""
	data, err := json.Marshal(manifest)
	if err != nil {
		return "", err
	}
	return hashBytes(data), nil
}

func encodeManifest(manifest SnapshotManifest) ([]byte, error) {
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode snapshot manifest: %w", err)
	}
	return append(data, '\n'), nil
}

func hashBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func hashString(value string) string { return hashBytes([]byte(value)) }

func validHash(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil && value == strings.ToLower(value)
}

func validSourceHash(value string) bool {
	return validHash(value) || strings.HasPrefix(value, "sha256:") && validHash(strings.TrimPrefix(value, "sha256:"))
}

func sameTabTopology(left, right []googleapi.TabTopology) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func activationEqual(left, right ActivationSeal) bool {
	leftData, _ := json.Marshal(left)
	rightData, _ := json.Marshal(right)
	return bytes.Equal(leftData, rightData)
}

func cloneStringMap(input map[string]string) map[string]string {
	result := make(map[string]string, len(input))
	for key, value := range input {
		result[key] = value
	}
	return result
}

func ensurePrivateSnapshotDir(path string) error {
	info, err := os.Lstat(path)
	if err == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("snapshot path %s is not a private directory", path)
		}
		if err := os.Chmod(path, 0o700); err != nil {
			return err
		}
		return nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	parent := filepath.Dir(path)
	if parent != path {
		if _, parentErr := os.Stat(parent); errors.Is(parentErr, os.ErrNotExist) {
			if err := ensurePrivateSnapshotDir(parent); err != nil {
				return err
			}
		}
	}
	if err := os.Mkdir(path, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	return os.Chmod(path, 0o700)
}

func validatePrivateSnapshotDir(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("snapshot generation is not a directory")
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return errors.New("snapshot directory permissions are not private")
	}
	return nil
}

func writeSnapshotFile(path string, data []byte) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	return file.Close()
}

func atomicSnapshotWrite(path string, data []byte, beforeRename func(string) error) error {
	if err := ensurePrivateSnapshotDir(filepath.Dir(path)); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".manifest-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if beforeRename != nil {
		if err := beforeRename(temporaryPath); err != nil {
			return err
		}
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	return syncSnapshotDir(filepath.Dir(path))
}

func readSnapshotFile(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("snapshot file is not regular")
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return nil, errors.New("snapshot file permissions are not private")
	}
	return os.ReadFile(path)
}

func syncSnapshotDir(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil && !errors.Is(err, fs.ErrInvalid) {
		return err
	}
	return nil
}
