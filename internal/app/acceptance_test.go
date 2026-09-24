package app

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/goliatone/mdoc/internal/auth"
	"github.com/goliatone/mdoc/internal/bundle"
	"github.com/goliatone/mdoc/internal/config"
	"github.com/goliatone/mdoc/internal/document"
	"github.com/goliatone/mdoc/internal/fields"
	"github.com/goliatone/mdoc/internal/googleapi"
	"github.com/goliatone/mdoc/internal/publication"
	"github.com/goliatone/mdoc/internal/state"
	"google.golang.org/api/drive/v3"
	"google.golang.org/api/option"
)

const (
	layoutAcceptanceSource  = "tools/mdoc/testdata/acceptance/content/media-and-layout.md"
	layoutAcceptanceHeading = "Media and Layout Acceptance"
)

var momentumDeliveryMembers = []string{
	"docs/engagement/12_FINAL_REPORT.md",
	"docs/engagement/09_BEST_PRACTICES.md",
	"docs/engagement/09_BEST_PRACTICES_APPENDIX.md",
	"docs/engagement/11_OPERATIONS_PROPOSAL.md",
	"docs/engagement/10_AUTH_ARCHITECTURE.md",
	"docs/engagement/08_SECURITY_ASSESSMENT.md",
	"docs/engagement/01_DISCOVERY.md",
	"docs/engagement/02_INTAKE_APP_AUDIT.md",
	"docs/engagement/03_PRODUCTION_APP_AUDIT.md",
	"docs/engagement/04_SCHEDULING_APP_AUDIT.md",
	"docs/engagement/05_GITHUB_DEPLOYMENT_AUDIT.md",
	"docs/engagement/06_VERCEL_AUDIT.md",
	"docs/engagement/07_SUPABASE_AUDIT.md",
}

var momentumReferenceRuleIDs = []string{
	"intake-findings",
	"production-findings",
	"scheduling-findings",
	"github-findings",
	"vercel-findings",
	"supabase-findings",
	"operations",
	"source-audits",
	"finding-register",
	"implementation-roadmap",
	"identity-design",
	"operating-playbook",
	"code-organization-guide",
	"scope-decisions-method",
	"appendix-a",
	"appendix-b",
	"appendix-c",
	"appendix-d",
	"appendix-e",
	"appendix-f",
}

type retainedAcceptanceArtifact struct {
	targetKey       string
	docxPath        string
	docxHash        string
	renderedRawHash string
}

