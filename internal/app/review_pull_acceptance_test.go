package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/goliatone/mdoc/internal/auth"
	"google.golang.org/api/docs/v1"
	"google.golang.org/api/drive/v3"
	"google.golang.org/api/option"
)

const reviewPullAcceptanceSource = "tools/mdoc/testdata/acceptance/review/source.md"

type liveReviewTarget struct {
	publication  string
	configPath   string
	statePath    string
	snapshotPath string
	sourcePath   string
	fileID       string
	auth         *auth.Service
	docs         *docs.Service
	drive        *drive.Service
}

type liveReviewReport struct {
	Result       string          `json:"result"`
	OpenComments int             `json:"open_comments"`
	Patch        string          `json:"patch"`
	PatchOutput  string          `json:"patch_output"`
	Changes      json.RawMessage `json:"changes"`
	Conflicts    json.RawMessage `json:"conflicts"`
	Unsupported  json.RawMessage `json:"unsupported"`
}

func TestLiveReviewPullProseCommentsAndConcurrency(t *testing.T) {
	target := newLiveReviewTarget(t)
	original, err := os.ReadFile(target.sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.WriteFile(target.sourcePath, original, 0o644); err != nil {
			t.Errorf("restore acceptance source: %v", err)
		}
	})
	beforeState := acceptanceTreeHash(t, target.statePath, target.snapshotPath)
	replacements := []struct {
		name string
		old  string
		new  string
	}{
		{name: "heading", old: "Reviewable Content", new: "Reviewed Content"},
		{name: "paragraph", old: "The reviewer can change this paragraph without changing the Markdown source.", new: "The reviewer changed this paragraph in Google Docs without changing the Markdown source."},
		{name: "list", old: "First review item", new: "First reviewed item"},
		{name: "block quote", old: "This block quote belongs to the source acceptance fixture.", new: "This reviewed block quote belongs to the source acceptance fixture."},
	}

	comment, err := target.drive.Comments.Create(target.fileID, &drive.Comment{Content: "mdoc review pull acceptance comment"}).Fields("id,resolved").Do()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := target.drive.Comments.Delete(target.fileID, comment.Id).Do(); err != nil {
			t.Errorf("delete acceptance comment: %v", err)
		}
	})
	for _, replacement := range replacements {
		replacement := replacement
		t.Run(replacement.name, func(t *testing.T) {
			applyLiveTextReplacements(t, target.docs, target.fileID, [][2]string{{replacement.old, replacement.new}})
			t.Cleanup(func() {
				applyLiveTextReplacements(t, target.docs, target.fileID, [][2]string{{replacement.new, replacement.old}})
			})
			beforeDocument, err := target.docs.Documents.Get(target.fileID).IncludeTabsContent(true).Do()
			if err != nil {
				t.Fatal(err)
			}
			patchPath := filepath.Join(t.TempDir(), "review.patch")
			report, pullErr := runLiveReviewPull(t, target, patchPath)
			if pullErr != nil || report.Result != string(ReviewCodeCleanPatch) || report.OpenComments < 1 || !strings.Contains(report.Patch, replacement.new) {
				t.Fatalf("clean pull = %#v, %v", report, pullErr)
			}
			if canonicalAcceptancePath(report.PatchOutput) != canonicalAcceptancePath(patchPath) {
				t.Fatalf("patch output = %q, expected %q", report.PatchOutput, patchPath)
			}
			assertPatchApplies(t, patchPath, original, replacement.new)
			afterDocument, err := target.docs.Documents.Get(target.fileID).IncludeTabsContent(true).Do()
			if err != nil {
				t.Fatal(err)
			}
			if beforeDocument.RevisionId != afterDocument.RevisionId {
				t.Fatalf("review pull changed Docs revision from %q to %q", beforeDocument.RevisionId, afterDocument.RevisionId)
			}
		})
	}
	if got := acceptanceTreeHash(t, target.statePath, target.snapshotPath); got != beforeState {
		t.Fatalf("review pull changed state or snapshot tree: %s != %s", got, beforeState)
	}
	if got, err := os.ReadFile(target.sourcePath); err != nil || !bytes.Equal(got, original) {
		t.Fatalf("review pull changed source: %v", err)
	}
	remaining, err := target.drive.Comments.Get(target.fileID, comment.Id).Fields("id,resolved").Do()
	if err != nil || remaining.Resolved {
		t.Fatalf("comment was changed by pull: %#v, %v", remaining, err)
	}

	paragraph := replacements[1]
	applyLiveTextReplacements(t, target.docs, target.fileID, [][2]string{{paragraph.old, paragraph.new}})
	t.Cleanup(func() {
		applyLiveTextReplacements(t, target.docs, target.fileID, [][2]string{{paragraph.new, paragraph.old}})
	})
	localNonOverlap := bytes.Replace(original, []byte("Second review item"), []byte("Second locally edited item"), 1)
	writeAcceptanceSource(t, target.sourcePath, localNonOverlap)
	report, pullErr := runLiveReviewPull(t, target, "")
	if pullErr != nil || report.Result != string(ReviewCodeCleanPatch) || !strings.Contains(report.Patch, "Second locally edited item") {
		t.Fatalf("non-overlap pull = %#v, %v", report, pullErr)
	}

	localOverlap := bytes.Replace(original, []byte(paragraph.old), []byte("The local author changed the same paragraph."), 1)
	writeAcceptanceSource(t, target.sourcePath, localOverlap)
	report, pullErr = runLiveReviewPull(t, target, "")
	if pullErr == nil || report.Result != string(ReviewCodeConflict) || report.Patch != "" {
		t.Fatalf("overlap pull = %#v, %v", report, pullErr)
	}
	writeAcceptanceSource(t, target.sourcePath, original)
}

