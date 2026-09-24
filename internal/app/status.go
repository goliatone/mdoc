package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/goliatone/mdoc/internal/config"
	"github.com/goliatone/mdoc/internal/document"
	"github.com/goliatone/mdoc/internal/publication"
	"github.com/goliatone/mdoc/internal/report"
	"github.com/goliatone/mdoc/internal/state"
)

func (a *Application) Status(ctx context.Context, options CommonOptions) error {
	if options.Remote {
		return a.remoteStatus(ctx, options)
	}
	return a.localStatus(ctx, options)
}

func (a *Application) remoteStatus(ctx context.Context, options CommonOptions) error {
	options.AllowDirect = false
	cfg, profile, store, err := a.load(ctx, options)
	if err != nil {
		return err
	}
	jsonOutput := cfg.Output.Format == "json"
	quiet := cfg.Output.Quiet
	value := report.RemoteStatusReport{
		SchemaVersion: 1, Kind: "remote_status", Result: "ok", Project: cfg.Path, Profile: profile.Name,
		Checks: map[string]report.StatusCheck{
			"config":    {State: report.CheckPassed},
			"discovery": {State: report.CheckPassed},
			"safety":    {State: report.CheckPassed},
			"pandoc":    {State: report.CheckPassed},
		},
	}
	if cfg.Version >= config.ProjectVersion3 {
		value.SchemaVersion = 2
	}
	selected, err := document.SelectSources(cfg.Root, profile)
	if err != nil {
		return WrapError(ClassValidation, "remote_status_discovery_invalid", err.Error(), err)
	}
	for _, source := range selected {
		value.Files = append(value.Files, source.Key)
	}
	value.Documents = len(selected)
	if cfg.Version >= config.ProjectVersion3 {
		value.Entry = "publication:" + profile.EntryPublication
	} else {
		value.Entry, err = document.ResolveEntrySourceKey(cfg.Root, selected, profile.Entry)
	}
	if len(selected) == 0 {
		value.Result = "invalid"
		value.Checks["discovery"] = report.StatusCheck{State: report.CheckFailed, Message: "source patterns matched no files"}
		_ = report.WriteRemoteStatus(a.Out, value, jsonOutput, quiet)
		return NewError(ClassValidation, "remote_status_no_sources", "source patterns matched no files")
	}
	if err != nil {
		value.Result = "invalid"
		value.Checks["discovery"] = report.StatusCheck{State: report.CheckFailed, Message: err.Error()}
		_ = report.WriteRemoteStatus(a.Out, value, jsonOutput, quiet)
		return WrapError(ClassValidation, "remote_status_entry_invalid", err.Error(), err)
	}
	publishState, stateErr := store.Load()
	var graph *document.Graph
	var publications publication.Set
	var publicationBuilds map[string]publicationBuild
	localInvalid := false
	if pandocErr := a.pandocCapability(ctx, cfg); pandocErr != nil {
		value.Result = "partial"
		value.Checks["pandoc"] = report.StatusCheck{State: report.CheckUnavailable, Message: pandocErr.Error()}
		value.Checks["validation"] = report.StatusCheck{State: report.CheckUnavailable, Message: "Pandoc is required for Markdown validation"}
		value.Checks["local"] = report.StatusCheck{State: report.CheckUnavailable, Message: "local validation is unavailable"}
	} else {
		value.Checks["pandoc"] = report.StatusCheck{State: report.CheckPassed}
		discovered, discoverErr := document.Discover(ctx, cfg.Root, profile, a.documentRunner(cfg))
		if discoverErr != nil {
			return WrapError(ClassValidation, "remote_status_discovery_invalid", discoverErr.Error(), discoverErr)
		}
		graph = discovered
		publications = publication.Resolve(cfg.Version, profile, graph)
		graph.Issues = append(graph.Issues, publications.Issues...)
		resolvedFields, fieldIssues := resolvePublicationFields(cfg, profile, publications, options)
		graph.Issues = append(graph.Issues, fieldIssues...)
		if cfg.Version >= config.ProjectVersion3 && !graph.HasErrors() {
			var externalTargets map[string]string
			if stateErr == nil {
				externalTargets = activePublicationURLs(publishState)
			}
			var buildIssues []document.Issue
			publicationBuilds, buildIssues = a.buildPublicationInputs(ctx, cfg, profile, graph, publications, resolvedFields, options.Bundle, externalTargets)
			graph.Issues = append(graph.Issues, buildIssues...)
		}
		value.FieldSummaries = fieldSummaries(resolvedFields)
		value.Documents = len(graph.Documents)
		value.Entry = graph.EntryKey
		if cfg.Version >= config.ProjectVersion3 {
			value.Entry = "publication:" + profile.EntryPublication
			value.Targets = publicationSummaries(publications, options.Bundle)
		}
		value.Issues = append(value.Issues, graph.Issues...)
		localInvalid = graph.HasErrors()
		if localInvalid {
			value.Result = "invalid"
			value.Checks["validation"] = report.StatusCheck{State: report.CheckFailed, Message: "document validation failed"}
			value.Checks["local"] = report.StatusCheck{State: report.CheckFailed, Message: "local validation failed"}
		} else {
			value.Checks["validation"] = report.StatusCheck{State: report.CheckPassed}
			value.Checks["local"] = report.StatusCheck{State: report.CheckPassed}
		}
	}
	if profile.ReferenceDOCX == "" {
		value.Checks["style"] = report.StatusCheck{State: report.CheckNotApplicable, Message: "using neutral Pandoc style"}
	} else if info, styleErr := os.Stat(profile.ReferenceDOCX); styleErr != nil || info.IsDir() {
		if value.Result == "ok" {
			value.Result = "partial"
		}
		value.Checks["style"] = report.StatusCheck{State: report.CheckUnavailable, Message: fmt.Sprintf("reference DOCX %q is unavailable", profile.ReferenceDOCX)}
	} else {
		value.Checks["style"] = report.StatusCheck{State: report.CheckPassed}
	}
	switch {
	case errors.Is(stateErr, state.ErrNotFound):
		value.Checks["state"] = report.StatusCheck{State: report.CheckUnavailable, Message: "publish state does not exist"}
	case stateErr != nil:
		value.Checks["state"] = report.StatusCheck{State: report.CheckUnavailable, Message: stateErr.Error()}
	default:
		value.Checks["state"] = report.StatusCheck{State: report.CheckPassed}
		if cfg.Version >= config.ProjectVersion3 && graph != nil {
			value.Changes = localPublicationChanges(publications, publicationBuilds, publishState, options.Bundle)
		} else {
			value.Changes = a.localChanges(ctx, cfg, profile, selected, graph, publishState)
		}
	}
	sort.Strings(value.Files)
	if cfg.Version >= config.ProjectVersion3 {
		if len(value.Targets) == 0 {
			value.Targets = configuredReviewTargetSummaries(profile, options.Bundle)
		}
		value.Targets, value.ReviewOrphans, value.Checks["review_pull"] = localReviewDiagnostics(cfg, profile, publishState, store, value.Targets)
	}
	if localInvalid {
		value.Targets = blockRemoteReviewDiagnostics(value.Targets, "local validation failed; repair the publication before pull")
		_ = report.WriteRemoteStatus(a.Out, value, jsonOutput, quiet)
		return NewError(ClassValidation, "remote_status_local_invalid", "local validation failed")
	}
	remoteIssues, err := a.validateRemote(ctx, cfg, profile, store, graph, options.Bundle)
	if err != nil {
		value.Result = "invalid"
		value.Checks["remote"] = report.StatusCheck{State: report.CheckFailed, Message: err.Error()}
		value.Targets = blockRemoteReviewDiagnostics(value.Targets, "remote validation failed; repair the target before pull")
		_ = report.WriteRemoteStatus(a.Out, value, jsonOutput, quiet)
		return err
	}
	value.Issues = append(value.Issues, remoteIssues...)
	value.Targets = remoteReviewDiagnostics(value.Targets, remoteIssues)
	for _, issue := range remoteIssues {
		if issue.Severity == document.SeverityError {
			value.Result = "invalid"
		}
	}
	if value.Result == "invalid" {
		value.Checks["remote"] = report.StatusCheck{State: report.CheckFailed, Message: "remote checks failed"}
	} else {
		value.Checks["remote"] = report.StatusCheck{State: report.CheckPassed}
	}
	if err := report.WriteRemoteStatus(a.Out, value, jsonOutput, quiet); err != nil {
		return err
	}
	if value.Result == "invalid" {
		return NewError(ClassValidation, "remote_status_invalid", "remote status found errors")
	}
	return nil
}

