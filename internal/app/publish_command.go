package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"

	"github.com/goliatone/mdoc/internal/config"
	"github.com/goliatone/mdoc/internal/document"
	"github.com/goliatone/mdoc/internal/googleapi"
	"github.com/goliatone/mdoc/internal/planner"
	"github.com/goliatone/mdoc/internal/render"
	"github.com/goliatone/mdoc/internal/report"
	"github.com/goliatone/mdoc/internal/state"
)

func (a *Application) Publish(ctx context.Context, options PublishOptions) error {
	cfg, profile, store, err := a.load(ctx, options.CommonOptions)
	if err != nil {
		return err
	}
	if cfg.Version >= config.ProjectVersion3 {
		return a.publishExplicit(ctx, options, cfg, profile, store)
	}
	options.JSON = cfg.Output.Format == "json"
	options.Quiet = cfg.Output.Quiet
	options.Config = cfg.Path
	options.Profile = profile.Name
	if err := store.Acquire(); err != nil {
		return WrapError(ClassConflict, "state_locked", err.Error(), err)
	}
	defer store.Release()
	if remap, remapErr := store.LoadRemapJournal(); remapErr == nil {
		return NewError(ClassConflict, "remap_resume_required", fmt.Sprintf("unfinished source remap %s to %s must be resumed before publishing", remap.From, remap.To))
	} else if !errors.Is(remapErr, state.ErrNotFound) {
		return partial(remapErr)
	}
	publishState, err := store.Load()
	if err != nil {
		return WrapError(ClassPartial, "state_reconcile_required", err.Error(), err)
	}
	if err := a.verifyStoredAccount(ctx, publishState); err != nil {
		return err
	}
	unfinished, journalErr := store.LoadJournal()
	if journalErr != nil && !errors.Is(journalErr, state.ErrNotFound) {
		return WrapError(ClassPartial, "publish_journal_invalid", fmt.Sprintf("%v; run `mdoc state reconcile --abandon-operation` to clear it after a safe remote scan", journalErr), journalErr)
	}
	recoverUnfinished := func(err error) error {
		if err == nil || unfinished == nil {
			return err
		}
		request := BatchRequest{Profile: profile.Name, ResumeCommand: publishResumeCommand(options.CommonOptions)}
		return WithRecovery(err, "publish_recovery_required", publishRecovery(request, unfinished, store))
	}
	graph, err := document.Discover(ctx, cfg.Root, profile, a.documentRunner(cfg))
	if err != nil {
		return recoverUnfinished(WrapError(ClassValidation, "discovery_failed", err.Error(), err))
	}
	if graph.HasErrors() {
		for _, issue := range graph.Issues {
			fmt.Fprintf(a.Out, "%s %s: %s: %s\n", issue.Severity, issue.Code, issue.Source, issue.Message)
		}
		return recoverUnfinished(NewError(ClassValidation, "local_validation_failed", "local validation failed; fix the reported errors and retry"))
	}
	if a.Auth == nil {
		return recoverUnfinished(NewError(ClassAuthentication, "auth_unavailable", "Google authorization is unavailable; run `mdoc auth login`"))
	}
	client, err := a.Auth.Client(ctx)
	if err != nil {
		return recoverUnfinished(WrapError(ClassAuthentication, "auth_client_failed", err.Error(), err))
	}
	clients, err := a.GoogleFactory.New(ctx, client)
	if err != nil {
		return recoverUnfinished(googleError(err))
	}
	if unfinished != nil {
		return recoverUnfinished(a.resumePublish(ctx, options, cfg, profile, store, publishState, graph, clients, unfinished))
	}
	selected, err := selectedSources(cfg.Root, graph, options.Files)
	if err != nil {
		return NewError(ClassCommand, "source_selection_invalid", err.Error())
	}
	profileBytes, _ := json.Marshal(newProfileFingerprint(cfg, profile))
	locals := make([]planner.LocalDocument, 0, len(graph.Documents))
	for _, doc := range graph.Documents {
		links, outbound, linkErr := provisionalLinks(graph, doc, publishState)
		if linkErr != nil {
			return NewError(ClassConflict, "link_mapping_missing", linkErr.Error())
		}
		request := documentRenderRequest(cfg, profile, graph, doc, profileBytes, links)
		hash, _, hashErr := a.renderer(cfg).Fingerprint(ctx, request)
		local := planner.LocalDocument{SourceKey: doc.SourceKey, Title: doc.Title, SourceHash: doc.SourceHash, RenderHash: hash, Outbound: outbound}
		if hashErr != nil {
			local.Failed = hashErr.Error()
		}
		locals = append(locals, local)
	}
	inspectionKeys := initialInspectionSources(locals, selected)
	remotes, err := a.inspectActiveTargets(ctx, cfg, profile, publishState, clients, inspectionKeys)
	if err != nil {
		return err
	}
	if len(selected) > 0 {
		closure := selectedPublishClosure(locals, publishState, remotes, selected, options.NewReview)
		extra := subtractSources(closure, inspectionKeys)
		if len(extra) > 0 {
			additional, inspectErr := a.inspectActiveTargets(ctx, cfg, profile, publishState, clients, extra)
			if inspectErr != nil {
				return inspectErr
			}
			for key, remote := range additional {
				remotes[key] = remote
			}
		}
	}
	plan := planner.Build(planner.Input{Local: locals, State: publishState, Remote: remotes, Selected: selected, NewReview: options.NewReview, ExpectedReviewFolder: publishState.Folders.ReviewID})
	for _, issue := range graph.Issues {
		if issue.Severity != document.SeverityWarning {
			continue
		}
		for index := range plan.Items {
			if plan.Items[index].SourceKey == issue.Source {
				plan.Items[index].Warnings = append(plan.Items[index].Warnings, fmt.Sprintf("%s: %s", issue.Code, issue.Message))
				break
			}
		}
	}
	entryURL := ""
	if entryState := publishState.Documents[graph.EntryKey]; entryState.ActiveTarget != nil {
		entryURL = entryState.ActiveTarget.URL
	}
	planReport := report.FromPlan(plan, entryURL)
	if plan.Blocked {
		_ = report.WritePlan(a.Out, planReport, options.JSON, options.Quiet)
		for _, item := range plan.Items {
			if item.Action == planner.ActionFailed {
				return NewError(ClassConversion, "publish_plan_failed", fmt.Sprintf("%s: %s", item.SourceKey, item.Reason))
			}
		}
		return NewError(ClassConflict, "publish_plan_blocked", "publish plan is blocked; resolve the reported items or use an explicit new review where allowed")
	}
	if plan.WriteCount() == 0 {
		return report.WritePlan(a.Out, planReport, options.JSON, options.Quiet)
	}
	if publishState.Folders.StagingID == "" || publishState.Folders.ReviewID == "" {
		return NewError(ClassValidation, "folders_missing", "publish folders are not configured; run `mdoc setup`")
	}
	destination, err := a.verifyPublishDestination(ctx, cfg, profile, publishState, clients)
	if err != nil {
		return err
	}
	if options.DryRun {
		return report.WritePlan(a.Out, planReport, options.JSON, options.Quiet)
	}
	a.showPublishDestination(destination)
	operationID, reviewSetID, err := operationIdentity(store)
	if err != nil {
		return err
	}
	actionItems := map[string]planner.Item{}
	for _, item := range plan.Items {
		if item.Action == planner.ActionCreate || item.Action == planner.ActionNewReview {
			actionItems[item.SourceKey] = item
		}
	}
	knownTargets := map[string]string{}
	for source, stored := range publishState.Documents {
		if stored.ActiveTarget != nil {
			knownTargets[source] = stored.ActiveTarget.URL
		}
	}
	batchItems := []BatchItem{}
	renderWarnings := []string{}
	keys := make([]string, 0, len(actionItems))
	for key := range actionItems {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		doc := graph.ByKey[key]
		if doc == nil {
			return NewError(ClassConflict, "resume_source_missing", fmt.Sprintf("unfinished operation source %s is no longer configured", key))
		}
		linkTargets := map[string]string{}
		pending := []PendingLink{}
		for _, link := range doc.Links {
			if link.Image {
				continue
			}
			target, fragment, local := graph.ResolveTarget(doc, link.Target)
			if !local {
				continue
			}
			if _, inBatch := actionItems[target]; inBatch {
				placeholder := PlaceholderURL(target)
				linkTargets[link.Target] = placeholder
				pending = append(pending, PendingLink{OriginalTarget: link.Target, Placeholder: placeholder, TargetSource: target, HadFragment: fragment != ""})
			} else if url := knownTargets[target]; url != "" {
				linkTargets[link.Target] = url
			} else {
				return NewError(ClassConflict, "link_mapping_missing", fmt.Sprintf("%s has no active mapping for %s", key, target))
			}
		}
		renderRequest := documentRenderRequest(cfg, profile, graph, doc, profileBytes, linkTargets)
		hashInput, err := a.renderer(cfg).HashInput(ctx, renderRequest)
		if err != nil {
			return WrapError(ClassConversion, "render_hash_failed", err.Error(), err)
		}
		rendered, err := a.renderer(cfg).Render(ctx, renderRequest)
		if err != nil {
			return WrapError(ClassConversion, "render_failed", err.Error(), err)
		}
		for _, warning := range rendered.Warnings {
			renderWarnings = append(renderWarnings, fmt.Sprintf("%s: %s", key, warning))
		}
		planned := actionItems[key]
		batchItems = append(batchItems, BatchItem{SourceKey: key, Title: doc.Title, DOCX: rendered.DOCX, SourceHash: doc.SourceHash, RenderHash: rendered.RenderHash, HashInput: &hashInput, Generation: planned.Generation, Links: pending})
	}
	result, err := (BatchPublisher{Drive: clients.Drive, Docs: clients.Docs, Store: store, AfterCreate: a.BatchAfterCreate}).Run(ctx, BatchRequest{WorkspaceID: cfg.WorkspaceID, Profile: profile.Name, StagingID: publishState.Folders.StagingID, ReviewID: publishState.Folders.ReviewID, ReviewSetID: reviewSetID, OperationID: operationID, ResumeCommand: publishResumeCommand(options.CommonOptions), Items: batchItems, KnownTargets: knownTargets})
	if err != nil {
		return err
	}
	result.Warnings = append(result.Warnings, renderWarnings...)
	if entry := result.Targets[graph.EntryKey]; entry.URL != "" {
		entryURL = entry.URL
	} else if stored := publishState.Documents[graph.EntryKey]; stored.ActiveTarget != nil {
		entryURL = stored.ActiveTarget.URL
	}
	return report.WritePublish(a.Out, report.PublishReport{Kind: "publish", Plan: planReport, Targets: result.Targets, Warnings: result.Warnings, EntryURL: entryURL}, options.JSON, options.Quiet)
}

