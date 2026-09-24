package probe

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	docs "google.golang.org/api/docs/v1"
	"google.golang.org/api/drive/v3"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"
)

const (
	docxMIME           = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	googleDocMIME      = "application/vnd.google-apps.document"
	googleFolderMIME   = "application/vnd.google-apps.folder"
	googleMarkdownMIME = "text/markdown"
	placeholderURL     = "https://link-01.mdoc.invalid/pending"
	probeEditMarker    = "Disposable review edit: café Ω 💡"
)

type ClientProvider interface {
	Client(context.Context) (*http.Client, error)
}

type Service struct {
	auth ClientProvider
}

type GoogleOptions struct {
	Fixture       string
	ReferenceDOCX string
	Keep          bool
}

type GoogleResult struct {
	ProbeID              string         `json:"probe_id"`
	RootFolderID         string         `json:"root_folder_id"`
	StagingFolderID      string         `json:"staging_folder_id"`
	ReviewFolderID       string         `json:"review_folder_id"`
	DocumentID           string         `json:"document_id"`
	DocumentURL          string         `json:"document_url"`
	NativeGoogleDoc      bool           `json:"native_google_doc"`
	AppPropertySearchIDs []string       `json:"app_property_search_ids"`
	InitialDriveVersion  int64          `json:"initial_drive_version"`
	PostLinkDriveVersion int64          `json:"post_link_drive_version"`
	PostEditDriveVersion int64          `json:"post_edit_drive_version"`
	FinalDriveVersion    int64          `json:"final_drive_version"`
	InitialDocsRevision  string         `json:"initial_docs_revision"`
	PostLinkDocsRevision string         `json:"post_link_docs_revision"`
	PostEditDocsRevision string         `json:"post_edit_docs_revision"`
	FinalDocsRevision    string         `json:"final_docs_revision"`
	PlaceholderSurvived  bool           `json:"placeholder_survived"`
	PlaceholderChanged   bool           `json:"placeholder_changed"`
	TableCount           int            `json:"table_count"`
	InlineObjectCount    int            `json:"inline_object_count"`
	NamedStyles          map[string]int `json:"named_styles"`
	CodeTextPresent      bool           `json:"code_text_present"`
	UnresolvedComments   int            `json:"unresolved_comments"`
	CommentPages         int            `json:"comment_pages"`
	SuggestionsDetected  bool           `json:"suggestions_detected"`
	MovePreservedID      bool           `json:"move_preserved_id"`
	MovePreservedURL     bool           `json:"move_preserved_url"`
	ReadyPropertyUpdated bool           `json:"ready_property_updated"`
	FinalParents         []string       `json:"final_parents"`
	ReferenceDOCX        string         `json:"reference_docx"`
	CleanedUp            bool           `json:"cleaned_up"`
	Markdown             MarkdownProbe  `json:"markdown"`
	ManualChecks         []string       `json:"manual_checks"`
}

type MarkdownProbe struct {
	Baseline                    MarkdownObservation    `json:"baseline"`
	AfterDirectEdit             MarkdownObservation    `json:"after_direct_edit"`
	AfterComments               MarkdownObservation    `json:"after_comments"`
	TabExports                  []TabExportObservation `json:"tab_exports"`
	DirectEditVisible           bool                   `json:"direct_edit_visible"`
	CommentsChangedExport       bool                   `json:"comments_changed_export"`
	MetadataAdvancedDriveOnly   bool                   `json:"metadata_advanced_drive_only"`
	DriveFileScopeExported      bool                   `json:"drive_file_scope_exported"`
	ExportLimitBytes            int64                  `json:"export_limit_bytes"`
	SuggestionViewsNeedManualUI bool                   `json:"suggestion_views_need_manual_ui"`
}

type MarkdownObservation struct {
	SHA256          string `json:"sha256"`
	Bytes           int    `json:"bytes"`
	Content         string `json:"content"`
	LFCount         int    `json:"lf_count"`
	CRLFCount       int    `json:"crlf_count"`
	EndsWithNewline bool   `json:"ends_with_newline"`
	ValidUTF8       bool   `json:"valid_utf8"`
	HasHeading      bool   `json:"has_heading"`
	HasList         bool   `json:"has_list"`
	HasBlockQuote   bool   `json:"has_block_quote"`
	HasTable        bool   `json:"has_table"`
	HasCodeFence    bool   `json:"has_code_fence"`
	HasImage        bool   `json:"has_image"`
	HasLink         bool   `json:"has_link"`
}

