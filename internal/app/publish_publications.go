package app

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/goliatone/mdoc/internal/bundle"
	"github.com/goliatone/mdoc/internal/config"
	"github.com/goliatone/mdoc/internal/document"
	"github.com/goliatone/mdoc/internal/fields"
	"github.com/goliatone/mdoc/internal/googleapi"
	"github.com/goliatone/mdoc/internal/planner"
	"github.com/goliatone/mdoc/internal/publication"
	"github.com/goliatone/mdoc/internal/render"
	"github.com/goliatone/mdoc/internal/report"
	"github.com/goliatone/mdoc/internal/reviewsync"
	"github.com/goliatone/mdoc/internal/state"
)

type publicationBuild struct {
	target       *publication.Publication
	fields       fields.Result
	local        planner.LocalDocument
	request      render.Request
	links        []PendingLink
	members      []state.JournalMember
	layoutHash   string
	topologyHash string
	issues       []document.Issue
}

func (a *Application) publishExplicit(ctx context.Context, options PublishOptions, cfg *config.Config, profile config.SelectedProfile, store *state.Store) error {
	options.JSON = cfg.Output.Format == "json"
	options.Quiet = cfg.Output.Quiet
	options.Config = cfg.Path
	options.Profile = profile.Name
	if len(options.Files) > 0 && options.Bundle != "" {
		return NewError(ClassCommand, "publication_selection_invalid", "--file and --bundle cannot be used together")
	}
	if err := store.Acquire(); err != nil {
		return WrapError(ClassConflict, "state_locked", err.Error(), err)
	}
	defer store.Release()
	if remap, err := store.LoadRemapJournal(); err == nil {
		return NewError(ClassConflict, "remap_resume_required", fmt.Sprintf("unfinished source remap %s to %s must be resumed before publishing", remap.From, remap.To))
	} else if !errors.Is(err, state.ErrNotFound) {
		return partial(err)
	}
	publishState, err := store.Load()
	if err != nil {
		return WrapError(ClassPartial, "state_reconcile_required", err.Error(), err)
	}
	if err := a.verifyStoredAccount(ctx, publishState); err != nil {
		return err
	}
	journal, journalErr := store.LoadJournal()
	if journalErr != nil && !errors.Is(journalErr, state.ErrNotFound) {
		return WrapError(ClassPartial, "publish_journal_invalid", journalErr.Error(), journalErr)
	}
	graph, err := document.Discover(ctx, cfg.Root, profile, a.documentRunner(cfg))
	if err != nil {
		return WrapError(ClassValidation, "discovery_failed", err.Error(), err)
	}
	publications := publication.Resolve(cfg.Version, profile, graph)
	graph.Issues = append(graph.Issues, publications.Issues...)
	resolvedFields, fieldIssues := resolvePublicationFields(cfg, profile, publications, options.CommonOptions)
	if journal != nil {
		resumePublications := map[string]bool{}
		for _, entry := range journal.Entries {
			resumePublications[entry.PublicationID] = true
			resolvedFields[entry.PublicationID] = fields.Result{Values: entry.ResolvedFields, Hash: entry.FieldsHash}
		}
		filtered := fieldIssues[:0]
		for _, issue := range fieldIssues {
			if !resumePublications[issue.Source] {
				filtered = append(filtered, issue)
			}
		}
		fieldIssues = filtered
	}
	graph.Issues = append(graph.Issues, fieldIssues...)
	if graph.HasErrors() {
		for _, issue := range graph.Issues {
			fmt.Fprintf(a.Out, "%s %s: %s: %s\n", issue.Severity, issue.Code, issue.Source, issue.Message)
		}
		return NewError(ClassValidation, "local_validation_failed", "local validation failed; fix the reported errors and retry")
	}
	selected, err := selectPublicationTargets(cfg.Root, graph, publications, options.Files, options.Bundle)
	if err != nil {
		return NewError(ClassCommand, "publication_selection_invalid", err.Error())
	}
	if journal != nil {
		selected = map[string]bool{}
		for _, key := range journal.Sources {
			selected[key] = true
		}
	}
	if a.Auth == nil {
		return NewError(ClassAuthentication, "auth_unavailable", "Google authorization is unavailable; run `mdoc auth login`")
	}
	client, err := a.Auth.Client(ctx)
	if err != nil {
		return WrapError(ClassAuthentication, "auth_client_failed", err.Error(), err)
	}
	clients, err := a.GoogleFactory.New(ctx, client)
	if err != nil {
		return googleError(err)
	}
	pathTargets := publicationPathTargets(publications)
	knownTargets := activePublicationURLs(publishState)
	stableTargets := map[string]string{}
	for _, target := range publications.Items {
		stableTargets[string(target.Target)] = PlaceholderURL(string(target.Target))
	}
	profileBytes, _ := json.Marshal(newProfileFingerprint(cfg, profile))
	builds := map[string]publicationBuild{}
	locals := make([]planner.LocalDocument, 0, len(publications.Items))
	for _, target := range publications.Items {
		computed := planPublicationComputed(cfg, profile, target)
		build, buildErr := a.buildPublication(ctx, cfg, profile, graph, target, resolvedFields[target.ID], profileBytes, pathTargets, stableTargets, computed)
		if buildErr != nil {
			build = publicationBuild{target: target, fields: resolvedFields[target.ID], local: publicationLocal(target)}
			build.local.Failed = buildErr.Error()
		}
		builds[string(target.Target)] = build
		locals = append(locals, build.local)
	}
	inspectionTargets := publicationInboundClosure(locals, selected)
	remotes, err := a.inspectPublicationTargets(ctx, cfg, profile, publishState, clients, inspectionTargets)
	if err != nil {
		return err
	}
	for key, remote := range remotes {
		if remote.MetadataMismatch {
			return NewError(ClassConflict, "publication_metadata_mismatch", fmt.Sprintf("active target metadata for %s does not match local state; reconcile or repair the remote metadata before publishing", key))
		}
		if remote.Ambiguous {
			return NewError(ClassConflict, "publication_generation_duplicate", fmt.Sprintf("more than one remote document claims the active generation for %s; resolve the duplicate before publishing", key))
		}
	}
	plan := planner.Build(planner.Input{Local: locals, State: publishState, Remote: remotes, Selected: selected, NewReview: options.NewReview, ExpectedReviewFolder: publishState.Folders.ReviewID})
	for key, build := range builds {
		for _, issue := range build.issues {
			if issue.Severity != document.SeverityWarning {
				continue
			}
			for index := range plan.Items {
				if plan.Items[index].Identity() == key {
					plan.Items[index].Warnings = append(plan.Items[index].Warnings, fmt.Sprintf("%s: %s: %s", issue.Code, issue.Source, issue.Message))
					break
				}
			}
		}
	}
	entryURL := activePublicationURL(publishState, publications.Entry)
	planReport := report.FromPlanVersion(plan, entryURL, 2)
	if plan.Blocked {
		_ = report.WritePlan(a.Out, planReport, options.JSON, options.Quiet)
		for _, item := range plan.Items {
			if item.Action == planner.ActionFailed {
				return NewError(ClassConversion, "publish_plan_failed", fmt.Sprintf("%s: %s", item.Identity(), item.Reason))
			}
		}
		return NewError(ClassConflict, "publish_plan_blocked", "publish plan is blocked; resolve the reported items or request a new review")
	}
	if plan.WriteCount() == 0 || options.DryRun {
		return report.WritePlan(a.Out, planReport, options.JSON, options.Quiet)
	}
	if publishState.Folders.StagingID == "" || publishState.Folders.ReviewID == "" {
		return NewError(ClassValidation, "folders_missing", "publish folders are not configured; run `mdoc setup`")
	}
	destination, err := a.verifyPublishDestination(ctx, cfg, profile, publishState, clients)
	if err != nil {
		return err
	}
	a.showPublishDestination(destination)
	operationID, reviewSetID, err := operationIdentity(store)
	if err != nil {
		return err
	}
	if journal != nil {
		operationID, reviewSetID = journal.OperationID, journal.ReviewSetID
	}
	actions := map[string]planner.Item{}
	for _, item := range plan.Items {
		if item.Action == planner.ActionCreate || item.Action == planner.ActionNewReview {
			actions[item.Identity()] = item
		}
	}
	if journal != nil {
		actions = map[string]planner.Item{}
		for _, key := range journal.Sources {
			entry := journal.Entries[key]
			actions[key] = planner.Item{Target: key, PublicationID: entry.PublicationID, PublicationKind: entry.PublicationKind, Generation: entry.Generation, Action: planner.ActionNewReview}
		}
	}
	finalTargets := cloneStrings(knownTargets)
	for key := range actions {
		finalTargets[key] = PlaceholderURL(key)
	}
	publishedAt := time.Now().UTC()
	items := make([]BatchItem, 0, len(actions))
	warnings := []string{}
	keys := sortedKeys(actions)
	for _, key := range keys {
		planned := actions[key]
		target := publications.ByTarget[publication.TargetKey(key)]
		if target == nil {
			return NewError(ClassConflict, "resume_publication_missing", fmt.Sprintf("publication %s is no longer configured", key))
		}
		computed := fields.FreezeComputed(fields.PlanComputedValues(fields.PlanComputed{ProjectID: cfg.WorkspaceID, Profile: profile.Name, PublicationID: target.ID, PublicationKind: string(target.Kind), PublicationTitle: target.Title}), fields.FrozenComputed{OperationID: operationID, ReviewSetID: reviewSetID, Generation: planned.Generation, PublishedAt: publishedAt})
		if journal != nil {
			computed = journal.Entries[key].ComputedValues
		}
		build, buildErr := a.buildPublication(ctx, cfg, profile, graph, target, resolvedFields[target.ID], profileBytes, pathTargets, finalTargets, computed)
		if buildErr != nil {
			return WrapError(ClassConversion, "render_input_failed", buildErr.Error(), buildErr)
		}
		rendered, renderErr := a.renderer(cfg).Render(ctx, build.request)
		if renderErr != nil {
			return WrapError(ClassConversion, "render_failed", renderErr.Error(), renderErr)
		}
		docx := rendered.DOCX
		if a.ArtifactGate != nil {
			docx, renderErr = a.ArtifactGate.Finalize(ctx, PublicationArtifact{
				TargetKey:       key,
				PublicationID:   target.ID,
				PublicationKind: string(target.Kind),
				DOCX:            append([]byte(nil), rendered.DOCX...),
			})
			if renderErr != nil {
				return WrapError(ClassConversion, "artifact_gate_failed", renderErr.Error(), renderErr)
			}
			if len(docx) == 0 {
				return NewError(ClassConversion, "artifact_gate_empty", fmt.Sprintf("artifact gate returned an empty DOCX for %s", key))
			}
		}
		for _, warning := range rendered.Warnings {
			warnings = append(warnings, fmt.Sprintf("%s: %s", key, warning))
		}
		hashInput, hashErr := a.renderer(cfg).HashInput(ctx, build.request)
		if hashErr != nil {
			return WrapError(ClassConversion, "render_hash_failed", hashErr.Error(), hashErr)
		}
		stable := builds[key]
		stableForPublish, stableErr := a.buildPublication(ctx, cfg, profile, graph, target, resolvedFields[target.ID], profileBytes, pathTargets, stableTargets, planPublicationComputed(cfg, profile, target))
		if stableErr != nil {
			return WrapError(ClassConversion, "render_hash_failed", stableErr.Error(), stableErr)
		}
		computedHash, _ := hashJSON(computed)
		batchItem := BatchItem{
			SourceKey: key, SourceInput: sourceInput(target), Title: target.Title, DOCX: docx,
			SourceHash: stable.local.SourceHash, RenderHash: stableForPublish.local.RenderHash, HashInput: &hashInput, Generation: planned.Generation,
			ResolvedFields: build.fields.Values, FieldsHash: build.fields.Hash, FieldFileHashes: fieldFileHashes(build.fields), ComputedValues: computed, ComputedHash: computedHash,
			PublicationID: target.ID, PublicationKind: string(target.Kind), Members: build.members, LayoutHash: build.layoutHash, TopologyHash: build.topologyHash, Links: build.links,
		}
		if target.ReviewPullEnabled {
			batchItem.Review, buildErr = reviewPublishInput(cfg, profile, target)
			if buildErr != nil {
				return WrapError(ClassConversion, "review_snapshot_source_failed", buildErr.Error(), buildErr)
			}
		}
		if journal != nil {
			entry := journal.Entries[key]
			currentParts := render.DescribeHashInput(hashInput)
			if !sameResumePublicationInputs(entry.HashParts, currentParts) {
				return NewError(ClassConflict, "resume_inputs_changed", fmt.Sprintf("%s style, asset, filter, link, layout, or source inputs changed after the interrupted publish", key))
			}
			batchItem.RenderHash = entry.RenderHash
			batchItem.FieldsHash = entry.FieldsHash
			batchItem.FieldFileHashes = append([]string(nil), entry.FieldFileHashes...)
			batchItem.HashParts = append([]state.HashPartDigest(nil), entry.HashParts...)
		}
		items = append(items, batchItem)
	}
	result, err := (BatchPublisher{Drive: clients.Drive, Docs: clients.Docs, Store: store, AfterCreate: a.BatchAfterCreate}).Run(ctx, BatchRequest{WorkspaceID: cfg.WorkspaceID, Profile: profile.Name, StagingID: publishState.Folders.StagingID, ReviewID: publishState.Folders.ReviewID, ReviewSetID: reviewSetID, OperationID: operationID, ResumeCommand: publishResumeCommand(options.CommonOptions), Items: items, KnownTargets: knownTargets, UseTargets: true})
	if err != nil {
		return err
	}
	result.Warnings = append(result.Warnings, warnings...)
	if entry := publications.Entry; entry != nil {
		if target := result.Targets[string(entry.Target)]; target.URL != "" {
			entryURL = target.URL
		}
	}
	return report.WritePublish(a.Out, report.PublishReport{SchemaVersion: 2, Kind: "publish", Plan: planReport, Targets: result.Targets, Warnings: result.Warnings, EntryURL: entryURL}, options.JSON, options.Quiet)
}

