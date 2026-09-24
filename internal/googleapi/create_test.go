package googleapi

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"
)

type creatorDrive struct {
	findResults [][]File
	findCalls   int
	createCalls int
	createFile  File
	createErr   error
}

func (f *creatorDrive) FindDocuments(context.Context, map[string]string) ([]File, error) {
	index := f.findCalls
	f.findCalls++
	if index >= len(f.findResults) {
		index = len(f.findResults) - 1
	}
	if index < 0 {
		return nil, nil
	}
	return f.findResults[index], nil
}
func (f *creatorDrive) CreateDocument(context.Context, CreateDocumentInput) (File, error) {
	f.createCalls++
	return f.createFile, f.createErr
}
func (f *creatorDrive) GetFile(context.Context, string) (File, error) { return f.createFile, nil }
func (*creatorDrive) EnsureFolders(context.Context, FolderSpec) (FolderPair, error) {
	return FolderPair{}, nil
}
func (*creatorDrive) FindFolders(context.Context, string, string, string) ([]Folder, error) {
	return nil, nil
}
func (*creatorDrive) CreateFolder(context.Context, string, string, string, string) (Folder, error) {
	return Folder{}, nil
}
func (*creatorDrive) UpdateProperties(context.Context, string, map[string]string) (File, error) {
	return File{}, nil
}
func (*creatorDrive) MoveFile(context.Context, string, string, string) (File, error) {
	return File{}, nil
}
func (*creatorDrive) ListComments(context.Context, string) (CommentStatus, error) {
	return CommentStatus{}, nil
}
func (*creatorDrive) ExportDocument(context.Context, string, string) ([]byte, error) {
	return []byte("# Export"), nil
}

type creatorDocs struct{ revision string }

func (f creatorDocs) GetDocument(context.Context, string) (Document, error) {
	return Document{RevisionID: f.revision}, nil
}
func (creatorDocs) UpdateLinks(context.Context, string, string, []LinkUpdate) (Document, error) {
	return Document{}, nil
}

func TestCreatorReturnsNativeMetadata(t *testing.T) {
	drive := &creatorDrive{findResults: [][]File{nil}, createFile: File{ID: "doc", URL: "https://docs.google.com/document/d/doc/edit", MimeType: DocumentMimeType, Version: "7", ModifiedTime: time.Now()}}
	result, err := (Creator{Drive: drive, Docs: creatorDocs{revision: "revision"}, Backoff: noBackoff}).Create(context.Background(), "Title", "staging", []byte("docx"), validMetadata())
	if err != nil {
		t.Fatal(err)
	}
	if result.ID != "doc" || result.Version != "7" || result.DocsRevision != "revision" {
		t.Fatalf("result = %#v", result)
	}
	if drive.createCalls != 1 {
		t.Fatalf("create calls = %d", drive.createCalls)
	}
}

func TestCreatorReconcilesLostResponseWithoutDuplicate(t *testing.T) {
	file := File{ID: "existing", URL: "url", Version: "2", MimeType: DocumentMimeType, Parents: []string{"staging"}, AppProperties: validMetadata().Properties()}
	drive := &creatorDrive{findResults: [][]File{nil, {file}}, createFile: file, createErr: &Error{Kind: KindRetryable, Operation: "create", Cause: io.ErrUnexpectedEOF}}
	result, err := (Creator{Drive: drive, Docs: creatorDocs{revision: "r"}, Backoff: noBackoff}).Create(context.Background(), "Title", "staging", []byte("docx"), validMetadata())
	if err != nil {
		t.Fatal(err)
	}
	if result.ID != "existing" || drive.createCalls != 1 {
		t.Fatalf("result = %#v, creates = %d", result, drive.createCalls)
	}
}

func TestCreatorRejectsReconcileMatchWithUnexpectedMetadata(t *testing.T) {
	file := File{ID: "existing", MimeType: DocumentMimeType, Parents: []string{"other"}, AppProperties: validMetadata().Properties()}
	drive := &creatorDrive{findResults: [][]File{{file}}}
	_, err := (Creator{Drive: drive, Docs: creatorDocs{}}).Create(context.Background(), "Title", "staging", []byte("docx"), validMetadata())
	var remote *Error
	if !errors.As(err, &remote) || remote.Kind != KindConflict {
		t.Fatalf("error = %v", err)
	}
	if drive.createCalls != 0 {
		t.Fatalf("create calls = %d", drive.createCalls)
	}
}

func TestCreatorBlocksDuplicateReconciliation(t *testing.T) {
	drive := &creatorDrive{findResults: [][]File{{{ID: "one"}, {ID: "two"}}}}
	_, err := (Creator{Drive: drive, Docs: creatorDocs{}}).Create(context.Background(), "Title", "staging", []byte("docx"), validMetadata())
	var remote *Error
	if !errors.As(err, &remote) || remote.Kind != KindConflict {
		t.Fatalf("error = %v", err)
	}
	if drive.createCalls != 0 {
		t.Fatalf("create calls = %d", drive.createCalls)
	}
}

func TestCreatorDoesNotRetryPermissionErrors(t *testing.T) {
	drive := &creatorDrive{findResults: [][]File{nil, nil}, createErr: &Error{Kind: KindPermission, Operation: "create", Cause: errors.New("denied")}}
	_, err := (Creator{Drive: drive, Docs: creatorDocs{}, Backoff: noBackoff}).Create(context.Background(), "Title", "staging", []byte("docx"), validMetadata())
	if err == nil || drive.createCalls != 1 {
		t.Fatalf("error = %v, creates = %d", err, drive.createCalls)
	}
}

func TestCreatorBoundsRetryableCreates(t *testing.T) {
	drive := &creatorDrive{findResults: [][]File{nil}, createErr: &Error{Kind: KindRetryable, Operation: "create", Cause: io.ErrUnexpectedEOF}}
	_, err := (Creator{Drive: drive, Docs: creatorDocs{}, Backoff: noBackoff, Attempts: 3}).Create(context.Background(), "Title", "staging", []byte("docx"), validMetadata())
	if err == nil || drive.createCalls != 3 {
		t.Fatalf("error=%v calls=%d", err, drive.createCalls)
	}
}

func validMetadata() PublishMetadata {
	return PublishMetadata{WorkspaceID: "workspace", Profile: "work", SourceKey: "docs/a.md", ReviewSetID: "set", Generation: 1, OperationID: "operation", ExpectedSetSize: 1, PublishStatus: "pending"}
}

func noBackoff(context.Context, int) error { return nil }

var _ io.Reader