type TabExportObservation struct {
	Case            string              `json:"case"`
	Topology        []TabTopologyRecord `json:"topology"`
	Export          MarkdownObservation `json:"export"`
	AddedTabVisible bool                `json:"added_tab_visible"`
}

type TabTopologyRecord struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Index    int64  `json:"index"`
	ParentID string `json:"parent_id,omitempty"`
}

func NewService(provider ClientProvider) *Service {
	return &Service{auth: provider}
}

func (s *Service) Google(ctx context.Context, options GoogleOptions) (result GoogleResult, returnedErr error) {
	if options.Fixture == "" {
		return result, errors.New("a synthetic Markdown fixture is required")
	}
	docxPath, cleanupRender, err := renderFixture(ctx, options)
	if err != nil {
		return result, err
	}
	defer cleanupRender()

	client, err := s.auth.Client(ctx)
	if err != nil {
		return result, fmt.Errorf("authorize Google probe: %w", err)
	}
	driveService, err := drive.NewService(ctx, option.WithHTTPClient(client))
	if err != nil {
		return result, fmt.Errorf("create Drive client: %w", err)
	}
	docsService, err := docs.NewService(ctx, option.WithHTTPClient(client))
	if err != nil {
		return result, fmt.Errorf("create Docs client: %w", err)
	}

	result.ProbeID = fmt.Sprintf("t02-%d", time.Now().UTC().UnixNano())
	result.ReferenceDOCX = options.ReferenceDOCX
	if result.ReferenceDOCX == "" {
		result.ReferenceDOCX = "Pandoc default reference"
	}

	root, err := createFolder(ctx, driveService, "mdoc T02 disposable probe", nil, result.ProbeID, "root")
	if err != nil {
		return result, err
	}
	result.RootFolderID = root.Id
	defer func() {
		if options.Keep || result.RootFolderID == "" {
			return
		}
		cleanupContext, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if cleanupErr := trashFolder(cleanupContext, driveService, result.RootFolderID); cleanupErr != nil {
			if returnedErr == nil {
				returnedErr = cleanupErr
			} else {
				returnedErr = fmt.Errorf("%w; cleanup failed: %v", returnedErr, cleanupErr)
			}
			return
		}
		result.CleanedUp = true
	}()

	staging, err := createFolder(ctx, driveService, "staging", []string{root.Id}, result.ProbeID, "staging")
	if err != nil {
		return result, err
	}
	result.StagingFolderID = staging.Id
	review, err := createFolder(ctx, driveService, "review", []string{root.Id}, result.ProbeID, "review")
	if err != nil {
		return result, err
	}
	result.ReviewFolderID = review.Id

	created, err := importDOCX(ctx, driveService, docxPath, staging.Id, result.ProbeID)
	if err != nil {
		return result, err
	}
	result.DocumentID = created.Id
	result.DocumentURL = created.WebViewLink
	result.NativeGoogleDoc = created.MimeType == googleDocMIME
	result.InitialDriveVersion = created.Version

	searchIDs, err := searchByProbeID(ctx, driveService, result.ProbeID)
	if err != nil {
		return result, err
	}
	result.AppPropertySearchIDs = searchIDs

	initialDocument, err := docsService.Documents.Get(created.Id).SuggestionsViewMode("SUGGESTIONS_INLINE").Context(ctx).Do()
	if err != nil {
		return result, fmt.Errorf("read imported Google Doc: %w", err)
	}
	result.InitialDocsRevision = initialDocument.RevisionId
	initialInspection := inspectDocument(initialDocument)
	result.PlaceholderSurvived = len(initialInspection.PlaceholderRanges) > 0
	result.TableCount = initialInspection.TableCount
	result.InlineObjectCount = len(initialDocument.InlineObjects)
	result.NamedStyles = initialInspection.NamedStyles
	result.CodeTextPresent = initialInspection.CodeTextPresent
	result.SuggestionsDetected = initialInspection.SuggestionsDetected
	if !result.PlaceholderSurvived {
		return result, errors.New("Google import removed the placeholder hyperlink")
	}

	finalTarget := "https://docs.google.com/document/d/" + created.Id + "/edit"
	if err := updateLinks(ctx, docsService, created.Id, initialDocument.RevisionId, initialInspection.PlaceholderRanges, finalTarget); err != nil {
		return result, err
	}
	postLinkDocument, err := docsService.Documents.Get(created.Id).SuggestionsViewMode("SUGGESTIONS_INLINE").Context(ctx).Do()
	if err != nil {
		return result, fmt.Errorf("read Google Doc after link update: %w", err)
	}
	result.PostLinkDocsRevision = postLinkDocument.RevisionId
	result.PlaceholderChanged = documentHasLink(postLinkDocument, finalTarget) && !documentHasLink(postLinkDocument, placeholderURL)
	postLinkFile, err := getFile(ctx, driveService, created.Id)
	if err != nil {
		return result, err
	}
	result.PostLinkDriveVersion = postLinkFile.Version
	baseline, err := exportMarkdown(ctx, driveService, created.Id)
	if err != nil {
		return result, err
	}
	result.Markdown.Baseline = observeMarkdown(baseline)
	result.Markdown.DriveFileScopeExported = true
	result.Markdown.ExportLimitBytes = 10 * 1024 * 1024
	result.Markdown.SuggestionViewsNeedManualUI = true

	postLinkWithTabs, err := getDocumentWithTabs(ctx, docsService, created.Id)
	if err != nil {
		return result, err
	}
	rootTabID, err := onlyRootTabID(postLinkWithTabs)
	if err != nil {
		return result, err
	}
	if err := insertProbeEdit(ctx, docsService, created.Id, postLinkWithTabs.RevisionId, rootTabID); err != nil {
		return result, err
	}
	postEditDocument, err := getDocumentWithTabs(ctx, docsService, created.Id)
	if err != nil {
		return result, err
	}
	postEditFile, err := getFile(ctx, driveService, created.Id)
	if err != nil {
		return result, err
	}
	result.PostEditDocsRevision = postEditDocument.RevisionId
	result.PostEditDriveVersion = postEditFile.Version
	afterEdit, err := exportMarkdown(ctx, driveService, created.Id)
	if err != nil {
		return result, err
	}
	result.Markdown.AfterDirectEdit = observeMarkdown(afterEdit)
	result.Markdown.DirectEditVisible = bytes.Contains(afterEdit, []byte(probeEditMarker))

	for index := 1; index <= 2; index++ {
		comment := &drive.Comment{Content: fmt.Sprintf("Synthetic T02 unresolved comment %d", index)}
		if _, err := driveService.Comments.Create(created.Id, comment).Fields("id,resolved,deleted").Context(ctx).Do(); err != nil {
			return result, fmt.Errorf("create synthetic comment %d: %w", index, err)
		}
	}
	commentResult, err := listComments(ctx, driveService, created.Id)
	if err != nil {
		return result, err
	}
	result.UnresolvedComments = commentResult.Unresolved
	result.CommentPages = commentResult.Pages
	afterComments, err := exportMarkdown(ctx, driveService, created.Id)
	if err != nil {
		return result, err
	}
	result.Markdown.AfterComments = observeMarkdown(afterComments)
	result.Markdown.CommentsChangedExport = !bytes.Equal(afterEdit, afterComments)

	result.Markdown.TabExports, err = probeTabExports(ctx, docsService, driveService, created.Id, rootTabID)
	if err != nil {
		return result, err
	}
	preMetadataDocument, err := getDocumentWithTabs(ctx, docsService, created.Id)
	if err != nil {
		return result, err
	}
	preMetadataFile, err := getFile(ctx, driveService, created.Id)
	if err != nil {
		return result, err
	}

	readyProperties := map[string]string{
		"mdoc_probe_id": result.ProbeID,
		"mdoc_role":     "document",
		"mdoc_status":   "ready",
	}
	if _, err := driveService.Files.Update(created.Id, &drive.File{AppProperties: readyProperties}).Fields(fileFields).Context(ctx).Do(); err != nil {
		return result, fmt.Errorf("update document app properties: %w", err)
	}
	moved, err := driveService.Files.Update(created.Id, &drive.File{}).
		AddParents(review.Id).
		RemoveParents(staging.Id).
		Fields(fileFields).
		Context(ctx).
		Do()
	if err != nil {
		return result, fmt.Errorf("move document to review folder: %w", err)
	}
	result.MovePreservedID = moved.Id == created.Id
	result.MovePreservedURL = moved.WebViewLink == created.WebViewLink
	result.ReadyPropertyUpdated = moved.AppProperties["mdoc_status"] == "ready"
	result.FinalParents = append([]string(nil), moved.Parents...)

	finalDocument, err := docsService.Documents.Get(created.Id).SuggestionsViewMode("SUGGESTIONS_INLINE").Context(ctx).Do()
	if err != nil {
		return result, fmt.Errorf("read final Google Doc revision: %w", err)
	}
	finalFile, err := getFile(ctx, driveService, created.Id)
	if err != nil {
		return result, err
	}
	result.FinalDocsRevision = finalDocument.RevisionId
	result.FinalDriveVersion = finalFile.Version
	result.Markdown.MetadataAdvancedDriveOnly = finalFile.Version > preMetadataFile.Version && finalDocument.RevisionId == preMetadataDocument.RevisionId
	result.SuggestionsDetected = inspectDocument(finalDocument).SuggestionsDetected
	result.ManualChecks = []string{
		"Inspect Title, Heading 1 through Heading 5, table, code, SVG, PNG, and JPEG fidelity in the document URL.",
		"Confirm headings do not show automatic bookmark icons.",
		"Add one suggestion in Google Docs, then run mdoc probe inspect <document-id>. Drive export has no accepted or rejected suggestion view selector.",
		"Accept and reject disposable suggestions in turn and compare the explicit Markdown exports. The Docs API cannot create suggestion-mode edits.",
	}
	if err := validateAutomatedResult(result); err != nil {
		return result, err
	}
	return result, nil
}