func sameResumePublicationInputs(stored []state.HashPartDigest, current []render.HashPartDigest) bool {
	left := comparableResumeParts(stored)
	rightInput := make([]state.HashPartDigest, 0, len(current))
	for _, part := range current {
		rightInput = append(rightInput, state.HashPartDigest{Name: part.Name, Hash: part.Hash})
	}
	right := comparableResumeParts(rightInput)
	if len(left) != len(right) {
		return false
	}
	for name, hash := range left {
		if right[name] != hash {
			return false
		}
	}
	return true
}

func comparableResumeParts(parts []state.HashPartDigest) map[string]string {
	result := map[string]string{}
	for _, part := range parts {
		name := part.Name
		if len(name) > len("part:") && name[:len("part:")] == "part:" {
			if separator := indexAfterPartNumber(name); separator >= 0 {
				name = name[separator+1:]
			}
			if len(name) >= len("field_file:") && name[:len("field_file:")] == "field_file:" {
				continue
			}
		}
		result[name] = part.Hash
	}
	return result
}

func indexAfterPartNumber(name string) int {
	for index := len("part:"); index < len(name); index++ {
		if name[index] == ':' {
			return index
		}
	}
	return -1
}

func (a *Application) buildPublication(ctx context.Context, cfg *config.Config, profile config.SelectedProfile, graph *document.Graph, target *publication.Publication, resolved fields.Result, profileBytes []byte, pathTargets, externalTargets map[string]string, computed map[string]any) (publicationBuild, error) {
	result := publicationBuild{target: target, fields: resolved, local: publicationLocal(target)}
	sourceHash, err := publication.SourceHash(target)
	if err != nil {
		return result, err
	}
	result.local.SourceHash = sourceHash
	assets := []publication.HashFile{}
	usedExternalTargets := map[string]string{}
	request := render.Request{ReferenceDOCX: profile.ReferenceDOCX, ProfileFingerprint: profileBytes, Reader: profile.Reader, HeadingFilter: profile.HeadingFilter, Filters: profile.Filters}
	stableComputed := planPublicationComputed(cfg, profile, target)
	if target.Kind == publication.KindBundle {
		assembled, err := bundle.AssembleWithOptions(ctx, target, a.documentRunner(cfg), bundle.AssembleOptions{AllowedRoots: target.AllowedRoots, PathTargets: pathTargets, ExternalTargets: externalTargets, Fields: resolved.Values, Computed: computed})
		if err != nil {
			return result, err
		}
		for _, issue := range assembled.Issues {
			if issue.Severity == document.SeverityError {
				return result, errors.New(issue.Message)
			}
		}
		result.issues = append(result.issues, assembled.Issues...)
		request.PandocJSON = assembled.JSON
		request.SourcePath = target.Members[0].Document.Path
		request.ResourcePaths = assembled.ResourcePaths
		for _, asset := range assembled.Assets {
			request.Assets = append(request.Assets, asset.Path)
			assets = append(assets, publication.HashFile{Label: asset.SourceKey + ":" + asset.Original, Path: asset.Path})
		}
		topology := assembled.Topology
		layout := target.Layout
		request.FinalizeJSON = func(filtered []byte) ([]byte, error) { return bundle.Finalize(topology, layout, filtered) }
		layoutPlan := publicationLayoutPlan(target, topology, resolved.Values, computed)
		request.Layout = &layoutPlan
		result.topologyHash, _ = hashJSON(topology)
		for _, member := range target.Members {
			result.members = append(result.members, state.JournalMember{SourceKey: member.SourceKey, SourceHash: member.Document.SourceHash})
		}
		for _, link := range assembled.ExternalLinks {
			usedExternalTargets[link.TargetSource] = externalTargets[link.TargetSource]
			if externalTargets[link.TargetSource] == PlaceholderURL(link.TargetSource) {
				result.links = append(result.links, PendingLink{Placeholder: link.Target, TargetSource: link.TargetSource, HadFragment: link.HadFragment})
			}
			result.local.Outbound = appendUnique(result.local.Outbound, link.TargetSource)
		}
	} else {
		if target.Source == nil {
			return result, fmt.Errorf("source publication %q has no source", target.ID)
		}
		links := map[string]string{}
		for _, link := range target.Source.Links {
			if link.Image {
				continue
			}
			resolvedPath, fragment, local := graph.ResolveTarget(target.Source, link.Target)
			if !local {
				continue
			}
			targetKey := pathTargets[canonicalPublicationPath(graph.ByKey[resolvedPath].Path)]
			if targetKey == "" || externalTargets[targetKey] == "" {
				return result, fmt.Errorf("Markdown target %q has no unique publication mapping", link.Target)
			}
			links[link.Target] = externalTargets[targetKey]
			usedExternalTargets[targetKey] = externalTargets[targetKey]
			result.local.Outbound = appendUnique(result.local.Outbound, targetKey)
			if externalTargets[targetKey] == PlaceholderURL(targetKey) {
				result.links = append(result.links, PendingLink{OriginalTarget: link.Target, Placeholder: externalTargets[targetKey], TargetSource: targetKey, HadFragment: fragment != ""})
			}
		}
		request = documentRenderRequest(cfg, profile, graph, target.Source, profileBytes, links)
		result.members = append(result.members, state.JournalMember{SourceKey: target.Source.SourceKey, SourceHash: target.Source.SourceHash})
		layoutPlan := publicationLayoutPlan(target, bundle.Topology{}, resolved.Values, computed)
		request.Layout = &layoutPlan
		for _, path := range request.Assets {
			assets = append(assets, publication.HashFile{Label: filepath.ToSlash(path), Path: path})
		}
	}
	fieldFiles := make([]publication.HashFile, 0, len(resolved.Files))
	for _, file := range resolved.Files {
		fieldFiles = append(fieldFiles, publication.HashFile{Label: file.Identity, Path: file.Path, Data: file.Data})
	}
	parts, err := publication.RenderHashParts(target, sourceHash, publication.RenderPartOptions{Fields: resolved.Values, FieldFiles: fieldFiles, StableComputed: stableComputed, Assets: assets, ExternalTargets: usedExternalTargets})
	if err != nil {
		return result, err
	}
	request.HashParts = parts
	result.request = request
	result.layoutHash, _ = hashJSON(target.Layout)
	result.local.RenderHash, _, err = a.renderer(cfg).Fingerprint(ctx, request)
	return result, err
}