func TestLiveMomentumReviewPullSafeProse(t *testing.T) {
	target := newLiveReviewTargetFor(t, "engagement-report")
	sourceRelative := "docs/engagement/12_FINAL_REPORT.md"
	sourcePath, err := filepath.Abs(filepath.Join("..", "..", "..", "..", sourceRelative))
	if err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	beforeState := acceptanceTreeHash(t, target.statePath, target.snapshotPath)
	oldText := "The reports give Momentum a clear view of its current position and a practical path forward."
	newText := "The reports give Momentum a clear view of its current position and a clear, practical path forward."
	applyLiveTextReplacements(t, target.docs, target.fileID, [][2]string{{oldText, newText}})
	t.Cleanup(func() {
		applyLiveTextReplacements(t, target.docs, target.fileID, [][2]string{{newText, oldText}})
	})

	beforeDocument, err := target.docs.Documents.Get(target.fileID).IncludeTabsContent(true).Do()
	if err != nil {
		t.Fatal(err)
	}
	patchPath := filepath.Join(t.TempDir(), "momentum-review.patch")
	report, pullErr := runLiveReviewPull(t, target, patchPath)
	if pullErr != nil || report.Result != string(ReviewCodeCleanPatch) || !strings.Contains(report.Patch, newText) {
		t.Fatalf("Momentum clean pull = %#v, %v", report, pullErr)
	}
	assertPatchAppliesToSource(t, patchPath, sourceRelative, original, newText)
	afterDocument, err := target.docs.Documents.Get(target.fileID).IncludeTabsContent(true).Do()
	if err != nil {
		t.Fatal(err)
	}
	if beforeDocument.RevisionId != afterDocument.RevisionId {
		t.Fatalf("review pull changed Docs revision from %q to %q", beforeDocument.RevisionId, afterDocument.RevisionId)
	}
	if got := acceptanceTreeHash(t, target.statePath, target.snapshotPath); got != beforeState {
		t.Fatalf("review pull changed state or snapshot tree: %s != %s", got, beforeState)
	}
	if got, err := os.ReadFile(sourcePath); err != nil || !bytes.Equal(got, original) {
		t.Fatalf("review pull changed Momentum source: %v", err)
	}
}

func canonicalAcceptancePath(path string) string {
	resolved, err := filepath.EvalSymlinks(path)
	if err == nil {
		return resolved
	}
	return filepath.Clean(path)
}

func TestLiveReviewPullTabTopologyGuards(t *testing.T) {
	target := newLiveReviewTarget(t)
	document, err := target.docs.Documents.Get(target.fileID).IncludeTabsContent(true).Do()
	if err != nil {
		t.Fatal(err)
	}
	if len(document.Tabs) != 1 {
		t.Fatalf("acceptance baseline has %d root tabs, expected 1", len(document.Tabs))
	}
	rootID := document.Tabs[0].TabProperties.TabId
	response, err := target.docs.Documents.BatchUpdate(target.fileID, &docs.BatchUpdateDocumentRequest{Requests: []*docs.Request{{
		AddDocumentTab: &docs.AddDocumentTabRequest{TabProperties: &docs.TabProperties{Title: "Acceptance tab"}},
	}}}).Do()
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Replies) != 1 || response.Replies[0].AddDocumentTab == nil || response.Replies[0].AddDocumentTab.TabProperties == nil {
		t.Fatalf("add tab response = %#v", response.Replies)
	}
	addedID := response.Replies[0].AddDocumentTab.TabProperties.TabId
	t.Cleanup(func() {
		_, err := target.docs.Documents.BatchUpdate(target.fileID, &docs.BatchUpdateDocumentRequest{Requests: []*docs.Request{{
			DeleteTab: &docs.DeleteTabRequest{TabId: addedID},
		}}}).Do()
		if err != nil {
			t.Errorf("delete acceptance tab: %v", err)
		}
	})
	assertLiveTabPullBlocked(t, target, "add")

	updateLiveTab(t, target, addedID, &docs.TabProperties{TabId: addedID, Title: "Renamed acceptance tab"}, "title")
	assertLiveTabPullBlocked(t, target, "rename")
	updateLiveTab(t, target, addedID, &docs.TabProperties{TabId: addedID, Index: 0}, "index")
	assertLiveTabPullBlocked(t, target, "reorder")
	updateLiveTab(t, target, addedID, &docs.TabProperties{TabId: addedID, ParentTabId: rootID}, "parent_tab_id")
	assertLiveTabPullBlocked(t, target, "nest")
}

