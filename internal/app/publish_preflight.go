package app

import (
	"context"
	"fmt"
	"sort"
	"strconv"

	"github.com/goliatone/mdoc/internal/config"
	"github.com/goliatone/mdoc/internal/googleapi"
	"github.com/goliatone/mdoc/internal/planner"
	"github.com/goliatone/mdoc/internal/sourcekey"
	"github.com/goliatone/mdoc/internal/state"
)

func initialInspectionSources(locals []planner.LocalDocument, selected map[string]bool) map[string]bool {
	result := map[string]bool{}
	if len(selected) > 0 {
		for key := range selected {
			result[key] = true
		}
		return result
	}
	for _, local := range locals {
		result[local.SourceKey] = true
	}
	return result
}

func selectedPublishClosure(locals []planner.LocalDocument, publishState *state.State, remotes map[string]planner.RemoteDocument, selected map[string]bool, newReview bool) map[string]bool {
	byKey := make(map[string]planner.LocalDocument, len(locals))
	reverse := map[string][]string{}
	for _, local := range locals {
		byKey[local.SourceKey] = local
		for _, target := range local.Outbound {
			reverse[target] = append(reverse[target], local.SourceKey)
		}
	}
	closure := map[string]bool{}
	queue := []string{}
	for key := range selected {
		local, ok := byKey[key]
		if ok && localNeedsPublish(local, publishState.Documents[key], remotes[key], newReview) {
			closure[key] = true
			queue = append(queue, key)
		}
	}
	for len(queue) > 0 {
		target := queue[0]
		queue = queue[1:]
		for _, source := range reverse[target] {
			if !closure[source] {
				closure[source] = true
				queue = append(queue, source)
			}
		}
	}
	return closure
}

func localNeedsPublish(local planner.LocalDocument, stored state.Document, remote planner.RemoteDocument, newReview bool) bool {
	if local.Failed != "" {
		return false
	}
	if stored.ActiveTarget == nil {
		return true
	}
	if !remote.Exists || !remote.Accessible || remote.Trashed || remote.Ambiguous || remote.MetadataMismatch {
		return newReview
	}
	return newReview || local.SourceHash != stored.SourceHash || local.RenderHash != stored.RenderHash
}

func subtractSources(all, existing map[string]bool) map[string]bool {
	result := map[string]bool{}
	for key := range all {
		if !existing[key] {
			result[key] = true
		}
	}
	return result
}

func (a *Application) inspectActiveTargets(ctx context.Context, cfg *config.Config, profile config.SelectedProfile, publishState *state.State, clients googleapi.Clients, sources map[string]bool) (map[string]planner.RemoteDocument, error) {
	result := map[string]planner.RemoteDocument{}
	activeCount := 0
	for source := range sources {
		if publishState.Documents[source].ActiveTarget != nil {
			activeCount++
		}
	}
	if activeCount == 0 {
		return result, nil
	}
	files, err := clients.Drive.FindDocuments(ctx, map[string]string{"mdoc_workspace": cfg.WorkspaceID, "mdoc_profile": profile.Name})
	if err != nil {
		return nil, googleError(fmt.Errorf("find active publish targets: %w", err))
	}
	keys := make([]string, 0, len(sources))
	for key := range sources {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		active := publishState.Documents[key].ActiveTarget
		if active == nil {
			continue
		}
		file, getErr := clients.Drive.GetFile(ctx, active.FileID)
		if getErr != nil {
			switch {
			case googleErrorIs(getErr, googleapi.KindNotFound):
				result[key] = planner.RemoteDocument{}
				continue
			default:
				return nil, googleError(fmt.Errorf("inspect active target for %s: %w", key, getErr))
			}
		}
		remote := planner.RemoteDocument{Exists: true, Accessible: true, Trashed: file.Trashed, Version: file.Version, Name: file.Name}
		if len(file.Parents) > 0 {
			remote.ParentID = file.Parents[0]
		}
		if !activeTargetMetadataMatches(file, cfg.WorkspaceID, profile.Name, key, *active) {
			remote.MetadataMismatch = true
			result[key] = remote
			continue
		}
		matches := matchingGeneration(files, key, active.Generation)
		if len(matches) != 1 || matches[0].ID != active.FileID {
			remote.Ambiguous = true
			result[key] = remote
			continue
		}
		if remote.Trashed {
			result[key] = remote
			continue
		}
		comments, commentsErr := clients.Drive.ListComments(ctx, active.FileID)
		if commentsErr != nil {
			return nil, googleError(fmt.Errorf("inspect comments for %s: %w", key, commentsErr))
		}
		document, docsErr := clients.Docs.GetDocument(ctx, active.FileID)
		if docsErr != nil {
			return nil, googleError(fmt.Errorf("inspect suggestions for %s: %w", key, docsErr))
		}
		remote.UnresolvedComments = comments.Unresolved
		remote.SuggestionsDetected = document.SuggestionsDetected
		result[key] = remote
	}
	return result, nil
}

func matchingGeneration(files []googleapi.File, source string, generation int) []googleapi.File {
	result := []googleapi.File{}
	for _, file := range files {
		remoteSource, err := sourcekey.Normalize(file.AppProperties["mdoc_source"])
		remoteGeneration, generationErr := strconv.Atoi(file.AppProperties["mdoc_generation"])
		if err == nil && generationErr == nil && remoteSource == source && remoteGeneration == generation {
			result = append(result, file)
		}
	}
	return result
}

func activeTargetMetadataMatches(file googleapi.File, workspaceID, profile, source string, active state.Target) bool {
	properties := file.AppProperties
	remoteSource, sourceErr := sourcekey.Normalize(properties["mdoc_source"])
	generation, generationErr := strconv.Atoi(properties["mdoc_generation"])
	expectedSize, expectedErr := strconv.Atoi(properties["mdoc_expected_set_size"])
	return file.ID == active.FileID && file.MimeType == googleapi.DocumentMimeType && file.URL != "" && active.URL != "" && file.URL == active.URL &&
		properties["mdoc_workspace"] == workspaceID && properties["mdoc_profile"] == profile && sourceErr == nil && remoteSource == source &&
		generationErr == nil && generation == active.Generation && properties["mdoc_review_set"] == active.ReviewSetID &&
		properties["mdoc_operation"] == active.OperationID && properties["mdoc_status"] == "ready" && active.PublishStatus == "ready" &&
		expectedErr == nil && expectedSize > 0
}