func reviewPublishInput(cfg *config.Config, profile config.SelectedProfile, target *publication.Publication) (*ReviewPublishInput, error) {
	result := &ReviewPublishInput{Reader: profile.Reader, Binary: cfg.PandocBinary}
	if target.Kind == publication.KindBundle {
		for _, member := range target.Members {
			content, err := os.ReadFile(member.Document.Path)
			if err != nil {
				return nil, fmt.Errorf("read review source %q: %w", member.SourceKey, err)
			}
			if document.SourceHash(content) != member.Document.SourceHash {
				return nil, fmt.Errorf("review source %q changed during publish", member.SourceKey)
			}
			result.Sources = append(result.Sources, reviewsync.SnapshotSource{SourceKey: member.SourceKey, SourceHash: member.Document.SourceHash, Content: content})
		}
	} else if target.Source != nil {
		content, err := os.ReadFile(target.Source.Path)
		if err != nil {
			return nil, fmt.Errorf("read review source %q: %w", target.Source.SourceKey, err)
		}
		if document.SourceHash(content) != target.Source.SourceHash {
			return nil, fmt.Errorf("review source %q changed during publish", target.Source.SourceKey)
		}
		result.Sources = append(result.Sources, reviewsync.SnapshotSource{SourceKey: target.Source.SourceKey, SourceHash: target.Source.SourceHash, Content: content})
	}
	if len(result.Sources) == 0 {
		return nil, fmt.Errorf("publication %q has no review sources", target.ID)
	}
	return result, nil
}