func (a *Application) localStatus(ctx context.Context, options CommonOptions) error {
	if output, outputErr := config.ResolveOutput(config.ResolverOptions{ProjectPath: options.Config, Overrides: options.Overrides}); outputErr == nil {
		if !options.JSON {
			options.JSON = output.Format == "json"
		}
		if !options.Quiet {
			options.Quiet = output.Quiet
		}
	}
	cfg, profile, store, err := a.load(ctx, options)
	if err != nil {
		value := report.LocalStatusReport{
			SchemaVersion: 1,
			Kind:          "local_status",
			Result:        "invalid",
			Profile:       options.Profile,
			Files:         []string{},
			Checks: map[string]report.StatusCheck{
				"config": {State: report.CheckFailed, Message: err.Error()},
			},
		}
		_ = report.WriteLocalStatus(a.Out, value, options.JSON, options.Quiet)
		return WrapError(ClassValidation, "local_status_config_invalid", err.Error(), err)
	}
	jsonOutput := cfg.Output.Format == "json"
	quiet := cfg.Output.Quiet
	var publishState *state.State
	var stateErr error
	if store != nil {
		publishState, stateErr = store.Load()
	}
	status := report.LocalStatusReport{
		SchemaVersion: 1, Kind: "local_status", Result: "ok", Project: cfg.Path, Profile: profile.Name,
		Checks: map[string]report.StatusCheck{
			"config":    {State: report.CheckPassed},
			"discovery": {State: report.CheckPassed},
			"safety":    {State: report.CheckPassed},
		},
	}
	if cfg.Version >= config.ProjectVersion3 {
		status.SchemaVersion = 2
	}
	selected, selectionErr := document.SelectSources(cfg.Root, profile)
	if selectionErr != nil {
		status.Result = "invalid"
		status.Checks["discovery"] = report.StatusCheck{State: report.CheckFailed, Message: selectionErr.Error()}
		if isSourceSafetyError(selectionErr) {
			status.Checks["safety"] = report.StatusCheck{State: report.CheckFailed, Message: selectionErr.Error()}
		}
		_ = report.WriteLocalStatus(a.Out, status, jsonOutput, quiet)
		return WrapError(ClassValidation, "local_status_discovery_invalid", selectionErr.Error(), selectionErr)
	}
	for _, source := range selected {
		status.Files = append(status.Files, source.Key)
	}
	if len(selected) == 0 {
		status.Result = "invalid"
		status.Checks["discovery"] = report.StatusCheck{State: report.CheckFailed, Message: "source patterns matched no files"}
		_ = report.WriteLocalStatus(a.Out, status, jsonOutput, quiet)
		return NewError(ClassValidation, "local_status_no_sources", "source patterns matched no files")
	}
	if cfg.Version >= config.ProjectVersion3 {
		status.Entry = "publication:" + profile.EntryPublication
	} else {
		status.Entry, selectionErr = document.ResolveEntrySourceKey(cfg.Root, selected, profile.Entry)
	}
	if selectionErr != nil {
		status.Result = "invalid"
		status.Checks["discovery"] = report.StatusCheck{State: report.CheckFailed, Message: selectionErr.Error()}
		_ = report.WriteLocalStatus(a.Out, status, jsonOutput, quiet)
		return WrapError(ClassValidation, "local_status_entry_invalid", selectionErr.Error(), selectionErr)
	}

	var graph *document.Graph
	var publications publication.Set
	var publicationBuilds map[string]publicationBuild
	pandocErr := a.pandocCapability(ctx, cfg)
	if pandocErr != nil {
		status.Result = "partial"
		status.Checks["pandoc"] = report.StatusCheck{State: report.CheckUnavailable, Message: pandocErr.Error()}
		status.Checks["validation"] = report.StatusCheck{State: report.CheckUnavailable, Message: "Pandoc is required for Markdown validation"}
	} else {
		status.Checks["pandoc"] = report.StatusCheck{State: report.CheckPassed}
		discovered, discoverErr := document.Discover(ctx, cfg.Root, profile, a.documentRunner(cfg))
		if discoverErr != nil {
			status.Result = "invalid"
			status.Checks["validation"] = report.StatusCheck{State: report.CheckFailed, Message: discoverErr.Error()}
			_ = report.WriteLocalStatus(a.Out, status, jsonOutput, quiet)
			return WrapError(ClassValidation, "local_status_validation_invalid", discoverErr.Error(), discoverErr)
		}
		graph = discovered
		publications = publication.Resolve(cfg.Version, profile, graph)
		graph.Issues = append(graph.Issues, publications.Issues...)
		resolvedFields, fieldIssues := resolvePublicationFields(cfg, profile, publications, options)
		graph.Issues = append(graph.Issues, fieldIssues...)
		if cfg.Version >= config.ProjectVersion3 && !graph.HasErrors() {
			var externalTargets map[string]string
			if stateErr == nil {
				externalTargets = activePublicationURLs(publishState)
			}
			var buildIssues []document.Issue
			publicationBuilds, buildIssues = a.buildPublicationInputs(ctx, cfg, profile, graph, publications, resolvedFields, options.Bundle, externalTargets)
			graph.Issues = append(graph.Issues, buildIssues...)
		}
		status.FieldSummaries = fieldSummaries(resolvedFields)
		status.Entry = graph.EntryKey
		if cfg.Version >= config.ProjectVersion3 {
			status.Entry = "publication:" + profile.EntryPublication
			status.Targets = publicationSummaries(publications, options.Bundle)
		}
		status.Issues = graph.Issues
		if graph.HasErrors() {
			status.Result = "invalid"
			status.Checks["validation"] = report.StatusCheck{State: report.CheckFailed, Message: "document validation failed"}
		} else {
			status.Checks["validation"] = report.StatusCheck{State: report.CheckPassed}
		}
	}

	if profile.ReferenceDOCX == "" {
		status.Checks["style"] = report.StatusCheck{State: report.CheckNotApplicable, Message: "using neutral Pandoc style"}
	} else if info, styleErr := os.Stat(profile.ReferenceDOCX); styleErr != nil || info.IsDir() {
		if status.Result == "ok" {
			status.Result = "partial"
		}
		status.Checks["style"] = report.StatusCheck{State: report.CheckUnavailable, Message: fmt.Sprintf("reference DOCX %q is unavailable", profile.ReferenceDOCX)}
	} else {
		status.Checks["style"] = report.StatusCheck{State: report.CheckPassed}
	}

	if store == nil {
		status.Checks["state"] = report.StatusCheck{State: report.CheckNotApplicable, Message: "direct mode does not use publish state"}
	} else {
		switch {
		case errors.Is(stateErr, state.ErrNotFound):
			if status.Result == "ok" {
				status.Result = "partial"
			}
			status.Checks["state"] = report.StatusCheck{State: report.CheckUnavailable, Message: "publish state does not exist"}
		case stateErr != nil:
			if status.Result == "ok" {
				status.Result = "partial"
			}
			status.Checks["state"] = report.StatusCheck{State: report.CheckUnavailable, Message: stateErr.Error()}
		default:
			status.Checks["state"] = report.StatusCheck{State: report.CheckPassed}
			if cfg.Version >= config.ProjectVersion3 && graph != nil {
				status.Changes = localPublicationChanges(publications, publicationBuilds, publishState, options.Bundle)
			} else {
				status.Changes = a.localChanges(ctx, cfg, profile, selected, graph, publishState)
			}
		}
	}
	if cfg.Version >= config.ProjectVersion3 {
		if len(status.Targets) == 0 {
			status.Targets = configuredReviewTargetSummaries(profile, options.Bundle)
		}
		status.Targets, status.ReviewOrphans, status.Checks["review_pull"] = localReviewDiagnostics(cfg, profile, publishState, store, status.Targets)
	}
	sort.Strings(status.Files)
	if err := report.WriteLocalStatus(a.Out, status, jsonOutput, quiet); err != nil {
		return err
	}
	if status.Result == "invalid" {
		return NewError(ClassValidation, "local_status_invalid", "local status found validation errors")
	}
	return nil
}