func (s *Service) Inspect(ctx context.Context, documentID string) (InspectResult, error) {
	client, err := s.auth.Client(ctx)
	if err != nil {
		return InspectResult{}, err
	}
	driveService, err := drive.NewService(ctx, option.WithHTTPClient(client))
	if err != nil {
		return InspectResult{}, err
	}
	docsService, err := docs.NewService(ctx, option.WithHTTPClient(client))
	if err != nil {
		return InspectResult{}, err
	}
	document, err := docsService.Documents.Get(documentID).SuggestionsViewMode("SUGGESTIONS_INLINE").Context(ctx).Do()
	if err != nil {
		return InspectResult{}, err
	}
	file, err := getFile(ctx, driveService, documentID)
	if err != nil {
		return InspectResult{}, err
	}
	comments, err := listComments(ctx, driveService, documentID)
	if err != nil {
		return InspectResult{}, err
	}
	inspection := inspectDocument(document)
	return InspectResult{
		DocumentID:          documentID,
		DocumentURL:         file.WebViewLink,
		DriveVersion:        file.Version,
		DocsRevision:        document.RevisionId,
		UnresolvedComments:  comments.Unresolved,
		CommentPages:        comments.Pages,
		SuggestionsDetected: inspection.SuggestionsDetected,
		NamedStyles:         inspection.NamedStyles,
		TableCount:          inspection.TableCount,
		InlineObjectCount:   len(document.InlineObjects),
		CodeTextPresent:     inspection.CodeTextPresent,
	}, nil
}

