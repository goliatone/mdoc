package app

import (
	"context"
	"errors"
	"io"
	"maps"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/goliatone/mdoc/internal/config"
	"github.com/goliatone/mdoc/internal/document"
	"github.com/goliatone/mdoc/internal/googleapi"
	"github.com/goliatone/mdoc/internal/planner"
	"github.com/goliatone/mdoc/internal/render"
	"github.com/goliatone/mdoc/internal/reviewsync"
	"github.com/goliatone/mdoc/internal/state"
)

type batchDrive struct {
	files             map[string]googleapi.File
	getErrors         map[string]error
	bySource          map[string]string
	createCalls       map[string]int
	failCreate        map[string]int
	docs              *batchDocs
	nextID            int
	failMoveAfter     int
	folders           map[string][]googleapi.Folder
	failReadyAfter    int
	comments          map[string]googleapi.CommentStatus
	propertyCalls     map[string]int
	failPropertyAfter int
	exportCalls       map[string]int
	failExport        int
	exportContent     []byte
	editOnMove        bool
}

func newBatchDrive(docs *batchDocs) *batchDrive {
	return &batchDrive{files: map[string]googleapi.File{}, getErrors: map[string]error{}, bySource: map[string]string{}, createCalls: map[string]int{}, failCreate: map[string]int{}, docs: docs, folders: map[string][]googleapi.Folder{}, comments: map[string]googleapi.CommentStatus{}, propertyCalls: map[string]int{}, exportCalls: map[string]int{}}
}

func (f *batchDrive) CreateDocument(_ context.Context, input googleapi.CreateDocumentInput) (googleapi.File, error) {
	source := input.AppProperties["mdoc_source"]
	f.createCalls[source]++
	if f.failCreate[source] > 0 {
		f.failCreate[source]--
		return googleapi.File{}, &googleapi.Error{Kind: googleapi.KindPermission, Operation: "create", Cause: errors.New("injected")}
	}
	_, _ = io.ReadAll(input.Content)
	f.nextID++
	id := "doc-" + strconv.Itoa(f.nextID)
	file := googleapi.File{ID: id, Name: input.Name, URL: "https://docs.google.com/document/d/" + id + "/edit", MimeType: googleapi.DocumentMimeType, Version: "1", ModifiedTime: time.Unix(int64(f.nextID), 0).UTC(), Parents: []string{input.ParentID}, AppProperties: copyProperties(input.AppProperties)}
	f.files[id] = file
	f.bySource[source] = id
	f.docs.create(id, source)
	return file, nil
}
func (f *batchDrive) FindDocuments(_ context.Context, properties map[string]string) ([]googleapi.File, error) {
	result := []googleapi.File{}
	for _, file := range f.files {
		if file.Trashed {
			continue
		}
		match := true
		for key, value := range properties {
			if file.AppProperties[key] != value {
				match = false
			}
		}
		if match {
			result = append(result, file)
		}
	}
	return result, nil
}

func TestInspectPublicationTargetsAllowsTrashedActiveWithoutLiveClaim(t *testing.T) {
	docs := newBatchDocs()
	drive := newBatchDrive(docs)
	active := state.Target{FileID: "doc-1", Generation: 4, ReviewSetID: "set-1", OperationID: "operation-1"}
	properties := map[string]string{
		"mdoc_workspace": "workspace", "mdoc_profile": "work", "mdoc_target_key": "publication:report",
		"mdoc_publication": "report", "mdoc_publication_kind": "bundle", "mdoc_source": "",
		"mdoc_generation": "4", "mdoc_review_set": active.ReviewSetID, "mdoc_operation": active.OperationID, "mdoc_status": "ready",
	}
	drive.files[active.FileID] = googleapi.File{
		ID: active.FileID, Name: "Report", URL: "https://docs.google.com/document/d/doc-1/edit", MimeType: googleapi.DocumentMimeType,
		Version: "9", Trashed: true, Parents: []string{"review"}, AppProperties: properties,
	}
	docs.documents[active.FileID] = googleapi.Document{ID: active.FileID, RevisionID: "r1", Links: map[string][]googleapi.TextRange{}}
	publishState := state.New("workspace", "work")
	publishState.Targets["publication:report"] = state.Document{
		TargetKey: "publication:report", PublicationID: "report", PublicationKind: "bundle", ActiveTarget: &active,
	}
	application := New(io.Discard)
	remotes, err := application.inspectPublicationTargets(
		context.Background(), &config.Config{WorkspaceID: "workspace"}, config.SelectedProfile{Name: "work"}, publishState,
		googleapi.Clients{Drive: drive, Docs: docs}, map[string]bool{"publication:report": true},
	)
	if err != nil {
		t.Fatal(err)
	}
	remote := remotes["publication:report"]
	if !remote.Trashed || remote.Ambiguous || remote.MetadataMismatch {
		t.Fatalf("trashed remote inspection = %#v", remote)
	}
}
func (f *batchDrive) GetFile(_ context.Context, id string) (googleapi.File, error) {
	if err := f.getErrors[id]; err != nil {
		return googleapi.File{}, err
	}
	file, ok := f.files[id]
	if !ok {
		return googleapi.File{}, errors.New("missing file")
	}
	return file, nil
}
func (f *batchDrive) UpdateProperties(_ context.Context, id string, properties map[string]string) (googleapi.File, error) {
	f.propertyCalls[id]++
	file := f.files[id]
	maps.Copy(file.AppProperties, properties)
	file.Version = increment(file.Version)
	f.files[id] = file
	if f.failPropertyAfter > 0 {
		f.failPropertyAfter--
		return googleapi.File{}, errors.New("lost property response")
	}
	if properties["mdoc_status"] == "ready" && f.failReadyAfter > 0 {
		f.failReadyAfter--
		return googleapi.File{}, errors.New("lost ready response")
	}
	return file, nil
}
func (f *batchDrive) MoveFile(_ context.Context, id, add, _ string) (googleapi.File, error) {
	file := f.files[id]
	file.Parents = []string{add}
	file.Version = increment(file.Version)
	f.files[id] = file
	if f.editOnMove {
		document := f.docs.documents[id]
		document.RevisionID = "r-edited"
		f.docs.documents[id] = document
	}
	if f.failMoveAfter > 0 {
		f.failMoveAfter--
		return googleapi.File{}, errors.New("lost move response")
	}
	return file, nil
}
func (f *batchDrive) EnsureFolders(context.Context, googleapi.FolderSpec) (googleapi.FolderPair, error) {
	return googleapi.FolderPair{}, nil
}
func (f *batchDrive) FindFolders(_ context.Context, _, _, role string) ([]googleapi.Folder, error) {
	return f.folders[role], nil
}
func (f *batchDrive) CreateFolder(context.Context, string, string, string, string) (googleapi.Folder, error) {
	return googleapi.Folder{}, nil
}
func (f *batchDrive) ListComments(_ context.Context, id string) (googleapi.CommentStatus, error) {
	return f.comments[id], nil
}
func (f *batchDrive) ExportDocument(_ context.Context, id, _ string) ([]byte, error) {
	f.exportCalls[id]++
	if f.failExport > 0 {
		f.failExport--
		return nil, errors.New("injected export failure")
	}
	if f.exportContent != nil {
		return append([]byte(nil), f.exportContent...), nil
	}
	return []byte("# Export"), nil
}

type batchDocs struct {
	documents        map[string]googleapi.Document
	linksBySource    map[string]map[string][]googleapi.TextRange
	failUpdates      int
	failAfterUpdates int
	failRetryable    int
	updateCalls      int
}

func TestSelectedSourcesNormalizesNFC(t *testing.T) {
	graph := &document.Graph{ByKey: map[string]*document.Document{"docs/caf\u00e9.md": {SourceKey: "docs/caf\u00e9.md"}}}
	selected, err := selectedSources(t.TempDir(), graph, []string{"docs/cafe\u0301.md"})
	if err != nil {
		t.Fatal(err)
	}
	if !selected["docs/caf\u00e9.md"] {
		t.Fatalf("selected = %#v", selected)
	}
}