func publicationLocal(target *publication.Publication) planner.LocalDocument {
	local := planner.LocalDocument{TargetKey: string(target.Target), PublicationID: target.ID, PublicationKind: string(target.Kind), Title: target.Title, MemberCount: len(target.Members)}
	if target.Source != nil {
		local.SourceKey = target.Source.SourceKey
	}
	return local
}

func publicationSummaries(publications publication.Set, bundleID string) []report.TargetSummary {
	result := []report.TargetSummary{}
	for _, target := range publications.Items {
		if bundleID != "" && target.ID != bundleID {
			continue
		}
		summary := report.TargetSummary{Target: string(target.Target), PublicationID: target.ID, PublicationKind: string(target.Kind), MemberCount: len(target.Members)}
		if target.Source != nil {
			summary.Source = target.Source.SourceKey
		}
		result = append(result, summary)
	}
	return result
}

func (a *Application) validatePublicationBuildInputs(ctx context.Context, cfg *config.Config, profile config.SelectedProfile, graph *document.Graph, publications publication.Set, resolvedFields map[string]fields.Result, bundleID string) []document.Issue {
	_, issues := a.buildPublicationInputs(ctx, cfg, profile, graph, publications, resolvedFields, bundleID, nil)
	return issues
}

func (a *Application) buildPublicationInputs(ctx context.Context, cfg *config.Config, profile config.SelectedProfile, graph *document.Graph, publications publication.Set, resolvedFields map[string]fields.Result, bundleID string, targets map[string]string) (map[string]publicationBuild, []document.Issue) {
	pathTargets := publicationPathTargets(publications)
	externalTargets := cloneStrings(targets)
	for _, target := range publications.Items {
		if externalTargets[string(target.Target)] == "" {
			externalTargets[string(target.Target)] = PlaceholderURL(string(target.Target))
		}
	}
	profileBytes, _ := json.Marshal(newProfileFingerprint(cfg, profile))
	builds := map[string]publicationBuild{}
	issues := []document.Issue{}
	svgRasterizerChecked := false
	var svgRasterizerErr error
	for _, target := range publications.Items {
		if bundleID != "" && target.ID != bundleID {
			continue
		}
		build, err := a.buildPublication(ctx, cfg, profile, graph, target, resolvedFields[target.ID], profileBytes, pathTargets, externalTargets, planPublicationComputed(cfg, profile, target))
		if err != nil {
			issues = append(issues, document.Issue{Severity: document.SeverityError, Code: "publication_render_inputs_invalid", Source: target.ID, Message: err.Error()})
			continue
		}
		builds[string(target.Target)] = build
		issues = append(issues, build.issues...)
		if publicationUsesSVG(build.request.Assets) {
			if !svgRasterizerChecked {
				svgRasterizerErr = a.renderer(cfg).ValidateSVGRasterizer(ctx)
				svgRasterizerChecked = true
			}
			if svgRasterizerErr != nil {
				issues = append(issues, document.Issue{Severity: document.SeverityError, Code: "svg_rasterizer_unavailable", Source: target.ID, Message: svgRasterizerErr.Error()})
			}
		}
	}
	return builds, issues
}

