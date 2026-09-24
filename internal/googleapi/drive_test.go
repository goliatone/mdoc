package googleapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"google.golang.org/api/drive/v3"
	"google.golang.org/api/option"
)

func folderJSON(id, name, parent, driveID string, properties map[string]string) string {
	value := map[string]any{
		"id": id, "name": name, "mimeType": FolderMimeType, "driveId": driveID,
		"parents": []string{parent}, "appProperties": properties,
		"owners":       []map[string]string{{"emailAddress": "author@example.com"}},
		"capabilities": map[string]bool{"canAddChildren": true, "canEdit": true},
	}
	data, _ := json.Marshal(value)
	return string(data)
}

func TestEnsureFoldersReusesUniqueAndCreatesMissing(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodGet && request.URL.Path == "/files" {
			query, _ := url.QueryUnescape(request.URL.Query().Get("q"))
			if strings.Contains(query, "value='staging'") {
				fmt.Fprint(writer, `{"files":[{"id":"staging-id","name":"Existing staging","mimeType":"application/vnd.google-apps.folder","appProperties":{"mdoc_workspace":"workspace","mdoc_profile":"work","mdoc_role":"staging","mdoc_kind":"folder"},"owners":[{"emailAddress":"author@example.com"}],"capabilities":{"canAddChildren":true,"canEdit":true}}]}`)
				return
			}
			fmt.Fprint(writer, `{"files":[]}`)
			return
		}
		if request.Method == http.MethodPost && request.URL.Path == "/files" {
			writer.Header().Set("Content-Type", "application/json")
			fmt.Fprint(writer, `{"id":"review-id","name":"Review","mimeType":"application/vnd.google-apps.folder","appProperties":{"mdoc_workspace":"workspace","mdoc_profile":"work","mdoc_role":"review","mdoc_kind":"folder"},"owners":[{"emailAddress":"author@example.com"}],"capabilities":{"canAddChildren":true,"canEdit":true}}`)
			return
		}
		http.Error(writer, request.Method+" "+request.URL.String(), http.StatusNotFound)
	}))
	defer server.Close()
	service, err := drive.NewService(context.Background(), option.WithEndpoint(server.URL+"/"), option.WithoutAuthentication())
	if err != nil {
		t.Fatal(err)
	}
	pair, err := NewDriveClient(service).EnsureFolders(context.Background(), FolderSpec{WorkspaceID: "workspace", Profile: "work", StagingName: "Staging", ReviewName: "Review"})
	if err != nil {
		t.Fatal(err)
	}
	if pair.Staging.ID != "staging-id" || pair.Review.ID != "review-id" {
		t.Fatalf("folders = %#v", pair)
	}
}

func TestEnsureFoldersBlocksDuplicateRoleMatches(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		fmt.Fprint(writer, `{"files":[{"id":"one","appProperties":{"mdoc_role":"staging"}},{"id":"two","appProperties":{"mdoc_role":"staging"}}]}`)
	}))
	defer server.Close()
	service, err := drive.NewService(context.Background(), option.WithEndpoint(server.URL+"/"), option.WithoutAuthentication())
	if err != nil {
		t.Fatal(err)
	}
	_, err = NewDriveClient(service).EnsureFolders(context.Background(), FolderSpec{WorkspaceID: "workspace", Profile: "work", StagingName: "Staging", ReviewName: "Review"})
	if err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("error = %v", err)
	}
}

