package iclouddrive

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"unsafe"

	"github.com/rclone/rclone/backend/iclouddrive/api"
	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/lib/pacer"
	"github.com/rclone/rclone/lib/rest"
)

func TestObjectOpenUsesSharedMetadataDownloadURL(t *testing.T) {
	const content = "shared-content"
	var byIDRequests, contentRequests int
	var serverURL string
	service, serverURL, closeServer := newOpenTestService(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ws/com.apple.CloudDocs/download/by_id":
			byIDRequests++
			http.Error(w, "Shared download-by-id must not be used", http.StatusInternalServerError)
		case "/v1/download/shared-token":
			contentRequests++
			_, _ = w.Write([]byte(content))
		default:
			http.NotFound(w, r)
		}
	})
	defer closeServer()

	o := &Object{
		fs: openTestFs(service),
	}
	item := &api.DriveItem{
		Drivewsid: "FILE_IN_SHARED_FOLDER::com.apple.CloudDocs::shared-doc",
		Type:      "FILE",
		Size:      int64(len(content)),
	}
	item.Urls.URLDownload = serverURL + "/v1/download/shared-token"
	if err := o.setMetaData(item); err != nil {
		t.Fatal(err)
	}

	got, err := o.Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer got.Close()
	body, err := io.ReadAll(got)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != content {
		t.Fatalf("content = %q, want %q", body, content)
	}
	if byIDRequests != 0 {
		t.Fatalf("download-by-id requests = %d, want 0", byIDRequests)
	}
	if contentRequests != 1 {
		t.Fatalf("content requests = %d, want 1", contentRequests)
	}
}

func TestObjectOpenUsesPersonalDownloadByIDFallback(t *testing.T) {
	const content = "personal-content"
	var byIDRequests, contentRequests int
	var serverURL string
	service, serverURL, closeServer := newOpenTestService(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ws/com.apple.CloudDocs/download/by_id":
			byIDRequests++
			if got := r.URL.Query().Get("document_id"); got != "personal-doc" {
				t.Errorf("document_id = %q, want personal-doc", got)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data_token": map[string]string{"url": serverURL + "/v1/download/personal-token"},
			})
		case "/v1/download/personal-token":
			contentRequests++
			_, _ = w.Write([]byte(content))
		default:
			http.NotFound(w, r)
		}
	})
	defer closeServer()

	o := &Object{
		fs:      openTestFs(service),
		size:    int64(len(content)),
		driveID: "FILE::com.apple.CloudDocs::personal-doc",
	}
	got, err := o.Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer got.Close()
	body, err := io.ReadAll(got)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != content {
		t.Fatalf("content = %q, want %q", body, content)
	}
	if byIDRequests != 1 {
		t.Fatalf("download-by-id requests = %d, want 1", byIDRequests)
	}
	if contentRequests != 1 {
		t.Fatalf("content requests = %d, want 1", contentRequests)
	}
}

func TestObjectOpenPreservesDownloadURLAcquisitionError(t *testing.T) {
	var byIDRequests, contentRequests int
	service, _, closeServer := newOpenTestService(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ws/com.apple.CloudDocs/download/by_id":
			byIDRequests++
			http.Error(w, "download object not found", http.StatusNotFound)
		case "/v1/download/personal-token":
			contentRequests++
			_, _ = w.Write([]byte("must not be reached"))
		default:
			http.NotFound(w, r)
		}
	})
	defer closeServer()

	o := &Object{
		fs:      openTestFs(service),
		size:    1,
		driveID: "FILE::com.apple.CloudDocs::missing-doc",
	}
	_, err := o.Open(context.Background())
	if err == nil {
		t.Fatal("Open returned nil error")
	}
	if !strings.Contains(err.Error(), "HTTP error 404") {
		t.Fatalf("error = %v, want original HTTP 404", err)
	}
	if strings.Contains(err.Error(), "RootURL not set") {
		t.Fatalf("error was masked by empty-URL download: %v", err)
	}
	if byIDRequests != 1 {
		t.Fatalf("download-by-id requests = %d, want 1", byIDRequests)
	}
	if contentRequests != 0 {
		t.Fatalf("content requests = %d, want 0", contentRequests)
	}
}

func TestObjectOpenNeverDownloadsAfterURLAcquisitionFailure(t *testing.T) {
	var requestPaths []string
	service, _, closeServer := newOpenTestService(t, func(w http.ResponseWriter, r *http.Request) {
		requestPaths = append(requestPaths, r.URL.Path)
		if r.URL.Path == "/ws/com.apple.CloudDocs/download/by_id" {
			http.Error(w, "download object not found", http.StatusNotFound)
			return
		}
		http.Error(w, "unexpected content request", http.StatusInternalServerError)
	})
	defer closeServer()

	o := &Object{
		fs:      openTestFs(service),
		size:    1,
		driveID: "FILE::com.apple.CloudDocs::missing-doc",
	}
	if _, err := o.Open(context.Background()); err == nil {
		t.Fatal("Open returned nil error")
	}
	if len(requestPaths) != 1 || requestPaths[0] != "/ws/com.apple.CloudDocs/download/by_id" {
		t.Fatalf("request paths = %#v, want only download-by-id", requestPaths)
	}
}

func newOpenTestService(t *testing.T, handler http.HandlerFunc) (*api.DriveService, string, func()) {
	t.Helper()
	server := httptest.NewServer(handler)
	session := api.NewSession()
	setOpenTestSessionServer(session, rest.NewClient(server.Client()).SetRoot(server.URL))
	accountInfo := map[string]any{"webservices": map[string]map[string]string{
		api.WsDrive: {"url": server.URL},
		api.WsDocs:  {"url": server.URL},
	}}
	encoded, err := json.Marshal(accountInfo)
	if err != nil {
		server.Close()
		t.Fatal(err)
	}
	if err := json.Unmarshal(encoded, &session.AccountInfo); err != nil {
		server.Close()
		t.Fatal(err)
	}
	client, err := api.New("", "", "", "", nil, nil, "open-test", "")
	if err != nil {
		server.Close()
		t.Fatal(err)
	}
	client.Session = session
	setOpenTestClientServer(client, rest.NewClient(server.Client()).SetRoot(server.URL))
	service, err := client.DriveService()
	if err != nil {
		server.Close()
		t.Fatal(err)
	}
	return service, server.URL, server.Close
}

func openTestFs(service *api.DriveService) *Fs {
	return &Fs{
		service: service,
		pacer:   fs.NewPacer(context.Background(), pacer.NewDefault()),
	}
}

func setOpenTestSessionServer(session *api.Session, client *rest.Client) {
	field := reflect.ValueOf(session).Elem().FieldByName("srv")
	reflect.NewAt(field.Type(), unsafe.Pointer(field.UnsafeAddr())).Elem().Set(reflect.ValueOf(client))
}

func setOpenTestClientServer(client *api.Client, restClient *rest.Client) {
	field := reflect.ValueOf(client).Elem().FieldByName("srv")
	reflect.NewAt(field.Type(), unsafe.Pointer(field.UnsafeAddr())).Elem().Set(reflect.ValueOf(restClient))
}
