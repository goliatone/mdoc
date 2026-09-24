package probe

import (
	"archive/zip"
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	docs "google.golang.org/api/docs/v1"
	"google.golang.org/api/drive/v3"
	"google.golang.org/api/option"
)

func TestInspectDocumentFindsStructureLinksAndSuggestions(t *testing.T) {
	document := &docs.Document{
		SuggestedDocumentStyleChanges: map[string]docs.SuggestedDocumentStyle{"suggestion-1": {}},
		InlineObjects:                 map[string]docs.InlineObject{"image-1": {}},
		Body: &docs.Body{Content: []*docs.StructuralElement{
			{Paragraph: &docs.Paragraph{
				ParagraphStyle: &docs.ParagraphStyle{NamedStyleType: "TITLE"},
				Elements: []*docs.ParagraphElement{{
					StartIndex: 1,
					EndIndex:   10,
					TextRun: &docs.TextRun{
						Content:   "probe",
						TextStyle: &docs.TextStyle{Link: &docs.Link{Url: placeholderURL}},
					},
				}},
			}},
			{Paragraph: &docs.Paragraph{
				ParagraphStyle: &docs.ParagraphStyle{NamedStyleType: "HEADING_1"},
				Elements: []*docs.ParagraphElement{
					{TextRun: &docs.TextRun{Content: "func "}},
					{TextRun: &docs.TextRun{Content: "ready()"}},
					{TextRun: &docs.TextRun{Content: " bool"}},
				},
			}},
			{Table: &docs.Table{TableRows: []*docs.TableRow{{TableCells: []*docs.TableCell{{
				Content: []*docs.StructuralElement{{Paragraph: &docs.Paragraph{
					ParagraphStyle: &docs.ParagraphStyle{NamedStyleType: "NORMAL_TEXT"},
				}}},
			}}}}}},
		}},
	}

	inspection := inspectDocument(document)
	if len(inspection.PlaceholderRanges) != 1 {
		t.Fatalf("placeholder ranges = %v", inspection.PlaceholderRanges)
	}
	if inspection.NamedStyles["TITLE"] != 1 || inspection.NamedStyles["HEADING_1"] != 1 {
		t.Fatalf("named styles = %v", inspection.NamedStyles)
	}
	if inspection.TableCount != 1 || !inspection.CodeTextPresent || !inspection.SuggestionsDetected {
		t.Fatalf("inspection = %+v", inspection)
	}
}

func TestRenderFixtureProducesPrivateDOCX(t *testing.T) {
	fixture := filepath.Join("..", "..", "testdata", "feasibility", "google-import.md")
	output, cleanup, err := renderFixture(context.Background(), GoogleOptions{Fixture: fixture})
	if err != nil {
		t.Fatal(err)
	}
	directory := filepath.Dir(output)
	defer cleanup()
	info, err := os.Stat(output)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() == 0 {
		t.Fatal("rendered DOCX is empty")
	}
	directoryInfo, err := os.Stat(directory)
	if err != nil {
		t.Fatal(err)
	}
	if got := directoryInfo.Mode().Perm(); got&0o077 != 0 {
		t.Fatalf("temporary directory mode = %04o", got)
	}
	documentXML := readDOCXPart(t, output, "word/document.xml")
	if bytes.Contains(documentXML, []byte("<w:bookmarkStart")) || bytes.Contains(documentXML, []byte("<w:bookmarkEnd")) {
		t.Fatal("rendered DOCX contains automatic heading bookmarks")
	}
	for _, style := range []string{"Title", "Heading1", "Heading2", "Heading3", "Heading4", "Heading5"} {
		marker := []byte(`w:pStyle w:val="` + style + `"`)
		if !bytes.Contains(documentXML, marker) {
			t.Errorf("rendered DOCX does not use %s", style)
		}
	}
}

func TestValidateProbeRootRejectsUntaggedFolder(t *testing.T) {
	if err := validateProbeRoot(&drive.File{Name: "unrelated", MimeType: googleFolderMIME}); err == nil {
		t.Fatal("expected cleanup guard error")
	}
	root := &drive.File{
		Name:     "mdoc T02 disposable probe",
		MimeType: googleFolderMIME,
		AppProperties: map[string]string{
			"mdoc_role":     "root",
			"mdoc_probe_id": "t02-123",
		},
	}
	if err := validateProbeRoot(root); err != nil {
		t.Fatalf("valid probe root rejected: %v", err)
	}
}

func TestValidateAutomatedResultReportsFailedGates(t *testing.T) {
	result := passingGoogleResult()
	if err := validateAutomatedResult(result); err != nil {
		t.Fatalf("passing result rejected: %v", err)
	}
	result.MovePreservedID = false
	result.NamedStyles["HEADING_5"] = 0
	err := validateAutomatedResult(result)
	if err == nil || !strings.Contains(err.Error(), "heading style ladder") || !strings.Contains(err.Error(), "ready folder move") {
		t.Fatalf("validation error = %v", err)
	}
}