func publicationUsesSVG(assets []string) bool {
	for _, asset := range assets {
		if strings.EqualFold(filepath.Ext(asset), ".svg") {
			return true
		}
	}
	return false
}

func publicationLayoutPlan(target *publication.Publication, topology bundle.Topology, values, computed map[string]any) render.LayoutPlan {
	plan := render.LayoutPlan{Page: target.Layout.Page, Header: target.Layout.Header, Footer: target.Layout.Footer, PublicationID: target.ID, Fields: values, Computed: computed, Headings: target.Layout.Headings, Tables: target.Layout.Tables}
	memberConfig := map[string]config.MemberConfig{}
	for _, member := range target.Members {
		memberConfig[member.SourceKey] = member.Config
	}
	for _, marker := range topology.Markers {
		if marker.Kind != bundle.MarkerMember {
			continue
		}
		configured := memberConfig[marker.SourceKey]
		plan.Boundaries = append(plan.Boundaries, render.SectionBoundary{BookmarkID: marker.ID, SourceKey: marker.SourceKey, Start: configured.Start, Section: configured.Section})
	}
	return plan
}

func planPublicationComputed(cfg *config.Config, profile config.SelectedProfile, target *publication.Publication) map[string]any {
	plan := fields.PlanComputedValues(fields.PlanComputed{ProjectID: cfg.WorkspaceID, Profile: profile.Name, PublicationID: target.ID, PublicationKind: string(target.Kind), PublicationTitle: target.Title})
	return fields.FreezeComputed(plan, fields.FrozenComputed{OperationID: "plan", ReviewSetID: "plan", Generation: 1, PublishedAt: time.Unix(0, 0).UTC()})
}