func (s *Service) Cleanup(ctx context.Context, folderID string) error {
	client, err := s.auth.Client(ctx)
	if err != nil {
		return err
	}
	driveService, err := drive.NewService(ctx, option.WithHTTPClient(client))
	if err != nil {
		return err
	}
	root, err := getFile(ctx, driveService, folderID)
	if err != nil {
		return err
	}
	if err := validateProbeRoot(root); err != nil {
		return err
	}
	return trashFolder(ctx, driveService, folderID)
}

func exportMarkdown(ctx context.Context, service *drive.Service, fileID string) ([]byte, error) {
	response, err := service.Files.Export(fileID, googleMarkdownMIME).Context(ctx).Download()
	if err != nil {
		return nil, fmt.Errorf("export disposable Google Doc as Markdown: %w", err)
	}
	defer response.Body.Close()
	const limit = int64(10 * 1024 * 1024)
	content, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("read disposable Markdown export: %w", err)
	}
	if int64(len(content)) > limit {
		return nil, fmt.Errorf("disposable Markdown export exceeds the 10 MB Drive limit")
	}
	if len(content) == 0 {
		return nil, errors.New("disposable Markdown export is empty")
	}
	return content, nil
}

func observeMarkdown(content []byte) MarkdownObservation {
	sum := sha256.Sum256(content)
	text := string(content)
	return MarkdownObservation{
		SHA256:          fmt.Sprintf("%x", sum[:]),
		Bytes:           len(content),
		Content:         text,
		LFCount:         bytes.Count(content, []byte("\n")),
		CRLFCount:       bytes.Count(content, []byte("\r\n")),
		EndsWithNewline: bytes.HasSuffix(content, []byte("\n")),
		ValidUTF8:       utf8.Valid(content),
		HasHeading:      strings.Contains(text, "# ") || strings.Contains(text, "## "),
		HasList:         strings.Contains(text, "\n- ") || strings.Contains(text, "\n* ") || strings.Contains(text, "\n1. "),
		HasBlockQuote:   strings.Contains(text, "\n> "),
		HasTable:        strings.Contains(text, "|---") || strings.Contains(text, "| ---"),
		HasCodeFence:    strings.Contains(text, "```") || strings.Contains(text, "~~~"),
		HasImage:        strings.Contains(text, "!["),
		HasLink:         strings.Contains(text, "]("),
	}
}

