package portable

import (
	"context"
	"testing"
)

const inventory = "# Bundle inventory\n\n| ID | Document | Section | Path | Role |\n|---|---|---|---|---|\n| PRD | [Product](https://docs.google.com/document/d/PRD/edit) | requirements | | document |\n| TDD | [Design](https://docs.google.com/document/d/TDD/edit) | | docs/overview.md | document |\n| DIAGRAMS | [Diagrams](https://drive.google.com/drive/folders/TREE) | | diagrams | asset-tree |\n"

func TestInventoryLayout(t *testing.T) {
	c := DefaultConfig()
	c.Project = "test"
	c.Index.Source = "index.md"
	c.Layout.Sections["requirements"] = "docs/requirements"
	m, err := (PandocConverter{}).Parse(context.Background(), []byte(inventory))
	if err != nil {
		t.Fatal(err)
	}
	entries, err := parseInventory(m, c)
	if err != nil {
		t.Fatal(err)
	}
	p, err := resolveLayout(c, entries)
	if err != nil {
		t.Fatal(err)
	}
	if p.Entries[0].Path != "docs/requirements/prd-product.md" || p.Entries[1].Path != "docs/overview.md" {
		t.Fatal(p)
	}
	for _, f := range []string{"README.md", "manifest.json", "system.png"} {
		if e := reserve(&p.Destinations, Reservation{Path: "diagrams/" + f, Kind: "asset"}); e != nil {
			t.Fatal(e)
		}
	}
	c.Layout.AssetIndex = "diagrams/{id}.md"
	if _, e := resolveLayout(c, entries); e == nil {
		t.Fatal("inside-tree listing allowed")
	}
}
func TestPathIntegrity(t *testing.T) {
	for _, p := range []string{"../escape", "/root", "a/../b", "C:/x", "x\\y", "a//b", "CON.md"} {
		if safePath(p) == nil {
			t.Fatal(p)
		}
	}
	var rs []Reservation
	for _, p := range []string{"é.md", "e\u0301.md"} {
		e := reserve(&rs, Reservation{Path: p, Kind: "document"})
		if len(rs) > 0 && p != "é.md" && e == nil {
			t.Fatal("unicode collision")
		}
	}
	rs = nil
	if e := reserve(&rs, Reservation{Path: "a.png", Kind: "asset"}); e != nil {
		t.Fatal(e)
	}
	if e := reserve(&rs, Reservation{Path: "a.png", Kind: "asset"}); e != nil || !rs[0].PendingEquality {
		t.Fatal(e, rs)
	}
}
func TestInventoryExternalReference(t *testing.T) {
	c := DefaultConfig()
	m, e := (PandocConverter{}).Parse(context.Background(), []byte(inventory+"| EXT | [Website](https://example.test/spec#details) | | | reference |\n"))
	if e != nil {
		t.Fatal(e)
	}
	entries, e := parseInventory(m, c)
	if e != nil || entries[len(entries)-1].Role != "reference" {
		t.Fatal(e)
	}
}