func selectPublicationTargets(root string, graph *document.Graph, publications publication.Set, files []string, bundleID string) (map[string]bool, error) {
	if bundleID != "" {
		target := publications.ByID[bundleID]
		if target == nil || target.Kind != publication.KindBundle {
			return nil, fmt.Errorf("bundle publication %q does not exist", bundleID)
		}
		return map[string]bool{string(target.Target): true}, nil
	}
	if len(files) == 0 {
		return nil, nil
	}
	selectedSources, err := selectedSources(root, graph, files)
	if err != nil {
		return nil, err
	}
	result := map[string]bool{}
	for _, target := range publications.Items {
		if target.Kind == publication.KindSource && target.Source != nil && selectedSources[target.Source.SourceKey] {
			result[string(target.Target)] = true
		}
	}
	if len(result) == 0 {
		return nil, errors.New("selected files do not map to source publications")
	}
	return result, nil
}

func publicationPathTargets(publications publication.Set) map[string]string {
	result := map[string]string{}
	for _, target := range publications.Items {
		if target.Kind != publication.KindSource || target.Source == nil {
			continue
		}
		path := canonicalPublicationPath(target.Source.Path)
		if prior, exists := result[path]; exists && prior != string(target.Target) {
			result[path] = ""
		} else {
			result[path] = string(target.Target)
		}
	}
	return result
}