func TestEnsureFoldersAdoptsExistingFoldersAfterCompletePreflight(t *testing.T) {
	writes := 0
	reviewRead := false
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		switch {
		case request.Method == http.MethodGet && request.URL.Path == "/files":
			fmt.Fprint(writer, `{"files":[]}`)
		case request.Method == http.MethodGet && request.URL.Path == "/files/parent":
			fmt.Fprint(writer, folderJSON("parent", "Parent", "root", "", nil))
		case request.Method == http.MethodGet && request.URL.Path == "/files/staging":
			fmt.Fprint(writer, folderJSON("staging", "Existing staging", "parent", "", nil))
		case request.Method == http.MethodGet && request.URL.Path == "/files/review":
			reviewRead = true
			fmt.Fprint(writer, folderJSON("review", "Existing review", "parent", "", nil))
		case request.Method == http.MethodPatch && strings.HasPrefix(request.URL.Path, "/files/"):
			if !reviewRead {
				t.Errorf("folder metadata write happened before both roles were preflighted")
			}
			writes++
			role := strings.TrimPrefix(request.URL.Path, "/files/")
			fmt.Fprint(writer, folderJSON(role, "Existing "+role, "parent", "", folderProperties("workspace", "testing", role)))
		default:
			http.Error(writer, request.Method+" "+request.URL.String(), http.StatusNotFound)
		}
	}))
	defer server.Close()
	service, err := drive.NewService(context.Background(), option.WithEndpoint(server.URL+"/"), option.WithoutAuthentication())
	if err != nil {
		t.Fatal(err)
	}
	pair, err := NewDriveClient(service).EnsureFolders(context.Background(), FolderSpec{
		WorkspaceID: "workspace", Profile: "testing", Account: "author@example.com", ParentID: "parent",
		StagingID: "staging", ReviewID: "review", StagingName: "Staging", ReviewName: "Review",
	})
	if err != nil {
		t.Fatal(err)
	}
	if writes != 2 || pair.Staging.Resolution != "adopted" || pair.Review.Resolution != "adopted" {
		t.Fatalf("writes=%d pair=%#v", writes, pair)
	}
}

func TestEnsureFoldersRejectsSharedDriveBeforeWrites(t *testing.T) {
	writes := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		if request.Method != http.MethodGet {
			writes++
		}
		switch request.URL.Path {
		case "/files":
			fmt.Fprint(writer, `{"files":[]}`)
		case "/files/parent":
			fmt.Fprint(writer, folderJSON("parent", "Parent", "root", "shared-drive", nil))
		default:
			http.Error(writer, "not found", http.StatusNotFound)
		}
	}))
	defer server.Close()
	service, err := drive.NewService(context.Background(), option.WithEndpoint(server.URL+"/"), option.WithoutAuthentication())
	if err != nil {
		t.Fatal(err)
	}
	_, err = NewDriveClient(service).EnsureFolders(context.Background(), FolderSpec{WorkspaceID: "workspace", Profile: "testing", ParentID: "parent"})
	if err == nil || !strings.Contains(err.Error(), "unsupported") || writes != 0 {
		t.Fatalf("error=%v writes=%d", err, writes)
	}
}

func TestEnsureFoldersCreatesBothRolesUnderParent(t *testing.T) {
	createdParents := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		switch {
		case request.Method == http.MethodGet && request.URL.Path == "/files":
			fmt.Fprint(writer, `{"files":[]}`)
		case request.Method == http.MethodGet && request.URL.Path == "/files/parent":
			fmt.Fprint(writer, folderJSON("parent", "Parent", "root", "", nil))
		case request.Method == http.MethodPost && request.URL.Path == "/files":
			var body struct {
				Name          string            `json:"name"`
				Parents       []string          `json:"parents"`
				AppProperties map[string]string `json:"appProperties"`
			}
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
				t.Errorf("decode create body: %v", err)
			}
			createdParents = append(createdParents, body.Parents...)
			role := body.AppProperties["mdoc_role"]
			fmt.Fprint(writer, folderJSON(role, body.Name, "parent", "", body.AppProperties))
		default:
			http.Error(writer, request.Method+" "+request.URL.String(), http.StatusNotFound)
		}
	}))
	defer server.Close()
	service, err := drive.NewService(context.Background(), option.WithEndpoint(server.URL+"/"), option.WithoutAuthentication())
	if err != nil {
		t.Fatal(err)
	}
	pair, err := NewDriveClient(service).EnsureFolders(context.Background(), FolderSpec{
		WorkspaceID: "workspace", Profile: "testing", Account: "author@example.com", ParentID: "parent", StagingName: "Staging", ReviewName: "Review",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(createdParents) != 2 || createdParents[0] != "parent" || createdParents[1] != "parent" || pair.Staging.Resolution != "created" || pair.Review.Resolution != "created" {
		t.Fatalf("parents=%v pair=%#v", createdParents, pair)
	}
}

func TestEnsureFoldersReconcilesRoleAfterPartialFailure(t *testing.T) {
	created := map[string]bool{}
	createCalls := map[string]int{}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		if request.Method == http.MethodGet && request.URL.Path == "/files" {
			query, _ := url.QueryUnescape(request.URL.Query().Get("q"))
			role := "review"
			if strings.Contains(query, "value='staging'") {
				role = "staging"
			}
			if created[role] {
				fmt.Fprintf(writer, `{"files":[%s]}`, folderJSON(role, role, "root", "", folderProperties("workspace", "testing", role)))
			} else {
				fmt.Fprint(writer, `{"files":[]}`)
			}
			return
		}
		if request.Method == http.MethodPost && request.URL.Path == "/files" {
			var body struct {
				Name          string            `json:"name"`
				AppProperties map[string]string `json:"appProperties"`
			}
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
				t.Errorf("decode create body: %v", err)
				return
			}
			role := body.AppProperties["mdoc_role"]
			createCalls[role]++
			if role == "review" && createCalls[role] == 1 {
				http.Error(writer, `{"error":{"code":403,"message":"denied"}}`, http.StatusForbidden)
				return
			}
			created[role] = true
			fmt.Fprint(writer, folderJSON(role, body.Name, "root", "", body.AppProperties))
			return
		}
		http.Error(writer, request.Method+" "+request.URL.String(), http.StatusNotFound)
	}))
	defer server.Close()
	service, err := drive.NewService(context.Background(), option.WithEndpoint(server.URL+"/"), option.WithoutAuthentication())
	if err != nil {
		t.Fatal(err)
	}
	client := NewDriveClient(service)
	spec := FolderSpec{WorkspaceID: "workspace", Profile: "testing", StagingName: "Staging", ReviewName: "Review"}
	if _, err := client.EnsureFolders(context.Background(), spec); err == nil {
		t.Fatal("expected the first review create to fail")
	}
	pair, err := client.EnsureFolders(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	if pair.Staging.ID != "staging" || pair.Review.ID != "review" || createCalls["staging"] != 1 || createCalls["review"] != 2 {
		t.Fatalf("pair=%#v create calls=%v", pair, createCalls)
	}
}