func (a *Application) resumePublish(ctx context.Context, options PublishOptions, cfg *config.Config, profile config.SelectedProfile, store *state.Store, publishState *state.State, graph *document.Graph, clients googleapi.Clients, journal *state.Journal) error {
	if journal.Kind != "publish" || journal.WorkspaceID != cfg.WorkspaceID || journal.Profile != profile.Name {
		return NewError(ClassConflict, "unfinished_operation_mismatch", "the unfinished operation does not belong to this workspace and profile; run `mdoc state reconcile --abandon-operation`")
	}
	if journal.StagingID != publishState.Folders.StagingID || journal.ReviewID != publishState.Folders.ReviewID {
		return NewError(ClassConflict, "resume_destination_changed", "publish folder mappings changed after the operation started; restore the prior state to resume, or run `mdoc state reconcile --abandon-operation` to discard the operation after a safe scan")
	}
	profileBytes, _ := json.Marshal(newProfileFingerprint(cfg, profile))
	plan := planner.Plan{}
	type resumeWork struct {
		key       string
		document  *document.Document
		entry     state.JournalEntry
		request   render.Request
		hashInput render.HashInput
		links     []PendingLink
	}
	work := make([]resumeWork, 0, len(journal.Sources))
	for _, key := range journal.Sources {
		entry, ok := journal.Entries[key]
		if !ok {
			return NewError(ClassConflict, "resume_journal_invalid", fmt.Sprintf("unfinished operation is missing %s; run `mdoc state reconcile --abandon-operation`", key))
		}
		doc := graph.ByKey[key]
		if doc == nil {
			return NewError(ClassConflict, "resume_source_missing", fmt.Sprintf("unfinished operation source %s is no longer configured; restore it before retrying", key))
		}
		linkTargets := map[string]string{}
		pending := make([]PendingLink, 0, len(entry.Links))
		for _, link := range entry.Links {
			target := journal.KnownTargets[link.TargetSource]
			if _, inBatch := journal.Entries[link.TargetSource]; inBatch {
				target = link.Placeholder
			}
			if target == "" {
				return NewError(ClassConflict, "resume_link_input_missing", fmt.Sprintf("unfinished operation has no frozen link target for %s to %s; run `mdoc state reconcile --abandon-operation`", key, link.TargetSource))
			}
			linkTargets[link.OriginalTarget] = target
			pending = append(pending, PendingLink{OriginalTarget: link.OriginalTarget, Placeholder: link.Placeholder, TargetSource: link.TargetSource, HadFragment: link.HadFragment})
		}
		renderRequest := documentRenderRequest(cfg, profile, graph, doc, profileBytes, linkTargets)
		hashInput, err := a.renderer(cfg).HashInput(ctx, renderRequest)
		if err != nil {
			return WrapError(ClassConversion, "resume_render_hash_failed", err.Error(), err)
		}
		if doc.SourceHash != entry.SourceHash || render.ComputeHash(hashInput) != entry.RenderHash {
			return NewError(ClassConflict, "resume_inputs_changed", fmt.Sprintf("%s changed after the interrupted publish; restore the source, style, assets, and link inputs used by operation %s before retrying", key, journal.OperationID))
		}
		work = append(work, resumeWork{key: key, document: doc, entry: entry, request: renderRequest, hashInput: hashInput, links: pending})
		action := planner.ActionNewReview
		if entry.Generation == 1 {
			action = planner.ActionCreate
		}
		plan.Items = append(plan.Items, planner.Item{SourceKey: key, Action: action, Generation: entry.Generation, Reason: "resume unfinished operation", Dependent: []planner.Action{planner.ActionActivation}})
	}
	entryURL := ""
	if entryState := publishState.Documents[graph.EntryKey]; entryState.ActiveTarget != nil {
		entryURL = entryState.ActiveTarget.URL
	}
	planReport := report.FromPlan(plan, entryURL)
	destination, err := a.verifyPublishDestination(ctx, cfg, profile, publishState, clients)
	if err != nil {
		return err
	}
	if options.DryRun {
		return report.WritePlan(a.Out, planReport, options.JSON, options.Quiet)
	}
	a.showPublishDestination(destination)
	items := make([]BatchItem, 0, len(work))
	renderWarnings := []string{}
	for _, item := range work {
		rendered, renderErr := a.renderer(cfg).Render(ctx, item.request)
		if renderErr != nil {
			return WrapError(ClassConversion, "resume_render_failed", renderErr.Error(), renderErr)
		}
		if rendered.RenderHash != item.entry.RenderHash || contentHash(rendered.DOCX) != item.entry.DOCXHash {
			return NewError(ClassConflict, "resume_inputs_changed", fmt.Sprintf("%s changed after the interrupted publish; restore the source, style, assets, and link inputs used by operation %s before retrying", item.key, journal.OperationID))
		}
		for _, warning := range rendered.Warnings {
			renderWarnings = append(renderWarnings, fmt.Sprintf("%s: %s", item.key, warning))
		}
		hashInput := item.hashInput
		items = append(items, BatchItem{SourceKey: item.key, Title: item.document.Title, DOCX: rendered.DOCX, SourceHash: item.document.SourceHash, RenderHash: rendered.RenderHash, HashInput: &hashInput, Generation: item.entry.Generation, Links: item.links})
	}
	result, err := (BatchPublisher{Drive: clients.Drive, Docs: clients.Docs, Store: store, AfterCreate: a.BatchAfterCreate}).Run(ctx, BatchRequest{WorkspaceID: cfg.WorkspaceID, Profile: profile.Name, StagingID: journal.StagingID, ReviewID: journal.ReviewID, ReviewSetID: journal.ReviewSetID, OperationID: journal.OperationID, ResumeCommand: publishResumeCommand(options.CommonOptions), Items: items, KnownTargets: cloneStrings(journal.KnownTargets)})
	if err != nil {
		return err
	}
	result.Warnings = append(result.Warnings, renderWarnings...)
	if entry := result.Targets[graph.EntryKey]; entry.URL != "" {
		entryURL = entry.URL
	}
	return report.WritePublish(a.Out, report.PublishReport{Kind: "publish", Plan: planReport, Targets: result.Targets, Warnings: result.Warnings, EntryURL: entryURL}, options.JSON, options.Quiet)
}

