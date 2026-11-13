package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/goliatone/mdoc/portable"
	"gopkg.in/yaml.v3"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type portableTransport struct{ server *url.URL }

func (t portableTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	copy := r.Clone(r.Context())
	u := *r.URL
	u.Scheme = t.server.Scheme
	u.Host = t.server.Host
	copy.URL = &u
	return http.DefaultTransport.RoundTrip(copy)
}
func TestBundleRegisteredCLIProductionSmoke(t *testing.T) {
	var pngData bytes.Buffer
	if e := png.Encode(&pngData, image.NewRGBA(image.Rect(0, 0, 2, 2))); e != nil {
		t.Fatal(e)
	}
	reads := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reads++
		if r.Method != "GET" {
			t.Error("remote mutation")
		}
		switch r.URL.Path {
		case "/v1/documents/DOC":
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"documentId":"DOC","revisionId":"rev1","body":{"content":[{"paragraph":{"elements":[{"textRun":{"content":"Architecture\n"}}]}},{"paragraph":{"elements":[{"inlineObjectElement":{"inlineObjectId":"image"}}]}}]},"inlineObjects":{"image":{"inlineObjectProperties":{"embeddedObject":{"description":"System diagram","imageProperties":{"contentUri":"https://lh3.googleusercontent.com/image"}}}}}}`))
		case "/drive/v3/files/DOC":
			if r.Header.Get("X-Goog-Drive-Resource-Keys") != "DOC/private-key" {
				t.Error("missing private source access header")
			}
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"id":"DOC","name":"Architecture","mimeType":"application/vnd.google-apps.document","version":"1"}`))
		case "/image":
			if r.Header.Get("Authorization") != "" {
				t.Error("image authorization leaked")
			}
			w.Header().Set("Content-Type", "image/png")
			w.Write(pngData.Bytes())
		default:
			http.Error(w, "missing", 404)
		}
	}))
	defer server.Close()
	endpoint, _ := url.Parse(server.URL)
	client := &http.Client{Transport: portableTransport{endpoint}}
	service, e := portable.New(portable.Options{HTTPClient: client, ImageHTTPClient: client})
	if e != nil {
		t.Fatal(e)
	}
	root := t.TempDir()
	index := `# Bundle inventory

| ID | Document | Section | Path | Role |
| --- | --- | --- | --- | --- |
| DOC | [Architecture](https://docs.google.com/document/d/DOC/edit?resourcekey=private-key) | | docs/overview.md | document |
`
	if e = os.WriteFile(filepath.Join(root, "index.md"), []byte(index), 0600); e != nil {
		t.Fatal(e)
	}
	config := portable.DefaultConfig()
	config.Project = "cli-smoke"
	config.Index.Source = "index.md"
	data, _ := yaml.Marshal(config)
	configPath := filepath.Join(root, "mdoc-bundle.yaml")
	os.WriteFile(configPath, data, 0600)
	dest := filepath.Join(root, "output")
	var out, errout bytes.Buffer
	for _, args := range [][]string{{"bundle", "plan", "--config", configPath, "--json"}, {"bundle", "export", "--config", configPath, "--output", dest, "--json"}} {
		out.Reset()
		if e = Execute(args, &out, &errout, &fakeAuth{}, service); e != nil {
			t.Fatal(e, errout.String())
		}
		if strings.Contains(out.String(), "private-key") {
			t.Fatal("private resource key leaked")
		}
		if !json.Valid(out.Bytes()) {
			t.Fatal(out.String())
		}
	}
	if reads == 0 {
		t.Fatal("provider adapter unused")
	}
	server.Close()
	moved := filepath.Join(root, "moved")
	if e = os.Rename(dest, moved); e != nil {
		t.Fatal(e)
	}
	out.Reset()
	if e = Execute([]string{"bundle", "verify", moved, "--json"}, &out, &errout, nil); e != nil {
		t.Fatal(e)
	}
	var verification portable.Verification
	if e = json.Unmarshal(out.Bytes(), &verification); e != nil || !verification.LocalComplete || !verification.NoOnlineDependencies {
		t.Fatal(verification, e)
	}
	out.Reset()
	if e = Execute([]string{"bundle", "init", "--directory", filepath.Join(root, "starter"), "--project", "demo", "--json"}, &out, &errout, nil); e != nil {
		t.Fatal(e)
	}
	if e = Execute([]string{"bundle", "export", "--config", configPath, "--output", moved}, &out, &errout, &fakeAuth{}, service); e == nil {
		t.Fatal("existing destination accepted")
	}
	var errorJSON bytes.Buffer
	WriteError(&errorJSON, &portable.Error{Code: "missing_asset", Message: "required image unavailable"}, true)
	if !strings.Contains(errorJSON.String(), `"class": "portable"`) || !WantsJSON([]string{"bundle", "export", "--json", "--output", "some-dir"}) {
		t.Fatal(errorJSON.String())
	}
	// The public API also verifies without a provider after CLI composition.
	offline, _ := portable.New(portable.Options{})
	if _, e = offline.Verify(context.Background(), portable.VerifyRequest{Directory: moved}); e != nil {
		t.Fatal(e)
	}
}
