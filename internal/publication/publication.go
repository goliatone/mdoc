package publication

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/goliatone/mdoc/internal/config"
	"github.com/goliatone/mdoc/internal/document"
	"github.com/goliatone/mdoc/internal/sourcekey"
)

type TargetKey string

type Kind string

const (
	KindSource Kind = "source"
	KindBundle Kind = "bundle"
)

type Member struct {
	SourceKey string
	Document  *document.Document
	Config    config.MemberConfig
}

type ReferenceLink struct {
	ID                  string
	Pattern             string
	DefinitionSourceKey string
	DefinitionMatch     string
	Unresolved          string
}

type Publication struct {
	ID                string
	Kind              Kind
	Target            TargetKey
	Title             string
	Source            *document.Document
	Members           []Member
	ReferenceLinks    []ReferenceLink
	Fields            map[string]any
	FieldFiles        []string
	Layout            config.LayoutConfig
	HeadingPolicy     string
	FragmentPolicy    string
	AllowedRoots      []string
	Explicit          bool
	ReviewPullEnabled bool
}

type Set struct {
	Items    []*Publication
	ByID     map[string]*Publication
	ByTarget map[TargetKey]*Publication
	Entry    *Publication
	Issues   []document.Issue
}

func ExplicitTargetKey(id string) TargetKey {
	return TargetKey("publication:" + id)
}

func LegacySourceTargetKey(key string) TargetKey {
	return TargetKey("source:" + key)
}

func Resolve(version int, profile config.SelectedProfile, graph *document.Graph) Set {
	if version < config.ProjectVersion3 {
		return normalizeLegacy(graph)
	}
	return resolveExplicit(profile, graph)
}

func normalizeLegacy(graph *document.Graph) Set {
	result := Set{ByID: map[string]*Publication{}, ByTarget: map[TargetKey]*Publication{}}
	for _, source := range graph.Documents {
		publication := &Publication{
			ID: source.SourceKey, Kind: KindSource, Target: LegacySourceTargetKey(source.SourceKey),
			Title: source.Title, Source: source,
		}
		result.Items = append(result.Items, publication)
		result.ByID[publication.ID] = publication
		result.ByTarget[publication.Target] = publication
		if source.SourceKey == graph.EntryKey {
			result.Entry = publication
		}
	}
	return result
}

func resolveExplicit(profile config.SelectedProfile, graph *document.Graph) Set {
	result := Set{ByID: map[string]*Publication{}, ByTarget: map[TargetKey]*Publication{}}
	ids := make([]string, 0, len(profile.Publications))
	for id := range profile.Publications {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		configured := profile.Publications[id]
		publication := &Publication{
			ID: id, Target: ExplicitTargetKey(id), Title: configured.Title, Explicit: true,
			Fields: configured.Fields, FieldFiles: append([]string(nil), configured.FieldFiles...), Layout: configured.Layout,
			HeadingPolicy: profile.HeadingJumps, FragmentPolicy: configured.FragmentPolicy,
			AllowedRoots:      append([]string(nil), graph.AllowedRoots...),
			ReviewPullEnabled: configured.Review.Pull.Enabled,
		}
		if publication.FragmentPolicy == "" {
			publication.FragmentPolicy = "error"
		}
		switch configured.Kind {
		case config.PublicationSource:
			publication.Kind = KindSource
			publication.Source = resolveDocument(configured.Source, graph, id, 0, &result.Issues)
			if publication.Source != nil && publication.Title == "" {
				publication.Title = publication.Source.Title
			}
		case config.PublicationBundle:
			publication.Kind = KindBundle
			seen := map[string]struct{}{}
			for index, memberConfig := range configured.Members {
				memberDocument := resolveDocument(memberConfig.Source, graph, id, index+1, &result.Issues)
				if memberDocument == nil {
					continue
				}
				if _, exists := seen[memberDocument.SourceKey]; exists {
					result.Issues = append(result.Issues, document.Issue{Severity: document.SeverityError, Code: "publication_member_duplicate", Source: memberDocument.SourceKey, Message: fmt.Sprintf("publication %q includes source %q more than once", id, memberDocument.SourceKey)})
					continue
				}
				seen[memberDocument.SourceKey] = struct{}{}
				publication.Members = append(publication.Members, Member{SourceKey: memberDocument.SourceKey, Document: memberDocument, Config: memberConfig})
			}
			for _, configuredRule := range configured.ReferenceLinks {
				definition := resolveDocument(configuredRule.Definition.Source, graph, id, 0, &result.Issues)
				if definition == nil {
					continue
				}
				if _, exists := seen[definition.SourceKey]; !exists {
					result.Issues = append(result.Issues, document.Issue{
						Severity: document.SeverityError,
						Code:     "publication_reference_source_not_member",
						Source:   definition.SourceKey,
						Message:  fmt.Sprintf("publication %q reference rule %q definition source %q is not a bundle member", id, configuredRule.ID, definition.SourceKey),
					})
					continue
				}
				publication.ReferenceLinks = append(publication.ReferenceLinks, ReferenceLink{
					ID: configuredRule.ID, Pattern: configuredRule.Pattern,
					DefinitionSourceKey: definition.SourceKey,
					DefinitionMatch:     configuredRule.Definition.Match,
					Unresolved:          configuredRule.Unresolved,
				})
			}
			normalizeTOCMembers(publication, &result.Issues)
			if publication.Title == "" {
				publication.Title = derivedBundleTitle(publication.Members)
			}
		}
		result.Items = append(result.Items, publication)
		result.ByID[id] = publication
		if previous, exists := result.ByTarget[publication.Target]; exists {
			result.Issues = append(result.Issues, document.Issue{Severity: document.SeverityError, Code: "publication_target_collision", Message: fmt.Sprintf("publications %q and %q resolve to target %q", previous.ID, id, publication.Target)})
		} else {
			result.ByTarget[publication.Target] = publication
		}
	}
	if entry, ok := result.ByID[profile.EntryPublication]; ok {
		result.Entry = entry
	} else {
		result.Issues = append(result.Issues, document.Issue{Severity: document.SeverityError, Code: "publication_entry_missing", Message: fmt.Sprintf("entry publication %q is not available", profile.EntryPublication)})
	}
	return result
}

