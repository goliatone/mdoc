package portable_test

import (
	"context"
	"github.com/goliatone/mdoc/portable"
	"testing"
)

func TestPublicContractsAndStrictConfig(t *testing.T) {
	c, err := portable.DecodeConfig([]byte("version: 1\nproject: demo\nindex:\n  source: index.md\n"))
	if err != nil {
		t.Fatal(err)
	}
	if c.Layout.Index != "00-START-HERE.md" || c.Limits.Attempts != 3 {
		t.Fatal(c)
	}
	for _, extra := range []string{"unknown: true\n", "limits:\n  attempts: 4\n", "naming:\n  document_pattern: '{unknown}'\n", "layout:\n  index: '../escape.md'\n"} {
		if _, e := portable.DecodeConfig([]byte("version: 1\nproject: demo\nindex:\n  source: index.md\n" + extra)); e == nil {
			t.Fatal(extra)
		}
	}
	s, e := portable.New(portable.Options{})
	if e != nil {
		t.Fatal(e)
	}
	var api portable.API = s
	if _, e = api.Export(context.Background(), portable.ExportRequest{Config: c}); e == nil {
		t.Fatal("export accepted a missing explicit destination")
	}
}
