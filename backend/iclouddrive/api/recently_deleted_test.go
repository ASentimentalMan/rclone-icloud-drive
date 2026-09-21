package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/rclone/rclone/lib/rest"
)

func newRecentlyDeletedTestService(t *testing.T, handler http.HandlerFunc) *DriveService {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	session := NewSession()
	session.srv = rest.NewClient(server.Client())
	return &DriveService{icloud: &Client{Session: session}, endpoint: server.URL, docsEndpoint: server.URL}
}

func TestBuildRecentlyDeletedRequestCaptureSchemas(t *testing.T) {
	items := []*DriveItem{
		{Drivewsid: "FILE::com.apple.CloudDocs::file", Etag: "file-etag"},
		{Drivewsid: "FOLDER::com.apple.CloudDocs::folder", Etag: "folder-etag"},
	}
	deleted, err := buildRecentlyDeletedRequest(items, true)
	if err != nil {
		t.Fatal(err)
	}
	deletedItems := deleted["items"].([]map[string]any)
	if len(deletedItems) != 2 || deletedItems[0]["clientId"] != items[0].Drivewsid || deletedItems[1]["clientId"] != items[1].Drivewsid {
		t.Fatalf("permanent-delete request = %#v", deleted)
	}
	recovered, err := buildRecentlyDeletedRequest(items, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range recovered["items"].([]map[string]any) {
		if _, ok := item["clientId"]; ok {
			t.Fatalf("recover request unexpectedly contains clientId: %#v", recovered)
		}
	}
}

func TestBuildRecentlyDeletedRequestFailsClosed(t *testing.T) {
	for _, test := range []struct {
		name  string
		items []*DriveItem
	}{
		{name: "empty"},
		{name: "missing etag", items: []*DriveItem{{Drivewsid: "FILE::com.apple.CloudDocs::file"}}},
		{name: "Shared identity", items: []*DriveItem{{Drivewsid: "FILE_IN_SHARED_FOLDER::com.apple.CloudDocs::file", Etag: "etag"}}},
		{name: "duplicate", items: []*DriveItem{{Drivewsid: "FILE::com.apple.CloudDocs::file", Etag: "etag"}, {Drivewsid: "FILE::com.apple.CloudDocs::file", Etag: "etag"}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := buildRecentlyDeletedRequest(test.items, true); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestRecentlyDeletedMutationTransportMatchesCapture(t *testing.T) {
	for _, test := range []struct {
		name            string
		path            string
		includeClientID bool
		call            func(context.Context, *DriveService, []*DriveItem) (*http.Response, error)
	}{
		{name: "permanent delete", path: "/deleteItems", includeClientID: true, call: func(ctx context.Context, d *DriveService, items []*DriveItem) (*http.Response, error) {
			return d.PermanentlyDeleteItems(ctx, items)
		}},
		{name: "recover", path: "/putBackItemsFromTrash", call: func(ctx context.Context, d *DriveService, items []*DriveItem) (*http.Response, error) {
			return d.RecoverItems(ctx, items)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			var count int32
			var body []byte
			d := newRecentlyDeletedTestService(t, func(w http.ResponseWriter, r *http.Request) {
				atomic.AddInt32(&count, 1)
				if r.Method != http.MethodPost || r.URL.Path != test.path {
					http.Error(w, "wrong request", http.StatusBadRequest)
					return
				}
				body, _ = io.ReadAll(r.Body)
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"items":[]}`))
			})
			items := []*DriveItem{{Drivewsid: "FILE::com.apple.CloudDocs::file", Etag: "etag"}, {Drivewsid: "FOLDER::com.apple.CloudDocs::folder", Etag: "folder-etag"}}
			if _, err := test.call(context.Background(), d, items); err != nil {
				t.Fatal(err)
			}
			if atomic.LoadInt32(&count) != 1 {
				t.Fatalf("request count = %d, want 1", count)
			}
			var request struct {
				Items []map[string]any `json:"items"`
			}
			if err := json.Unmarshal(body, &request); err != nil {
				t.Fatal(err)
			}
			if len(request.Items) != 2 {
				t.Fatalf("items = %d, want 2", len(request.Items))
			}
			for i, item := range request.Items {
				_, hasClientID := item["clientId"]
				if hasClientID != test.includeClientID {
					t.Fatalf("item %d clientId presence = %v, want %v", i, hasClientID, test.includeClientID)
				}
			}
		})
	}
}

func TestRecentlyDeletedMutationDoesNotRetry(t *testing.T) {
	var count int32
	d := newRecentlyDeletedTestService(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&count, 1)
		http.Error(w, "expired", http.StatusUnauthorized)
	})
	_, err := d.PermanentlyDeleteItems(context.Background(), []*DriveItem{{Drivewsid: "FILE::com.apple.CloudDocs::file", Etag: "etag"}})
	if err == nil {
		t.Fatal("expected HTTP error")
	}
	if atomic.LoadInt32(&count) != 1 {
		t.Fatalf("request count = %d, want exactly one", count)
	}
}

func TestGetRecentlyDeletedUsesCapturedSupportingRead(t *testing.T) {
	d := newRecentlyDeletedTestService(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/retrieveItemDetailsInFolders" {
			http.Error(w, "wrong request", http.StatusBadRequest)
			return
		}
		var request []struct {
			Drivewsid        string `json:"drivewsid"`
			PartialData      bool   `json:"partialData"`
			IncludeHierarchy bool   `json:"includeHierarchy"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		if len(request) != 1 || request[0].Drivewsid != "TRASH_ROOT" || request[0].PartialData || !request[0].IncludeHierarchy {
			t.Errorf("request = %#v", request)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"drivewsid":"TRASH_ROOT","items":[{"drivewsid":"FILE::com.apple.CloudDocs::file","etag":"etag","name":"file","extension":"txt","type":"FILE"}]}]`))
	})
	items, _, err := d.GetRecentlyDeleted(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Drivewsid != "FILE::com.apple.CloudDocs::file" {
		t.Fatalf("items = %#v", items)
	}
}

func TestGetRecentlyDeletedRejectsEmptyResponse(t *testing.T) {
	d := newRecentlyDeletedTestService(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[]`))
	})
	if _, _, err := d.GetRecentlyDeleted(context.Background()); err == nil {
		t.Fatal("empty Recently Deleted response was accepted")
	}
}

func TestPersonalFileCopyTransportMatchesCaptureAndDoesNotRetry(t *testing.T) {
	var count int32
	var body []byte
	d := newRecentlyDeletedTestService(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&count, 1)
		if r.URL.Path != "/v1/item/copy/personal-item-id" {
			http.Error(w, "wrong request", http.StatusBadRequest)
			return
		}
		body, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"item_id":"new-item","item_info":{"drivewsid":"FILE::com.apple.CloudDocs::new-file"}}`))
	})
	info, _, err := d.CopyDocByItemID(context.Background(), "personal-item-id")
	if err != nil {
		t.Fatal(err)
	}
	if info.ItemID != "new-item" || string(body) != `{"info_to_update":{}}` {
		t.Fatalf("copy info/body = %#v %s", info, body)
	}
	if atomic.LoadInt32(&count) != 1 {
		t.Fatalf("request count = %d, want 1", count)
	}
}