func TestPropertyQueryEscapesValues(t *testing.T) {
	query := propertyQuery("key", `value'with\\slashes`)
	if query != `appProperties has { key='key' and value='value\'with\\\\slashes' }` {
		t.Fatalf("query = %q", query)
	}
}

func TestListCommentsFollowsEveryPage(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests++
		writer.Header().Set("Content-Type", "application/json")
		if request.URL.Query().Get("pageToken") == "next" {
			fmt.Fprint(writer, `{"comments":[{"id":"two","resolved":false,"deleted":false}]}`)
			return
		}
		fmt.Fprint(writer, `{"nextPageToken":"next","comments":[{"id":"one","resolved":true,"deleted":false}]}`)
	}))
	defer server.Close()
	service, err := drive.NewService(context.Background(), option.WithEndpoint(server.URL+"/"), option.WithoutAuthentication())
	if err != nil {
		t.Fatal(err)
	}
	status, err := NewDriveClient(service).ListComments(context.Background(), "file")
	if err != nil {
		t.Fatal(err)
	}
	if status.Pages != 2 || status.Unresolved != 1 || requests != 2 {
		t.Fatalf("status=%#v requests=%d", status, requests)
	}
}

func TestExportDocumentEnforcesMarkdownContentContract(t *testing.T) {
	tests := []struct {
		name     string
		content  string
		wantKind ErrorKind
	}{
		{name: "success", content: "# Review\n"},
		{name: "empty", content: "", wantKind: KindEmptyExport},
		{name: "too large", content: strings.Repeat("x", int(MaxExportBytes)+1), wantKind: KindExportSize},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if request.URL.Path != "/files/doc/export" || request.URL.Query().Get("mimeType") != MarkdownMimeType {
					http.Error(writer, "unexpected export request", http.StatusBadRequest)
					return
				}
				_, _ = writer.Write([]byte(test.content))
			}))
			defer server.Close()
			service, err := drive.NewService(context.Background(), option.WithEndpoint(server.URL+"/"), option.WithoutAuthentication())
			if err != nil {
				t.Fatal(err)
			}
			content, err := NewDriveClient(service).ExportDocument(context.Background(), "doc", MarkdownMimeType)
			if test.wantKind == "" {
				if err != nil || string(content) != test.content {
					t.Fatalf("content=%q error=%v", content, err)
				}
				return
			}
			var typed *Error
			if !errors.As(err, &typed) || typed.Kind != test.wantKind {
				t.Fatalf("error = %v", err)
			}
		})
	}
}
