package bundle

import (
	"bytes"
	"strings"
	"testing"
)

func TestGenerateBookmarksExtendsCollidingPrefixesDeterministically(t *testing.T) {
	hash := func(value string) []byte {
		prefix := bytes.Repeat([]byte{0xaa}, 6)
		if value == "first" {
			return append(prefix, 0x01, 0x02)
		}
		return append(prefix, 0x03, 0x04)
	}
	forward, err := GenerateBookmarks([]BookmarkCandidate{{Logical: "first", Label: "Same"}, {Logical: "second", Label: "Same"}}, hash)
	if err != nil {
		t.Fatal(err)
	}
	reverse, err := GenerateBookmarks([]BookmarkCandidate{{Logical: "second", Label: "Same"}, {Logical: "first", Label: "Same"}}, hash)
	if err != nil {
		t.Fatal(err)
	}
	if forward["first"] == forward["second"] || forward["first"] != reverse["first"] || !strings.HasPrefix(forward["first"], "m_aaaaaaaaaaaa01") {
		t.Fatalf("bookmarks = %#v, reverse = %#v", forward, reverse)
	}
}

func TestGenerateBookmarksRejectsUnresolvedCollision(t *testing.T) {
	_, err := GenerateBookmarks([]BookmarkCandidate{{Logical: "first", Label: "Same"}, {Logical: "second", Label: "Same"}}, func(string) []byte { return []byte{1, 2, 3} })
	if err == nil || !strings.Contains(err.Error(), "cannot be resolved") {
		t.Fatalf("error = %v", err)
	}
}