func canonicalPublicationPath(path string) string {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return filepath.Clean(path)
	}
	if real, err := filepath.EvalSymlinks(absolute); err == nil {
		absolute = real
	}
	return filepath.Clean(absolute)
}

func publicationInboundClosure(locals []planner.LocalDocument, selected map[string]bool) map[string]bool {
	if len(selected) == 0 {
		return nil
	}
	result := map[string]bool{}
	queue := []string{}
	for key := range selected {
		result[key] = true
		queue = append(queue, key)
	}
	for len(queue) > 0 {
		target := queue[0]
		queue = queue[1:]
		for _, local := range locals {
			key := local.TargetKey
			if result[key] {
				continue
			}
			for _, outbound := range local.Outbound {
				if outbound == target {
					result[key] = true
					queue = append(queue, key)
					break
				}
			}
		}
	}
	return result
}

func activePublicationURLs(value *state.State) map[string]string {
	result := map[string]string{}
	for key, target := range value.Targets {
		if target.ActiveTarget != nil {
			result[key] = target.ActiveTarget.URL
		}
	}
	return result
}

func activePublicationURL(value *state.State, target *publication.Publication) string {
	if target == nil {
		return ""
	}
	stored := value.Targets[string(target.Target)]
	if stored.ActiveTarget == nil {
		return ""
	}
	return stored.ActiveTarget.URL
}

