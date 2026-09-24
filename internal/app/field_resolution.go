package app

import (
	"fmt"
	"time"

	"github.com/goliatone/mdoc/internal/config"
	"github.com/goliatone/mdoc/internal/document"
	"github.com/goliatone/mdoc/internal/fields"
	"github.com/goliatone/mdoc/internal/publication"
)

func resolvePublicationFields(cfg *config.Config, profile config.SelectedProfile, publications publication.Set, options CommonOptions) (map[string]fields.Result, []document.Issue) {
	results := map[string]fields.Result{}
	issues := []document.Issue{}
	if cfg.Version < config.ProjectVersion3 {
		return results, issues
	}
	allowedRoots := append([]string(nil), profile.FieldRoots...)
	if len(allowedRoots) == 0 {
		allowedRoots = []string{cfg.Root}
	}
	for _, target := range publications.Items {
		resolved, err := fields.Resolve(fields.ResolveOptions{
			PublicationID:     target.ID,
			Defaults:          profile.FieldDefaults,
			ConfiguredFiles:   target.FieldFiles,
			PublicationFields: target.Fields,
			CommandFiles:      options.FieldFiles,
			Assignments:       options.FieldAssignments,
			ProjectRoot:       cfg.Root,
			AllowedRoots:      allowedRoots,
		})
		if err != nil {
			issues = append(issues, document.Issue{Severity: document.SeverityError, Code: "publication_fields_invalid", Source: target.ID, Message: err.Error()})
			continue
		}
		results[target.ID] = resolved
		if err := validatePublicationTemplates(cfg, profile, target, resolved.Values); err != nil {
			issues = append(issues, document.Issue{Severity: document.SeverityError, Code: "publication_template_invalid", Source: target.ID, Message: err.Error()})
		}
	}
	return results, issues
}

func validatePublicationTemplates(cfg *config.Config, profile config.SelectedProfile, target *publication.Publication, values map[string]any) error {
	computed := fields.FreezeComputed(fields.PlanComputedValues(fields.PlanComputed{
		ProjectID: cfg.WorkspaceID, Profile: profile.Name, PublicationID: target.ID, PublicationKind: string(target.Kind), PublicationTitle: target.Title,
	}), fields.FrozenComputed{OperationID: "validation", ReviewSetID: "validation", Generation: 1, PublishedAt: time.Unix(0, 0).UTC()})
	templates := []struct {
		name      string
		source    string
		allowPage bool
	}{
		{"publication.title", target.Title, false},
		{"cover.title", target.Layout.Cover.Title, false},
		{"cover.subtitle", target.Layout.Cover.Subtitle, false},
		{"cover.prose", target.Layout.Cover.Prose, false},
		{"header.left", target.Layout.Header.Left, true},
		{"header.center", target.Layout.Header.Center, true},
		{"header.right", target.Layout.Header.Right, true},
		{"footer.left", target.Layout.Footer.Left, true},
		{"footer.center", target.Layout.Footer.Center, true},
		{"footer.right", target.Layout.Footer.Right, true},
	}
	if first := target.Layout.Header.FirstPage; first != nil {
		templates = append(templates, struct {
			name      string
			source    string
			allowPage bool
		}{"header.first_page.left", first.Left, true}, struct {
			name      string
			source    string
			allowPage bool
		}{"header.first_page.center", first.Center, true}, struct {
			name      string
			source    string
			allowPage bool
		}{"header.first_page.right", first.Right, true})
	}
	if first := target.Layout.Footer.FirstPage; first != nil {
		templates = append(templates, struct {
			name      string
			source    string
			allowPage bool
		}{"footer.first_page.left", first.Left, true}, struct {
			name      string
			source    string
			allowPage bool
		}{"footer.first_page.center", first.Center, true}, struct {
			name      string
			source    string
			allowPage bool
		}{"footer.first_page.right", first.Right, true})
	}
	for index, member := range target.Members {
		templates = append(templates, struct {
			name      string
			source    string
			allowPage bool
		}{fmt.Sprintf("member.%d.appendix_title", index+1), member.Config.AppendixTitle, false}, struct {
			name      string
			source    string
			allowPage bool
		}{fmt.Sprintf("member.%d.appendix_prefix", index+1), member.Config.AppendixPrefix, false})
	}
	for _, candidate := range templates {
		if candidate.source == "" {
			continue
		}
		parsed, err := fields.ParseTemplate(candidate.name, candidate.source)
		if err != nil {
			return fmt.Errorf("%s: %w", candidate.name, err)
		}
		if _, err := parsed.Evaluate(fields.TemplateContext{PublicationID: target.ID, Fields: values, Computed: computed, AllowPageFields: candidate.allowPage}); err != nil {
			return err
		}
	}
	return nil
}

func fieldSummaries(results map[string]fields.Result) map[string]fields.Summary {
	if len(results) == 0 {
		return nil
	}
	summaries := make(map[string]fields.Summary, len(results))
	for id, result := range results {
		summaries[id] = result.Summary
	}
	return summaries
}