func getDocumentWithTabs(ctx context.Context, service *docs.Service, documentID string) (*docs.Document, error) {
	document, err := service.Documents.Get(documentID).
		SuggestionsViewMode("SUGGESTIONS_INLINE").
		IncludeTabsContent(true).
		Context(ctx).
		Do()
	if err != nil {
		return nil, fmt.Errorf("read disposable Google Doc tabs: %w", err)
	}
	return document, nil
}

func onlyRootTabID(document *docs.Document) (string, error) {
	if document == nil || len(document.Tabs) != 1 || document.Tabs[0] == nil || document.Tabs[0].TabProperties == nil || len(document.Tabs[0].ChildTabs) != 0 {
		return "", errors.New("disposable import did not produce exactly one root tab")
	}
	if document.Tabs[0].TabProperties.TabId == "" {
		return "", errors.New("disposable import root tab has no ID")
	}
	return document.Tabs[0].TabProperties.TabId, nil
}

func insertProbeEdit(ctx context.Context, service *docs.Service, documentID, revisionID, tabID string) error {
	_, err := service.Documents.BatchUpdate(documentID, &docs.BatchUpdateDocumentRequest{
		Requests: []*docs.Request{{InsertText: &docs.InsertTextRequest{
			EndOfSegmentLocation: &docs.EndOfSegmentLocation{TabId: tabID},
			Text:                 "\n" + probeEditMarker + "\n",
		}}},
		WriteControl: &docs.WriteControl{RequiredRevisionId: revisionID},
	}).Context(ctx).Do()
	if err != nil {
		return fmt.Errorf("insert disposable direct edit: %w", err)
	}
	return nil
}