func sourceInput(target *publication.Publication) string {
	if target.Kind == publication.KindSource && target.Source != nil {
		return target.Source.SourceKey
	}
	return ""
}

func fieldFileHashes(result fields.Result) []string {
	hashes := make([]string, 0, len(result.Files))
	for _, file := range result.Files {
		hashes = append(hashes, file.Hash)
	}
	return hashes
}

func hashJSON(value any) (string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(data)
	return fmt.Sprintf("sha256:%x", digest[:]), nil
}

func appendUnique(values []string, value string) []string {
	for _, current := range values {
		if current == value {
			return values
		}
	}
	return append(values, value)
}

func sortedKeys[T any](values map[string]T) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func (a *Application) inspectPublicationTargets(ctx context.Context, cfg *config.Config, profile config.SelectedProfile, publishState *state.State, clients googleapi.Clients, selected map[string]bool) (map[string]planner.RemoteDocument, error) {
	keys := selected
	if len(keys) == 0 {
		keys = map[string]bool{}
		for key := range publishState.Targets {
			if len(key) >= len("publication:") && key[:len("publication:")] == "publication:" {
				keys[key] = true
			}
		}
	}
	result := map[string]planner.RemoteDocument{}
	files, err := clients.Drive.FindDocuments(ctx, map[string]string{"mdoc_workspace": cfg.WorkspaceID, "mdoc_profile": profile.Name})
	if err != nil {
		return nil, googleError(err)
	}
	for key := range keys {
		stored := publishState.Targets[key]
		if stored.ActiveTarget == nil {
			continue
		}
		active := *stored.ActiveTarget
		file, err := clients.Drive.GetFile(ctx, active.FileID)
		if err != nil {
			if googleErrorIs(err, googleapi.KindNotFound) {
				result[key] = planner.RemoteDocument{}
				continue
			}
			return nil, googleError(err)
		}
		remote := planner.RemoteDocument{Exists: true, Accessible: true, Trashed: file.Trashed, Version: file.Version, Name: file.Name}
		if len(file.Parents) > 0 {
			remote.ParentID = file.Parents[0]
		}
		properties := file.AppProperties
		generation, generationErr := strconv.Atoi(properties["mdoc_generation"])
		if properties["mdoc_workspace"] != cfg.WorkspaceID || properties["mdoc_profile"] != profile.Name || properties["mdoc_target_key"] != key || properties["mdoc_publication"] != stored.PublicationID || properties["mdoc_publication_kind"] != stored.PublicationKind || properties["mdoc_source"] != stored.Source || generationErr != nil || generation != active.Generation || properties["mdoc_review_set"] != active.ReviewSetID || properties["mdoc_operation"] != active.OperationID || properties["mdoc_status"] != "ready" {
			remote.MetadataMismatch = true
			result[key] = remote
			continue
		}
		matches := []string{}
		for _, candidate := range files {
			candidateGeneration, _ := strconv.Atoi(candidate.AppProperties["mdoc_generation"])
			if candidate.AppProperties["mdoc_target_key"] == key && candidateGeneration == active.Generation {
				matches = append(matches, candidate.ID)
			}
		}
		validClaim := len(matches) == 1 && matches[0] == active.FileID
		if file.Trashed {
			validClaim = len(matches) == 0
		}
		if !validClaim {
			remote.Ambiguous = true
			result[key] = remote
			continue
		}
		comments, err := clients.Drive.ListComments(ctx, active.FileID)
		if err != nil {
			return nil, googleError(err)
		}
		doc, err := clients.Docs.GetDocument(ctx, active.FileID)
		if err != nil {
			return nil, googleError(err)
		}
		remote.UnresolvedComments = comments.Unresolved
		remote.SuggestionsDetected = doc.SuggestionsDetected
		result[key] = remote
	}
	return result, nil
}