func publishResumeCommand(options CommonOptions) string {
	return fmt.Sprintf("mdoc publish --config %s --profile %s", strconv.Quote(options.Config), strconv.Quote(options.Profile))
}

type profileFingerprint struct {
	WorkspaceID     string            `json:"workspace_id"`
	Profile         string            `json:"profile"`
	MaxTableColumns int               `json:"max_table_columns"`
	TitlePolicy     string            `json:"title_policy"`
	HeadingJumps    string            `json:"heading_jumps"`
	LinkPolicy      string            `json:"link_policy"`
	NamingSource    string            `json:"naming_source"`
	NameMapping     map[string]string `json:"name_mapping,omitempty"`
	ImageTypes      []string          `json:"image_types"`
	Reader          string            `json:"reader"`
	HeadingFilter   string            `json:"heading_filter"`
}

func newProfileFingerprint(cfg *config.Config, profile config.SelectedProfile) profileFingerprint {
	return profileFingerprint{
		WorkspaceID: cfg.WorkspaceID, Profile: profile.Name, MaxTableColumns: profile.MaxTableColumns,
		TitlePolicy: profile.TitlePolicy, HeadingJumps: profile.HeadingJumps, LinkPolicy: profile.LinkPolicy,
		NamingSource: profile.NamingSource, NameMapping: profile.NameMapping, ImageTypes: profile.ImageTypes,
		Reader: profile.Reader, HeadingFilter: profile.HeadingFilter,
	}
}

