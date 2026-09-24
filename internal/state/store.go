package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/goliatone/mdoc/internal/sourcekey"
	"golang.org/x/sys/unix"
)

var (
	ErrNotFound = errors.New("publish state not found")
	ErrDamaged  = errors.New("publish state is damaged")
)

type Store struct {
	path     string
	lockFile *os.File
}

func DefaultPath(workspaceID, profile string) (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("find user config directory: %w", err)
	}
	if !safeSegment(workspaceID) || !safeSegment(profile) {
		return "", errors.New("workspace and profile must be safe path segments")
	}
	return filepath.Join(base, "mdoc", "workspaces", workspaceID, "profiles", profile, "state.json"), nil
}

func NewStore(path string) *Store         { return &Store{path: path} }
func (s *Store) Path() string             { return s.path }
func (s *Store) BackupPath() string       { return s.path + ".bak" }
func (s *Store) PreV4BackupPath() string  { return s.path + ".pre-v4.bak" }
func (s *Store) JournalPath() string      { return s.path + ".journal" }
func (s *Store) RemapJournalPath() string { return s.path + ".remap-journal" }

func (s *Store) Acquire() error {
	if s.lockFile != nil {
		return errors.New("state lock is already held by this store")
	}
	if err := ensurePrivateDir(filepath.Dir(s.path)); err != nil {
		return err
	}
	lockPath := s.path + ".lock"
	file, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("open state lock: %w", err)
	}
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		file.Close()
		if errors.Is(err, unix.EWOULDBLOCK) {
			return errors.New("another mdoc command is using this workspace and profile")
		}
		return fmt.Errorf("acquire state lock: %w", err)
	}
	s.lockFile = file
	return nil
}

func (s *Store) Release() error {
	if s.lockFile == nil {
		return nil
	}
	file := s.lockFile
	s.lockFile = nil
	if err := unix.Flock(int(file.Fd()), unix.LOCK_UN); err != nil {
		file.Close()
		return fmt.Errorf("release state lock: %w", err)
	}
	return file.Close()
}

func (s *Store) Load() (*State, error) {
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("read publish state: %w", err)
	}
	result, err := decode(data)
	if err != nil {
		return nil, fmt.Errorf("%w: %v; run `mdoc state reconcile`", ErrDamaged, err)
	}
	return result, nil
}

