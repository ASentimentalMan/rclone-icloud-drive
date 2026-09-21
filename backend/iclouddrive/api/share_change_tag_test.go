package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rclone/rclone/lib/rest"
)

func TestLookupSharedShareChangeTag(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/database/1/com.apple.clouddocs/production/shared/records/lookup" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		var request struct {
			Records []struct {
				RecordName string `json:"recordName"`
			} `json:"records"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if len(request.Records) != 1 || request.Records[0].RecordName != "cloudkit.share-record" {
			t.Fatalf("unexpected lookup: %+v", request)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"records":[{"recordName":"cloudkit.share-record","recordChangeTag":"fresh-tag","zoneID":{"zoneName":"com.apple.CloudDocs","ownerRecordName":"owner"}}]}`))
	}))
	defer server.Close()
	s := NewSession()
	s.srv = rest.NewClient(server.Client())
	s.AccountInfo.Webservices = map[string]*webService{WsPhotos: {URL: server.URL}}
	d := &DriveService{icloud: &Client{Session: s}}
	got, err := d.LookupSharedShareChangeTag(context.Background(), "cloudkit.share-record", "com.apple.CloudDocs", "owner")
	if err != nil || got != "fresh-tag" {
		t.Fatalf("tag = %q, err = %v", got, err)
	}
}

func TestLookupSharedShareChangeTagFailsClosed(t *testing.T) {
	for name, body := range map[string]string{
		"missing tag":    `{"records":[{"recordName":"cloudkit.share-record","zoneID":{"zoneName":"com.apple.CloudDocs","ownerRecordName":"owner"}}]}`,
		"wrong identity": `{"records":[{"recordName":"other","recordChangeTag":"fresh-tag","zoneID":{"zoneName":"com.apple.CloudDocs","ownerRecordName":"owner"}}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			var requests int
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				_, _ = w.Write([]byte(body))
			}))
			defer server.Close()
			s := NewSession()
			s.srv = rest.NewClient(server.Client())
			s.AccountInfo.Webservices = map[string]*webService{WsPhotos: {URL: server.URL}}
			d := &DriveService{icloud: &Client{Session: s}}
			if _, err := d.LookupSharedShareChangeTag(context.Background(), "cloudkit.share-record", "com.apple.CloudDocs", "owner"); err == nil {
				t.Fatal("expected lookup error")
			}
			if requests != 1 {
				t.Fatalf("lookup requests = %d", requests)
			}
		})
	}
}

func TestLookupTagFeedsRenameWithoutChangingIdentity(t *testing.T) {
	var renameBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/database/1/com.apple.clouddocs/production/shared/records/lookup" {
			body, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(body, &renameBody)
			_, _ = w.Write([]byte(`{"items":[{"status":"OK"}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"records":[{"recordName":"cloudkit.share-record","recordChangeTag":"fresh-tag","zoneID":{"zoneName":"com.apple.CloudDocs","ownerRecordName":"owner"}}]}`))
	}))
	defer server.Close()
	s := NewSession()
	s.srv = rest.NewClient(server.Client())
	s.AccountInfo.Webservices = map[string]*webService{WsPhotos: {URL: server.URL}}
	d := &DriveService{icloud: &Client{Session: s}, endpoint: server.URL}
	tag, err := d.LookupSharedShareChangeTag(context.Background(), "cloudkit.share-record", "com.apple.CloudDocs", "owner")
	if err != nil {
		t.Fatal(err)
	}
	item := &DriveItem{Drivewsid: "FOLDER_IN_SHARED_FOLDER::com.apple.CloudDocs::doc", Etag: "fresh-etag"}
	item.ShareID.ShareName = "share"
	item.ShareID.RecordName = "cloudkit.share-record"
	item.ShareID.ZoneID.ZoneName = "com.apple.CloudDocs"
	item.ShareID.ZoneID.OwnerRecordName = "owner"
	item.ShareID.ShareChangeTag = tag
	if _, _, err := d.RenameSharedItem(context.Background(), item, "renamed"); err != nil {
		t.Fatal(err)
	}
	items, ok := renameBody["items"].([]any)
	if !ok || len(items) != 1 {
		t.Fatalf("unexpected rename body: %#v", renameBody)
	}
	requestItem := items[0].(map[string]any)
	if requestItem["drivewsid"] != item.Drivewsid || requestItem["etag"] != item.Etag || requestItem["name"] != "renamed" {
		t.Fatalf("identity changed: %#v", requestItem)
	}
	share := requestItem["shareID"].(map[string]any)
	if share["shareChangeTag"] != tag {
		t.Fatalf("tag = %#v, want lookup tag", share["shareChangeTag"])
	}
}