func isSourceSafetyError(err error) bool {
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "outside") || strings.Contains(message, "symlink") || strings.Contains(message, "casing")
}

func localPublicationChanges(publications publication.Set, builds map[string]publicationBuild, publishState *state.State, bundleID string) []report.LocalChange {
	result := []report.LocalChange{}
	for _, target := range publications.Items {
		if bundleID != "" && target.ID != bundleID {
			continue
		}
		change := report.LocalChange{Target: string(target.Target), PublicationID: target.ID, PublicationKind: string(target.Kind), State: "unknown"}
		build, built := builds[string(target.Target)]
		stored := publishState.Targets[string(target.Target)]
		switch {
		case !built:
			change.State = "unknown"
		case stored.ActiveTarget == nil:
			change.State = "unpublished"
		case stored.SourceHash != build.local.SourceHash || stored.RenderHash != build.local.RenderHash:
			change.State = "modified"
		default:
			change.State = "unchanged"
		}
		result = append(result, change)
	}
	return result
}

func (a *Application) pandocCapability(ctx context.Context, cfg *config.Config) error {
	if a.DocumentRunner != nil {
		return nil
	}
	return a.renderer(cfg).ValidatePandoc(ctx)
}

func (a *Application) localChanges(ctx context.Context, cfg *config.Config, profile config.SelectedProfile, selected []document.SourceRef, graph *document.Graph, publishState *state.State) []report.LocalChange {
	result := []report.LocalChange{}
	seen := map[string]bool{}
	profileBytes, _ := json.Marshal(newProfileFingerprint(cfg, profile))
	for _, source := range selected {
		seen[source.Key] = true
		data, err := os.ReadFile(source.Path)
		change := "unknown"
		if err == nil {
			stored, exists := publishState.Documents[source.Key]
			switch {
			case !exists:
				change = "new"
			case stored.SourceHash != document.SourceHash(data):
				change = "modified"
			case graph == nil || graph.ByKey[source.Key] == nil:
				change = "unknown"
			default:
				doc := graph.ByKey[source.Key]
				links, _, linkErr := provisionalLinks(graph, doc, publishState)
				if linkErr != nil {
					break
				}
				request := documentRenderRequest(cfg, profile, graph, doc, profileBytes, links)
				hash, _, hashErr := a.renderer(cfg).Fingerprint(ctx, request)
				if hashErr == nil {
					if hash == stored.RenderHash {
						change = "unchanged"
					} else {
						change = "modified"
					}
				}
			}
		}
		result = append(result, report.LocalChange{Source: source.Key, State: change})
	}
	for key := range publishState.Documents {
		if !seen[key] {
			result = append(result, report.LocalChange{Source: key, State: "removed"})
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Source < result[j].Source })
	return result
}
