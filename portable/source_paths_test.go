package portable

import (
	"context"
	"errors"
	"strings"
	"testing"

	"google.golang.org/api/drive/v3"
)

func TestRelativeAncestryBounds(t *testing.T) {
	s, _ := New(Options{})
	cat := newCatalog()
	cat.ancestorLimit = 0
	source := Evidence{Source: SourceRef{FileID: "DOC", Kind: "drive"}, Parents: []string{"FOLDER"}}
	for _, target := range []string{"../TDD.md", strings.Repeat("../", 33) + "TDD.md"} {
		_, e := s.resolveRelative(context.Background(), target, source, cat)
		var pe *Error
		if !errors.As(e, &pe) || pe.Code != "limit_exceeded" {
			t.Fatalf("unbounded ancestry for %s: %v", target, e)
		}
	}
	cat.files["FOLDER"] = &drive.File{Id: "FOLDER", MimeType: folderMIME, Parents: []string{"PARENT"}}
	cat.files["PARENT"] = &drive.File{Id: "PARENT", MimeType: folderMIME, Parents: []string{"ROOT"}}
	cat.files["TDD"] = &drive.File{Id: "TDD", Name: "TDD.md", Parents: []string{"ROOT"}}
	got, e := s.resolveRelative(context.Background(), "../../TDD.md", source, cat)
	if e != nil || got != "TDD" {
		t.Fatal(got, e)
	}
	cat.files["PARENT"].Parents = []string{"FOLDER"}
	if _, e = s.resolveRelative(context.Background(), "../../TDD.md", source, cat); e == nil {
		t.Fatal("multi-folder cycle accepted")
	}
}