func TestMomentumAcceptanceConfigSeparatesLayoutFixture(t *testing.T) {
	configPath, err := filepath.Abs(filepath.Join("..", "..", "..", "..", ".mdoc-acceptance.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Resolve(context.Background(), config.ResolverOptions{ProjectPath: configPath, Profile: "acceptance"})
	if err != nil {
		t.Fatal(err)
	}
	legacyConfig, profile := cfg.Legacy()
	graph, err := document.Discover(context.Background(), legacyConfig.Root, profile, document.CommandRunner{Binary: cfg.PandocBinary, Reader: profile.Reader})
	if err != nil {
		t.Fatal(err)
	}
	publications := publication.Resolve(cfg.Version, profile, graph)
	graph.Issues = append(graph.Issues, publications.Issues...)
	if graph.HasErrors() {
		t.Fatalf("acceptance input issues: %#v", graph.Issues)
	}
	for _, id := range []string{"engagement-report", "report-map"} {
		assertMomentumDeliveryMembers(t, publications.ByID[id])
		assertMomentumReferenceRules(t, publications.ByID[id])
	}
	for _, id := range []string{"engagement-report", "review-source", "review-bundle"} {
		target := publications.ByID[id]
		if target == nil || !target.ReviewPullEnabled {
			t.Fatalf("acceptance publication %q does not enable review pull", id)
		}
	}
	assertLayoutAcceptanceMembers(t, publications.ByID["layout-acceptance"])
}

func TestMomentumReferenceRulesCoverFindingAndNamedReferences(t *testing.T) {
	configPath, err := filepath.Abs(filepath.Join("..", "..", "..", "..", ".mdoc.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Resolve(context.Background(), config.ResolverOptions{ProjectPath: configPath, Profile: "work"})
	if err != nil {
		t.Fatal(err)
	}
	legacyConfig, profile := cfg.Legacy()
	runner := document.CommandRunner{Binary: cfg.PandocBinary, Reader: profile.Reader}
	graph, err := document.Discover(context.Background(), legacyConfig.Root, profile, runner)
	if err != nil {
		t.Fatal(err)
	}
	publications := publication.Resolve(cfg.Version, profile, graph)
	target := publications.ByID["engagement-report"]
	assertMomentumReferenceRules(t, target)
	assembled, err := bundle.AssembleWithOptions(context.Background(), target, runner, bundle.AssembleOptions{
		Fields:   profile.FieldDefaults,
		Computed: planPublicationComputed(legacyConfig, profile, target),
	})
	if err != nil {
		t.Fatal(err)
	}
	anchors := protectedReferenceAnchors(assembled.Document)
	for _, expected := range []struct {
		text          string
		headingPrefix string
	}{
		{text: "E02-F04", headingPrefix: "E02-F04 -"},
		{text: "E03-F04", headingPrefix: "E03-F04 -"},
		{text: "E04-F04", headingPrefix: "E04-F04 -"},
		{text: "E05-F04", headingPrefix: "E05-F04 -"},
		{text: "E06-F04", headingPrefix: "E06-F04 -"},
		{text: "E07-F04", headingPrefix: "E07-F04 -"},
		{text: "E02", headingPrefix: "E02 - Intake"},
		{text: "E05", headingPrefix: "E05 - GitHub and release process"},
		{text: "Consolidated Finding Register", headingPrefix: "Consolidated Finding Register"},
		{text: "Implementation Roadmap", headingPrefix: "Implementation Roadmap"},
		{text: "Shared Identity and Permission Design", headingPrefix: "Shared Identity and Permission Design"},
		{text: "Operating Playbook", headingPrefix: "Operating Playbook"},
		{text: "Code Organization Guide", headingPrefix: "Code Organization Guide"},
		{text: "Scope, Decisions, and Method", headingPrefix: "Scope, Decisions, and Method"},
		{text: "Appendix A", headingPrefix: "Appendix A - Intake Application Findings"},
		{text: "Appendix F", headingPrefix: "Appendix F - Supabase and Data Security Findings"},
	} {
		targetAnchor := ""
		for _, heading := range assembled.Topology.Headings {
			if heading.ID != "" && strings.HasPrefix(heading.Text, expected.headingPrefix) {
				targetAnchor = "#" + heading.ID
				break
			}
		}
		if targetAnchor == "" || !anchors[expected.text][targetAnchor] {
			t.Fatalf("reference %q anchors = %v, expected target %q", expected.text, anchors[expected.text], targetAnchor)
		}
	}
}

func TestRetainedMomentumDeliveryDOCXExcludesAcceptanceFixture(t *testing.T) {
	outputDirectory := filepath.Join("..", "..", "testdata", "acceptance", "output")
	for _, name := range []string{"momentum-engagement.docx", "momentum-report-map.docx"} {
		docx, err := os.ReadFile(filepath.Join(outputDirectory, name))
		if err != nil {
			t.Fatal(err)
		}
		assertDOCXExcludesAcceptanceFixture(t, docx)
	}
}

func TestRetainedAcceptanceArtifactRequiresBothHashes(t *testing.T) {
	raw := []byte("fresh raw DOCX")
	retained := []byte("sanitized retained DOCX")
	retainedPath := filepath.Join(t.TempDir(), "retained.docx")
	if err := os.WriteFile(retainedPath, retained, 0o600); err != nil {
		t.Fatal(err)
	}
	hash := func(value []byte) string {
		digest := sha256.Sum256(value)
		return fmt.Sprintf("%x", digest)
	}
	gate := retainedAcceptanceArtifact{
		targetKey:       "publication:report",
		docxPath:        retainedPath,
		docxHash:        hash(retained),
		renderedRawHash: hash(raw),
	}
	got, err := gate.Finalize(context.Background(), PublicationArtifact{TargetKey: "publication:report", DOCX: raw})
	if err != nil || string(got) != string(retained) {
		t.Fatalf("finalize = %q, %v", got, err)
	}
	for _, test := range []struct {
		name     string
		artifact PublicationArtifact
		change   func(*retainedAcceptanceArtifact)
	}{
		{name: "target", artifact: PublicationArtifact{TargetKey: "publication:other", DOCX: raw}},
		{name: "raw hash", artifact: PublicationArtifact{TargetKey: "publication:report", DOCX: []byte("changed")}},
		{name: "retained hash", artifact: PublicationArtifact{TargetKey: "publication:report", DOCX: raw}, change: func(value *retainedAcceptanceArtifact) { value.docxHash = strings.Repeat("0", sha256.Size*2) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidate := gate
			if test.change != nil {
				test.change(&candidate)
			}
			if _, err := candidate.Finalize(context.Background(), test.artifact); err == nil {
				t.Fatal("expected artifact gate failure")
			}
		})
	}
}

func (gate retainedAcceptanceArtifact) Finalize(_ context.Context, artifact PublicationArtifact) ([]byte, error) {
	if artifact.TargetKey != gate.targetKey {
		return nil, fmt.Errorf("acceptance artifact target is %q, expected %q", artifact.TargetKey, gate.targetKey)
	}
	renderedHash := sha256.Sum256(artifact.DOCX)
	if got := fmt.Sprintf("%x", renderedHash); got != gate.renderedRawHash {
		return nil, fmt.Errorf("fresh raw DOCX hash is %s, expected inspected input %s", got, gate.renderedRawHash)
	}
	docx, err := os.ReadFile(gate.docxPath)
	if err != nil {
		return nil, fmt.Errorf("read retained acceptance DOCX: %w", err)
	}
	digest := sha256.Sum256(docx)
	if got := fmt.Sprintf("%x", digest); got != gate.docxHash {
		return nil, fmt.Errorf("retained acceptance DOCX hash is %s, expected %s", got, gate.docxHash)
	}
	return docx, nil
}

func TestWriteMomentumAcceptanceDOCX(t *testing.T) {
	if os.Getenv("MDOC_RUN_ACCEPTANCE") != "1" {
		t.Skip("set MDOC_RUN_ACCEPTANCE=1 to write the retained acceptance DOCX")
	}
	configPath, err := filepath.Abs(filepath.Join("..", "..", "..", "..", ".mdoc-acceptance.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Resolve(context.Background(), config.ResolverOptions{ProjectPath: configPath, Profile: "acceptance"})
	if err != nil {
		t.Fatal(err)
	}
	legacyConfig, profile := cfg.Legacy()
	graph, err := document.Discover(context.Background(), legacyConfig.Root, profile, document.CommandRunner{Binary: cfg.PandocBinary, Reader: profile.Reader})
	if err != nil {
		t.Fatal(err)
	}
	publications := publication.Resolve(cfg.Version, profile, graph)
	graph.Issues = append(graph.Issues, publications.Issues...)
	resolved, fieldIssues := resolvePublicationFields(legacyConfig, profile, publications, CommonOptions{})
	graph.Issues = append(graph.Issues, fieldIssues...)
	if graph.HasErrors() {
		t.Fatalf("acceptance input issues: %#v", graph.Issues)
	}
	targetID := strings.TrimSpace(os.Getenv("MDOC_ACCEPTANCE_PUBLICATION"))
	if targetID == "" {
		targetID = "engagement-report"
	}
	target := publications.ByID[targetID]
	if target == nil {
		t.Fatalf("acceptance publication %q does not exist", targetID)
	}
	if isMomentumDelivery(targetID) {
		assertMomentumDeliveryMembers(t, target)
	} else if targetID == "layout-acceptance" {
		assertLayoutAcceptanceMembers(t, target)
	}
	computed := fields.FreezeComputed(fields.PlanComputedValues(fields.PlanComputed{
		ProjectID: cfg.WorkspaceID, Profile: profile.Name, PublicationID: target.ID, PublicationKind: string(target.Kind), PublicationTitle: target.Title,
	}), fields.FrozenComputed{OperationID: "acceptance", ReviewSetID: "acceptance", Generation: 1, PublishedAt: time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)})
	application := New(os.Stdout)
	profileBytes := mustJSON(t, newProfileFingerprint(legacyConfig, profile))
	stableTargets := map[string]string{string(target.Target): PlaceholderURL(string(target.Target))}
	build, err := application.buildPublication(context.Background(), legacyConfig, profile, graph, target, resolved[target.ID], profileBytes, publicationPathTargets(publications), stableTargets, computed)
	if err != nil {
		t.Fatal(err)
	}
	rendered, err := application.renderer(legacyConfig).Render(context.Background(), build.request)
	if err != nil {
		t.Fatal(err)
	}
	if isMomentumDelivery(targetID) {
		assertDOCXExcludesAcceptanceFixture(t, rendered.DOCX)
		assertMomentumReferenceLinks(t, rendered.DOCX)
	} else if targetID == "layout-acceptance" {
		assertDOCXIncludesLayoutFixture(t, rendered.DOCX)
	}
	outputDirectory := filepath.Join("..", "..", "testdata", "acceptance", "output")
	if err := os.MkdirAll(outputDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	outputName := "momentum-engagement.raw.docx"
	hashName := "RAW_SHA256"
	if targetID != "engagement-report" {
		outputName = "momentum-" + targetID + ".raw.docx"
		hashName = "momentum-" + targetID + ".raw.sha256"
	}
	outputPath := filepath.Join(outputDirectory, outputName)
	if err := os.WriteFile(outputPath, rendered.DOCX, 0o600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(rendered.DOCX)
	if err := os.WriteFile(filepath.Join(outputDirectory, hashName), []byte(fmt.Sprintf("%x  %s\n", digest, outputName)), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestPublishMomentumAcceptanceDOCX(t *testing.T) {
	if os.Getenv("MDOC_RUN_GOOGLE_ACCEPTANCE") != "1" {
		t.Skip("set MDOC_RUN_GOOGLE_ACCEPTANCE=1 to import the retained acceptance DOCX")
	}
	configPath, err := filepath.Abs(filepath.Join("..", "..", "..", "..", ".mdoc-acceptance.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	targetID := strings.TrimSpace(os.Getenv("MDOC_ACCEPTANCE_PUBLICATION"))
	if targetID == "" {
		targetID = "engagement-report"
	}
	outputDirectory := filepath.Join("..", "..", "testdata", "acceptance", "output")
	docxName := "momentum-engagement.docx"
	rawHashName := "RAW_SHA256"
	if targetID != "engagement-report" {
		docxName = "momentum-" + targetID + ".docx"
		rawHashName = "momentum-" + targetID + ".raw.sha256"
	}
	docxHash, err := checksumFor(filepath.Join(outputDirectory, "SHA256"), docxName)
	if err != nil {
		t.Fatal(err)
	}
	rawName := strings.TrimSuffix(docxName, ".docx") + ".raw.docx"
	rawHash, err := checksumFor(filepath.Join(outputDirectory, rawHashName), rawName)
	if err != nil {
		t.Fatal(err)
	}
	if isMomentumDelivery(targetID) {
		retained, readErr := os.ReadFile(filepath.Join(outputDirectory, docxName))
		if readErr != nil {
			t.Fatal(readErr)
		}
		assertDOCXExcludesAcceptanceFixture(t, retained)
	}
	authService, err := auth.NewService(auth.ServiceOptions{PromptWriter: os.Stderr})
	if err != nil {
		t.Fatal(err)
	}
	application := New(os.Stdout, authService)
	application.ErrOut = os.Stderr
	expectControlledFailure := os.Getenv("MDOC_ACCEPTANCE_FAIL_AFTER_CREATE") == "1"
	if expectControlledFailure {
		injected := true
		application.BatchAfterCreate = func(targetKey, fileID string) error {
			if injected {
				injected = false
				return fmt.Errorf("controlled acceptance failure after creating %s as %s", targetKey, fileID)
			}
			return nil
		}
	}
	application.ArtifactGate = retainedAcceptanceArtifact{
		targetKey:       "publication:" + targetID,
		docxPath:        filepath.Join(outputDirectory, docxName),
		docxHash:        docxHash,
		renderedRawHash: rawHash,
	}
	err = application.Publish(context.Background(), PublishOptions{CommonOptions: CommonOptions{
		Config: configPath, Profile: "acceptance", Bundle: targetID,
	}})
	if expectControlledFailure {
		if err == nil {
			t.Fatal("expected controlled acceptance failure")
		}
		journal, journalErr := state.NewStore(filepath.Join(outputDirectory, "state.json")).LoadJournal()
		if journalErr != nil {
			t.Fatal(journalErr)
		}
		entry := journal.Entries["publication:"+targetID]
		if entry.Stage != state.StageCreated || entry.FileID == "" {
			t.Fatalf("controlled failure journal = %#v", entry)
		}
		t.Logf("controlled failure retained file %s at stage %s", entry.FileID, entry.Stage)
		return
	}
	if err != nil {
		t.Fatal(err)
	}
}

func TestExportMomentumAcceptancePDF(t *testing.T) {
	if os.Getenv("MDOC_RUN_GOOGLE_ACCEPTANCE") != "1" {
		t.Skip("set MDOC_RUN_GOOGLE_ACCEPTANCE=1 to export the converted Google Doc")
	}
	targetID := strings.TrimSpace(os.Getenv("MDOC_ACCEPTANCE_PUBLICATION"))
	if targetID == "" {
		targetID = "engagement-report"
	}
	outputDirectory := filepath.Join("..", "..", "testdata", "acceptance", "output")
	stateBytes, err := os.ReadFile(filepath.Join(outputDirectory, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	var acceptanceState struct {
		Targets map[string]struct {
			ActiveTarget *struct {
				FileID string `json:"file_id"`
			} `json:"active_target"`
		} `json:"targets"`
	}
	if err := json.Unmarshal(stateBytes, &acceptanceState); err != nil {
		t.Fatal(err)
	}
	target := acceptanceState.Targets["publication:"+targetID].ActiveTarget
	if target == nil || strings.TrimSpace(target.FileID) == "" {
		t.Fatalf("acceptance target %q has no active Google Doc", targetID)
	}
	authService, err := auth.NewService(auth.ServiceOptions{PromptWriter: os.Stderr})
	if err != nil {
		t.Fatal(err)
	}
	client, err := authService.Client(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	driveService, err := drive.NewService(context.Background(), option.WithHTTPClient(client))
	if err != nil {
		t.Fatal(err)
	}
	response, err := driveService.Files.Export(target.FileID, "application/pdf").Download()
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	pdf, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if len(pdf) == 0 {
		t.Fatal("Google Docs PDF export is empty")
	}
	name := "momentum-" + targetID + ".google.pdf"
	if targetID == "engagement-report" {
		name = "momentum-engagement.google.pdf"
	}
	if err := os.WriteFile(filepath.Join(outputDirectory, "rendered", name), pdf, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Logf("exported Google Doc %s to %s (%d bytes)", target.FileID, name, len(pdf))
}

func TestInspectMomentumGoogleDocument(t *testing.T) {
	if os.Getenv("MDOC_RUN_GOOGLE_ACCEPTANCE") != "1" {
		t.Skip("set MDOC_RUN_GOOGLE_ACCEPTANCE=1 to inspect the converted Google Doc")
	}
	targetID := strings.TrimSpace(os.Getenv("MDOC_ACCEPTANCE_PUBLICATION"))
	if targetID == "" {
		targetID = "engagement-report"
	}
	outputDirectory := filepath.Join("..", "..", "testdata", "acceptance", "output")
	stateBytes, err := os.ReadFile(filepath.Join(outputDirectory, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	var acceptanceState struct {
		Targets map[string]struct {
			ActiveTarget *struct {
				FileID string `json:"file_id"`
			} `json:"active_target"`
		} `json:"targets"`
	}
	if err := json.Unmarshal(stateBytes, &acceptanceState); err != nil {
		t.Fatal(err)
	}
	target := acceptanceState.Targets["publication:"+targetID].ActiveTarget
	if target == nil || strings.TrimSpace(target.FileID) == "" {
		t.Fatalf("acceptance target %q has no active Google Doc", targetID)
	}
	authService, err := auth.NewService(auth.ServiceOptions{PromptWriter: os.Stderr})
	if err != nil {
		t.Fatal(err)
	}
	client, err := authService.Client(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	clients, err := (googleapi.ServiceFactory{}).New(context.Background(), client)
	if err != nil {
		t.Fatal(err)
	}
	document, err := clients.Docs.GetDocument(context.Background(), target.FileID)
	if err != nil {
		t.Fatal(err)
	}
	assertGoogleAcceptanceStructure(t, targetID, document)
	comments, err := clients.Drive.ListComments(context.Background(), target.FileID)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("Google structure: headings=%d internal_links=%d objects=%d tables=%d sections=%d suggestions=%t unresolved_comments=%d", document.HeadingCount, document.InternalLinkCount, document.InlineObjectCount, document.TableCount, document.SectionBreakCount, document.SuggestionsDetected, comments.Unresolved)
}

func isMomentumDelivery(targetID string) bool {
	return targetID == "engagement-report" || targetID == "report-map"
}

func assertMomentumDeliveryMembers(t *testing.T, target *publication.Publication) {
	t.Helper()
	if target == nil {
		t.Fatal("Momentum delivery publication is missing")
	}
	if len(target.Members) != len(momentumDeliveryMembers) {
		t.Fatalf("publication %q has %d members, expected %d", target.ID, len(target.Members), len(momentumDeliveryMembers))
	}
	for index, expected := range momentumDeliveryMembers {
		got := filepath.ToSlash(target.Members[index].SourceKey)
		if got != expected {
			t.Fatalf("publication %q member %d = %q, expected %q", target.ID, index+1, got, expected)
		}
		if strings.HasPrefix(got, "tools/mdoc/testdata/") {
			t.Fatalf("publication %q includes acceptance-only member %q", target.ID, got)
		}
	}
}

func assertMomentumReferenceRules(t *testing.T, target *publication.Publication) {
	t.Helper()
	if target == nil {
		t.Fatal("Momentum publication is missing")
	}
	if len(target.ReferenceLinks) != len(momentumReferenceRuleIDs) {
		t.Fatalf("reference rule count = %d, expected %d", len(target.ReferenceLinks), len(momentumReferenceRuleIDs))
	}
	for index, expected := range momentumReferenceRuleIDs {
		if got := target.ReferenceLinks[index].ID; got != expected {
			t.Fatalf("reference rule %d = %q, expected %q", index, got, expected)
		}
	}
}

func protectedReferenceAnchors(value any) map[string]map[string]bool {
	result := map[string]map[string]bool{}
	var visit func(any)
	visit = func(current any) {
		switch typed := current.(type) {
		case []any:
			for _, item := range typed {
				visit(item)
			}
		case map[string]any:
			if _, isNode := typed["t"]; !isNode {
				for _, item := range typed {
					visit(item)
				}
				return
			}
			if typed["t"] == "Link" {
				content, _ := typed["c"].([]any)
				if len(content) >= 3 && pandocAttributeHasClass(content[0], "mdoc-protected-dependency") {
					target, _ := content[2].([]any)
					if len(target) == 0 {
						return
					}
					anchor, _ := target[0].(string)
					if strings.HasPrefix(anchor, "#") {
						text := pandocInlineText(content[1])
						if result[text] == nil {
							result[text] = map[string]bool{}
						}
						result[text][anchor] = true
					}
				}
				return
			}
			visit(typed["c"])
		}
	}
	visit(value)
	return result
}

func pandocAttributeHasClass(value any, expected string) bool {
	attribute, _ := value.([]any)
	if len(attribute) < 2 {
		return false
	}
	classes, _ := attribute[1].([]any)
	for _, class := range classes {
		if class == expected {
			return true
		}
	}
	return false
}

func pandocInlineText(value any) string {
	switch typed := value.(type) {
	case []any:
		var result strings.Builder
		for _, item := range typed {
			result.WriteString(pandocInlineText(item))
		}
		return result.String()
	case map[string]any:
		switch typed["t"] {
		case "Str":
			text, _ := typed["c"].(string)
			return text
		case "Space", "SoftBreak", "LineBreak":
			return " "
		default:
			return pandocInlineText(typed["c"])
		}
	default:
		return ""
	}
}

func assertLayoutAcceptanceMembers(t *testing.T, target *publication.Publication) {
	t.Helper()
	if target == nil {
		t.Fatal("layout-acceptance publication is missing")
	}
	if len(target.Members) != 1 || filepath.ToSlash(target.Members[0].SourceKey) != layoutAcceptanceSource {
		t.Fatalf("layout-acceptance members = %#v, expected only %q", target.Members, layoutAcceptanceSource)
	}
}

func assertDOCXExcludesAcceptanceFixture(t *testing.T, docx []byte) {
	t.Helper()
	documentXML, _ := inspectAcceptanceDOCX(t, docx)
	if bytes.Contains(documentXML, []byte(layoutAcceptanceHeading)) {
		t.Fatalf("retained delivery DOCX contains acceptance-only heading %q", layoutAcceptanceHeading)
	}
}

func assertDOCXIncludesLayoutFixture(t *testing.T, docx []byte) {
	t.Helper()
	documentXML, mediaCount := inspectAcceptanceDOCX(t, docx)
	if !bytes.Contains(documentXML, []byte(layoutAcceptanceHeading)) {
		t.Fatalf("layout acceptance DOCX is missing heading %q", layoutAcceptanceHeading)
	}
	if !bytes.Contains(documentXML, []byte(`w:orient="landscape"`)) {
		t.Fatal("layout acceptance DOCX is missing its landscape section")
	}
	if mediaCount < 4 {
		t.Fatalf("layout acceptance DOCX has %d media parts, expected PNG, JPEG, SVG, and SVG fallback", mediaCount)
	}
}

func assertMomentumReferenceLinks(t *testing.T, docx []byte) {
	t.Helper()
	documentXML, _ := inspectAcceptanceDOCX(t, docx)
	hyperlinkPattern := regexp.MustCompile(`(?s)<w:hyperlink\b[^>]*w:anchor="([^"]+)"[^>]*>(.*?)</w:hyperlink>`)
	textPattern := regexp.MustCompile(`<w:t\b[^>]*>([^<]*)</w:t>`)
	bookmarkPattern := regexp.MustCompile(`<w:bookmarkStart\b[^>]*w:name="([^"]+)"`)
	codePattern := regexp.MustCompile(`^OP[0-9]{2}$`)
	bookmarks := map[string]bool{}
	for _, match := range bookmarkPattern.FindAllSubmatch(documentXML, -1) {
		bookmarks[string(match[1])] = true
	}
	counts := map[string]int{}
	anchors := map[string]map[string]bool{}
	for _, match := range hyperlinkPattern.FindAllSubmatch(documentXML, -1) {
		text := ""
		for _, textMatch := range textPattern.FindAllSubmatch(match[2], -1) {
			text += string(textMatch[1])
		}
		if !codePattern.MatchString(text) {
			continue
		}
		anchor := string(match[1])
		counts[text]++
		if anchors[text] == nil {
			anchors[text] = map[string]bool{}
		}
		anchors[text][anchor] = true
		if !bookmarks[anchor] {
			t.Fatalf("reference %s targets missing bookmark %q", text, anchor)
		}
	}
	total := 0
	for number := 1; number <= 17; number++ {
		code := fmt.Sprintf("OP%02d", number)
		total += counts[code]
		if counts[code] == 0 || len(anchors[code]) != 1 {
			t.Fatalf("reference %s count=%d anchors=%v", code, counts[code], anchors[code])
		}
	}
	if total != 469 {
		t.Fatalf("Momentum DOCX contains %d OP reference links, expected 469", total)
	}
}

func inspectAcceptanceDOCX(t *testing.T, docx []byte) ([]byte, int) {
	t.Helper()
	reader, err := zip.NewReader(bytes.NewReader(docx), int64(len(docx)))
	if err != nil {
		t.Fatalf("read retained DOCX: %v", err)
	}
	var documentXML []byte
	mediaCount := 0
	for _, file := range reader.File {
		if strings.HasPrefix(file.Name, "word/media/") {
			mediaCount++
		}
		if file.Name != "word/document.xml" {
			continue
		}
		stream, openErr := file.Open()
		if openErr != nil {
			t.Fatalf("open retained DOCX document: %v", openErr)
		}
		content, readErr := io.ReadAll(stream)
		closeErr := stream.Close()
		if readErr != nil {
			t.Fatalf("read retained DOCX document: %v", readErr)
		}
		if closeErr != nil {
			t.Fatalf("close retained DOCX document: %v", closeErr)
		}
		documentXML = content
	}
	if len(documentXML) == 0 {
		t.Fatal("retained DOCX is missing word/document.xml")
	}
	return documentXML, mediaCount
}

func assertGoogleAcceptanceStructure(t *testing.T, targetID string, document googleapi.Document) {
	t.Helper()
	if targetID == "layout-acceptance" {
		if document.HeadingCount < 6 || document.InternalLinkCount < 6 || document.InlineObjectCount < 4 || document.TableCount == 0 || document.SectionBreakCount < 2 {
			t.Fatalf("converted layout acceptance structure is incomplete: %#v", document)
		}
		return
	}
	if document.HeadingCount < len(momentumDeliveryMembers) || document.InternalLinkCount < len(momentumDeliveryMembers) || document.TableCount == 0 || document.SectionBreakCount < 2 {
		t.Fatalf("converted delivery structure is incomplete: %#v", document)
	}
}

func checksumFor(checksumPath, fileName string) (string, error) {
	file, err := os.Open(checksumPath)
	if err != nil {
		return "", fmt.Errorf("open checksum file: %w", err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) == 2 && fields[1] == fileName {
			if len(fields[0]) != sha256.Size*2 {
				return "", fmt.Errorf("checksum for %s has invalid length", fileName)
			}
			return strings.ToLower(fields[0]), nil
		}
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("read checksum file: %w", err)
	}
	return "", fmt.Errorf("checksum for %s is missing from %s", fileName, checksumPath)
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