func TestLiveReviewPullRejectsUnsupportedTable(t *testing.T) {
	target := newLiveReviewTarget(t)
	document, err := target.docs.Documents.Get(target.fileID).IncludeTabsContent(true).Do()
	if err != nil {
		t.Fatal(err)
	}
	if len(document.Tabs) != 1 || document.Tabs[0].DocumentTab == nil || document.Tabs[0].DocumentTab.Body == nil {
		t.Fatal("acceptance document does not have one document tab body")
	}
	content := document.Tabs[0].DocumentTab.Body.Content
	if len(content) == 0 {
		t.Fatal("acceptance document body is empty")
	}
	tabID := document.Tabs[0].TabProperties.TabId
	insertIndex := content[len(content)-1].EndIndex - 1
	_, err = target.docs.Documents.BatchUpdate(target.fileID, &docs.BatchUpdateDocumentRequest{Requests: []*docs.Request{{
		InsertTable: &docs.InsertTableRequest{Rows: 2, Columns: 2, Location: &docs.Location{Index: insertIndex, TabId: tabID}},
	}}}).Do()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		updated, err := target.docs.Documents.Get(target.fileID).IncludeTabsContent(true).Do()
		if err != nil {
			t.Errorf("read acceptance table for cleanup: %v", err)
			return
		}
		body := updated.Tabs[0].DocumentTab.Body.Content
		endIndex := body[len(body)-1].EndIndex - 1
		_, err = target.docs.Documents.BatchUpdate(target.fileID, &docs.BatchUpdateDocumentRequest{Requests: []*docs.Request{{
			DeleteContentRange: &docs.DeleteContentRangeRequest{Range: &docs.Range{StartIndex: insertIndex, EndIndex: endIndex, TabId: tabID}},
		}}}).Do()
		if err != nil {
			t.Errorf("delete acceptance table: %v", err)
		}
	})
	report, pullErr := runLiveReviewPull(t, target, "")
	if pullErr == nil || report.Result != string(ReviewCodeUnsupported) || len(report.Unsupported) == 0 || report.Patch != "" {
		t.Fatalf("unsupported table pull = %#v, %v", report, pullErr)
	}
}

func newLiveReviewTarget(t *testing.T) liveReviewTarget {
	return newLiveReviewTargetFor(t, "review-source")
}