func normalizeTOCMembers(publication *Publication, issues *[]document.Issue) {
	if publication == nil || len(publication.Layout.TableOfContents.Members) == 0 {
		return
	}
	byPath := map[string]string{}
	byKey := map[string]string{}
	for _, member := range publication.Members {
		byPath[filepath.Clean(member.Config.Source)] = member.SourceKey
		byKey[filepath.ToSlash(filepath.Clean(member.SourceKey))] = member.SourceKey
	}
	selectors := append([]config.TOCMemberConfig(nil), publication.Layout.TableOfContents.Members...)
	for index, selector := range selectors {
		sourceKey := byPath[filepath.Clean(selector.Source)]
		if sourceKey == "" {
			sourceKey = byKey[filepath.ToSlash(filepath.Clean(selector.Source))]
		}
		if sourceKey == "" {
			*issues = append(*issues, document.Issue{
				Severity: document.SeverityError,
				Code:     "publication_toc_member_missing",
				Source:   selector.Source,
				Message:  fmt.Sprintf("publication %q table of contents selects source %q, which is not a bundle member", publication.ID, selector.Source),
			})
			continue
		}
		selectors[index].Source = sourceKey
	}
	publication.Layout.TableOfContents.Members = selectors
}

func derivedBundleTitle(members []Member) string {
	for _, member := range members {
		if member.Config.TitleMode != "document_title" || member.Document == nil {
			continue
		}
		for _, heading := range member.Document.Headings {
			if heading.Level == 1 {
				return heading.Text
			}
		}
	}
	return ""
}

func resolveDocument(path string, graph *document.Graph, publicationID string, memberIndex int, issues *[]document.Issue) *document.Document {
	clean := filepath.Clean(path)
	for _, candidate := range graph.Documents {
		if filepath.Clean(candidate.Path) == clean {
			return candidate
		}
	}
	label := fmt.Sprintf("publication %q source", publicationID)
	if memberIndex > 0 {
		label = fmt.Sprintf("publication %q member %d", publicationID, memberIndex)
	}
	code := "publication_source_missing"
	message := fmt.Sprintf("%s %q was not discovered", label, path)
	if !withinAllowedRoots(path, graph.AllowedRoots) {
		code = "publication_source_unsafe"
		message = fmt.Sprintf("%s %q is outside approved source roots", label, path)
	} else if info, err := os.Stat(path); err == nil && !info.IsDir() {
		code = "publication_source_excluded"
		message = fmt.Sprintf("%s %q exists but is excluded from discovery", label, path)
	}
	source := path
	if relative, err := filepath.Rel(graph.Root, path); err == nil {
		if key, normalizeErr := sourcekey.Normalize(relative); normalizeErr == nil {
			source = key
		}
	}
	*issues = append(*issues, document.Issue{Severity: document.SeverityError, Code: code, Source: source, Message: message})
	return nil
}

func withinAllowedRoots(path string, roots []string) bool {
	cleanPath, err := canonicalCandidate(path)
	if err != nil {
		return false
	}
	for _, root := range roots {
		cleanRoot, rootErr := canonicalCandidate(root)
		if rootErr != nil {
			continue
		}
		relative, relErr := filepath.Rel(cleanRoot, cleanPath)
		if relErr == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

func canonicalCandidate(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	candidate := absolute
	suffix := []string{}
	for {
		resolved, resolveErr := filepath.EvalSymlinks(candidate)
		if resolveErr == nil {
			return filepath.Join(append([]string{resolved}, suffix...)...), nil
		}
		parent := filepath.Dir(candidate)
		if parent == candidate {
			return "", resolveErr
		}
		suffix = append([]string{filepath.Base(candidate)}, suffix...)
		candidate = parent
	}
}