func documentRenderRequest(cfg *config.Config, profile config.SelectedProfile, graph *document.Graph, doc *document.Document, profileBytes []byte, linkTargets map[string]string) render.Request {
	return render.Request{
		SourcePath:         doc.Path,
		ReferenceDOCX:      profile.ReferenceDOCX,
		ResourcePaths:      []string{filepath.Dir(doc.Path)},
		Assets:             graph.AssetPaths(doc),
		ProfileFingerprint: profileBytes,
		LinkTargets:        linkTargets,
		Reader:             profile.Reader,
		HeadingFilter:      profile.HeadingFilter,
		Filters:            profile.Filters,
	}
}

func provisionalLinks(graph *document.Graph, doc *document.Document, publishState *state.State) (map[string]string, []string, error) {
	links := map[string]string{}
	outbound := []string{}
	seen := map[string]bool{}
	for _, link := range doc.Links {
		if link.Image {
			continue
		}
		target, _, local := graph.ResolveTarget(doc, link.Target)
		if !local {
			continue
		}
		if !seen[target] {
			seen[target] = true
			outbound = append(outbound, target)
		}
		if stored := publishState.Documents[target]; stored.ActiveTarget != nil {
			links[link.Target] = stored.ActiveTarget.URL
		} else {
			links[link.Target] = PlaceholderURL(target)
		}
	}
	sort.Strings(outbound)
	return links, outbound, nil
}

func selectedSources(root string, graph *document.Graph, files []string) (map[string]bool, error) {
	if len(files) == 0 {
		return nil, nil
	}
	sources := make([]document.SourceRef, 0, len(graph.ByKey))
	for key, source := range graph.ByKey {
		sources = append(sources, document.SourceRef{Path: source.Path, Key: key})
	}
	result := map[string]bool{}
	for _, file := range files {
		key, err := document.ResolveSelectedSourceKey(root, sources, file)
		if err != nil {
			return nil, err
		}
		result[key] = true
	}
	return result, nil
}

func operationIdentity(store *state.Store) (string, string, error) {
	journal, err := store.LoadJournal()
	if err == nil {
		return journal.OperationID, journal.ReviewSetID, nil
	}
	if !errors.Is(err, state.ErrNotFound) {
		return "", "", partial(err)
	}
	operation, err := randomID()
	if err != nil {
		return "", "", err
	}
	reviewSet, err := randomID()
	return operation, reviewSet, err
}
func randomID() (string, error) {
	data := make([]byte, 16)
	if _, err := rand.Read(data); err != nil {
		return "", err
	}
	return hex.EncodeToString(data), nil
}
