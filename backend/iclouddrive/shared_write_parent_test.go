package iclouddrive

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rclone/rclone/backend/iclouddrive/api"
	"github.com/rclone/rclone/lib/rest"
)

type sharedWriteParentTestState struct {
	hierarchyReads int
	documentReads  int
	createRequests int
	createParent   string
	failDocument   bool
}

func newSharedWriteParentTestService(t *testing.T, sharedRoot *api.DriveItem, state *sharedWriteParentTestState) (*api.DriveService, func()) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/retrieveItemDetailsInFolders":
			state.hierarchyReads++
			_ = json.NewEncoder(w).Encode([]*api.DriveItem{{
				Drivewsid: "FOLDER::com.apple.CloudDocs::root",
				Items:     []*api.DriveItem{sharedRoot},
			}})
		case "/ws/com.apple.CloudDocs/list/lookup_by_id":
			state.documentReads++
			if state.failDocument {
				http.Error(w, "document lookup unavailable", http.StatusInternalServerError)
				return
			}
			_ = json.NewEncoder(w).Encode(&api.Document{
				DocumentID: "destination-doc",
				ItemID:     "destination-item",
				Etag:       "destination-etag",
				ParentID:   sharedRoot.Drivewsid,
				Name:       "parent-a",
				Type:       "FOLDER",
				Zone:       "com.apple.CloudDocs",
			})
		case "/createFolders":
			state.createRequests++
			var request struct {
				DestinationDrivewsID string `json:"destinationDrivewsId"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Errorf("decode createFolders request: %v", err)
				return
			}
			state.createParent = request.DestinationDrivewsID
			_, _ = w.Write([]byte(`{"folders":[{"status":"OK","type":"FOLDER","drivewsid":"FOLDER_IN_SHARED_FOLDER::com.apple.CloudDocs::created-doc","docwsid":"created-doc","item_id":"created-item","etag":"created-etag"}]}`))
		default:
			http.NotFound(w, r)
		}
	}))

	session := api.NewSession()
	setSharedResolverSessionServer(session, rest.NewClient(server.Client()).SetRoot(server.URL))
	accountInfo := map[string]any{"webservices": map[string]map[string]string{
		api.WsDrive: {"url": server.URL}, api.WsDocs: {"url": server.URL}, api.WsPhotos: {"url": server.URL},
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
	client, err := api.New("", "", "", "", nil, nil, "shared-write-parent-test", "")
	if err != nil {
		server.Close()
		t.Fatal(err)
	}
	client.Session = session
	service, err := client.DriveService()
	if err != nil {
		server.Close()
		t.Fatal(err)
	}
	return service, server.Close
}

func sharedWriteParentFixture() (*api.DriveItem, *api.DriveItem) {
	sharedRoot := &api.DriveItem{
		Itemid:    "shared-root-item",
		Drivewsid: "SHARED_FOLDER::com.apple.CloudDocs::shared-root-doc",
		Docwsid:   "shared-root-doc",
		Type:      "FOLDER",
	}
	sharedRoot.ShareID.ShareName = "share"
	sharedRoot.ShareID.RecordName = "share-record"
	sharedRoot.ShareID.ShareChangeTag = "share-change"
	sharedRoot.ShareID.ZoneID.ZoneName = "com.apple.CloudDocs"
	sharedRoot.ShareID.ZoneID.OwnerRecordName = "owner"
	partial := &api.DriveItem{
		Itemid:   "destination-item",
		Name:     "parent-a",
		Type:     "FOLDER",
		ParentID: sharedRoot.Drivewsid,
		ShareID:  sharedRoot.ShareID,
	}
	sharedRoot.Items = []*api.DriveItem{partial}
	return sharedRoot, partial
}

func sharedWriteParentFs(service *api.DriveService, partial *api.DriveItem) *Fs {
	return &Fs{
		rootID:      "FOLDER::com.apple.CloudDocs::root",
		service:     service,
		sharedItems: map[string]*api.DriveItem{"SHARED::destination-item": partial},
	}
}

func TestSharedWriteParentResolvesIncompleteCachedIdentity(t *testing.T) {
	sharedRoot, partial := sharedWriteParentFixture()
	state := &sharedWriteParentTestState{}
	service, closeServer := newSharedWriteParentTestService(t, sharedRoot, state)
	defer closeServer()
	f := sharedWriteParentFs(service, partial)

	got, err := f.resolveSharedWriteParent(context.Background(), "SHARED::destination-item")
	if err != nil {
		t.Fatalf("resolveSharedWriteParent returned error: %v", err)
	}
	if got.Drivewsid != "FOLDER_IN_SHARED_FOLDER::com.apple.CloudDocs::destination-doc" || got.Docwsid != "destination-doc" {
		t.Fatalf("resolved writable identity = %#v", got)
	}
	if got.ShareID.RecordName != "share-record" || got.ShareID.ZoneID.OwnerRecordName != "owner" {
		t.Fatalf("resolved share context = %#v", got.ShareID)
	}
	if state.hierarchyReads != 1 || state.documentReads != 1 {
		t.Fatalf("canonical lookup reads = hierarchy %d, document %d; want one each", state.hierarchyReads, state.documentReads)
	}
	if partial.Drivewsid != "" || partial.Docwsid != "" {
		t.Fatalf("incomplete listing cache item was mutated: %#v", partial)
	}
}

func TestSharedNestedMkdirUsesResolvedWritableParent(t *testing.T) {
	sharedRoot, partial := sharedWriteParentFixture()
	state := &sharedWriteParentTestState{}
	service, closeServer := newSharedWriteParentTestService(t, sharedRoot, state)
	defer closeServer()
	f := sharedWriteParentFs(service, partial)

	got, err := f.CreateDir(context.Background(), "SHARED::destination-item#stale-etag", "child")
	if err != nil {
		t.Fatalf("CreateDir returned error: %v", err)
	}
	if got != "FOLDER_IN_SHARED_FOLDER::com.apple.CloudDocs::created-doc#created-etag" {
		t.Fatalf("created folder identity = %q", got)
	}
	if state.createRequests != 1 || state.createParent != "FOLDER_IN_SHARED_FOLDER::com.apple.CloudDocs::destination-doc" {
		t.Fatalf("createFolders parent = %q in %d requests", state.createParent, state.createRequests)
	}
}

func TestSharedNestedUploadUsesResolvedWritableParent(t *testing.T) {
	sharedRoot, partial := sharedWriteParentFixture()
	state := &sharedWriteParentTestState{}
	service, closeServer := newSharedWriteParentTestService(t, sharedRoot, state)
	defer closeServer()
	f := sharedWriteParentFs(service, partial)

	parent, startingDocumentID, err := f.resolveSharedUploadParent(context.Background(), "SHARED::destination-item")
	if err != nil {
		t.Fatalf("resolveSharedUploadParent returned error: %v", err)
	}
	if parent.Drivewsid != "FOLDER_IN_SHARED_FOLDER::com.apple.CloudDocs::destination-doc" || parent.Docwsid != "destination-doc" {
		t.Fatalf("upload parent identity = %#v", parent)
	}
	if startingDocumentID != "destination-doc" {
		t.Fatalf("upload starting document ID = %q", startingDocumentID)
	}
	if parent.ShareID.RecordName != "share-record" || parent.ShareID.ZoneID.OwnerRecordName != "owner" {
		t.Fatalf("upload parent share context = %#v", parent.ShareID)
	}
}

func TestSharedWriteParentFailsWhenCanonicalLookupFails(t *testing.T) {
	sharedRoot, partial := sharedWriteParentFixture()
	partial.Drivewsid = "FOLDER_IN_SHARED_FOLDER::com.apple.CloudDocs::stale-parent"
	state := &sharedWriteParentTestState{failDocument: true}
	service, closeServer := newSharedWriteParentTestService(t, sharedRoot, state)
	defer closeServer()
	f := sharedWriteParentFs(service, partial)

	got, err := f.resolveSharedWriteParent(context.Background(), "SHARED::destination-item")
	if err == nil {
		t.Fatalf("resolveSharedWriteParent returned cached identity %#v without canonical lookup", got)
	}
	if err.Error() != "Shared write parent canonical identity unavailable: Shared directory canonical metadata unavailable" {
		t.Fatalf("resolver error = %q", err)
	}
	if got != nil || state.hierarchyReads != 1 || state.documentReads != 1 {
		t.Fatalf("resolver result = %#v; reads hierarchy %d, document %d", got, state.hierarchyReads, state.documentReads)
	}
}
