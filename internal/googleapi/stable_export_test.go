package googleapi

import (
	"context"
	"errors"
	"testing"
)

type stableDriveFake struct {
	files         []File
	fileReads     int
	exports       int
	comments      CommentStatus
	exportContent []byte
}

func (f *stableDriveFake) GetFile(context.Context, string) (File, error) {
	if f.fileReads >= len(f.files) {
		return File{}, errors.New("unexpected file read")
	}
	result := f.files[f.fileReads]
	f.fileReads++
	return result, nil
}

func (f *stableDriveFake) ExportDocument(context.Context, string, string) ([]byte, error) {
	f.exports++
	return append([]byte(nil), f.exportContent...), nil
}

func (f *stableDriveFake) ListComments(context.Context, string) (CommentStatus, error) {
	return f.comments, nil
}

type stableDocsFake struct {
	documents []Document
	reads     int
}

func (f *stableDocsFake) GetDocument(context.Context, string) (Document, error) {
	if f.reads >= len(f.documents) {
		return Document{}, errors.New("unexpected document read")
	}
	result := f.documents[f.reads]
	f.reads++
	return result, nil
}

func stableFile(version string) File {
	return File{ID: "doc", MimeType: DocumentMimeType, Version: version}
}

func stableDocument(revision string) Document {
	return Document{ID: "doc", RevisionID: revision, Tabs: []TabTopology{{ID: "tab", Title: "Tab 1", Index: 0}}}
}

func TestStableReaderReturnsContentCommentsAndTuple(t *testing.T) {
	drive := &stableDriveFake{files: []File{stableFile("3"), stableFile("3")}, comments: CommentStatus{Unresolved: 2, Pages: 1}, exportContent: []byte("# Review")}
	docs := &stableDocsFake{documents: []Document{stableDocument("r3"), stableDocument("r3")}}
	result, err := (StableReader{Drive: drive, Docs: docs, Backoff: stableNoBackoff}).Read(context.Background(), "doc", []TabTopology{{ID: "tab", Title: "Tab 1", Index: 0}})
	if err != nil {
		t.Fatal(err)
	}
	if string(result.Content) != "# Review" || result.File.Version != "3" || result.Document.RevisionID != "r3" || result.Comments.Unresolved != 2 || drive.exports != 1 {
		t.Fatalf("result=%#v exports=%d", result, drive.exports)
	}
}

func TestStableReaderRetriesVersionOrRevisionChanges(t *testing.T) {
	drive := &stableDriveFake{files: []File{stableFile("1"), stableFile("2"), stableFile("2"), stableFile("2")}, exportContent: []byte("review")}
	docs := &stableDocsFake{documents: []Document{stableDocument("r1"), stableDocument("r2"), stableDocument("r2"), stableDocument("r2")}}
	result, err := (StableReader{Drive: drive, Docs: docs, Attempts: 2, Backoff: stableNoBackoff}).Read(context.Background(), "doc", nil)
	if err != nil || result.File.Version != "2" || drive.exports != 2 {
		t.Fatalf("result=%#v exports=%d error=%v", result, drive.exports, err)
	}
}

func TestStableReaderReportsExhaustedChanges(t *testing.T) {
	drive := &stableDriveFake{files: []File{stableFile("1"), stableFile("2"), stableFile("2"), stableFile("3")}, exportContent: []byte("review")}
	docs := &stableDocsFake{documents: []Document{stableDocument("r1"), stableDocument("r2"), stableDocument("r2"), stableDocument("r3")}}
	_, err := (StableReader{Drive: drive, Docs: docs, Attempts: 2, Backoff: stableNoBackoff}).Read(context.Background(), "doc", nil)
	if !hasGoogleKind(err, KindUnstable) || drive.exports != 2 {
		t.Fatalf("error=%v exports=%d", err, drive.exports)
	}
}

func TestStableReaderBlocksSuggestionsIdentityAndTopology(t *testing.T) {
	tests := []struct {
		name     string
		file     File
		document Document
		expected []TabTopology
		kind     ErrorKind
	}{
		{name: "suggestions", file: stableFile("1"), document: func() Document { d := stableDocument("r1"); d.SuggestionsDetected = true; return d }(), kind: KindSuggestions},
		{name: "identity", file: func() File { f := stableFile("1"); f.ID = "other"; return f }(), document: stableDocument("r1"), kind: KindConflict},
		{name: "added root", file: stableFile("1"), document: func() Document {
			d := stableDocument("r1")
			d.Tabs = append(d.Tabs, TabTopology{ID: "two", Title: "Two", Index: 1})
			return d
		}(), kind: KindUnsupported},
		{name: "added child", file: stableFile("1"), document: func() Document {
			d := stableDocument("r1")
			d.Tabs = append(d.Tabs, TabTopology{ID: "child", Title: "Child", Index: 0, ParentID: "tab"})
			return d
		}(), kind: KindUnsupported},
		{name: "renamed", file: stableFile("1"), document: func() Document { d := stableDocument("r1"); d.Tabs[0].Title = "Renamed"; return d }(), expected: stableDocument("r1").Tabs, kind: KindUnsupported},
		{name: "reordered", file: stableFile("1"), document: func() Document { d := stableDocument("r1"); d.Tabs[0].Index = 1; return d }(), kind: KindUnsupported},
		{name: "reparented", file: stableFile("1"), document: func() Document { d := stableDocument("r1"); d.Tabs[0].ParentID = "parent"; return d }(), kind: KindUnsupported},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			drive := &stableDriveFake{files: []File{test.file}, exportContent: []byte("review")}
			docs := &stableDocsFake{documents: []Document{test.document}}
			_, err := (StableReader{Drive: drive, Docs: docs, Backoff: stableNoBackoff}).Read(context.Background(), "doc", test.expected)
			if !hasGoogleKind(err, test.kind) || drive.exports != 0 {
				t.Fatalf("error=%v exports=%d", err, drive.exports)
			}
		})
	}
}

func TestStableReaderBlocksTopologyChangeAfterExport(t *testing.T) {
	drive := &stableDriveFake{files: []File{stableFile("1"), stableFile("1")}, exportContent: []byte("review")}
	before := stableDocument("r1")
	after := stableDocument("r1")
	after.Tabs[0].Title = "Renamed"
	docs := &stableDocsFake{documents: []Document{before, after}}
	_, err := (StableReader{Drive: drive, Docs: docs, Backoff: stableNoBackoff}).Read(context.Background(), "doc", nil)
	if !hasGoogleKind(err, KindUnsupported) || drive.exports != 1 {
		t.Fatalf("error=%v exports=%d", err, drive.exports)
	}
}

func stableNoBackoff(context.Context, int) error { return nil }

func hasGoogleKind(err error, kind ErrorKind) bool {
	var typed *Error
	return errors.As(err, &typed) && typed.Kind == kind
}