func newLiveReviewTargetFor(t *testing.T, publication string) liveReviewTarget {
	t.Helper()
	if os.Getenv("MDOC_RUN_REVIEW_PULL_ACCEPTANCE") != "1" {
		t.Skip("set MDOC_RUN_REVIEW_PULL_ACCEPTANCE=1 to edit the disposable review pull acceptance document")
	}
	configPath, err := filepath.Abs(filepath.Join("..", "..", "..", "..", ".mdoc-acceptance.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join("..", "..", "testdata", "acceptance", "output", "state.json")
	stateBytes, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	var value struct {
		Targets map[string]struct {
			ActiveTarget *struct {
				FileID string `json:"file_id"`
			} `json:"active_target"`
		} `json:"targets"`
	}
	if err := json.Unmarshal(stateBytes, &value); err != nil {
		t.Fatal(err)
	}
	active := value.Targets["publication:"+publication].ActiveTarget
	if active == nil || active.FileID == "" {
		t.Fatalf("%s has no active acceptance target", publication)
	}
	authService, err := auth.NewService(auth.ServiceOptions{PromptWriter: os.Stderr})
	if err != nil {
		t.Fatal(err)
	}
	client, err := authService.Client(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	docsService, err := docs.NewService(context.Background(), option.WithHTTPClient(client))
	if err != nil {
		t.Fatal(err)
	}
	driveService, err := drive.NewService(context.Background(), option.WithHTTPClient(client))
	if err != nil {
		t.Fatal(err)
	}
	sourcePath, err := filepath.Abs(filepath.Join("..", "..", "..", "..", reviewPullAcceptanceSource))
	if err != nil {
		t.Fatal(err)
	}
	return liveReviewTarget{
		publication: publication,
		configPath:  configPath, statePath: statePath, snapshotPath: statePath + ".review-snapshots",
		sourcePath: sourcePath, fileID: active.FileID, auth: authService, docs: docsService, drive: driveService,
	}
}

func applyLiveTextReplacements(t *testing.T, service *docs.Service, fileID string, replacements [][2]string) {
	t.Helper()
	requests := make([]*docs.Request, 0, len(replacements))
	for _, replacement := range replacements {
		requests = append(requests, &docs.Request{ReplaceAllText: &docs.ReplaceAllTextRequest{
			ContainsText: &docs.SubstringMatchCriteria{Text: replacement[0], MatchCase: true}, ReplaceText: replacement[1],
		}})
	}
	response, err := service.Documents.BatchUpdate(fileID, &docs.BatchUpdateDocumentRequest{Requests: requests}).Do()
	if err != nil {
		t.Fatal(err)
	}
	for index, reply := range response.Replies {
		if reply.ReplaceAllText == nil || reply.ReplaceAllText.OccurrencesChanged != 1 {
			t.Fatalf("replacement %d changed %d occurrences", index, reply.ReplaceAllText.OccurrencesChanged)
		}
	}
}

func runLiveReviewPull(t *testing.T, target liveReviewTarget, patchPath string) (liveReviewReport, error) {
	t.Helper()
	var output bytes.Buffer
	application := New(&output, target.auth)
	err := application.ReviewPull(context.Background(), ReviewPullOptions{
		CommonOptions: CommonOptions{Config: target.configPath, Profile: "acceptance", JSON: true},
		Publication:   target.publication,
		PatchOutput:   patchPath,
	})
	var value liveReviewReport
	if len(output.Bytes()) > 0 {
		if decodeErr := json.Unmarshal(output.Bytes(), &value); decodeErr != nil {
			t.Fatalf("decode review pull report: %v\n%s", decodeErr, output.String())
		}
	}
	return value, err
}

func assertPatchApplies(t *testing.T, patchPath string, original []byte, expected string) {
	assertPatchAppliesToSource(t, patchPath, reviewPullAcceptanceSource, original, expected)
}

func assertPatchAppliesToSource(t *testing.T, patchPath, sourceRelative string, original []byte, expected string) {
	t.Helper()
	root := t.TempDir()
	source := filepath.Join(root, filepath.FromSlash(sourceRelative))
	if err := os.MkdirAll(filepath.Dir(source), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, original, 0o600); err != nil {
		t.Fatal(err)
	}
	command := exec.Command("git", "apply", patchPath)
	command.Dir = root
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("apply review patch: %v: %s", err, output)
	}
	got, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(got, []byte(expected)) {
		t.Fatalf("applied patch did not contain accepted edits:\n%s", got)
	}
	application := New(io.Discard)
	if err := application.Validate(context.Background(), ValidateOptions{CommonOptions: CommonOptions{
		AllowDirect: true,
		Sources:     []string{sourceRelative},
		Root:        root,
	}}); err != nil {
		t.Fatalf("validate applied review patch: %v", err)
	}
}

func writeAcceptanceSource(t *testing.T, path string, content []byte) {
	t.Helper()
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}
}

func acceptanceTreeHash(t *testing.T, paths ...string) string {
	t.Helper()
	hash := sha256.New()
	for _, root := range paths {
		info, err := os.Stat(root)
		if err != nil {
			t.Fatal(err)
		}
		if !info.IsDir() {
			content, readErr := os.ReadFile(root)
			if readErr != nil {
				t.Fatal(readErr)
			}
			fmt.Fprintf(hash, "%s\x00", filepath.Base(root))
			hash.Write(content)
			continue
		}
		if err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() {
				return nil
			}
			relative, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			content, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			fmt.Fprintf(hash, "%s\x00", filepath.ToSlash(relative))
			hash.Write(content)
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func updateLiveTab(t *testing.T, target liveReviewTarget, tabID string, properties *docs.TabProperties, fields string) {
	t.Helper()
	properties.TabId = tabID
	_, err := target.docs.Documents.BatchUpdate(target.fileID, &docs.BatchUpdateDocumentRequest{Requests: []*docs.Request{{
		UpdateDocumentTabProperties: &docs.UpdateDocumentTabPropertiesRequest{TabProperties: properties, Fields: fields},
	}}}).Do()
	if err != nil {
		t.Fatal(err)
	}
}

func assertLiveTabPullBlocked(t *testing.T, target liveReviewTarget, operation string) {
	t.Helper()
	value, err := runLiveReviewPull(t, target, "")
	if err == nil || value.Result != "" || (!strings.Contains(err.Error(), string(ReviewCodeUnsupported)) && !strings.Contains(err.Error(), "expected one root tab with no children")) {
		t.Fatalf("%s tab pull was not blocked as unsupported: %#v, %v", operation, value, err)
	}
}
