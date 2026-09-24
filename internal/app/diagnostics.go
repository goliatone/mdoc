package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/goliatone/mdoc/internal/config"
	"github.com/goliatone/mdoc/internal/document"
	"github.com/goliatone/mdoc/internal/publication"
	"github.com/goliatone/mdoc/internal/report"
	"github.com/goliatone/mdoc/internal/state"
)

func (a *Application) Plan(ctx context.Context, options PublishOptions) error {
	options.DryRun = true
	return a.Publish(ctx, options)
}

func (a *Application) Doctor(ctx context.Context, options DoctorOptions) error {
	cfg, profile, store, err := a.load(ctx, options.CommonOptions)
	if err != nil {
		return WrapError(ClassValidation, "doctor_config_invalid", err.Error(), err)
	}
	value := report.DoctorReport{
		SchemaVersion: 1, Kind: "doctor", Result: "ready", Project: cfg.Path, Profile: profile.Name,
		Checks: map[string]report.StatusCheck{"config": {State: report.CheckPassed}},
	}
	if cfg.Version >= config.ProjectVersion3 {
		value.SchemaVersion = 2
	}
	fail := func(name, message string) {
		value.Result = "not_ready"
		value.Checks[name] = report.StatusCheck{State: report.CheckFailed, Message: message}
	}
	if options.Bundle != "" {
		configured, ok := profile.Publications[options.Bundle]
		if !ok || configured.Kind != config.PublicationBundle {
			fail("publication", fmt.Sprintf("bundle publication %q does not exist", options.Bundle))
		} else {
			value.Checks["publication"] = report.StatusCheck{State: report.CheckPassed}
		}
	}
	selected, selectionErr := document.SelectSources(cfg.Root, profile)
	if selectionErr != nil {
		fail("discovery", selectionErr.Error())
	} else if len(selected) == 0 {
		fail("discovery", "source patterns matched no files")
	} else {
		value.Checks["discovery"] = report.StatusCheck{State: report.CheckPassed}
	}
	pandocReady := false
	if pandocErr := a.renderer(cfg).ValidatePandoc(ctx); pandocErr != nil {
		fail("pandoc", pandocErr.Error())
	} else {
		value.Checks["pandoc"] = report.StatusCheck{State: report.CheckPassed}
		pandocReady = true
	}
	if cfg.Version >= config.ProjectVersion3 && pandocReady && selectionErr == nil && len(selected) > 0 {
		graph, discoverErr := document.Discover(ctx, cfg.Root, profile, a.documentRunner(cfg))
		if discoverErr != nil {
			fail("publication", discoverErr.Error())
		} else {
			publications := publication.Resolve(cfg.Version, profile, graph)
			graph.Issues = append(graph.Issues, publications.Issues...)
			resolvedFields, fieldIssues := resolvePublicationFields(cfg, profile, publications, options.CommonOptions)
			graph.Issues = append(graph.Issues, fieldIssues...)
			if !graph.HasErrors() {
				graph.Issues = append(graph.Issues, a.validatePublicationBuildInputs(ctx, cfg, profile, graph, publications, resolvedFields, options.Bundle)...)
			}
			if graph.HasErrors() {
				fail("publication", "publication render inputs are invalid")
			} else if options.Bundle != "" {
				value.Checks["publication"] = report.StatusCheck{State: report.CheckPassed}
			}
		}
	}
	if profile.ReferenceDOCX == "" {
		value.Checks["style"] = report.StatusCheck{State: report.CheckNotApplicable, Message: "using neutral Pandoc style"}
	} else if styleErr := a.renderer(cfg).ValidateStyle(ctx, profile.ReferenceDOCX); styleErr != nil {
		fail("style", fmt.Sprintf("reference DOCX %q is invalid: %v", profile.ReferenceDOCX, styleErr))
	} else {
		value.Checks["style"] = report.StatusCheck{State: report.CheckPassed}
	}
	if store == nil {
		value.Checks["auth"] = report.StatusCheck{State: report.CheckNotApplicable, Message: "direct mode does not use Google auth"}
		value.Checks["destination"] = report.StatusCheck{State: report.CheckNotApplicable, Message: "direct mode has no Google destination"}
	} else if a.Auth == nil {
		fail("auth", "Google authorization is unavailable; run mdoc auth login")
		value.Checks["destination"] = report.StatusCheck{State: report.CheckUnavailable, Message: "auth is required"}
	} else {
		client, authErr := a.Auth.Client(ctx)
		if authErr != nil {
			fail("auth", authErr.Error())
			value.Checks["destination"] = report.StatusCheck{State: report.CheckUnavailable, Message: "auth is required"}
		} else if account, accountErr := a.currentAccount(ctx); accountErr != nil {
			fail("auth", accountErr.Error())
			value.Checks["destination"] = report.StatusCheck{State: report.CheckUnavailable, Message: "account identity is required"}
		} else {
			value.Checks["auth"] = report.StatusCheck{State: report.CheckPassed}
			clients, clientErr := a.GoogleFactory.New(ctx, client)
			if clientErr != nil {
				fail("destination", clientErr.Error())
			} else {
				publishState, stateErr := store.Load()
				if errors.Is(stateErr, state.ErrNotFound) {
					fail("destination", "publish state is missing; run mdoc setup")
				} else if stateErr != nil {
					fail("destination", stateErr.Error())
				} else {
					var readinessErr error
					if accountMatchErr := verifyAccountMatch(publishState, account); accountMatchErr != nil {
						readinessErr = accountMatchErr
					}
					resolvedIDs := map[string]string{}
					for _, role := range []string{"staging", "review"} {
						folders, folderErr := clients.Drive.FindFolders(ctx, cfg.WorkspaceID, profile.Name, role)
						if folderErr != nil {
							readinessErr = folderErr
							break
						}
						if len(folders) != 1 {
							readinessErr = fmt.Errorf("expected one %s folder; found %d", role, len(folders))
							break
						}
						expected := publishState.Folders.StagingID
						if role == "review" {
							expected = publishState.Folders.ReviewID
						}
						if err := validateMappedDestinationFolder(folders[0], account, cfg, profile, expected, role); err != nil {
							readinessErr = err
							break
						}
						resolvedIDs[role] = folders[0].ID
					}
					if resolvedIDs["staging"] != "" && resolvedIDs["staging"] == resolvedIDs["review"] {
						readinessErr = errors.New("staging and review folders resolve to the same Google folder")
					}
					if readinessErr == nil {
						value.Checks["destination"] = report.StatusCheck{State: report.CheckPassed}
					} else {
						fail("destination", readinessErr.Error()+"; run mdoc setup")
					}
				}
			}
		}
	}
	jsonOutput := cfg.Output.Format == "json"
	if err := report.WriteDoctor(a.Out, value, jsonOutput, cfg.Output.Quiet); err != nil {
		return err
	}
	if value.Result != "ready" {
		return NewError(ClassValidation, "doctor_not_ready", "doctor found missing required capabilities")
	}
	return nil
}
