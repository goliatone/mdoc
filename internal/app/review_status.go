package app

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/goliatone/mdoc/internal/config"
	"github.com/goliatone/mdoc/internal/document"
	"github.com/goliatone/mdoc/internal/report"
	"github.com/goliatone/mdoc/internal/reviewsync"
	"github.com/goliatone/mdoc/internal/state"
)

const (
	reviewStateDisabled          = "disabled"
	reviewStateBaselineAvailable = "baseline_available"
	reviewStatePullReady         = "pull_ready"
	reviewStateChanged           = "changed"
	reviewStateBlocked           = "blocked"
	reviewStateUnavailable       = "unavailable"
)

func configuredReviewTargetSummaries(profile config.SelectedProfile, selectedPublication string) []report.TargetSummary {
	ids := make([]string, 0, len(profile.Publications))
	for id := range profile.Publications {
		if selectedPublication == "" || id == selectedPublication {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	result := make([]report.TargetSummary, 0, len(ids))
	for _, id := range ids {
		publication := profile.Publications[id]
		result = append(result, report.TargetSummary{Target: "publication:" + id, PublicationID: id, PublicationKind: string(publication.Kind), MemberCount: len(publication.Members)})
	}
	return result
}

func localReviewDiagnostics(cfg *config.Config, profile config.SelectedProfile, publishState *state.State, store *state.Store, targets []report.TargetSummary) ([]report.TargetSummary, []report.ReviewSnapshotOrphan, report.StatusCheck) {
	result := cloneReviewTargets(targets)
	var snapshotStore *reviewsync.SnapshotStore
	var snapshotErr error
	if store != nil {
		snapshotStore, snapshotErr = reviewsync.NewSnapshotStore(store.Path())
	}
	for index := range result {
		target := &result[index]
		enabled := profile.Publications[target.PublicationID].Review.Pull.Enabled
		target.Review = &report.ReviewTargetStatus{PullEnabled: enabled, State: reviewStateDisabled, Message: "review pull is disabled for this publication"}
		if !enabled {
			continue
		}
		if publishState == nil {
			target.Review.State = reviewStateUnavailable
			target.Review.Message = "publish state is unavailable; publish a new generation after state recovery"
			continue
		}
		if publishState.WorkspaceID != cfg.WorkspaceID || publishState.Profile != profile.Name {
			target.Review.State = reviewStateBlocked
			target.Review.Message = "publish state does not match the configured workspace and profile"
			continue
		}
		stored, exists := publishState.Targets[target.Target]
		if !exists || stored.ActiveTarget == nil {
			target.Review.State = reviewStateUnavailable
			target.Review.Message = "no active review target; publish a new generation"
			continue
		}
		active := *stored.ActiveTarget
		if active.ReviewSnapshot == nil {
			target.Review.State = reviewStateUnavailable
			target.Review.Message = "active target has no baseline; publish a new generation"
			continue
		}
		if snapshotErr != nil {
			target.Review.State = reviewStateBlocked
			target.Review.Message = "review snapshot storage is unavailable"
			continue
		}
		snapshot, err := snapshotStore.Load(target.Target, active.Generation, true)
		if err != nil || validateSnapshotReference(snapshot, stored, active, *active.ReviewSnapshot, cfg.WorkspaceID, profile.Name, publishState.Folders.ReviewID) != nil {
			target.Review.State = reviewStateBlocked
			target.Review.Message = "review baseline is missing or damaged; publish a new generation"
			continue
		}
		target.Review.BaselineAvailable = true
		target.Review.State = reviewStateBaselineAvailable
		target.Review.Message = "sealed local baseline is available"
	}
	check := report.StatusCheck{State: report.CheckPassed, Message: summarizeReviewStates(result)}
	if snapshotErr != nil {
		check = report.StatusCheck{State: report.CheckUnavailable, Message: snapshotErr.Error()}
	}
	orphans, orphanErr := reviewSnapshotOrphans(snapshotStore, publishState)
	if orphanErr != nil {
		check = report.StatusCheck{State: report.CheckUnavailable, Message: orphanErr.Error()}
		return result, nil, check
	}
	if len(orphans) > 0 {
		check.Message += fmt.Sprintf("; %d orphan snapshots are retained and require manual confirmation before removal", len(orphans))
	}
	return result, orphans, check
}

func remoteReviewDiagnostics(targets []report.TargetSummary, issues []document.Issue) []report.TargetSummary {
	result := cloneReviewTargets(targets)
	for index := range result {
		target := &result[index]
		if target.Review == nil || target.Review.State != reviewStateBaselineAvailable {
			continue
		}
		target.Review.State = reviewStatePullReady
		target.Review.Message = "remote target and sealed baseline are ready for pull"
		changed := false
		blocked := false
		for _, issue := range issues {
			if issue.Source != "" && issue.Source != target.Target {
				continue
			}
			if issue.Source == "" && !globalReviewBlocker(issue.Code) {
				continue
			}
			if issue.Severity == document.SeverityError || issue.Code == "unresolved_suggestions" {
				blocked = true
			}
			if issue.Code == "docs_revision_change" {
				changed = true
			}
		}
		switch {
		case blocked:
			target.Review.State = reviewStateBlocked
			target.Review.Message = "remote target must be repaired or suggestions resolved before pull"
		case changed:
			target.Review.State = reviewStateChanged
			target.Review.Message = "remote content changed after activation; run review pull"
		}
	}
	return result
}

func globalReviewBlocker(code string) bool {
	switch code {
	case "state_missing", "folder_invalid", "folder_mapping_mismatch", "folder_collision":
		return true
	default:
		return false
	}
}

func blockRemoteReviewDiagnostics(targets []report.TargetSummary, message string) []report.TargetSummary {
	result := cloneReviewTargets(targets)
	for index := range result {
		if result[index].Review != nil && result[index].Review.BaselineAvailable {
			result[index].Review.State = reviewStateBlocked
			result[index].Review.Message = message
		}
	}
	return result
}

func cloneReviewTargets(targets []report.TargetSummary) []report.TargetSummary {
	result := append([]report.TargetSummary(nil), targets...)
	for index := range result {
		if result[index].Review != nil {
			review := *result[index].Review
			result[index].Review = &review
		}
	}
	return result
}

func reviewSnapshotOrphans(snapshotStore *reviewsync.SnapshotStore, publishState *state.State) ([]report.ReviewSnapshotOrphan, error) {
	if snapshotStore == nil {
		return nil, nil
	}
	orphans, err := snapshotStore.Orphans(reviewSnapshotReferences(publishState))
	if err != nil {
		return nil, err
	}
	result := make([]report.ReviewSnapshotOrphan, 0, len(orphans))
	for _, orphan := range orphans {
		result = append(result, report.ReviewSnapshotOrphan{Path: orphan.Path, Target: orphan.TargetKey, Generation: orphan.Generation, Reason: orphan.Reason})
	}
	return result, nil
}

func reviewSnapshotReferences(publishState *state.State) []reviewsync.SnapshotReference {
	if publishState == nil {
		return nil
	}
	result := []reviewsync.SnapshotReference{}
	for key, stored := range publishState.Targets {
		targets := append([]state.Target(nil), stored.PriorTargets...)
		if stored.ActiveTarget != nil {
			targets = append(targets, *stored.ActiveTarget)
		}
		for _, target := range targets {
			if target.ReviewSnapshot != nil {
				result = append(result, reviewsync.SnapshotReference{TargetKey: key, Generation: target.Generation, ManifestHash: target.ReviewSnapshot.ManifestHash})
			}
		}
	}
	return result
}

func summarizeReviewStates(targets []report.TargetSummary) string {
	counts := map[string]int{}
	for _, target := range targets {
		if target.Review != nil {
			counts[target.Review.State]++
		}
	}
	keys := make([]string, 0, len(counts))
	for key := range counts {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, fmt.Sprintf("%s=%d", key, counts[key]))
	}
	if len(parts) == 0 {
		return "no version 3 publication review targets"
	}
	return strings.Join(parts, ", ")
}

func recoverReviewSnapshotReferences(statePath string, recovered *state.State) int {
	if recovered == nil {
		return 0
	}
	snapshotStore, err := reviewsync.NewSnapshotStore(statePath)
	if err != nil {
		return 0
	}
	recoveredCount := 0
	for key, stored := range recovered.Targets {
		if stored.ActiveTarget != nil && recoverReviewSnapshotReference(snapshotStore, recovered, stored, stored.ActiveTarget) {
			recoveredCount++
		}
		for index := range stored.PriorTargets {
			if recoverReviewSnapshotReference(snapshotStore, recovered, stored, &stored.PriorTargets[index]) {
				recoveredCount++
			}
		}
		recovered.Targets[key] = stored
	}
	return recoveredCount
}

func recoverReviewSnapshotReference(snapshotStore *reviewsync.SnapshotStore, recovered *state.State, stored state.Document, target *state.Target) bool {
	if target == nil {
		return false
	}
	snapshot, err := snapshotStore.Load(stored.TargetKey, target.Generation, true)
	if err != nil {
		return false
	}
	manifest := snapshot.Manifest
	if manifest.Activation == nil {
		return false
	}
	reference := state.ReviewSnapshotRef{Version: manifest.Version, ManifestHash: manifest.IntegrityHash, BaselineHash: manifest.BaselineHash, CapturedRevision: manifest.Capture.DocsRevision, ActivatedRevision: manifest.Activation.DocsRevision}
	if err := validateSnapshotReference(snapshot, stored, *target, reference, recovered.WorkspaceID, recovered.Profile, recovered.Folders.ReviewID); err != nil {
		return false
	}
	expectedMetadata := map[string]string{
		"mdoc_workspace": recovered.WorkspaceID, "mdoc_profile": recovered.Profile, "mdoc_target_key": stored.TargetKey,
		"mdoc_publication": stored.PublicationID, "mdoc_publication_kind": stored.PublicationKind, "mdoc_source": stored.Source,
		"mdoc_generation": strconv.Itoa(target.Generation), "mdoc_review_set": target.ReviewSetID, "mdoc_operation": target.OperationID, "mdoc_status": "ready",
	}
	if !metadataContains(manifest.Activation.ReadyMetadata, expectedMetadata) {
		return false
	}
	target.ReviewSnapshot = &reference
	return true
}