func probeTabExports(ctx context.Context, docsService *docs.Service, driveService *drive.Service, documentID, rootTabID string) ([]TabExportObservation, error) {
	observations := make([]TabExportObservation, 0, 5)
	observe := func(name string) error {
		document, err := getDocumentWithTabs(ctx, docsService, documentID)
		if err != nil {
			return err
		}
		content, err := exportMarkdown(ctx, driveService, documentID)
		if err != nil {
			return err
		}
		observations = append(observations, TabExportObservation{
			Case:            name,
			Topology:        flattenTabTopology(document.Tabs),
			Export:          observeMarkdown(content),
			AddedTabVisible: bytes.Contains(content, []byte("Disposable second tab content")),
		})
		return nil
	}
	if err := observe("single-root"); err != nil {
		return nil, err
	}
	response, err := docsService.Documents.BatchUpdate(documentID, &docs.BatchUpdateDocumentRequest{Requests: []*docs.Request{{
		AddDocumentTab: &docs.AddDocumentTabRequest{TabProperties: &docs.TabProperties{Title: "Added tab"}},
	}}}).Context(ctx).Do()
	if err != nil {
		return nil, fmt.Errorf("add disposable root tab: %w", err)
	}
	if len(response.Replies) != 1 || response.Replies[0].AddDocumentTab == nil || response.Replies[0].AddDocumentTab.TabProperties == nil {
		return nil, errors.New("add disposable root tab returned no tab ID")
	}
	addedTabID := response.Replies[0].AddDocumentTab.TabProperties.TabId
	if _, err := docsService.Documents.BatchUpdate(documentID, &docs.BatchUpdateDocumentRequest{Requests: []*docs.Request{{
		InsertText: &docs.InsertTextRequest{EndOfSegmentLocation: &docs.EndOfSegmentLocation{TabId: addedTabID}, Text: "Disposable second tab content\n"},
	}}}).Context(ctx).Do(); err != nil {
		return nil, fmt.Errorf("write disposable root tab: %w", err)
	}
	if err := observe("added-root"); err != nil {
		return nil, err
	}
	if err := updateProbeTab(ctx, docsService, documentID, &docs.TabProperties{TabId: addedTabID, Title: "Renamed tab"}, "title"); err != nil {
		return nil, err
	}
	if err := observe("renamed-root"); err != nil {
		return nil, err
	}
	properties := &docs.TabProperties{TabId: addedTabID, Index: 0, ForceSendFields: []string{"Index"}}
	if err := updateProbeTab(ctx, docsService, documentID, properties, "index"); err != nil {
		return nil, err
	}
	if err := observe("reordered-root"); err != nil {
		return nil, err
	}
	if err := updateProbeTab(ctx, docsService, documentID, &docs.TabProperties{TabId: addedTabID, ParentTabId: rootTabID}, "parentTabId"); err != nil {
		return nil, err
	}
	if err := observe("nested-child"); err != nil {
		return nil, err
	}
	if _, err := docsService.Documents.BatchUpdate(documentID, &docs.BatchUpdateDocumentRequest{Requests: []*docs.Request{{
		DeleteTab: &docs.DeleteTabRequest{TabId: addedTabID},
	}}}).Context(ctx).Do(); err != nil {
		return nil, fmt.Errorf("delete disposable tab: %w", err)
	}
	return observations, nil
}

func updateProbeTab(ctx context.Context, service *docs.Service, documentID string, properties *docs.TabProperties, fields string) error {
	_, err := service.Documents.BatchUpdate(documentID, &docs.BatchUpdateDocumentRequest{Requests: []*docs.Request{{
		UpdateDocumentTabProperties: &docs.UpdateDocumentTabPropertiesRequest{TabProperties: properties, Fields: fields},
	}}}).Context(ctx).Do()
	if err != nil {
		return fmt.Errorf("update disposable tab %s: %w", fields, err)
	}
	return nil
}

func flattenTabTopology(tabs []*docs.Tab) []TabTopologyRecord {
	result := make([]TabTopologyRecord, 0)
	var walk func([]*docs.Tab)
	walk = func(items []*docs.Tab) {
		for _, tab := range items {
			if tab == nil || tab.TabProperties == nil {
				continue
			}
			result = append(result, TabTopologyRecord{
				ID:       tab.TabProperties.TabId,
				Title:    tab.TabProperties.Title,
				Index:    tab.TabProperties.Index,
				ParentID: tab.TabProperties.ParentTabId,
			})
			walk(tab.ChildTabs)
		}
	}
	walk(tabs)
	return result
}

func validateProbeRoot(file *drive.File) error {
	if file == nil || file.MimeType != googleFolderMIME || file.Name != "mdoc T02 disposable probe" || file.AppProperties["mdoc_role"] != "root" || !strings.HasPrefix(file.AppProperties["mdoc_probe_id"], "t02-") {
		return errors.New("refusing to trash a folder that is not an app-created T02 probe root")
	}
	return nil
}

