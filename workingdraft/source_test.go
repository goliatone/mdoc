package workingdraft_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	wd "github.com/goliatone/mdoc/workingdraft"
)

func TestCaptureRedactsRotatingImageCapabilities(t *testing.T) {
	calls := 0
	store := memoryStore{}
	client := &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodGet {
			t.Fatalf("unexpected write %s", r.Method)
		}
		data := `{"id":"doc","name":"Draft","mimeType":"application/vnd.google-apps.document","version":"1"}`
		if r.URL.Host == "docs.googleapis.com" {
			if r.URL.Query().Get("includeTabsContent") != "true" || r.URL.Query().Get("suggestionsViewMode") != "SUGGESTIONS_INLINE" {
				t.Fatal("missing document read options")
			}
			calls++
			data = fmt.Sprintf(`{"documentId":"doc","title":"Draft","revisionId":"r1","tabs":[{"tabProperties":{"tabId":"t.0"},"documentTab":{"body":{"content":[]},"inlineObjects":{"image":{"inlineObjectProperties":{"embeddedObject":{"imageProperties":{"contentUri":"https://private/image-%d","sourceUri":"https://example.com/authored-image"}}}}}}}]}`, calls)
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(data))}, nil
	})}
	service, err := wd.New(wd.Options{HTTPClient: client, Store: store})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := service.Capture(context.Background(), wd.SourceRef{DocumentID: "doc"})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 || snapshot.BodyUsable || len(snapshot.Diagnostics) == 0 {
		t.Fatalf("unexpected capture: calls=%d usable=%v", calls, snapshot.BodyUsable)
	}
	data, _ := json.Marshal(snapshot)
	if strings.Contains(string(data), "private/image") || strings.Contains(string(data), "contentUri") {
		t.Fatal("snapshot leaked image capability")
	}
	if !strings.Contains(string(snapshot.RawContent), "authored-image") {
		t.Fatal("removed authored source URI")
	}
	if _, err := wd.LoadSnapshot(context.Background(), store, snapshot.SnapshotDigest); err != nil {
		t.Fatal(err)
	}
	for _, value := range store {
		if strings.Contains(string(value), "private/image") {
			t.Fatal("stored image capability")
		}
	}
	snapshot.RawContent = json.RawMessage(`{"imageProperties":{"contentUri":"https://private/legacy"}}`)
	snapshot.SnapshotDigest = ""
	data, _ = json.Marshal(snapshot)
	sum := sha256.Sum256(data)
	snapshot.SnapshotDigest = hex.EncodeToString(sum[:])
	data, _ = json.Marshal(snapshot)
	if _, err := wd.DecodeSnapshot(data); err == nil {
		t.Fatal("accepted correctly sealed legacy snapshot containing a capability")
	}
	if err := wd.SaveSnapshot(context.Background(), store, snapshot); err == nil {
		t.Fatal("persisted unsafe legacy snapshot")
	}
}

type rawProvider struct {
	*provider
	raw json.RawMessage
}

func (p rawProvider) Document(context.Context, string) (json.RawMessage, error) { return p.raw, nil }

func TestInheritedStylesPreservePresence(t *testing.T) {
	for _, tc := range []struct {
		name, normal, heading, direct, want string
		blocked                             bool
	}{
		{name: "inherited bold", normal: `{"bold":true}`, direct: `{}`, want: "**body**\n"},
		{name: "explicit false", normal: `{"bold":true}`, direct: `{"bold":false}`, want: "body\n"},
		{name: "heading inherits normal", normal: `{"bold":true}`, heading: `{"italic":true}`, direct: `{}`, want: "# ***body***\n"},
		{name: "heading resets normal", normal: `{"bold":true}`, heading: `{"bold":false}`, direct: `{}`, want: "# body\n"},
		{name: "run resets heading", normal: `{}`, heading: `{"bold":true,"italic":true}`, direct: `{"bold":false,"italic":false}`, want: "# body\n"},
		{name: "inherited underline", normal: `{"underline":true}`, direct: `{}`, blocked: true},
		{name: "inherited small caps", normal: `{"smallCaps":true}`, direct: `{}`, blocked: true},
		{name: "inherited superscript", normal: `{"baselineOffset":"SUPERSCRIPT"}`, direct: `{}`, blocked: true},
		{name: "explicit baseline", normal: `{"baselineOffset":"SUPERSCRIPT"}`, direct: `{"baselineOffset":"NONE"}`, want: "body\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			named := `{"namedStyleType":"NORMAL_TEXT","textStyle":` + tc.normal + `}`
			style := "NORMAL_TEXT"
			if tc.heading != "" {
				named += `,{"namedStyleType":"HEADING_1","textStyle":` + tc.heading + `}`
				style = "HEADING_1"
			}
			raw := fmt.Sprintf(`{"documentId":"doc","title":"Draft","tabs":[{"tabProperties":{"tabId":"t.0"},"documentTab":{"namedStyles":{"styles":[%s]},"body":{"content":[{"paragraph":{"paragraphStyle":{"namedStyleType":%q},"elements":[{"textRun":{"content":"body\n","textStyle":%s}}]}}]}}}]}`, named, style, tc.direct)
			service, err := wd.New(wd.Options{Provider: rawProvider{&provider{doc: document("body")}, json.RawMessage(raw)}})
			if err != nil {
				t.Fatal(err)
			}
			inspection, err := service.Inspect(context.Background(), wd.SourceRef{DocumentID: "doc"})
			if err != nil {
				t.Fatal(err)
			}
			snapshot, err := service.Capture(context.Background(), wd.SourceRef{DocumentID: "doc"})
			if err != nil {
				t.Fatal(err)
			}
			if snapshot.BodyUsable == tc.blocked || inspection.Supported == tc.blocked || snapshot.NormalizedContent.Body != tc.want {
				t.Fatalf("usable=%v body=%q diagnostics=%v", snapshot.BodyUsable, snapshot.NormalizedContent.Body, snapshot.Diagnostics)
			}
			if strings.Contains(tc.direct, "false") && !strings.Contains(string(snapshot.RawContent), "false") {
				t.Fatal("lost explicit false in evidence")
			}
		})
	}
}
