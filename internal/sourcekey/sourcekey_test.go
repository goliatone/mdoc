package sourcekey

import (
	"path/filepath"
	"testing"
)

func TestNormalizeUsesNFCAndRejectsUnsafeKeys(t *testing.T) {
	key, err := Normalize("docs/cafe\u0301.md")
	if err != nil {
		t.Fatal(err)
	}
	if key != "docs/caf\u00e9.md" {
		t.Fatalf("key = %q", key)
	}
	for _, value := range []string{"../a.md", "/tmp/a.md", "."} {
		if _, err := Normalize(value); err == nil {
			t.Fatalf("expected %q to be rejected", value)
		}
	}
}

func TestNormalizePreservesValidWhitespace(t *testing.T) {
	key, err := Normalize(" docs/report.md ")
	if err != nil {
		t.Fatal(err)
	}
	if key != " docs/report.md " {
		t.Fatalf("key = %q", key)
	}
}

func TestFromPathNormalizesRelativePath(t *testing.T) {
	root := t.TempDir()
	key, err := FromPath(root, filepath.Join(root, "docs", "cafe\u0301.md"))
	if err != nil {
		t.Fatal(err)
	}
	if key != "docs/caf\u00e9.md" {
		t.Fatalf("key = %q", key)
	}
}