func validateAutomatedResult(result GoogleResult) error {
	failures := make([]string, 0)
	require := func(ok bool, name string) {
		if !ok {
			failures = append(failures, name)
		}
	}
	require(result.NativeGoogleDoc, "native Google Doc import")
	require(containsEvery(result.AppPropertySearchIDs, result.RootFolderID, result.StagingFolderID, result.ReviewFolderID, result.DocumentID), "app property search")
	require(result.InitialDriveVersion > 0 && result.PostLinkDriveVersion > 0 && result.PostEditDriveVersion > 0 && result.FinalDriveVersion > 0, "Drive version reads")
	require(result.InitialDocsRevision != "" && result.PostLinkDocsRevision != "" && result.FinalDocsRevision != "" && result.InitialDocsRevision != result.PostLinkDocsRevision, "Docs revision progression")
	require(result.PlaceholderSurvived && result.PlaceholderChanged, "placeholder link fixup")
	require(result.NamedStyles["TITLE"] > 0 && result.NamedStyles["HEADING_1"] > 0 && result.NamedStyles["HEADING_2"] > 0 && result.NamedStyles["HEADING_3"] > 0 && result.NamedStyles["HEADING_4"] > 0 && result.NamedStyles["HEADING_5"] > 0, "heading style ladder")
	require(result.TableCount > 0, "table import")
	require(result.InlineObjectCount >= 3, "image import")
	require(result.CodeTextPresent, "code import")
	require(result.UnresolvedComments >= 2 && result.CommentPages >= 2, "paginated unresolved comments")
	require(result.Markdown.DriveFileScopeExported && result.Markdown.Baseline.Bytes > 0 && result.Markdown.Baseline.ValidUTF8, "native Markdown export")
	require(result.Markdown.DirectEditVisible && !result.Markdown.CommentsChangedExport, "review edit and comment export behavior")
	require(len(result.Markdown.TabExports) == 5, "tab export cases")
	require(result.MovePreservedID && result.MovePreservedURL && result.ReadyPropertyUpdated, "ready folder move")
	require(len(result.FinalParents) == 1 && result.FinalParents[0] == result.ReviewFolderID, "final review parent")
	if len(failures) > 0 {
		return fmt.Errorf("Google probe automated checks failed: %s", strings.Join(failures, ", "))
	}
	return nil
}

func containsEvery(values []string, required ...string) bool {
	present := make(map[string]struct{}, len(values))
	for _, value := range values {
		present[value] = struct{}{}
	}
	for _, value := range required {
		if value == "" {
			return false
		}
		if _, ok := present[value]; !ok {
			return false
		}
	}
	return true
}

type InspectResult struct {
	DocumentID          string         `json:"document_id"`
	DocumentURL         string         `json:"document_url"`
	DriveVersion        int64          `json:"drive_version"`
	DocsRevision        string         `json:"docs_revision"`
	UnresolvedComments  int            `json:"unresolved_comments"`
	CommentPages        int            `json:"comment_pages"`
	SuggestionsDetected bool           `json:"suggestions_detected"`
	NamedStyles         map[string]int `json:"named_styles"`
	TableCount          int            `json:"table_count"`
	InlineObjectCount   int            `json:"inline_object_count"`
	CodeTextPresent     bool           `json:"code_text_present"`
}

const fileFields = "id,name,mimeType,parents,appProperties,version,modifiedTime,webViewLink,trashed"

func renderFixture(ctx context.Context, options GoogleOptions) (string, func(), error) {
	fixture, err := filepath.Abs(options.Fixture)
	if err != nil {
		return "", func() {}, err
	}
	if _, err := os.Stat(fixture); err != nil {
		return "", func() {}, fmt.Errorf("inspect synthetic fixture: %w", err)
	}
	resourcePath := filepath.Dir(fixture)
	filterPath := filepath.Join(resourcePath, "title-style.lua")
	if _, err := os.Stat(filterPath); err != nil {
		return "", func() {}, fmt.Errorf("inspect title style filter: %w", err)
	}
	temporaryDir, err := os.MkdirTemp("", "mdoc-t02-*")
	if err != nil {
		return "", func() {}, err
	}
	cleanup := func() { _ = os.RemoveAll(temporaryDir) }
	output := filepath.Join(temporaryDir, "google-import.docx")
	arguments := []string{
		fixture,
		"--from=gfm",
		"--to=docx",
		"--lua-filter=" + filterPath,
		"--resource-path=" + resourcePath,
		"--output=" + output,
	}
	if options.ReferenceDOCX != "" {
		reference, absoluteErr := filepath.Abs(options.ReferenceDOCX)
		if absoluteErr != nil {
			cleanup()
			return "", func() {}, absoluteErr
		}
		arguments = append(arguments, "--reference-doc="+reference)
	}
	command := exec.CommandContext(ctx, "pandoc", arguments...)
	combined, err := command.CombinedOutput()
	if err != nil {
		cleanup()
		return "", func() {}, fmt.Errorf("render synthetic DOCX: %w: %s", err, string(combined))
	}
	return output, cleanup, nil
}