func decode(data []byte) (*State, error) {
	var raw struct {
		Version int `json:"version"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	if raw.Version < 1 || raw.Version > CurrentVersion {
		return nil, fmt.Errorf("unsupported state version %d", raw.Version)
	}
	var result State
	if raw.Version <= 3 {
		var legacy struct {
			Version     int                 `json:"version"`
			WorkspaceID string              `json:"workspace_id"`
			Profile     string              `json:"profile"`
			Account     string              `json:"account"`
			Folders     Folders             `json:"folders"`
			Documents   map[string]Document `json:"documents"`
		}
		if err := json.Unmarshal(data, &legacy); err != nil {
			return nil, err
		}
		result = State{Version: legacy.Version, WorkspaceID: legacy.WorkspaceID, Profile: legacy.Profile, Account: legacy.Account, Folders: legacy.Folders, Documents: legacy.Documents}
	} else if err := json.Unmarshal(data, &result); err != nil {
		return nil, err
	}
	for result.Version < CurrentVersion {
		switch result.Version {
		case 1:
			result.Version = 2
		case 2:
			if err := migrateStateSourceKeys(&result); err != nil {
				return nil, err
			}
			result.Version = 3
		case 3:
			if err := migrateStateTargets(&result); err != nil {
				return nil, err
			}
			result.Version = 4
		case 4:
			// Existing targets have no review snapshot. Review pull remains unavailable
			// until a later review-enabled publish records one.
			result.Version = 5
		default:
			return nil, fmt.Errorf("no migration from state version %d", result.Version)
		}
	}
	populateLegacyDocuments(&result)
	if err := result.Validate(); err != nil {
		return nil, err
	}
	return &result, nil
}

func (s *Store) Save(value *State) error {
	if s.lockFile == nil {
		return errors.New("state lock must be held before saving")
	}
	value.Version = CurrentVersion
	if err := synchronizeTargets(value); err != nil {
		return err
	}
	if err := value.Validate(); err != nil {
		return err
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("encode publish state: %w", err)
	}
	data = append(data, '\n')
	if current, err := os.ReadFile(s.path); err == nil {
		var raw struct {
			Version int `json:"version"`
		}
		if json.Unmarshal(current, &raw) == nil && raw.Version > 0 && raw.Version < 4 {
			if _, backupErr := os.Stat(s.PreV4BackupPath()); errors.Is(backupErr, os.ErrNotExist) {
				if err := atomicWrite(s.PreV4BackupPath(), current, 0o600); err != nil {
					return fmt.Errorf("save pre-v4 state backup: %w", err)
				}
			} else if backupErr != nil {
				return fmt.Errorf("check pre-v4 state backup: %w", backupErr)
			}
		}
		if _, decodeErr := decode(current); decodeErr == nil {
			if err := atomicWrite(s.BackupPath(), current, 0o600); err != nil {
				return fmt.Errorf("save state backup: %w", err)
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read current state for backup: %w", err)
	}
	if err := atomicWrite(s.path, data, 0o600); err != nil {
		return fmt.Errorf("save publish state: %w", err)
	}
	return nil
}

func (s *Store) WriteJournal(journal *Journal) error {
	if s.lockFile == nil {
		return errors.New("state lock must be held before writing the journal")
	}
	if journal.Version == 0 {
		journal.Version = CurrentJournalVersion
	}
	if journal.Kind == "" {
		journal.Kind = "publish"
	}
	journal.UpdatedAt = time.Now().UTC()
	if err := journal.Validate(); err != nil {
		return err
	}
	data, err := json.MarshalIndent(journal, "", "  ")
	if err != nil {
		return fmt.Errorf("encode operation journal: %w", err)
	}
	return atomicWrite(s.JournalPath(), append(data, '\n'), 0o600)
}

func (s *Store) AdvanceJournal(journal *Journal, source string, next Stage, fileID string) error {
	entry, ok := journal.Entries[source]
	if !ok {
		return fmt.Errorf("source %q is not part of operation %s", source, journal.OperationID)
	}
	if !validTransition(entry.Stage, next) {
		return fmt.Errorf("invalid journal transition for %q: %s to %s", source, entry.Stage, next)
	}
	entry.Stage = next
	if fileID != "" {
		entry.FileID = fileID
	}
	journal.Entries[source] = entry
	return s.WriteJournal(journal)
}

func (s *Store) AdvanceReviewJournal(journal *Journal, source string, next Stage, manifestHash string) error {
	entry, ok := journal.Entries[source]
	if !ok {
		return fmt.Errorf("source %q is not part of operation %s", source, journal.OperationID)
	}
	if !entry.ReviewPullEnabled || !validTransition(entry.Stage, next) || !validSHA256(manifestHash) {
		return fmt.Errorf("invalid review snapshot journal transition for %q: %s to %s", source, entry.Stage, next)
	}
	entry.Stage = next
	switch next {
	case StageReviewCaptured:
		entry.ReviewCandidateHash = manifestHash
	case StageReviewSealed:
		entry.ReviewSealedHash = manifestHash
	default:
		return fmt.Errorf("stage %s cannot record a review snapshot hash", next)
	}
	journal.Entries[source] = entry
	return s.WriteJournal(journal)
}

func (s *Store) LoadJournal() (*Journal, error) {
	data, err := os.ReadFile(s.JournalPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("read operation journal: %w", err)
	}
	var journal Journal
	if err := json.Unmarshal(data, &journal); err != nil {
		return nil, fmt.Errorf("decode operation journal: %w", err)
	}
	if journal.Version == 2 {
		if err := migrateJournalSourceKeys(&journal); err != nil {
			return nil, fmt.Errorf("migrate operation journal: %w", err)
		}
		journal.Version = 3
	}
	if journal.Version == 3 {
		if err := migrateJournalTargets(&journal); err != nil {
			return nil, fmt.Errorf("migrate operation journal targets: %w", err)
		}
		journal.Version = CurrentJournalVersion
	}
	if journal.Version == 4 {
		journal.Version = CurrentJournalVersion
	}
	if journal.Version != CurrentJournalVersion {
		return nil, fmt.Errorf("unsupported operation journal version %d; run `mdoc state reconcile --abandon-operation` before publishing", journal.Version)
	}
	if err := journal.Validate(); err != nil {
		return nil, err
	}
	return &journal, nil
}

func (s *Store) ClearJournal() error {
	if s.lockFile == nil {
		return errors.New("state lock must be held before clearing the journal")
	}
	if err := os.Remove(s.JournalPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove operation journal: %w", err)
	}
	return syncDir(filepath.Dir(s.path))
}

func (s *Store) WriteRemapJournal(journal *RemapJournal) error {
	if s.lockFile == nil {
		return errors.New("state lock must be held before writing the remap journal")
	}
	if journal.Version == 0 {
		journal.Version = CurrentRemapJournalVersion
	}
	journal.UpdatedAt = time.Now().UTC()
	if err := journal.Validate(); err != nil {
		return err
	}
	data, err := json.MarshalIndent(journal, "", "  ")
	if err != nil {
		return fmt.Errorf("encode remap journal: %w", err)
	}
	return atomicWrite(s.RemapJournalPath(), append(data, '\n'), 0o600)
}

func (s *Store) AdvanceRemapJournal(journal *RemapJournal, fileID string, next RemapStage) error {
	entry, ok := journal.Entries[fileID]
	if !ok {
		return fmt.Errorf("file %q is not part of the remap", fileID)
	}
	if !validRemapTransition(entry.Stage, next) {
		return fmt.Errorf("invalid remap transition for %q: %s to %s", fileID, entry.Stage, next)
	}
	entry.Stage = next
	journal.Entries[fileID] = entry
	return s.WriteRemapJournal(journal)
}

func (s *Store) LoadRemapJournal() (*RemapJournal, error) {
	data, err := os.ReadFile(s.RemapJournalPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("read remap journal: %w", err)
	}
	var journal RemapJournal
	if err := json.Unmarshal(data, &journal); err != nil {
		return nil, fmt.Errorf("decode remap journal: %w", err)
	}
	if journal.Version == 1 {
		from, err := sourcekey.Normalize(journal.From)
		if err != nil {
			return nil, fmt.Errorf("migrate remap journal from key: %w", err)
		}
		to, err := sourcekey.Normalize(journal.To)
		if err != nil {
			return nil, fmt.Errorf("migrate remap journal to key: %w", err)
		}
		journal.From = from
		journal.To = to
		journal.Version = CurrentRemapJournalVersion
	}
	if err := journal.Validate(); err != nil {
		return nil, err
	}
	return &journal, nil
}

func (s *Store) ClearRemapJournal() error {
	if s.lockFile == nil {
		return errors.New("state lock must be held before clearing the remap journal")
	}
	if err := os.Remove(s.RemapJournalPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove remap journal: %w", err)
	}
	return syncDir(filepath.Dir(s.path))
}

func (s *State) Validate() error {
	if s.Version != CurrentVersion {
		return fmt.Errorf("state version must be %d", CurrentVersion)
	}
	if !safeSegment(s.WorkspaceID) || !safeSegment(s.Profile) {
		return errors.New("state workspace and profile are invalid")
	}
	for source := range s.Documents {
		normalized, err := sourcekey.Normalize(source)
		if err != nil || normalized != source {
			return fmt.Errorf("invalid legacy source key %q", source)
		}
	}
	for key, document := range s.Targets {
		if err := validateTargetKey(key); err != nil {
			return err
		}
		if document.TargetKey != key || document.PublicationKind != "source" && document.PublicationKind != "bundle" {
			return fmt.Errorf("target %q identity is incomplete", key)
		}
		if strings.HasPrefix(key, "publication:") && document.PublicationID == "" {
			return fmt.Errorf("target %q has no publication ID", key)
		}
		if strings.HasPrefix(key, "publication:") && document.PublicationID != strings.TrimPrefix(key, "publication:") {
			return fmt.Errorf("target %q publication ID does not match its key", key)
		}
		if strings.HasPrefix(key, "source:") && document.Source != strings.TrimPrefix(key, "source:") {
			return fmt.Errorf("target %q source does not match its key", key)
		}
		if document.PublicationKind == "source" && document.Source == "" {
			return fmt.Errorf("target %q source publication has no source", key)
		}
		targets := append([]Target(nil), document.PriorTargets...)
		if document.ActiveTarget != nil {
			targets = append(targets, *document.ActiveTarget)
		}
		seen := map[int]string{}
		for _, target := range targets {
			if target.FileID == "" || target.Generation < 1 || target.ReviewSetID == "" || target.OperationID == "" {
				return fmt.Errorf("target %q has an incomplete remote target", key)
			}
			if prior, exists := seen[target.Generation]; exists {
				return fmt.Errorf("target %q generation %d has duplicate files %s and %s", key, target.Generation, prior, target.FileID)
			}
			seen[target.Generation] = target.FileID
			if target.ReviewSnapshot != nil {
				if err := validateReviewSnapshotRef(*target.ReviewSnapshot); err != nil {
					return fmt.Errorf("target %q generation %d review snapshot: %w", key, target.Generation, err)
				}
			}
		}
	}
	return nil
}

func validateReviewSnapshotRef(value ReviewSnapshotRef) error {
	if value.Version != 1 && value.Version != 2 || !validSHA256(value.ManifestHash) || !validSHA256(value.BaselineHash) || value.CapturedRevision == "" || value.ActivatedRevision == "" {
		return errors.New("reference is incomplete")
	}
	return nil
}

func validSHA256(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, char := range value {
		if char < '0' || char > '9' && char < 'a' || char > 'f' {
			return false
		}
	}
	return true
}

func (j *Journal) Validate() error {
	if j.Version != CurrentJournalVersion || j.Kind != "publish" || !safeSegment(j.WorkspaceID) || !safeSegment(j.Profile) || j.OperationID == "" || j.ReviewSetID == "" || j.StagingID == "" || j.ReviewID == "" {
		return errors.New("operation journal identity is incomplete")
	}
	if len(j.Sources) == 0 || len(j.Entries) != len(j.Sources) {
		return errors.New("operation journal sources and entries do not match")
	}
	for source := range j.KnownTargets {
		if !validJournalKey(source) {
			return fmt.Errorf("operation journal known target key %q is not canonical", source)
		}
	}
	sorted := append([]string(nil), j.Sources...)
	sort.Strings(sorted)
	for _, source := range sorted {
		if !validJournalKey(source) {
			return fmt.Errorf("operation journal source key %q is not canonical", source)
		}
		entry, ok := j.Entries[source]
		if !ok || entry.SourceKey != source || entry.Generation < 1 || entry.SourceHash == "" || entry.RenderHash == "" || entry.DOCXHash == "" || !validStage(entry.Stage) {
			return fmt.Errorf("operation journal entry for %q is invalid", source)
		}
		for _, link := range entry.Links {
			if link.Placeholder == "" || !validJournalKey(link.TargetSource) {
				return fmt.Errorf("operation journal link for %q is invalid", source)
			}
		}
		if entry.ReviewPullEnabled {
			if entry.ReviewReader == "" {
				return fmt.Errorf("operation journal review settings for %q are invalid", source)
			}
			if stageRankForValidation(entry.Stage) >= stageRankForValidation(StageReviewCaptured) && !validSHA256(entry.ReviewCandidateHash) {
				return fmt.Errorf("operation journal candidate snapshot for %q is invalid", source)
			}
			if stageRankForValidation(entry.Stage) >= stageRankForValidation(StageReviewSealed) && !validSHA256(entry.ReviewSealedHash) {
				return fmt.Errorf("operation journal sealed snapshot for %q is invalid", source)
			}
		} else if entry.Stage == StageCapturingReview || entry.Stage == StageReviewCaptured || entry.Stage == StageSealingReview || entry.Stage == StageReviewSealed || entry.ReviewReader != "" || entry.ReviewCandidateHash != "" || entry.ReviewSealedHash != "" {
			return fmt.Errorf("operation journal disabled review settings for %q are invalid", source)
		}
	}
	return nil
}

func synchronizeTargets(value *State) error {
	targets := map[string]Document{}
	for key, document := range value.Targets {
		if strings.HasPrefix(key, "publication:") {
			targets[key] = document
		}
	}
	for source, document := range value.Documents {
		normalized, err := sourcekey.Normalize(source)
		if err != nil || normalized != source {
			return fmt.Errorf("invalid legacy source key %q", source)
		}
		key := "source:" + source
		document.TargetKey = key
		document.PublicationKind = "source"
		document.Source = source
		document.PublicationID = ""
		document.Members = nil
		targets[key] = document
	}
	value.Targets = targets
	return nil
}

func migrateStateTargets(value *State) error {
	value.Targets = map[string]Document{}
	for source, document := range value.Documents {
		key := "source:" + source
		if _, exists := value.Targets[key]; exists {
			return fmt.Errorf("target keys collide during migration at %q", key)
		}
		document.TargetKey = key
		document.PublicationKind = "source"
		document.Source = source
		value.Targets[key] = document
	}
	return nil
}

func populateLegacyDocuments(value *State) {
	value.Documents = map[string]Document{}
	for key, document := range value.Targets {
		if strings.HasPrefix(key, "source:") {
			value.Documents[strings.TrimPrefix(key, "source:")] = document
		}
	}
}

func validateTargetKey(key string) error {
	switch {
	case strings.HasPrefix(key, "source:"):
		source := strings.TrimPrefix(key, "source:")
		normalized, err := sourcekey.Normalize(source)
		if err != nil || normalized != source {
			return fmt.Errorf("invalid legacy target key %q", key)
		}
	case strings.HasPrefix(key, "publication:"):
		id := strings.TrimPrefix(key, "publication:")
		if len(id) < 1 || len(id) > 63 || !safeSegment(id) {
			return fmt.Errorf("invalid publication target key %q", key)
		}
	default:
		return fmt.Errorf("invalid target key %q", key)
	}
	return nil
}

func validJournalKey(key string) bool {
	if strings.HasPrefix(key, "source:") || strings.HasPrefix(key, "publication:") {
		return validateTargetKey(key) == nil
	}
	normalized, err := sourcekey.Normalize(key)
	return err == nil && normalized == key
}

func migrateJournalTargets(value *Journal) error {
	sources := make([]string, 0, len(value.Sources))
	entries := make(map[string]JournalEntry, len(value.Entries))
	for _, oldKey := range value.Sources {
		key := "source:" + oldKey
		if _, exists := entries[key]; exists {
			return fmt.Errorf("journal target keys collide at %q", key)
		}
		entry, ok := value.Entries[oldKey]
		if !ok {
			return fmt.Errorf("entry for source %q is missing", oldKey)
		}
		entry.SourceKey = key
		entry.PublicationKind = "source"
		for index := range entry.Links {
			entry.Links[index].TargetSource = "source:" + entry.Links[index].TargetSource
		}
		entries[key] = entry
		sources = append(sources, key)
	}
	known := map[string]string{}
	for key, target := range value.KnownTargets {
		known["source:"+key] = target
	}
	sort.Strings(sources)
	value.Sources = sources
	value.Entries = entries
	value.KnownTargets = known
	return nil
}

func (j *RemapJournal) Validate() error {
	if j.Version != CurrentRemapJournalVersion || j.OperationID == "" || !safeSegment(j.WorkspaceID) || !safeSegment(j.Profile) || j.From == "" || j.To == "" || j.From == j.To {
		return errors.New("remap journal identity is incomplete")
	}
	from, fromErr := sourcekey.Normalize(j.From)
	to, toErr := sourcekey.Normalize(j.To)
	if fromErr != nil || toErr != nil || from != j.From || to != j.To {
		return errors.New("remap journal source keys are not canonical")
	}
	if len(j.Targets) == 0 || len(j.Entries) != len(j.Targets) {
		return errors.New("remap journal targets and entries do not match")
	}
	seen := map[string]bool{}
	for _, fileID := range j.Targets {
		entry, ok := j.Entries[fileID]
		if seen[fileID] || !ok || entry.FileID != fileID || entry.Generation < 1 || !validRemapStage(entry.Stage) {
			return fmt.Errorf("remap journal entry for %q is invalid", fileID)
		}
		seen[fileID] = true
	}
	return nil
}

func migrateStateSourceKeys(value *State) error {
	documents := make(map[string]Document, len(value.Documents))
	origins := make(map[string]string, len(value.Documents))
	for oldKey, document := range value.Documents {
		newKey, err := sourcekey.Normalize(oldKey)
		if err != nil {
			return fmt.Errorf("migrate source key %q: %w", oldKey, err)
		}
		if _, exists := documents[newKey]; exists {
			return fmt.Errorf("source keys collide after canonicalization: %q and %q", origins[newKey], oldKey)
		}
		documents[newKey] = document
		origins[newKey] = oldKey
	}
	value.Documents = documents
	return nil
}

func migrateJournalSourceKeys(value *Journal) error {
	sources := make([]string, 0, len(value.Sources))
	entries := make(map[string]JournalEntry, len(value.Entries))
	origins := make(map[string]string, len(value.Entries))
	for _, oldKey := range value.Sources {
		newKey, err := sourcekey.Normalize(oldKey)
		if err != nil {
			return fmt.Errorf("source key %q: %w", oldKey, err)
		}
		entry, ok := value.Entries[oldKey]
		if !ok {
			return fmt.Errorf("entry for source %q is missing", oldKey)
		}
		if _, exists := entries[newKey]; exists {
			return fmt.Errorf("source keys collide after canonicalization: %q and %q", origins[newKey], oldKey)
		}
		entry.SourceKey = newKey
		for index := range entry.Links {
			target, err := sourcekey.Normalize(entry.Links[index].TargetSource)
			if err != nil {
				return fmt.Errorf("link target %q: %w", entry.Links[index].TargetSource, err)
			}
			entry.Links[index].TargetSource = target
		}
		sources = append(sources, newKey)
		entries[newKey] = entry
		origins[newKey] = oldKey
	}
	knownTargets := make(map[string]string, len(value.KnownTargets))
	knownOrigins := make(map[string]string, len(value.KnownTargets))
	for oldKey, fileID := range value.KnownTargets {
		newKey, err := sourcekey.Normalize(oldKey)
		if err != nil {
			return fmt.Errorf("known target key %q: %w", oldKey, err)
		}
		if _, exists := knownTargets[newKey]; exists {
			return fmt.Errorf("known target keys collide after canonicalization: %q and %q", knownOrigins[newKey], oldKey)
		}
		knownTargets[newKey] = fileID
		knownOrigins[newKey] = oldKey
	}
	sort.Strings(sources)
	value.Sources = sources
	value.Entries = entries
	value.KnownTargets = knownTargets
	return nil
}

func validRemapStage(stage RemapStage) bool {
	return stage == RemapStagePlanned || stage == RemapStageUpdating || stage == RemapStageUpdated
}

func validRemapTransition(current, next RemapStage) bool {
	order := map[RemapStage]int{RemapStagePlanned: 0, RemapStageUpdating: 1, RemapStageUpdated: 2}
	currentOrder, currentOK := order[current]
	nextOrder, nextOK := order[next]
	return currentOK && nextOK && (nextOrder == currentOrder || nextOrder == currentOrder+1)
}

func validStage(stage Stage) bool {
	switch stage {
	case StagePlanned, StageCreating, StageCreated, StageFixingLinks, StageLinksFixed, StageCapturingReview, StageReviewCaptured, StageMarkingReady, StageReady, StageMoving, StageMoved, StageSealingReview, StageReviewSealed, StageCommitting, StageCommitted:
		return true
	default:
		return false
	}
}

func validTransition(current, next Stage) bool {
	order := map[Stage]int{
		StagePlanned: 0, StageCreating: 1, StageCreated: 2, StageFixingLinks: 3,
		StageLinksFixed: 4, StageCapturingReview: 5, StageReviewCaptured: 6,
		StageMarkingReady: 7, StageReady: 8, StageMoving: 9, StageMoved: 10,
		StageSealingReview: 11, StageReviewSealed: 12, StageCommitting: 13, StageCommitted: 14,
	}
	currentOrder, currentOK := order[current]
	nextOrder, nextOK := order[next]
	if !currentOK || !nextOK {
		return false
	}
	if nextOrder == currentOrder || nextOrder == currentOrder+1 {
		return true
	}
	return current == StageLinksFixed && next == StageMarkingReady || current == StageMoved && next == StageCommitting
}

func stageRankForValidation(stage Stage) int {
	order := []Stage{StagePlanned, StageCreating, StageCreated, StageFixingLinks, StageLinksFixed, StageCapturingReview, StageReviewCaptured, StageMarkingReady, StageReady, StageMoving, StageMoved, StageSealingReview, StageReviewSealed, StageCommitting, StageCommitted}
	for index, candidate := range order {
		if stage == candidate {
			return index
		}
	}
	return -1
}

func atomicWrite(path string, data []byte, mode fs.FileMode) error {
	return atomicWriteWithHook(path, data, mode, nil)
}

func atomicWriteWithHook(path string, data []byte, mode fs.FileMode, beforeRename func(string) error) error {
	if err := ensurePrivateDir(filepath.Dir(path)); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".mdoc-state-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(mode); err != nil {
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
	return syncDir(filepath.Dir(path))
}

func ensurePrivateDir(path string) error {
	info, err := os.Stat(path)
	if err == nil {
		if !info.IsDir() {
			return fmt.Errorf("state parent %s is not a directory", path)
		}
		return nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(path, 0o700); err != nil {
		return err
	}
	return os.Chmod(path, 0o700)
}

func syncDir(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func safeSegment(value string) bool {
	return value != "" && value != "." && value != ".." && !strings.ContainsAny(value, `/\\`)
}
