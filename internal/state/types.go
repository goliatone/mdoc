package state

import "time"

const CurrentVersion = 5
const CurrentJournalVersion = 5
const CurrentRemapJournalVersion = 2

type State struct {
	Version     int                 `json:"version"`
	WorkspaceID string              `json:"workspace_id"`
	Profile     string              `json:"profile"`
	Account     string              `json:"account,omitempty"`
	Folders     Folders             `json:"folders"`
	Targets     map[string]Document `json:"targets"`
	Documents   map[string]Document `json:"-"`
}

type Folders struct {
	StagingID string `json:"staging_id,omitempty"`
	ReviewID  string `json:"review_id,omitempty"`
}

type Document struct {
	TargetKey       string   `json:"target_key"`
	PublicationID   string   `json:"publication_id,omitempty"`
	PublicationKind string   `json:"publication_kind"`
	Source          string   `json:"source,omitempty"`
	Members         []string `json:"members,omitempty"`
	SourceHash      string   `json:"source_hash"`
	RenderHash      string   `json:"render_hash"`
	ActiveTarget    *Target  `json:"active_target,omitempty"`
	PriorTargets    []Target `json:"prior_targets,omitempty"`
}

type Target struct {
	FileID         string             `json:"file_id"`
	URL            string             `json:"url"`
	ReviewSetID    string             `json:"review_set_id"`
	Generation     int                `json:"generation"`
	OperationID    string             `json:"operation_id"`
	PublishStatus  string             `json:"publish_status"`
	RemoteVersion  string             `json:"remote_version"`
	DocsRevision   string             `json:"docs_revision"`
	ModifiedTime   time.Time          `json:"modified_time"`
	PublishedAt    time.Time          `json:"published_at"`
	ReviewSnapshot *ReviewSnapshotRef `json:"review_snapshot,omitempty"`
}

type ReviewSnapshotRef struct {
	Version           int    `json:"version"`
	ManifestHash      string `json:"manifest_hash"`
	BaselineHash      string `json:"baseline_hash"`
	CapturedRevision  string `json:"captured_revision"`
	ActivatedRevision string `json:"activated_revision"`
}

type Stage string

const (
	StagePlanned         Stage = "planned"
	StageCreating        Stage = "creating"
	StageCreated         Stage = "created"
	StageFixingLinks     Stage = "fixing_links"
	StageLinksFixed      Stage = "links_fixed"
	StageCapturingReview Stage = "capturing_review"
	StageReviewCaptured  Stage = "review_captured"
	StageMarkingReady    Stage = "marking_ready"
	StageReady           Stage = "ready"
	StageMoving          Stage = "moving"
	StageMoved           Stage = "moved"
	StageSealingReview   Stage = "sealing_review"
	StageReviewSealed    Stage = "review_sealed"
	StageCommitting      Stage = "committing"
	StageCommitted       Stage = "committed"
)

type Journal struct {
	Version      int                     `json:"version"`
	Kind         string                  `json:"kind"`
	WorkspaceID  string                  `json:"workspace_id"`
	Profile      string                  `json:"profile"`
	OperationID  string                  `json:"operation_id"`
	ReviewSetID  string                  `json:"review_set_id"`
	StagingID    string                  `json:"staging_id"`
	ReviewID     string                  `json:"review_id"`
	KnownTargets map[string]string       `json:"known_targets,omitempty"`
	Sources      []string                `json:"sources"`
	Entries      map[string]JournalEntry `json:"entries"`
	UpdatedAt    time.Time               `json:"updated_at"`
}

type JournalLink struct {
	OriginalTarget string `json:"original_target"`
	Placeholder    string `json:"placeholder"`
	TargetSource   string `json:"target_source"`
	HadFragment    bool   `json:"had_fragment,omitempty"`
}

type JournalEntry struct {
	SourceKey           string           `json:"source_key"`
	FileID              string           `json:"file_id,omitempty"`
	Generation          int              `json:"generation"`
	SourceHash          string           `json:"source_hash"`
	RenderHash          string           `json:"render_hash"`
	DOCXHash            string           `json:"docx_hash"`
	ResolvedFields      map[string]any   `json:"resolved_fields,omitempty"`
	FieldsHash          string           `json:"fields_hash,omitempty"`
	FieldFileHashes     []string         `json:"field_file_hashes,omitempty"`
	ComputedValues      map[string]any   `json:"computed_values,omitempty"`
	ComputedHash        string           `json:"computed_hash,omitempty"`
	ArtifactHash        string           `json:"artifact_hash,omitempty"`
	HashParts           []HashPartDigest `json:"hash_parts,omitempty"`
	PublicationID       string           `json:"publication_id,omitempty"`
	PublicationKind     string           `json:"publication_kind,omitempty"`
	Members             []JournalMember  `json:"members,omitempty"`
	LayoutHash          string           `json:"layout_hash,omitempty"`
	TopologyHash        string           `json:"topology_hash,omitempty"`
	Links               []JournalLink    `json:"links,omitempty"`
	ReviewPullEnabled   bool             `json:"review_pull_enabled,omitempty"`
	ReviewReader        string           `json:"review_reader,omitempty"`
	ReviewCandidateHash string           `json:"review_candidate_hash,omitempty"`
	ReviewSealedHash    string           `json:"review_sealed_hash,omitempty"`
	Stage               Stage            `json:"stage"`
}

type JournalMember struct {
	SourceKey  string `json:"source_key"`
	SourceHash string `json:"source_hash"`
}

type HashPartDigest struct {
	Name string `json:"name"`
	Hash string `json:"hash"`
}

type RemapStage string

const (
	RemapStagePlanned  RemapStage = "planned"
	RemapStageUpdating RemapStage = "updating"
	RemapStageUpdated  RemapStage = "updated"
)

type RemapJournal struct {
	Version     int                          `json:"version"`
	OperationID string                       `json:"operation_id"`
	WorkspaceID string                       `json:"workspace_id"`
	Profile     string                       `json:"profile"`
	From        string                       `json:"from"`
	To          string                       `json:"to"`
	Targets     []string                     `json:"targets"`
	Entries     map[string]RemapJournalEntry `json:"entries"`
	UpdatedAt   time.Time                    `json:"updated_at"`
}

type RemapJournalEntry struct {
	FileID     string     `json:"file_id"`
	Generation int        `json:"generation"`
	Stage      RemapStage `json:"stage"`
}

func New(workspaceID, profile string) *State {
	return &State{Version: CurrentVersion, WorkspaceID: workspaceID, Profile: profile, Targets: map[string]Document{}, Documents: map[string]Document{}}
}