func createFolder(ctx context.Context, service *drive.Service, name string, parents []string, probeID, role string) (*drive.File, error) {
	folder := &drive.File{
		Name:     name,
		MimeType: googleFolderMIME,
		Parents:  parents,
		AppProperties: map[string]string{
			"mdoc_probe_id": probeID,
			"mdoc_role":     role,
		},
	}
	created, err := service.Files.Create(folder).Fields(fileFields).Context(ctx).Do()
	if err != nil {
		return nil, fmt.Errorf("create %s probe folder: %w", role, err)
	}
	return created, nil
}

func importDOCX(ctx context.Context, service *drive.Service, docxPath, stagingID, probeID string) (*drive.File, error) {
	file, err := os.Open(docxPath)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	metadata := &drive.File{
		Name:     "Synthetic Import Probe",
		MimeType: googleDocMIME,
		Parents:  []string{stagingID},
		AppProperties: map[string]string{
			"mdoc_probe_id": probeID,
			"mdoc_role":     "document",
			"mdoc_status":   "pending",
		},
	}
	created, err := service.Files.Create(metadata).
		Media(file, googleapi.ContentType(docxMIME)).
		Fields(fileFields).
		Context(ctx).
		Do()
	if err != nil {
		return nil, fmt.Errorf("import synthetic DOCX: %w", err)
	}
	return created, nil
}

func searchByProbeID(ctx context.Context, service *drive.Service, probeID string) ([]string, error) {
	query := fmt.Sprintf("trashed = false and appProperties has { key='mdoc_probe_id' and value='%s' }", probeID)
	list, err := service.Files.List().Q(query).Fields("files(id),nextPageToken").PageSize(100).Context(ctx).Do()
	if err != nil {
		return nil, fmt.Errorf("search probe app properties: %w", err)
	}
	ids := make([]string, 0, len(list.Files))
	for _, file := range list.Files {
		ids = append(ids, file.Id)
	}
	sort.Strings(ids)
	return ids, nil
}

func getFile(ctx context.Context, service *drive.Service, fileID string) (*drive.File, error) {
	file, err := service.Files.Get(fileID).Fields(fileFields).Context(ctx).Do()
	if err != nil {
		return nil, fmt.Errorf("read Drive file metadata: %w", err)
	}
	return file, nil
}

func updateLinks(ctx context.Context, service *docs.Service, documentID, revisionID string, ranges []linkRange, target string) error {
	requests := make([]*docs.Request, 0, len(ranges))
	for _, found := range ranges {
		requests = append(requests, &docs.Request{UpdateTextStyle: &docs.UpdateTextStyleRequest{
			Range:     &docs.Range{StartIndex: found.Start, EndIndex: found.End},
			TextStyle: &docs.TextStyle{Link: &docs.Link{Url: target}},
			Fields:    "link",
		}})
	}
	_, err := service.Documents.BatchUpdate(documentID, &docs.BatchUpdateDocumentRequest{
		Requests:     requests,
		WriteControl: &docs.WriteControl{RequiredRevisionId: revisionID},
	}).Context(ctx).Do()
	if err != nil {
		return fmt.Errorf("update placeholder links: %w", err)
	}
	return nil
}

type commentListResult struct {
	Unresolved int
	Pages      int
}

func listComments(ctx context.Context, service *drive.Service, fileID string) (commentListResult, error) {
	result := commentListResult{}
	pageToken := ""
	for {
		call := service.Comments.List(fileID).
			PageSize(1).
			Fields("comments(id,resolved,deleted),nextPageToken").
			Context(ctx)
		if pageToken != "" {
			call = call.PageToken(pageToken)
		}
		page, err := call.Do()
		if err != nil {
			return result, fmt.Errorf("list Google Drive comments: %w", err)
		}
		result.Pages++
		for _, comment := range page.Comments {
			if !comment.Deleted && !comment.Resolved {
				result.Unresolved++
			}
		}
		if page.NextPageToken == "" {
			break
		}
		pageToken = page.NextPageToken
	}
	return result, nil
}

func trashFolder(ctx context.Context, service *drive.Service, folderID string) error {
	updated, err := service.Files.Update(folderID, &drive.File{Trashed: true, ForceSendFields: []string{"Trashed"}}).
		Fields("id,trashed").
		Context(ctx).
		Do()
	if err != nil {
		return fmt.Errorf("trash disposable probe folder: %w", err)
	}
	if !updated.Trashed {
		return errors.New("Google did not mark the disposable probe folder as trashed")
	}
	return nil
}