func TestSelectedSourcesAcceptsApprovedExternalPathAndKey(t *testing.T) {
	root := t.TempDir()
	external := t.TempDir()
	path := filepath.Join(external, "outside.md")
	if err := os.WriteFile(path, []byte("# External\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	graph := &document.Graph{
		Documents: []*document.Document{{SourceKey: "external/1/outside.md", Path: path}},
		ByKey:     map[string]*document.Document{"external/1/outside.md": {SourceKey: "external/1/outside.md", Path: path}},
	}
	for _, value := range []string{path, "external/1/outside.md"} {
		selected, err := selectedSources(root, graph, []string{value})
		if err != nil {
			t.Fatalf("select %q: %v", value, err)
		}
		if !selected["external/1/outside.md"] {
			t.Fatalf("selected %q = %#v", value, selected)
		}
	}
}

func newBatchDocs() *batchDocs {
	return &batchDocs{documents: map[string]googleapi.Document{}, linksBySource: map[string]map[string][]googleapi.TextRange{}}
}
func (f *batchDocs) create(id, source string) {
	f.documents[id] = googleapi.Document{ID: id, RevisionID: "r1", Links: copyLinks(f.linksBySource[source]), Tabs: []googleapi.TabTopology{{ID: "tab", Title: "Tab", Index: 0}}}
}
func (f *batchDocs) GetDocument(_ context.Context, id string) (googleapi.Document, error) {
	doc, ok := f.documents[id]
	if !ok {
		return googleapi.Document{}, errors.New("missing doc")
	}
	return doc, nil
}
func (f *batchDocs) UpdateLinks(_ context.Context, id, revision string, updates []googleapi.LinkUpdate) (googleapi.Document, error) {
	f.updateCalls++
	if f.failRetryable > 0 {
		f.failRetryable--
		return googleapi.Document{}, &googleapi.Error{Kind: googleapi.KindRetryable, Operation: "links", Cause: errors.New("transient")}
	}
	if f.failUpdates > 0 {
		f.failUpdates--
		return googleapi.Document{}, errors.New("injected link failure")
	}
	doc := f.documents[id]
	if doc.RevisionID != revision {
		return googleapi.Document{}, errors.New("revision mismatch")
	}
	for _, update := range updates {
		moved := []googleapi.TextRange{}
		for url, ranges := range doc.Links {
			kept := []googleapi.TextRange{}
			for _, textRange := range ranges {
				if textRange.StartIndex == update.StartIndex && textRange.EndIndex == update.EndIndex {
					moved = append(moved, textRange)
				} else {
					kept = append(kept, textRange)
				}
			}
			if len(kept) == 0 {
				delete(doc.Links, url)
			} else {
				doc.Links[url] = kept
			}
		}
		doc.Links[update.URL] = append(doc.Links[update.URL], moved...)
	}
	doc.RevisionID = incrementRevision(doc.RevisionID)
	f.documents[id] = doc
	if f.failAfterUpdates > 0 {
		f.failAfterUpdates--
		return googleapi.Document{}, errors.New("lost link response")
	}
	return doc, nil
}

func TestBatchPublishResolvesLinksAndCommitsFinalState(t *testing.T) {
	docs := newBatchDocs()
	placeholder := PlaceholderURL("docs/b.md")
	docs.linksBySource["docs/a.md"] = map[string][]googleapi.TextRange{placeholder: {{StartIndex: 1, EndIndex: 5}}}
	drive := newBatchDrive(docs)
	store := newBatchStore(t)
	defer store.Release()
	request := linkedBatch(placeholder)
	result, err := (BatchPublisher{Drive: drive, Docs: docs, Store: store, Now: func() time.Time { return time.Unix(50, 0).UTC() }}).Run(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	bURL := result.Targets["docs/b.md"].URL
	aID := result.Targets["docs/a.md"].FileID
	if len(docs.documents[aID].Links[bURL]) != 1 {
		t.Fatalf("links = %#v", docs.documents[aID].Links)
	}
	loaded, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Documents["docs/a.md"].ActiveTarget.PublishStatus != "ready" || loaded.Documents["docs/a.md"].ActiveTarget.DocsRevision == "" {
		t.Fatalf("state = %#v", loaded)
	}
	if _, err := store.LoadJournal(); !errors.Is(err, state.ErrNotFound) {
		t.Fatalf("journal = %v", err)
	}
}

func TestBatchPublishBlocksMissingMappingBeforeWrites(t *testing.T) {
	docs := newBatchDocs()
	drive := newBatchDrive(docs)
	store := newBatchStore(t)
	defer store.Release()
	request := linkedBatch(PlaceholderURL("docs/missing.md"))
	request.Items = request.Items[:1]
	request.Items[0].Links[0].TargetSource = "docs/missing.md"
	_, err := (BatchPublisher{Drive: drive, Docs: docs, Store: store}).Run(context.Background(), request)
	if err == nil {
		t.Fatal("expected blocked plan")
	}
	if drive.nextID != 0 {
		t.Fatalf("remote creates = %d", drive.nextID)
	}
	if _, journalErr := store.LoadJournal(); !errors.Is(journalErr, state.ErrNotFound) {
		t.Fatalf("journal = %v", journalErr)
	}
}

func TestBatchPublishResumesCreatesWithSameIDs(t *testing.T) {
	docs := newBatchDocs()
	placeholder := PlaceholderURL("docs/b.md")
	docs.linksBySource["docs/a.md"] = map[string][]googleapi.TextRange{placeholder: {{StartIndex: 1, EndIndex: 5}}}
	drive := newBatchDrive(docs)
	drive.failCreate["docs/b.md"] = 1
	store := newBatchStore(t)
	defer store.Release()
	request := linkedBatch(placeholder)
	if _, err := (BatchPublisher{Drive: drive, Docs: docs, Store: store}).Run(context.Background(), request); err == nil {
		t.Fatal("expected partial failure")
	} else {
		var typed *Error
		if !errors.As(err, &typed) || typed.Recovery == nil {
			t.Fatalf("recovery error = %#v", err)
		}
		if typed.Recovery.Kind != "publish" || typed.Recovery.OperationID != request.OperationID || typed.Recovery.ReviewSetID != request.ReviewSetID || typed.Recovery.FileIDs["docs/a.md"] == "" || typed.Recovery.JournalPath != store.JournalPath() || !strings.Contains(typed.Recovery.NextCommand, "mdoc publish") {
			t.Fatalf("recovery = %#v", typed.Recovery)
		}
		for _, detail := range []string{request.OperationID, request.ReviewSetID, typed.Recovery.FileIDs["docs/a.md"], store.JournalPath(), typed.Recovery.NextCommand} {
			if !strings.Contains(err.Error(), detail) {
				t.Fatalf("human error %q omits %q", err, detail)
			}
		}
	}
	firstID := drive.bySource["docs/a.md"]
	result, err := (BatchPublisher{Drive: drive, Docs: docs, Store: store}).Run(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if result.Targets["docs/a.md"].FileID != firstID || drive.createCalls["docs/a.md"] != 1 {
		t.Fatalf("first ID was not reused: %#v calls=%#v", result.Targets, drive.createCalls)
	}
}

func TestBatchPublishResumesAfterCreateHookFailureWithSameID(t *testing.T) {
	docs := newBatchDocs()
	drive := newBatchDrive(docs)
	store := newBatchStore(t)
	defer store.Release()
	request := BatchRequest{WorkspaceID: "workspace", Profile: "work", StagingID: "staging", ReviewID: "review", ReviewSetID: "set", OperationID: "operation", Items: []BatchItem{{SourceKey: "docs/a.md", Title: "A", DOCX: []byte("a"), SourceHash: "source", RenderHash: "render", Generation: 1}}}
	injected := true
	publisher := BatchPublisher{Drive: drive, Docs: docs, Store: store, AfterCreate: func(sourceKey, fileID string) error {
		if injected {
			injected = false
			return errors.New("controlled failure after create")
		}
		return nil
	}}
	if _, err := publisher.Run(context.Background(), request); err == nil {
		t.Fatal("expected controlled failure")
	}
	createdID := drive.bySource["docs/a.md"]
	journal, err := store.LoadJournal()
	if err != nil {
		t.Fatal(err)
	}
	if journal.Entries["docs/a.md"].FileID != createdID || journal.Entries["docs/a.md"].Stage != state.StageCreated {
		t.Fatalf("journal = %#v", journal.Entries["docs/a.md"])
	}
	result, err := publisher.Run(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if result.Targets["docs/a.md"].FileID != createdID || drive.createCalls["docs/a.md"] != 1 {
		t.Fatalf("result=%#v creates=%#v", result.Targets, drive.createCalls)
	}
}

func TestBatchPublishCapturesAndSealsReviewSnapshot(t *testing.T) {
	docs := newBatchDocs()
	drive := newBatchDrive(docs)
	store := newBatchStore(t)
	defer store.Release()
	request := reviewBatch()
	result, err := (BatchPublisher{Drive: drive, Docs: docs, Store: store, Now: func() time.Time { return time.Unix(50, 0).UTC() }}).Run(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	target := result.Targets["publication:report"]
	if target.ReviewSnapshot == nil || target.RemoteVersion != "3" || target.ReviewSnapshot.CapturedRevision != "r1" || target.ReviewSnapshot.ActivatedRevision != "r1" {
		t.Fatalf("target = %#v", target)
	}
	if drive.exportCalls[target.FileID] != 1 {
		t.Fatalf("export calls = %#v", drive.exportCalls)
	}
	snapshots, err := reviewsync.NewSnapshotStore(store.Path())
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := snapshots.Load("publication:report", 1, true)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Manifest.Capture.DriveVersion != "1" || loaded.Manifest.Activation.DriveVersion != "3" || loaded.Manifest.IntegrityHash != target.ReviewSnapshot.ManifestHash {
		t.Fatalf("snapshot = %#v", loaded.Manifest)
	}
	stateValue, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if stateValue.Targets["publication:report"].ActiveTarget.ReviewSnapshot == nil {
		t.Fatal("sealed snapshot reference was not committed")
	}
}

func TestBatchPublishReviewCandidateResumeDoesNotRecapture(t *testing.T) {
	docs := newBatchDocs()
	drive := newBatchDrive(docs)
	store := newBatchStore(t)
	defer store.Release()
	request := reviewBatch()
	fail := true
	publisher := BatchPublisher{Drive: drive, Docs: docs, Store: store, AfterReviewCandidate: func(string) error {
		if fail {
			fail = false
			return errors.New("failure after candidate")
		}
		return nil
	}}
	if _, err := publisher.Run(context.Background(), request); err == nil {
		t.Fatal("expected candidate boundary failure")
	}
	fileID := drive.bySource["docs/a.md"]
	journal, err := store.LoadJournal()
	if err != nil {
		t.Fatal(err)
	}
	if journal.Entries["publication:report"].Stage != state.StageCapturingReview {
		t.Fatalf("journal = %#v", journal.Entries["publication:report"])
	}
	result, err := publisher.Run(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if result.Targets["publication:report"].FileID != fileID || drive.exportCalls[fileID] != 1 || drive.createCalls["docs/a.md"] != 1 {
		t.Fatalf("result=%#v exports=%#v creates=%#v", result, drive.exportCalls, drive.createCalls)
	}
}

func TestBatchPublishReviewSealResumeDoesNotRecaptureOrDuplicate(t *testing.T) {
	docs := newBatchDocs()
	drive := newBatchDrive(docs)
	store := newBatchStore(t)
	defer store.Release()
	request := reviewBatch()
	fail := true
	publisher := BatchPublisher{Drive: drive, Docs: docs, Store: store, AfterReviewSeal: func(string) error {
		if fail {
			fail = false
			return errors.New("failure after seal")
		}
		return nil
	}}
	if _, err := publisher.Run(context.Background(), request); err == nil {
		t.Fatal("expected seal boundary failure")
	}
	fileID := drive.bySource["docs/a.md"]
	journal, err := store.LoadJournal()
	if err != nil {
		t.Fatal(err)
	}
	if journal.Entries["publication:report"].Stage != state.StageSealingReview {
		t.Fatalf("journal = %#v", journal.Entries["publication:report"])
	}
	result, err := publisher.Run(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if result.Targets["publication:report"].FileID != fileID || drive.exportCalls[fileID] != 1 || drive.createCalls["docs/a.md"] != 1 {
		t.Fatalf("result=%#v exports=%#v creates=%#v", result, drive.exportCalls, drive.createCalls)
	}
}

func TestBatchPublishReviewCaptureFailuresDoNotActivateTarget(t *testing.T) {
	for _, test := range []struct {
		name  string
		setup func(*batchDrive)
	}{
		{name: "export", setup: func(drive *batchDrive) { drive.failExport = 1 }},
		{name: "source map", setup: func(drive *batchDrive) { drive.exportContent = []byte("# Different") }},
	} {
		t.Run(test.name, func(t *testing.T) {
			docs := newBatchDocs()
			drive := newBatchDrive(docs)
			test.setup(drive)
			store := newBatchStore(t)
			defer store.Release()
			if _, err := (BatchPublisher{Drive: drive, Docs: docs, Store: store}).Run(context.Background(), reviewBatch()); err == nil {
				t.Fatal("expected review capture failure")
			}
			loaded, err := store.Load()
			if err != nil {
				t.Fatal(err)
			}
			if len(loaded.Targets) != 0 {
				t.Fatalf("failed capture activated state: %#v", loaded.Targets)
			}
			file := drive.files[drive.bySource["docs/a.md"]]
			if file.AppProperties["mdoc_status"] != "pending" || !contains(file.Parents, "staging") {
				t.Fatalf("failed capture activated remote: %#v", file)
			}
		})
	}
}

func TestBatchPublishReviewResumesReadyAndMoveFailuresFromCandidate(t *testing.T) {
	for _, test := range []struct {
		name  string
		setup func(*batchDrive)
	}{
		{name: "ready", setup: func(drive *batchDrive) { drive.failReadyAfter = 1 }},
		{name: "move", setup: func(drive *batchDrive) { drive.failMoveAfter = 1 }},
	} {
		t.Run(test.name, func(t *testing.T) {
			docs := newBatchDocs()
			drive := newBatchDrive(docs)
			test.setup(drive)
			store := newBatchStore(t)
			defer store.Release()
			request := reviewBatch()
			publisher := BatchPublisher{Drive: drive, Docs: docs, Store: store}
			if _, err := publisher.Run(context.Background(), request); err == nil {
				t.Fatal("expected activation boundary failure")
			}
			fileID := drive.bySource["docs/a.md"]
			result, err := publisher.Run(context.Background(), request)
			if err != nil {
				t.Fatal(err)
			}
			if result.Targets["publication:report"].ReviewSnapshot == nil || drive.exportCalls[fileID] != 1 || drive.createCalls["docs/a.md"] != 1 {
				t.Fatalf("result=%#v exports=%#v creates=%#v", result, drive.exportCalls, drive.createCalls)
			}
		})
	}
}

func TestBatchPublishReviewRefusesRevisionChangeBeforeSeal(t *testing.T) {
	docs := newBatchDocs()
	drive := newBatchDrive(docs)
	drive.editOnMove = true
	store := newBatchStore(t)
	defer store.Release()
	request := reviewBatch()
	if _, err := (BatchPublisher{Drive: drive, Docs: docs, Store: store}).Run(context.Background(), request); err == nil || !strings.Contains(err.Error(), "activation seal") {
		t.Fatalf("error = %v", err)
	}
	loaded, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Targets) != 0 {
		t.Fatalf("revision mismatch activated state: %#v", loaded.Targets)
	}
	snapshots, _ := reviewsync.NewSnapshotStore(store.Path())
	snapshot, err := snapshots.Load("publication:report", 1, false)
	if err != nil || snapshot.Manifest.Status != reviewsync.SnapshotCandidate {
		t.Fatalf("snapshot = %#v, error = %v", snapshot.Manifest, err)
	}
}

func TestBatchPublishDisabledReviewMakesNoExportCall(t *testing.T) {
	docs := newBatchDocs()
	drive := newBatchDrive(docs)
	store := newBatchStore(t)
	defer store.Release()
	request := BatchRequest{WorkspaceID: "workspace", Profile: "work", StagingID: "staging", ReviewID: "review", ReviewSetID: "set", OperationID: "operation", Items: []BatchItem{{SourceKey: "docs/a.md", Title: "A", DOCX: []byte("a"), SourceHash: "source", RenderHash: "render", Generation: 1}}}
	result, err := (BatchPublisher{Drive: drive, Docs: docs, Store: store}).Run(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if len(drive.exportCalls) != 0 || result.Targets["docs/a.md"].ReviewSnapshot != nil {
		t.Fatalf("exports=%#v target=%#v", drive.exportCalls, result.Targets["docs/a.md"])
	}
}

func reviewBatch() BatchRequest {
	source := []byte("# Export\n")
	return BatchRequest{
		WorkspaceID: "workspace", Profile: "work", StagingID: "staging", ReviewID: "review", ReviewSetID: "set", OperationID: "operation", UseTargets: true,
		Items: []BatchItem{{
			SourceKey: "publication:report", SourceInput: "docs/a.md", Title: "Report", DOCX: []byte("docx"),
			SourceHash: document.SourceHash(source), RenderHash: "render", Generation: 1, PublicationID: "report", PublicationKind: "source",
			Members: []state.JournalMember{{SourceKey: "docs/a.md", SourceHash: document.SourceHash(source)}},
			Review:  &ReviewPublishInput{Reader: "gfm", Sources: []reviewsync.SnapshotSource{{SourceKey: "docs/a.md", SourceHash: document.SourceHash(source), Content: source}}},
		}},
	}
}

func TestBatchPublishRejectsChangedInputsDuringResume(t *testing.T) {
	docs := newBatchDocs()
	drive := newBatchDrive(docs)
	drive.failCreate["docs/b.md"] = 1
	store := newBatchStore(t)
	defer store.Release()
	request := BatchRequest{WorkspaceID: "workspace", Profile: "work", StagingID: "staging", ReviewID: "review", ReviewSetID: "set", OperationID: "operation", Items: []BatchItem{
		{SourceKey: "docs/a.md", Title: "A", DOCX: []byte("old-a"), SourceHash: "source-a", RenderHash: "render-a", Generation: 1},
		{SourceKey: "docs/b.md", Title: "B", DOCX: []byte("old-b"), SourceHash: "source-b", RenderHash: "render-b", Generation: 1},
	}}
	if _, err := (BatchPublisher{Drive: drive, Docs: docs, Store: store}).Run(context.Background(), request); err == nil {
		t.Fatal("expected partial failure")
	}
	firstID := drive.bySource["docs/a.md"]
	request.Items[0].DOCX = []byte("new-a")
	request.Items[0].SourceHash = "new-source-a"
	request.Items[0].RenderHash = "new-render-a"
	_, err := (BatchPublisher{Drive: drive, Docs: docs, Store: store}).Run(context.Background(), request)
	if err == nil || !strings.Contains(err.Error(), "inputs changed") {
		t.Fatalf("error = %v", err)
	}
	var typed *Error
	if !errors.As(err, &typed) || typed.Recovery == nil || typed.Recovery.OperationID != "operation" || typed.Recovery.JournalPath != store.JournalPath() {
		t.Fatalf("recovery error = %#v", err)
	}
	if drive.bySource["docs/a.md"] != firstID || drive.createCalls["docs/a.md"] != 1 || drive.createCalls["docs/b.md"] != 1 {
		t.Fatalf("created targets changed: ids=%#v calls=%#v", drive.bySource, drive.createCalls)
	}
	loaded, loadErr := store.Load()
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if len(loaded.Documents) != 0 {
		t.Fatalf("state committed changed inputs: %#v", loaded.Documents)
	}
}

func TestBatchPublishResumesFailedLinkFixup(t *testing.T) {
	docs := newBatchDocs()
	placeholder := PlaceholderURL("docs/b.md")
	docs.linksBySource["docs/a.md"] = map[string][]googleapi.TextRange{placeholder: {{StartIndex: 1, EndIndex: 5}}}
	docs.failUpdates = 1
	drive := newBatchDrive(docs)
	store := newBatchStore(t)
	defer store.Release()
	request := linkedBatch(placeholder)
	if _, err := (BatchPublisher{Drive: drive, Docs: docs, Store: store}).Run(context.Background(), request); err == nil {
		t.Fatal("expected link failure")
	}
	result, err := (BatchPublisher{Drive: drive, Docs: docs, Store: store}).Run(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if drive.createCalls["docs/a.md"] != 1 || drive.createCalls["docs/b.md"] != 1 || docs.updateCalls != 2 {
		t.Fatalf("creates=%#v updates=%d result=%#v", drive.createCalls, docs.updateCalls, result)
	}
}

func TestBatchPublishRetriesLinkUpdateAfterSafeRecheck(t *testing.T) {
	docs := newBatchDocs()
	placeholder := PlaceholderURL("docs/b.md")
	docs.linksBySource["docs/a.md"] = map[string][]googleapi.TextRange{placeholder: {{StartIndex: 1, EndIndex: 5}}}
	docs.failRetryable = 1
	drive := newBatchDrive(docs)
	store := newBatchStore(t)
	defer store.Release()
	request := linkedBatch(placeholder)
	_, err := (BatchPublisher{Drive: drive, Docs: docs, Store: store, Retry: googleapi.RetryPolicy{Backoff: func(context.Context, int) error { return nil }}}).Run(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if docs.updateCalls != 2 {
		t.Fatalf("link update attempts = %d", docs.updateCalls)
	}
}

func TestBatchPublishReconcilesLostLinkAndMoveResponses(t *testing.T) {
	docs := newBatchDocs()
	placeholder := PlaceholderURL("docs/b.md")
	docs.linksBySource["docs/a.md"] = map[string][]googleapi.TextRange{placeholder: {{StartIndex: 1, EndIndex: 5}}}
	docs.failAfterUpdates = 1
	drive := newBatchDrive(docs)
	store := newBatchStore(t)
	defer store.Release()
	request := linkedBatch(placeholder)
	if _, err := (BatchPublisher{Drive: drive, Docs: docs, Store: store}).Run(context.Background(), request); err == nil {
		t.Fatal("expected lost link response")
	}
	drive.failMoveAfter = 1
	if _, err := (BatchPublisher{Drive: drive, Docs: docs, Store: store}).Run(context.Background(), request); err == nil {
		t.Fatal("expected lost move response")
	}
	result, err := (BatchPublisher{Drive: drive, Docs: docs, Store: store}).Run(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if docs.updateCalls != 1 {
		t.Fatalf("link update repeated: %d", docs.updateCalls)
	}
	if len(result.Targets) != 2 {
		t.Fatalf("targets = %#v", result.Targets)
	}
}

func TestBatchPublishResumesLostReadyAndStateCommitResponses(t *testing.T) {
	docs := newBatchDocs()
	drive := newBatchDrive(docs)
	drive.failReadyAfter = 1
	store := newBatchStore(t)
	defer store.Release()
	request := BatchRequest{WorkspaceID: "workspace", Profile: "work", StagingID: "staging", ReviewID: "review", ReviewSetID: "set", OperationID: "operation", Items: []BatchItem{{SourceKey: "docs/a.md", Title: "A", DOCX: []byte("a"), SourceHash: "source", RenderHash: "render", Generation: 1}}}
	if _, err := (BatchPublisher{Drive: drive, Docs: docs, Store: store}).Run(context.Background(), request); err == nil {
		t.Fatal("expected lost ready response")
	}
	injected := true
	publisher := BatchPublisher{Drive: drive, Docs: docs, Store: store, AfterStateSave: func() error {
		if injected {
			injected = false
			return errors.New("lost state commit response")
		}
		return nil
	}}
	if _, err := publisher.Run(context.Background(), request); err == nil {
		t.Fatal("expected lost state commit response")
	}
	if _, err := publisher.Run(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	documentState := loaded.Documents["docs/a.md"]
	if drive.createCalls["docs/a.md"] != 1 || documentState.ActiveTarget == nil || len(documentState.PriorTargets) != 0 {
		t.Fatalf("creates=%#v state=%#v", drive.createCalls, documentState)
	}
	if drive.propertyCalls[documentState.ActiveTarget.FileID] != 1 {
		t.Fatalf("ready metadata writes = %d", drive.propertyCalls[documentState.ActiveTarget.FileID])
	}
}

func TestApplicationPublishSkipsUnchangedSecondRun(t *testing.T) {
	directory := t.TempDir()
	if err := os.Mkdir(filepath.Join(directory, "docs"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "docs", "a.md"), []byte("# A\n\nContent."), 0o600); err != nil {
		t.Fatal(err)
	}
	rootReference, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "document-styles", "work-reference.docx"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "reference.docx"), rootReference, 0o600); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(directory, ".mdoc.yaml")
	configYAML := "version: 1\nworkspace_id: workspace\nprofiles:\n  work:\n    sources: [docs/*.md]\n    entry: docs/a.md\n    review_folder_name: Review\n    staging_folder_name: Staging\n    reference_docx: reference.docx\n    max_table_columns: 5\n    state_file: private/state.json\n"
	if err := os.WriteFile(configPath, []byte(configYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	store := state.NewStore(filepath.Join(directory, "private", "state.json"))
	if err := store.Acquire(); err != nil {
		t.Fatal(err)
	}
	initial := state.New("workspace", "work")
	initial.Folders = state.Folders{StagingID: "staging", ReviewID: "review"}
	if err := store.Save(initial); err != nil {
		t.Fatal(err)
	}
	store.Release()
	docs := newBatchDocs()
	drive := newBatchDrive(docs)
	drive.folders["staging"] = []googleapi.Folder{safeMappedFolder("staging", "Staging", "workspace", "work", "staging")}
	drive.folders["review"] = []googleapi.Folder{safeMappedFolder("review", "Review", "workspace", "work", "review")}
	var output strings.Builder
	application := New(&output, fakeClientProvider{})
	application.GoogleFactory = fakeFactory{drive: drive, docs: docs}
	options := PublishOptions{CommonOptions: CommonOptions{Config: configPath, Profile: "work"}}
	if err := application.Publish(context.Background(), options); err != nil {
		t.Fatal(err)
	}
	if drive.nextID != 1 {
		t.Fatalf("creates after first run = %d", drive.nextID)
	}
	output.Reset()
	if err := application.Publish(context.Background(), options); err != nil {
		t.Fatal(err)
	}
	if drive.nextID != 1 {
		t.Fatalf("unchanged run created another target: %d", drive.nextID)
	}
	if !strings.Contains(output.String(), "skip docs/a.md") {
		t.Fatalf("output = %q", output.String())
	}
	var statusJSON, dryJSON, planJSON strings.Builder
	application.Out = &statusJSON
	if err := application.Status(context.Background(), CommonOptions{Config: configPath, Profile: "work", JSON: true}); err != nil {
		t.Fatal(err)
	}
	application.Out = &dryJSON
	if err := application.Publish(context.Background(), PublishOptions{CommonOptions: CommonOptions{Config: configPath, Profile: "work", JSON: true}, DryRun: true}); err != nil {
		t.Fatal(err)
	}
	application.Out = &planJSON
	if err := application.Plan(context.Background(), PublishOptions{CommonOptions: CommonOptions{Config: configPath, Profile: "work", JSON: true}}); err != nil {
		t.Fatal(err)
	}
	if planJSON.String() != dryJSON.String() || !strings.Contains(planJSON.String(), `"schema_version": 1`) {
		t.Fatalf("plan and dry run differ:\nplan=%s\ndry=%s", planJSON.String(), dryJSON.String())
	}
	if !strings.Contains(statusJSON.String(), `"kind": "local_status"`) || !strings.Contains(dryJSON.String(), `"kind": "plan"`) {
		t.Fatalf("status and dry run migration kinds are wrong:\n%s\n%s", statusJSON.String(), dryJSON.String())
	}
	writesBeforeReads := drive.nextID
	var remoteJSON strings.Builder
	application.Out = &remoteJSON
	if err := application.Status(context.Background(), CommonOptions{Config: configPath, Profile: "work", JSON: true, Remote: true}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(remoteJSON.String(), `"kind": "remote_status"`) || drive.nextID != writesBeforeReads {
		t.Fatalf("remote status output=%s writes=%d before=%d", remoteJSON.String(), drive.nextID, writesBeforeReads)
	}
	var doctorJSON strings.Builder
	application.Out = &doctorJSON
	if err := application.Doctor(context.Background(), DoctorOptions{CommonOptions: CommonOptions{Config: configPath, Profile: "work", JSON: true}}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(doctorJSON.String(), `"kind": "doctor"`) || !strings.Contains(doctorJSON.String(), `"result": "ready"`) || drive.nextID != writesBeforeReads {
		t.Fatalf("doctor output=%s writes=%d before=%d", doctorJSON.String(), drive.nextID, writesBeforeReads)
	}
}

func TestVersionThreeBundlePublishCreatesOneTargetAndThenSkips(t *testing.T) {
	directory := t.TempDir()
	if err := os.MkdirAll(filepath.Join(directory, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"a.md", "b.md"} {
		if err := os.WriteFile(filepath.Join(directory, "docs", name), []byte("# "+name+"\n\nContent.\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(directory, "fields.yaml"), []byte("classification: internal\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(directory, ".mdoc.yaml")
	project := `version: 3
project_id: workspace
profiles:
  work:
    sources:
      include: [docs/*.md]
    entry: report
    destination:
      review_folder_name: Review
      staging_folder_name: Staging
    state_file: private/state.json
    publications:
      report:
        kind: bundle
        field_files: [fields.yaml]
        members:
          - source: docs/a.md
            title_mode: document_title
          - source: docs/b.md
`
	if err := os.WriteFile(configPath, []byte(project), 0o600); err != nil {
		t.Fatal(err)
	}
	store := state.NewStore(filepath.Join(directory, "private", "state.json"))
	if err := store.Acquire(); err != nil {
		t.Fatal(err)
	}
	initial := state.New("workspace", "work")
	initial.Folders = state.Folders{StagingID: "staging", ReviewID: "review"}
	if err := store.Save(initial); err != nil {
		t.Fatal(err)
	}
	store.Release()
	docs := newBatchDocs()
	drive := newBatchDrive(docs)
	drive.folders["staging"] = []googleapi.Folder{safeMappedFolder("staging", "Staging", "workspace", "work", "staging")}
	drive.folders["review"] = []googleapi.Folder{safeMappedFolder("review", "Review", "workspace", "work", "review")}
	var output strings.Builder
	application := New(&output, fakeClientProvider{})
	application.DocumentRunner = staticDocumentRunner{}
	application.GoogleFactory = fakeFactory{drive: drive, docs: docs}
	t.Chdir(t.TempDir())
	options := PublishOptions{CommonOptions: CommonOptions{Config: configPath, Profile: "work", Bundle: "report"}}
	if err := application.Publish(context.Background(), options); err != nil {
		t.Fatal(err)
	}
	if drive.nextID != 1 {
		t.Fatalf("bundle creates = %d", drive.nextID)
	}
	file := drive.files["doc-1"]
	for key, want := range map[string]string{"mdoc_target_key": "publication:report", "mdoc_publication": "report", "mdoc_publication_kind": "bundle"} {
		if file.AppProperties[key] != want {
			t.Fatalf("%s = %q, want %q; properties=%#v", key, file.AppProperties[key], want, file.AppProperties)
		}
	}
	if file.AppProperties["mdoc_source"] != "" {
		t.Fatalf("bundle source metadata = %q", file.AppProperties["mdoc_source"])
	}
	loaded, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	stored := loaded.Targets["publication:report"]
	if stored.ActiveTarget == nil || stored.PublicationKind != "bundle" || len(stored.Members) != 2 {
		t.Fatalf("stored bundle = %#v", stored)
	}
	output.Reset()
	if err := application.Publish(context.Background(), options); err != nil {
		t.Fatal(err)
	}
	if drive.nextID != 1 || !strings.Contains(output.String(), "skip publication:report") {
		t.Fatalf("second publish creates=%d output=%q", drive.nextID, output.String())
	}
	for _, remote := range []bool{false, true} {
		output.Reset()
		if err := application.Status(context.Background(), CommonOptions{Config: configPath, Profile: "work", Bundle: "report", JSON: true, Remote: remote}); err != nil {
			t.Fatalf("bundle status remote=%t: %v; output=%s", remote, err, output.String())
		}
		for _, expected := range []string{`"schema_version": 2`, `"target": "publication:report"`, `"publication_kind": "bundle"`} {
			if !strings.Contains(output.String(), expected) {
				t.Fatalf("bundle status remote=%t missing %s: %s", remote, expected, output.String())
			}
		}
	}
	browser := &recordingBrowser{}
	application.Browser = browser
	output.Reset()
	if err := application.Open(context.Background(), OpenOptions{CommonOptions: CommonOptions{Config: configPath, Profile: "work", Bundle: "report"}}); err != nil {
		t.Fatal(err)
	}
	if browser.target != file.URL {
		t.Fatalf("opened bundle URL = %q, want %q", browser.target, file.URL)
	}
	output.Reset()
	if err := application.Open(context.Background(), OpenOptions{CommonOptions: CommonOptions{Config: configPath, Profile: "work", Bundle: "report", JSON: true}}); err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{`"schema_version":2`, `"target":"publication:report"`, `"publication_kind":"bundle"`} {
		if !strings.Contains(output.String(), expected) {
			t.Fatalf("open JSON missing %s: %s", expected, output.String())
		}
	}
	output.Reset()
	if err := application.Reconcile(context.Background(), ReconcileOptions{CommonOptions: CommonOptions{Config: configPath, Profile: "work"}}); err != nil {
		t.Fatal(err)
	}
	reconciled, err := store.Load()
	if err != nil || reconciled.Targets["publication:report"].ActiveTarget == nil {
		t.Fatalf("reconciled bundle state=%#v error=%v", reconciled, err)
	}
	duplicate := file
	duplicate.ID = "doc-duplicate"
	drive.files[duplicate.ID] = duplicate
	docs.documents[duplicate.ID] = googleapi.Document{ID: duplicate.ID, RevisionID: "r1", Links: map[string][]googleapi.TextRange{}}
	if err := application.Reconcile(context.Background(), ReconcileOptions{CommonOptions: CommonOptions{Config: configPath, Profile: "work"}}); err == nil || !strings.Contains(err.Error(), "duplicate target") {
		t.Fatalf("duplicate bundle reconcile error = %v", err)
	}
	creates := drive.nextID
	if err := application.Publish(context.Background(), PublishOptions{CommonOptions: options.CommonOptions, NewReview: true}); err == nil || !strings.Contains(err.Error(), "more than one remote document") {
		t.Fatalf("duplicate bundle publish error = %v", err)
	}
	if drive.nextID != creates {
		t.Fatalf("duplicate preflight made a write: creates=%d before=%d", drive.nextID, creates)
	}
}

func TestVersionThreeBundlePublishResumesCreateWithoutDuplicate(t *testing.T) {
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "a.md"), []byte("# A\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(directory, ".mdoc.yaml")
	project := `version: 3
project_id: workspace
profiles:
  work:
    sources:
      include: [a.md]
    entry: report
    destination:
      review_folder_name: Review
      staging_folder_name: Staging
    state_file: state.json
    publications:
      report:
        kind: bundle
        fields:
          unused: true
        layout:
          cover:
            enabled: true
            title: '{{ field "client" }}'
        members:
          - source: a.md
`
	if err := os.WriteFile(configPath, []byte(project), 0o600); err != nil {
		t.Fatal(err)
	}
	store := state.NewStore(filepath.Join(directory, "state.json"))
	if err := store.Acquire(); err != nil {
		t.Fatal(err)
	}
	initial := state.New("workspace", "work")
	initial.Folders = state.Folders{StagingID: "staging", ReviewID: "review"}
	if err := store.Save(initial); err != nil {
		t.Fatal(err)
	}
	store.Release()
	docs := newBatchDocs()
	drive := newBatchDrive(docs)
	drive.failCreate[""] = 1
	drive.folders["staging"] = []googleapi.Folder{safeMappedFolder("staging", "Staging", "workspace", "work", "staging")}
	drive.folders["review"] = []googleapi.Folder{safeMappedFolder("review", "Review", "workspace", "work", "review")}
	application := New(io.Discard, fakeClientProvider{})
	application.DocumentRunner = staticDocumentRunner{}
	application.GoogleFactory = fakeFactory{drive: drive, docs: docs}
	options := PublishOptions{CommonOptions: CommonOptions{Config: configPath, Profile: "work", Bundle: "report", FieldAssignments: []string{`/client="Frozen Client"`}}}
	if err := application.Publish(context.Background(), options); err == nil {
		t.Fatal("expected injected create failure")
	}
	if _, err := store.LoadJournal(); err != nil {
		t.Fatalf("load resume journal: %v", err)
	}
	resumeOptions := options
	resumeOptions.FieldAssignments = nil
	if err := os.WriteFile(filepath.Join(directory, "a.md"), []byte("# Changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := application.Publish(context.Background(), resumeOptions); err == nil || !strings.Contains(err.Error(), "inputs changed") {
		t.Fatalf("changed resume input error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(directory, "a.md"), []byte("# A\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := application.Publish(context.Background(), resumeOptions); err != nil {
		t.Fatal(err)
	}
	if drive.nextID != 1 {
		t.Fatalf("resumed create count = %d", drive.nextID)
	}
	if _, err := store.LoadJournal(); !errors.Is(err, state.ErrNotFound) {
		t.Fatalf("journal after resume = %v", err)
	}
	if err := os.WriteFile(filepath.Join(directory, "a.md"), []byte("# A\n\nUpdated member.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := application.Publish(context.Background(), options); err != nil {
		t.Fatal(err)
	}
	updated, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	stored := updated.Targets["publication:report"]
	if drive.nextID != 2 || stored.ActiveTarget == nil || stored.ActiveTarget.Generation != 2 || len(stored.PriorTargets) != 1 || stored.PriorTargets[0].Generation != 1 {
		t.Fatalf("member update creates=%d state=%#v", drive.nextID, stored)
	}
}

func TestVersionThreeBundleFilterCannotChangeProtectedTopology(t *testing.T) {
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "a.md"), []byte("# A\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	filterPath := filepath.Join(directory, "remove-protected.lua")
	filter := `function Div(element)
  for _, class in ipairs(element.classes) do
    if class == "mdoc-protected" then return {} end
  end
end
`
	if err := os.WriteFile(filterPath, []byte(filter), 0o600); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(directory, ".mdoc.yaml")
	project := `version: 3
project_id: workspace
profiles:
  work:
    sources:
      include: [a.md]
    entry: report
    style:
      filters: [remove-protected.lua]
    destination:
      review_folder_name: Review
      staging_folder_name: Staging
    state_file: state.json
    publications:
      report:
        kind: bundle
        members:
          - source: a.md
`
	if err := os.WriteFile(configPath, []byte(project), 0o600); err != nil {
		t.Fatal(err)
	}
	store := state.NewStore(filepath.Join(directory, "state.json"))
	if err := store.Acquire(); err != nil {
		t.Fatal(err)
	}
	initial := state.New("workspace", "work")
	initial.Folders = state.Folders{StagingID: "staging", ReviewID: "review"}
	if err := store.Save(initial); err != nil {
		t.Fatal(err)
	}
	store.Release()
	docs := newBatchDocs()
	drive := newBatchDrive(docs)
	drive.folders["staging"] = []googleapi.Folder{safeMappedFolder("staging", "Staging", "workspace", "work", "staging")}
	drive.folders["review"] = []googleapi.Folder{safeMappedFolder("review", "Review", "workspace", "work", "review")}
	application := New(io.Discard, fakeClientProvider{})
	application.DocumentRunner = staticDocumentRunner{}
	application.GoogleFactory = fakeFactory{drive: drive, docs: docs}
	common := CommonOptions{Config: configPath, Profile: "work", Bundle: "report"}
	if err := application.Validate(context.Background(), ValidateOptions{CommonOptions: common}); err != nil {
		t.Fatalf("validate executed the topology filter: %v", err)
	}
	if err := application.Status(context.Background(), common); err != nil {
		t.Fatalf("status executed the topology filter: %v", err)
	}
	if err := application.Doctor(context.Background(), DoctorOptions{CommonOptions: common}); err != nil {
		t.Fatalf("doctor executed the topology filter: %v", err)
	}
	if err := application.Publish(context.Background(), PublishOptions{CommonOptions: common, DryRun: true}); err != nil {
		t.Fatalf("plan executed the topology filter: %v", err)
	}
	err := application.Publish(context.Background(), PublishOptions{CommonOptions: common})
	if err == nil || !strings.Contains(err.Error(), "protected marker count changed") {
		t.Fatalf("topology filter error = %v", err)
	}
	if drive.nextID != 0 {
		t.Fatalf("topology failure created %d documents", drive.nextID)
	}
}

func TestVersionThreeFileSelectionPublishesEachSourcePublication(t *testing.T) {
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "a.md"), []byte("# A\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(directory, ".mdoc.yaml")
	project := `version: 3
project_id: workspace
profiles:
  work:
    sources:
      include: [a.md]
    entry: first
    destination:
      review_folder_name: Review
      staging_folder_name: Staging
    state_file: state.json
    publications:
      first:
        kind: source
        source: a.md
      second:
        kind: source
        source: a.md
`
	if err := os.WriteFile(configPath, []byte(project), 0o600); err != nil {
		t.Fatal(err)
	}
	store := state.NewStore(filepath.Join(directory, "state.json"))
	if err := store.Acquire(); err != nil {
		t.Fatal(err)
	}
	initial := state.New("workspace", "work")
	initial.Folders = state.Folders{StagingID: "staging", ReviewID: "review"}
	if err := store.Save(initial); err != nil {
		t.Fatal(err)
	}
	store.Release()
	docs := newBatchDocs()
	drive := newBatchDrive(docs)
	drive.folders["staging"] = []googleapi.Folder{safeMappedFolder("staging", "Staging", "workspace", "work", "staging")}
	drive.folders["review"] = []googleapi.Folder{safeMappedFolder("review", "Review", "workspace", "work", "review")}
	var output strings.Builder
	application := New(&output, fakeClientProvider{})
	application.DocumentRunner = staticDocumentRunner{}
	application.GoogleFactory = fakeFactory{drive: drive, docs: docs}
	options := PublishOptions{CommonOptions: CommonOptions{Config: configPath, Profile: "work", JSON: true}, Files: []string{"a.md"}}
	if err := application.Publish(context.Background(), options); err != nil {
		t.Fatal(err)
	}
	if drive.nextID != 2 {
		t.Fatalf("source publication creates = %d", drive.nextID)
	}
	seen := map[string]bool{}
	for _, file := range drive.files {
		if file.AppProperties["mdoc_source"] != "a.md" || file.AppProperties["mdoc_publication_kind"] != "source" {
			t.Fatalf("source publication metadata = %#v", file.AppProperties)
		}
		seen[file.AppProperties["mdoc_target_key"]] = true
	}
	if !seen["publication:first"] || !seen["publication:second"] {
		t.Fatalf("source publication targets = %#v", seen)
	}
	for _, expected := range []string{`"schema_version": 2`, `"target": "publication:first"`, `"target": "publication:second"`} {
		if !strings.Contains(output.String(), expected) {
			t.Fatalf("source publication output missing %s: %s", expected, output.String())
		}
	}
	if err := application.Reconcile(context.Background(), ReconcileOptions{CommonOptions: CommonOptions{Config: configPath, Profile: "work"}}); err != nil {
		t.Fatal(err)
	}
	reconciled, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"publication:first", "publication:second"} {
		stored := reconciled.Targets[key]
		if stored.ActiveTarget == nil || stored.Source != "a.md" || stored.PublicationID != strings.TrimPrefix(key, "publication:") {
			t.Fatalf("reconciled source publication %s = %#v", key, stored)
		}
	}
}

func TestResumePlanDoesNotExecuteFiltersAndPublishPreflightsDestinationFirst(t *testing.T) {
	directory := t.TempDir()
	configPath := filepath.Join(directory, ".mdoc.yaml")
	projectYAML := `version: 2
project_id: resume-test
default_profile: review
root: .
pandoc_binary: pandoc
output:
  format: human
  quiet: false
profiles:
  review:
    sources:
      include: [README.md]
      exclude: []
      external_roots: []
    entry: README.md
    naming:
      source: file
      mapping: {}
    validation:
      title: optional
      heading_jumps: warning
      unpublished_markdown_links: warning
      max_table_columns: 5
      image_types: [png, jpg, jpeg, svg]
    style:
      reference_docx: ""
      heading_filter: none
      filters: [filter.lua]
      roots: []
      reader: gfm
    destination:
      mode: immutable_review
      parent_folder_id: ""
      staging_folder_id: ""
      review_folder_id: ""
      staging_folder_name: Documents Staging
      review_folder_name: Documents Review
    state_file: private/state.json
`
	if err := os.WriteFile(configPath, []byte(projectYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "README.md"), []byte("# Resume test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	markerPath := filepath.Join(directory, "filter-executed")
	filterSource := "local marker = assert(io.open(" + strconv.Quote(markerPath) + ", \"w\"))\nmarker:write(\"executed\")\nmarker:close()\n"
	if err := os.WriteFile(filepath.Join(directory, "filter.lua"), []byte(filterSource), 0o600); err != nil {
		t.Fatal(err)
	}

	store := state.NewStore(filepath.Join(directory, "private", "state.json"))
	if err := store.Acquire(); err != nil {
		t.Fatal(err)
	}
	initial := state.New("", "review")
	resolved, err := config.Resolve(context.Background(), config.ResolverOptions{ProjectPath: configPath, Profile: "review", WorkingDir: directory})
	if err != nil {
		t.Fatal(err)
	}
	initial.WorkspaceID = resolved.Identity()
	initial.Folders = state.Folders{StagingID: "staging", ReviewID: "review"}
	if err := store.Save(initial); err != nil {
		t.Fatal(err)
	}
	store.Release()

	docs := newBatchDocs()
	drive := newBatchDrive(docs)
	drive.failCreate["README.md"] = 1
	folder := func(role, id string) googleapi.Folder {
		return safeMappedFolder(id, role, resolved.Identity(), "review", role)
	}
	drive.folders["staging"] = []googleapi.Folder{folder("staging", "staging")}
	drive.folders["review"] = []googleapi.Folder{folder("review", "review")}
	var output strings.Builder
	application := New(&output, fakeClientProvider{})
	application.GoogleFactory = fakeFactory{drive: drive, docs: docs}
	options := PublishOptions{CommonOptions: CommonOptions{Config: configPath, Profile: "review", JSON: true}}
	if err := application.Publish(context.Background(), options); err == nil {
		t.Fatal("expected interrupted publish")
	}
	if _, err := os.Stat(markerPath); err != nil {
		t.Fatalf("initial publish did not execute filter: %v", err)
	}
	if err := os.Remove(markerPath); err != nil {
		t.Fatal(err)
	}

	output.Reset()
	if err := application.Plan(context.Background(), options); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(markerPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("resume plan executed custom filter: %v", err)
	}
	if !strings.Contains(output.String(), `"kind": "plan"`) {
		t.Fatalf("plan output = %q", output.String())
	}

	drive.folders["review"] = []googleapi.Folder{folder("review", "wrong-review")}
	if err := application.Publish(context.Background(), options); err == nil || !strings.Contains(err.Error(), "folder") {
		t.Fatalf("publish destination error = %v", err)
	}
	if _, err := os.Stat(markerPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("resume publish executed custom filter before destination preflight: %v", err)
	}

	drive.folders["review"] = []googleapi.Folder{folder("review", "review")}
	if err := application.Publish(context.Background(), options); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(markerPath); err != nil {
		t.Fatalf("valid resume publish did not execute custom filter: %v", err)
	}
}

func TestPublishPreflightBlocksMetadataMismatchAndDuplicateIdentity(t *testing.T) {
	docs := newBatchDocs()
	drive := newBatchDrive(docs)
	publishState := state.New("workspace", "work")
	active, file := readyActiveTarget("a", "docs/a.md", 1)
	publishState.Documents["docs/a.md"] = state.Document{ActiveTarget: &active}
	drive.files[file.ID] = file
	docs.documents[file.ID] = googleapi.Document{ID: file.ID, RevisionID: "r1"}
	application := New(io.Discard, nil)

	file.AppProperties["mdoc_status"] = "pending"
	drive.files[file.ID] = file
	result, err := application.inspectActiveTargets(context.Background(), &config.Config{WorkspaceID: "workspace"}, config.SelectedProfile{Name: "work"}, publishState, googleapi.Clients{Drive: drive, Docs: docs}, map[string]bool{"docs/a.md": true})
	if err != nil || !result["docs/a.md"].MetadataMismatch {
		t.Fatalf("result=%#v error=%v", result, err)
	}

	file.AppProperties["mdoc_status"] = "ready"
	drive.files[file.ID] = file
	duplicate := file
	duplicate.ID = "duplicate"
	drive.files[duplicate.ID] = duplicate
	result, err = application.inspectActiveTargets(context.Background(), &config.Config{WorkspaceID: "workspace"}, config.SelectedProfile{Name: "work"}, publishState, googleapi.Clients{Drive: drive, Docs: docs}, map[string]bool{"docs/a.md": true})
	if err != nil || !result["docs/a.md"].Ambiguous {
		t.Fatalf("result=%#v error=%v", result, err)
	}
}

func TestPublishPreflightDoesNotReadUnrelatedActiveTarget(t *testing.T) {
	docs := newBatchDocs()
	drive := newBatchDrive(docs)
	publishState := state.New("workspace", "work")
	for _, source := range []string{"docs/a.md", "docs/b.md"} {
		active, file := readyActiveTarget(strings.TrimSuffix(filepath.Base(source), ".md"), source, 1)
		publishState.Documents[source] = state.Document{ActiveTarget: &active}
		drive.files[file.ID] = file
		docs.documents[file.ID] = googleapi.Document{ID: file.ID, RevisionID: "r1"}
	}
	drive.getErrors["b"] = &googleapi.Error{Kind: googleapi.KindPermission, Operation: "get", Cause: errors.New("denied")}
	application := New(io.Discard, nil)
	result, err := application.inspectActiveTargets(context.Background(), &config.Config{WorkspaceID: "workspace"}, config.SelectedProfile{Name: "work"}, publishState, googleapi.Clients{Drive: drive, Docs: docs}, map[string]bool{"docs/a.md": true})
	if err != nil || !result["docs/a.md"].Accessible {
		t.Fatalf("result=%#v error=%v", result, err)
	}
}

func TestPublishPreflightPreservesSelectedTargetErrorClass(t *testing.T) {
	docs := newBatchDocs()
	drive := newBatchDrive(docs)
	publishState := state.New("workspace", "work")
	active, file := readyActiveTarget("a", "docs/a.md", 1)
	publishState.Documents["docs/a.md"] = state.Document{ActiveTarget: &active}
	drive.files[file.ID] = file
	drive.getErrors[file.ID] = &googleapi.Error{Kind: googleapi.KindPermission, Operation: "get", Cause: errors.New("denied")}
	application := New(io.Discard, nil)
	_, err := application.inspectActiveTargets(context.Background(), &config.Config{WorkspaceID: "workspace"}, config.SelectedProfile{Name: "work"}, publishState, googleapi.Clients{Drive: drive, Docs: docs}, map[string]bool{"docs/a.md": true})
	var typed *Error
	if !errors.As(err, &typed) || typed.Class != ClassAuthentication {
		t.Fatalf("error = %#v", err)
	}
}

func TestSelectedPublishClosureIncludesOnlyTransitiveInboundLinks(t *testing.T) {
	locals := []planner.LocalDocument{
		{SourceKey: "a", SourceHash: "new", RenderHash: "r"},
		{SourceKey: "b", SourceHash: "s", RenderHash: "r", Outbound: []string{"a"}},
		{SourceKey: "c", SourceHash: "s", RenderHash: "r", Outbound: []string{"b"}},
		{SourceKey: "unrelated", SourceHash: "s", RenderHash: "r"},
	}
	publishState := state.New("workspace", "work")
	remotes := map[string]planner.RemoteDocument{}
	for _, local := range locals {
		active, _ := readyActiveTarget(local.SourceKey, local.SourceKey, 1)
		publishState.Documents[local.SourceKey] = state.Document{SourceHash: "s", RenderHash: "r", ActiveTarget: &active}
		remotes[local.SourceKey] = planner.RemoteDocument{Exists: true, Accessible: true}
	}
	closure := selectedPublishClosure(locals, publishState, remotes, map[string]bool{"a": true}, false)
	if !closure["a"] || !closure["b"] || !closure["c"] || closure["unrelated"] {
		t.Fatalf("closure = %#v", closure)
	}
}

func readyActiveTarget(fileID, source string, generation int) (state.Target, googleapi.File) {
	url := "https://docs.google.com/document/d/" + fileID + "/edit"
	target := state.Target{FileID: fileID, URL: url, ReviewSetID: "set", Generation: generation, OperationID: "operation", PublishStatus: "ready", RemoteVersion: "1"}
	properties := googleapi.PublishMetadata{WorkspaceID: "workspace", Profile: "work", SourceKey: source, ReviewSetID: target.ReviewSetID, Generation: generation, OperationID: target.OperationID, ExpectedSetSize: 1, PublishStatus: "ready"}.Properties()
	file := googleapi.File{ID: fileID, Name: source, URL: url, MimeType: googleapi.DocumentMimeType, Version: "1", Parents: []string{"review"}, AppProperties: properties}
	return target, file
}

func TestBatchPublishKeepsPriorReviewTargetHistory(t *testing.T) {
	docs := newBatchDocs()
	drive := newBatchDrive(docs)
	store := newBatchStore(t)
	defer store.Release()
	publishState, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	old := state.Target{FileID: "old", URL: "https://docs.google.com/document/d/old/edit", ReviewSetID: "old-set", Generation: 1, OperationID: "old-operation", PublishStatus: "ready", RemoteVersion: "9", DocsRevision: "old-revision"}
	publishState.Documents["docs/a.md"] = state.Document{SourceHash: "old-source", RenderHash: "old-render", ActiveTarget: &old}
	if err := store.Save(publishState); err != nil {
		t.Fatal(err)
	}
	request := BatchRequest{WorkspaceID: "workspace", Profile: "work", StagingID: "staging", ReviewID: "review", ReviewSetID: "new-set", OperationID: "new-operation", Items: []BatchItem{{SourceKey: "docs/a.md", Title: "A", DOCX: []byte("new"), SourceHash: "new-source", RenderHash: "new-render", Generation: 2}}}
	if _, err := (BatchPublisher{Drive: drive, Docs: docs, Store: store}).Run(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	documentState := loaded.Documents["docs/a.md"]
	if documentState.ActiveTarget.Generation != 2 || len(documentState.PriorTargets) != 1 || documentState.PriorTargets[0].FileID != "old" {
		t.Fatalf("document state = %#v", documentState)
	}
}

func TestDocumentRenderRequestUsesOnlyTheSourceDirectoryForAssets(t *testing.T) {
	root := t.TempDir()
	sourceDir := filepath.Join(root, "docs")
	doc := &document.Document{Path: filepath.Join(sourceDir, "source.md")}
	request := documentRenderRequest(&config.Config{Root: root}, config.SelectedProfile{}, &document.Graph{}, doc, nil, nil)
	if len(request.ResourcePaths) != 1 || request.ResourcePaths[0] != sourceDir {
		t.Fatalf("resource paths = %#v, want only %q", request.ResourcePaths, sourceDir)
	}
}

func TestPublishJournalStoresArtifactHashAndRedactedHashParts(t *testing.T) {
	store := newBatchStore(t)
	t.Cleanup(func() { _ = store.Release() })
	input := render.HashInput{Source: []byte("private source"), Parts: []render.HashPart{{Name: "resolved_fields", Data: []byte("private field")}}}
	request := BatchRequest{WorkspaceID: "workspace", Profile: "work", StagingID: "staging", ReviewID: "review", ReviewSetID: "set", OperationID: "operation"}
	item := BatchItem{SourceKey: "docs/a.md", DOCX: []byte("docx"), SourceHash: "sha256:source", RenderHash: render.ComputeHash(input), HashInput: &input, Generation: 1, ComputedValues: map[string]any{"generation": 1}}
	journal, err := (BatchPublisher{Store: store}).loadOrCreateJournal(request, []BatchItem{item})
	if err != nil {
		t.Fatal(err)
	}
	entry := journal.Entries[item.SourceKey]
	if entry.ArtifactHash == "" || len(entry.HashParts) == 0 {
		t.Fatalf("entry = %#v", entry)
	}
	for _, part := range entry.HashParts {
		if strings.Contains(part.Hash, "private") {
			t.Fatalf("hash part leaked content: %#v", part)
		}
	}
}

func TestBundlePublishJournalRejectsChangedFrozenInputs(t *testing.T) {
	base := BatchItem{
		SourceKey:       "publication:handbook",
		DOCX:            []byte("docx"),
		SourceHash:      "sha256:source",
		RenderHash:      "sha256:render",
		Generation:      1,
		PublicationID:   "handbook",
		PublicationKind: "bundle",
		Members: []state.JournalMember{
			{SourceKey: "docs/a.md", SourceHash: "sha256:a"},
			{SourceKey: "docs/b.md", SourceHash: "sha256:b"},
		},
		LayoutHash:   "sha256:layout",
		TopologyHash: "sha256:topology",
	}
	tests := map[string]func(*BatchItem){
		"member":   func(item *BatchItem) { item.Members[1].SourceHash = "sha256:changed" },
		"layout":   func(item *BatchItem) { item.LayoutHash = "sha256:changed" },
		"topology": func(item *BatchItem) { item.TopologyHash = "sha256:changed" },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			store := newBatchStore(t)
			t.Cleanup(func() { _ = store.Release() })
			request := BatchRequest{WorkspaceID: "workspace", Profile: "work", StagingID: "staging", ReviewID: "review", ReviewSetID: "set", OperationID: "operation"}
			publisher := BatchPublisher{Store: store}
			if _, err := publisher.loadOrCreateJournal(request, []BatchItem{base}); err != nil {
				t.Fatal(err)
			}
			changed := base
			changed.Members = append([]state.JournalMember(nil), base.Members...)
			mutate(&changed)
			if _, err := publisher.loadOrCreateJournal(request, []BatchItem{changed}); err == nil || !strings.Contains(err.Error(), "inputs changed") {
				t.Fatalf("changed %s error = %v", name, err)
			}
		})
	}
}

func newBatchStore(t *testing.T) *state.Store {
	t.Helper()
	store := state.NewStore(t.TempDir() + "/state.json")
	if err := store.Acquire(); err != nil {
		t.Fatal(err)
	}
	initial := state.New("workspace", "work")
	initial.Folders = state.Folders{StagingID: "staging", ReviewID: "review"}
	if err := store.Save(initial); err != nil {
		t.Fatal(err)
	}
	return store
}
func linkedBatch(placeholder string) BatchRequest {
	return BatchRequest{WorkspaceID: "workspace", Profile: "work", StagingID: "staging", ReviewID: "review", ReviewSetID: "set", OperationID: "operation", Items: []BatchItem{{SourceKey: "docs/a.md", Title: "A", DOCX: []byte("a"), SourceHash: "source-a", RenderHash: "render-a", Generation: 1, Links: []PendingLink{{Placeholder: placeholder, TargetSource: "docs/b.md", HadFragment: true}}}, {SourceKey: "docs/b.md", Title: "B", DOCX: []byte("b"), SourceHash: "source-b", RenderHash: "render-b", Generation: 1}}}
}
func copyProperties(input map[string]string) map[string]string {
	result := map[string]string{}
	maps.Copy(result, input)
	return result
}
func copyLinks(input map[string][]googleapi.TextRange) map[string][]googleapi.TextRange {
	result := map[string][]googleapi.TextRange{}
	for key, value := range input {
		result[key] = append([]googleapi.TextRange(nil), value...)
	}
	return result
}
func increment(value string) string {
	number, _ := strconv.Atoi(value)
	return strconv.Itoa(number + 1)
}
func incrementRevision(value string) string { return "r" + increment(value[1:]) }