func TestObserveMarkdownRecordsExactExportProperties(t *testing.T) {
	content := []byte("# Title\r\n\r\n> café Ω\r\n\r\n- item\r\n\r\n| A |\r\n|---|\r\n\r\n```go\r\ncode\r\n```\r\n\r\n![alt](image.png) [link](https://example.com)\r\n")
	got := observeMarkdown(content)
	if got.Content != string(content) || got.Bytes != len(content) || !got.ValidUTF8 || !got.EndsWithNewline {
		t.Fatalf("observation = %+v", got)
	}
	if got.CRLFCount == 0 || !got.HasHeading || !got.HasList || !got.HasBlockQuote || !got.HasTable || !got.HasCodeFence || !got.HasImage || !got.HasLink {
		t.Fatalf("missing Markdown feature: %+v", got)
	}
}

func TestFlattenTabTopologyPreservesAPINestingOrder(t *testing.T) {
	tabs := []*docs.Tab{{TabProperties: &docs.TabProperties{TabId: "root", Title: "Root", Index: 0}, ChildTabs: []*docs.Tab{{
		TabProperties: &docs.TabProperties{TabId: "child", Title: "Child", Index: 0, ParentTabId: "root"},
	}}}, {TabProperties: &docs.TabProperties{TabId: "second", Title: "Second", Index: 1}}}
	got := flattenTabTopology(tabs)
	if len(got) != 3 || got[0].ID != "root" || got[1].ID != "child" || got[1].ParentID != "root" || got[2].ID != "second" {
		t.Fatalf("topology = %+v", got)
	}
}

func passingGoogleResult() GoogleResult {
	return GoogleResult{
		RootFolderID:         "root",
		StagingFolderID:      "staging",
		ReviewFolderID:       "review",
		DocumentID:           "document",
		NativeGoogleDoc:      true,
		AppPropertySearchIDs: []string{"root", "staging", "review", "document"},
		InitialDriveVersion:  1,
		PostLinkDriveVersion: 2,
		PostEditDriveVersion: 3,
		FinalDriveVersion:    4,
		InitialDocsRevision:  "revision-1",
		PostLinkDocsRevision: "revision-2",
		PostEditDocsRevision: "revision-3",
		FinalDocsRevision:    "revision-2",
		PlaceholderSurvived:  true,
		PlaceholderChanged:   true,
		NamedStyles: map[string]int{
			"TITLE": 1, "HEADING_1": 1, "HEADING_2": 1, "HEADING_3": 1, "HEADING_4": 1, "HEADING_5": 1,
		},
		TableCount:         1,
		InlineObjectCount:  3,
		CodeTextPresent:    true,
		UnresolvedComments: 2,
		CommentPages:       2,
		Markdown: MarkdownProbe{
			Baseline: MarkdownObservation{
				Bytes: 10, ValidUTF8: true, HasHeading: true, HasList: true, HasBlockQuote: true,
				HasTable: true, HasCodeFence: true, HasImage: true, HasLink: true,
			},
			DriveFileScopeExported: true, DirectEditVisible: true, MetadataAdvancedDriveOnly: true,
			TabExports: []TabExportObservation{{}, {}, {}, {}, {}},
		},
		MovePreservedID:      true,
		MovePreservedURL:     true,
		ReadyPropertyUpdated: true,
		FinalParents:         []string{"review"},
	}
}

func readDOCXPart(t *testing.T, path, name string) []byte {
	t.Helper()
	archive, err := zip.OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()
	for _, file := range archive.File {
		if file.Name != name {
			continue
		}
		reader, err := file.Open()
		if err != nil {
			t.Fatal(err)
		}
		defer reader.Close()
		content, err := io.ReadAll(reader)
		if err != nil {
			t.Fatal(err)
		}
		return content
	}
	t.Fatalf("DOCX part %s not found", name)
	return nil
}

func TestListCommentsFollowsEveryPage(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests++
		if request.URL.Query().Get("pageSize") != "1" {
			t.Errorf("pageSize = %q", request.URL.Query().Get("pageSize"))
		}
		writer.Header().Set("Content-Type", "application/json")
		if request.URL.Query().Get("pageToken") == "next" {
			_, _ = writer.Write([]byte(`{"comments":[{"id":"two","resolved":false}]}`))
			return
		}
		_, _ = writer.Write([]byte(`{"comments":[{"id":"one","resolved":false}],"nextPageToken":"next"}`))
	}))
	defer server.Close()

	service, err := drive.NewService(
		context.Background(),
		option.WithEndpoint(server.URL+"/"),
		option.WithHTTPClient(server.Client()),
		option.WithoutAuthentication(),
	)
	if err != nil {
		t.Fatal(err)
	}
	result, err := listComments(context.Background(), service, "document")
	if err != nil {
		t.Fatal(err)
	}
	if result.Pages != 2 || result.Unresolved != 2 || requests != 2 {
		t.Fatalf("result = %+v, requests = %d", result, requests)
	}
}
