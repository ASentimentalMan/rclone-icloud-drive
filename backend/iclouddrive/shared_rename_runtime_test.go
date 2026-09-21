package iclouddrive

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"unsafe"

	"github.com/rclone/rclone/backend/iclouddrive/api"
	"github.com/rclone/rclone/lib/rest"
)

func TestSharedFileMoveDestinationResolutionUsesCanonicalFolderIdentity(t *testing.T) {
	sharedRoot := &api.DriveItem{Itemid: "shared-root-item", Drivewsid: "SHARED_FOLDER::com.apple.CloudDocs::shared-root-doc", Type: "FOLDER"}
	sharedRoot.ShareID.ShareName = "share"
	sharedRoot.ShareID.RecordName = "record"
	sharedRoot.ShareID.ShareChangeTag = "change"
	sharedRoot.ShareID.ZoneID.ZoneName = "com.apple.CloudDocs"
	sharedRoot.ShareID.ZoneID.OwnerRecordName = "owner"
	// This models a Shared file at the root moving into an existing nested
	// folder whose hierarchy entry is a valid folder but lacks canonical
	// DriveWS metadata. The authenticated document lookup is the only source
	// allowed to supply the destination write identity.
	sharedRoot.Items = []*api.DriveItem{{
		Itemid:   "destination-item",
		Name:     "parent-a",
		Type:     "FOLDER",
		ParentID: sharedRoot.Drivewsid,
	}}

	service, closeServer := newSharedDestinationResolverTestService(t, sharedRoot)
	defer closeServer()
	f := &Fs{
		rootID:      "FOLDER::com.apple.CloudDocs::root",
		service:     service,
		sharedItems: map[string]*api.DriveItem{},
	}

	got, err := f.resolveSharedMoveItem(context.Background(), "SHARED::destination-item#stale")
	if err != nil {
		t.Fatalf("resolveSharedMoveItem returned error: %v", err)
	}
	if got.Drivewsid != "FOLDER_IN_SHARED_FOLDER::com.apple.CloudDocs::destination-doc" {
		t.Fatalf("destination DriveWS identity = %q, want authenticated folder identity", got.Drivewsid)
	}
	if got.Docwsid != "destination-doc" || got.Etag != "destination-etag" || got.ParentID != sharedRoot.Drivewsid {
		t.Fatalf("destination canonical metadata = %#v", got)
	}
	if got.ShareID.RecordName != sharedRoot.ShareID.RecordName || got.ShareID.ZoneID.OwnerRecordName != sharedRoot.ShareID.ZoneID.OwnerRecordName {
		t.Fatalf("destination ShareID = %#v, want authenticated Shared root context", got.ShareID)
	}
}

func TestSharedFileReparentRootedFsRoutesNeverUseMoveItems(t *testing.T) {
	for _, test := range []struct {
		name           string
		sourceID       string
		sourceParentID string
		destinationID  string
		sourceFolder   bool
	}{
		{
			name:           "Shared root to first-level folder",
			sourceID:       "FILE_IN_SHARED_FOLDER::com.apple.CloudDocs::source-doc",
			sourceParentID: "SHARED::shared-root-item",
			destinationID:  "SHARED::destination-item",
		},
		{
			name:           "first-level folder to second-level folder",
			sourceID:       "FILE::com.apple.CloudDocs::source-doc",
			sourceParentID: "SHARED::source-parent-item",
			destinationID:  "SHARED::destination-item",
		},
		{
			name:           "rooted Shared folder to second-level folder",
			sourceID:       "FOLDER::com.apple.CloudDocs::source-doc",
			sourceParentID: "SHARED::source-parent-item",
			destinationID:  "SHARED::destination-item",
			sourceFolder:   true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			service, sourceParent, closeServer := newSharedRouteTestService(t, test.sourceFolder)
			defer closeServer()
			f := &Fs{rootID: "FOLDER::com.apple.CloudDocs::root", service: service, sharedItems: map[string]*api.DriveItem{}}

			got, err := f.move(context.Background(), test.sourceID, "source-item", test.sourceParentID, "source.txt", "enumerated-etag", test.destinationID, "source.txt", f, sourceParent)
			if err != nil {
				t.Fatalf("Shared file reparent route failed: %v", err)
			}
			if got == nil || got.ParentID != "FOLDER_IN_SHARED_FOLDER::com.apple.CloudDocs::destination-doc" {
				t.Fatalf("reparent result = %#v", got)
			}
		})
	}
}

func newSharedRouteTestService(t *testing.T, sourceFolder bool) (*api.DriveService, *api.DriveItem, func()) {
	t.Helper()
	sharedRoot := &api.DriveItem{Itemid: "shared-root-item", Drivewsid: "SHARED_FOLDER::com.apple.CloudDocs::shared-root-doc", Docwsid: "shared-root-doc", Type: "FOLDER"}
	sharedRoot.ShareID.ShareName = "share"
	sharedRoot.ShareID.RecordName = "share-record"
	sharedRoot.ShareID.ShareChangeTag = "share-change"
	sharedRoot.ShareID.ZoneID.ZoneName = "com.apple.CloudDocs"
	sharedRoot.ShareID.ZoneID.OwnerRecordName = "owner"
	sourceParent := &api.DriveItem{Itemid: "source-parent-item", Drivewsid: "FOLDER_IN_SHARED_FOLDER::com.apple.CloudDocs::source-parent-doc", Docwsid: "source-parent-doc", Type: "FOLDER", ShareID: sharedRoot.ShareID}
	sourceType := "FILE"
	sourceDriveID := "FILE_IN_SHARED_FOLDER::com.apple.CloudDocs::source-doc"
	sourceRecord := "documentStructure/source-doc"
	if sourceFolder {
		sourceType = "FOLDER"
		sourceDriveID = "FOLDER_IN_SHARED_FOLDER::com.apple.CloudDocs::source-doc"
		sourceRecord = "directory/SOURCE-DOC"
	}
	source := &api.DriveItem{Itemid: "source-item", Drivewsid: sourceDriveID, Docwsid: "source-doc", ParentID: sourceParent.Drivewsid, Etag: "source-etag", Name: "source.txt", Type: sourceType, ShareID: sharedRoot.ShareID}
	destination := &api.DriveItem{Itemid: "destination-item", Drivewsid: "FOLDER_IN_SHARED_FOLDER::com.apple.CloudDocs::destination-doc", Docwsid: "destination-doc", ParentID: sourceParent.Drivewsid, Etag: "destination-etag", Type: "FOLDER", ShareID: sharedRoot.ShareID}
	sharedRoot.Items = []*api.DriveItem{sourceParent, source, destination}

	cloudKitLookupCount := 0
	cloudKitModifyCount := 0
	moveItemsCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/retrieveItemDetailsInFolders":
			_ = json.NewEncoder(w).Encode([]*api.DriveItem{{Drivewsid: "FOLDER::com.apple.CloudDocs::root", Items: []*api.DriveItem{sharedRoot}}})
		case "/database/1/com.apple.clouddocs/production/shared/records/lookup":
			cloudKitLookupCount++
			var request struct {
				Records []struct {
					RecordName string `json:"recordName"`
				} `json:"records"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Errorf("decode lookup request: %v", err)
				return
			}
			if len(request.Records) != 1 {
				t.Errorf("lookup records = %d, want 1", len(request.Records))
				return
			}
			if request.Records[0].RecordName == "share-record" {
				_, _ = w.Write([]byte(`{"records":[{"recordName":"share-record","recordChangeTag":"fresh-share-change","zoneID":{"zoneName":"com.apple.CloudDocs","ownerRecordName":"owner"}}]}`))
				return
			}
			if request.Records[0].RecordName != sourceRecord {
				t.Errorf("CloudKit source record = %q", request.Records[0].RecordName)
				return
			}
			_, _ = w.Write([]byte(fmt.Sprintf(`{"records":[{"recordName":%q,"recordChangeTag":"source-change","fields":{}}]}`, sourceRecord)))
		case "/database/1/com.apple.clouddocs/production/shared/records/modify":
			cloudKitModifyCount++
			_, _ = w.Write([]byte(`{"records":[{"recordName":"documentStructure/source-doc","recordChangeTag":"updated"}]}`))
		case "/moveItems":
			moveItemsCount++
			http.Error(w, "generic moveItems must not be selected", http.StatusInternalServerError)
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
	client, err := api.New("", "", "", "", nil, nil, "shared-route-test", "")
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
	return service, sourceParent, func() {
		server.Close()
		if moveItemsCount != 0 {
			t.Errorf("generic /moveItems submissions = %d, want 0", moveItemsCount)
		}
		if cloudKitLookupCount != 2 {
			t.Errorf("CloudKit lookup submissions = %d, want 2", cloudKitLookupCount)
		}
		if cloudKitModifyCount != 1 {
			t.Errorf("CloudKit modify submissions = %d, want 1", cloudKitModifyCount)
		}
	}
}

func newSharedDestinationResolverTestService(t *testing.T, sharedRoot *api.DriveItem) (*api.DriveService, func()) {
	t.Helper()
	hierarchyReads := 0
	documentReads := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/retrieveItemDetailsInFolders":
			hierarchyReads++
			_ = json.NewEncoder(w).Encode([]*api.DriveItem{{
				Drivewsid: "FOLDER::com.apple.CloudDocs::root",
				Items:     []*api.DriveItem{sharedRoot},
			}})
		case "/ws/com.apple.CloudDocs/list/lookup_by_id":
			documentReads++
			_ = json.NewEncoder(w).Encode(&api.Document{
				DocumentID: "destination-doc",
				ItemID:     "destination-item",
				Etag:       "destination-etag",
				ParentID:   sharedRoot.Drivewsid,
				Name:       "parent-a",
				Type:       "FOLDER",
				Zone:       "com.apple.CloudDocs",
			})
		default:
			http.NotFound(w, r)
		}
	}))

	session := api.NewSession()
	setSharedResolverSessionServer(session, rest.NewClient(server.Client()).SetRoot(server.URL))
	accountInfo := map[string]any{
		"webservices": map[string]map[string]string{
			api.WsDrive:  {"url": server.URL},
			api.WsDocs:   {"url": server.URL},
			api.WsPhotos: {"url": server.URL},
		},
	}
	encoded, err := json.Marshal(accountInfo)
	if err != nil {
		server.Close()
		t.Fatal(err)
	}
	if err := json.Unmarshal(encoded, &session.AccountInfo); err != nil {
		server.Close()
		t.Fatal(err)
	}
	client, err := api.New("", "", "", "", nil, nil, "shared-destination-test", "")
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
	return service, func() {
		server.Close()
		if hierarchyReads != 1 {
			t.Errorf("authenticated hierarchy reads = %d, want 1", hierarchyReads)
		}
		if documentReads != 1 {
			t.Errorf("authenticated document reads = %d, want 1", documentReads)
		}
	}
}

func setSharedResolverSessionServer(session *api.Session, client *rest.Client) {
	field := reflect.ValueOf(session).Elem().FieldByName("srv")
	reflect.NewAt(field.Type(), unsafe.Pointer(field.UnsafeAddr())).Elem().Set(reflect.ValueOf(client))
}

func TestCanonicalSharedFileFromAuthenticatedDocument(t *testing.T) {
	root := &api.DriveItem{Drivewsid: "SHARED_FOLDER::com.apple.CloudDocs::root"}
	root.ShareID.ShareName = "share"
	root.ShareID.RecordName = "record"
	root.ShareID.ZoneID.ZoneName = "com.apple.CloudDocs"
	root.ShareID.ZoneID.OwnerRecordName = "owner"
	doc := &api.Document{
		DocumentID: "doc-from-read",
		ItemID:     "item-from-read",
		Etag:       "fresh-etag",
		ParentID:   "FOLDER_IN_SHARED_FOLDER::com.apple.CloudDocs::parent",
		Name:       "uploaded.txt",
		Type:       "FILE",
		Size:       31,
	}
	got := canonicalSharedItemFromDocument(doc, "item-from-read", root)
	if got == nil {
		t.Fatal("expected canonical Shared file")
	}
	if got.Drivewsid != "FILE_IN_SHARED_FOLDER::com.apple.CloudDocs::doc-from-read" || got.Docwsid != "doc-from-read" || got.Etag != "fresh-etag" || got.ParentID != doc.ParentID || got.Size != 31 {
		t.Fatalf("canonical file = %#v", got)
	}
	if got.ShareID.RecordName != "record" || got.ShareID.ZoneID.OwnerRecordName != "owner" {
		t.Fatalf("ShareID was not inherited from authenticated Shared root: %#v", got.ShareID)
	}
}

func TestCanonicalSharedNestedFolderFromAuthenticatedDocument(t *testing.T) {
	root := &api.DriveItem{Drivewsid: "SHARED_FOLDER::com.apple.CloudDocs::root-doc", Type: "FOLDER"}
	root.ShareID.ShareName = "share"
	root.ShareID.RecordName = "record"
	root.ShareID.ZoneID.ZoneName = "com.apple.CloudDocs"
	root.ShareID.ZoneID.OwnerRecordName = "owner"
	parentA := &api.DriveItem{
		Itemid:    "parent-a-item",
		Drivewsid: "FOLDER_IN_SHARED_FOLDER::com.apple.CloudDocs::parent-a-doc",
		Docwsid:   "parent-a-doc",
		Type:      "FOLDER",
	}
	parentB := &api.DriveItem{
		Itemid:    "parent-b-item",
		Drivewsid: "FOLDER_IN_SHARED_FOLDER::com.apple.CloudDocs::parent-b-doc",
		Docwsid:   "parent-b-doc",
		Type:      "FOLDER",
	}
	root.Items = []*api.DriveItem{parentA, parentB}

	// The Shared-root hierarchy is deliberately non-recursive: the source is
	// not embedded under parent-a. Its authenticated document lookup is the
	// identity bridge used by the runtime resolver.
	if got := findSharedCanonicalHierarchyItem(root, "source-folder-item"); got != nil {
		t.Fatalf("root hierarchy unexpectedly exposed nested source: %#v", got)
	}
	doc := &api.Document{
		DocumentID: "source-folder-doc",
		ItemID:     "source-folder-item",
		Etag:       "fresh-folder-etag",
		ParentID:   parentA.Drivewsid,
		Name:       "source-folder",
		Type:       "FOLDER",
	}
	got := canonicalSharedItemFromDocument(doc, "source-folder-item", root)
	if got == nil {
		t.Fatal("authenticated folder document was not canonicalized")
	}
	if got.Drivewsid != "FOLDER_IN_SHARED_FOLDER::com.apple.CloudDocs::source-folder-doc" || got.Docwsid != "source-folder-doc" {
		t.Fatalf("canonical folder identity = %#v", got)
	}
	if got.ParentID != parentA.Drivewsid || got.Etag != "fresh-folder-etag" || !got.IsFolder() {
		t.Fatalf("canonical folder metadata = %#v", got)
	}
	if !hasSharedShareIdentity(got) {
		t.Fatalf("source-side share identity not inherited: %#v", got.ShareID)
	}
}

func TestSharedFolderMoveLayoutsUseCanonicalSource(t *testing.T) {
	sharedRoot := &api.DriveItem{Drivewsid: "SHARED_FOLDER::com.apple.CloudDocs::root-doc", Type: "FOLDER"}
	sharedRoot.ShareID.ShareName = "share"
	sharedRoot.ShareID.RecordName = "record"
	sharedRoot.ShareID.ZoneID.ZoneName = "com.apple.CloudDocs"
	sharedRoot.ShareID.ZoneID.OwnerRecordName = "owner"
	parentA := &api.DriveItem{Drivewsid: "FOLDER_IN_SHARED_FOLDER::com.apple.CloudDocs::parent-a-doc", Docwsid: "parent-a-doc", Type: "FOLDER", ShareID: sharedRoot.ShareID}

	for _, test := range []struct {
		name         string
		sourceParent *api.DriveItem
	}{
		{name: "root to nested", sourceParent: sharedRoot},
		{name: "nested to root", sourceParent: parentA},
		{name: "nested to nested", sourceParent: parentA},
	} {
		t.Run(test.name, func(t *testing.T) {
			doc := &api.Document{
				DocumentID: "source-folder-doc",
				ItemID:     "source-folder-item",
				Etag:       "fresh-folder-etag",
				ParentID:   test.sourceParent.Drivewsid,
				Name:       "source-folder",
				Type:       "FOLDER",
			}
			source := canonicalSharedItemFromDocument(doc, doc.ItemID, sharedRoot)
			if source == nil || source.Drivewsid != "FOLDER_IN_SHARED_FOLDER::com.apple.CloudDocs::source-folder-doc" || !source.IsFolder() {
				t.Fatalf("canonical source = %#v", source)
			}
		})
	}
}

func TestSharedObjectClassificationSurvivesMissingEnumeratedDrivewsid(t *testing.T) {
	o := &Object{}
	item := &api.DriveItem{
		Itemid:   "uploaded-item",
		Etag:     "fresh",
		ParentID: "FOLDER_IN_SHARED_FOLDER::com.apple.CloudDocs::parent",
	}
	item.ShareID.ShareName = "share"
	if err := o.setMetaData(item); err != nil {
		t.Fatal(err)
	}
	if !o.shared {
		t.Fatal("Shared object with inherited ShareID was classified as Personal")
	}
}

func TestPrepareSharedRenamePreservesCanonicalFolderAndFileIdentity(t *testing.T) {
	tests := []struct {
		name      string
		canonical string
		cacheID   string
	}{
		{
			name:      "folder",
			canonical: "FOLDER_IN_SHARED_FOLDER::com.apple.CloudDocs::<folder-docwsid>",
			cacheID:   "SHARED::<folder-item-id>",
		},
		{
			name:      "file",
			canonical: "FILE_IN_SHARED_FOLDER::com.apple.CloudDocs::<file-docwsid>",
			cacheID:   "SHARED::<file-item-id>",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			item := &api.DriveItem{Drivewsid: test.canonical}
			prepareSharedRenameItem(item, test.cacheID, "<fresh-etag>")
			if item.Drivewsid != test.canonical {
				t.Fatalf("DriveWS identity = %q, want canonical %q", item.Drivewsid, test.canonical)
			}
			if item.Drivewsid == test.cacheID {
				t.Fatalf("cache identity contaminated canonical DriveWS identity: %q", item.Drivewsid)
			}
			if item.Etag != "<fresh-etag>" {
				t.Fatalf("etag = %q, want fresh runtime etag", item.Etag)
			}
		})
	}
}

func TestSharedRenameRuntimeDoesNotPromoteCacheIdentity(t *testing.T) {
	cacheID := "SHARED::<item-id>"
	got := sharedRenameDriveID("FOLDER", "com.apple.CloudDocs", "<canonical-docwsid>")
	if got == cacheID || got != "FOLDER_IN_SHARED_FOLDER::com.apple.CloudDocs::<canonical-docwsid>" {
		t.Fatalf("runtime rename identity = %q", got)
	}
}

func TestSharedRenameCanonicalResolverContractFolderAndFile(t *testing.T) {
	item := &api.DriveItem{}
	item.ShareID.ShareName = "share"
	item.ShareID.RecordName = "record"
	item.ShareID.ShareChangeTag = "change"
	item.ShareID.ZoneID.ZoneName = "com.apple.CloudDocs"
	item.ShareID.ZoneID.OwnerRecordName = "owner"
	if !hasCompleteSharedShareID(item) {
		t.Fatal("complete ShareID rejected")
	}
	for _, drivewsid := range []string{
		"FOLDER_IN_SHARED_FOLDER::com.apple.CloudDocs::folder-doc",
		"FILE_IN_SHARED_FOLDER::com.apple.CloudDocs::file-doc",
	} {
		item := &api.DriveItem{Itemid: "item", Drivewsid: drivewsid, Etag: "fresh"}
		item.ShareID.ShareName = "share"
		item.ShareID.RecordName = "record"
		item.ShareID.ShareChangeTag = "change"
		item.ShareID.ZoneID.ZoneName = "com.apple.CloudDocs"
		item.ShareID.ZoneID.OwnerRecordName = "owner"
		if item.Itemid == "SHARED::<item>" || !hasCompleteSharedShareID(item) || item.Etag == "" {
			t.Fatalf("canonical resolver contract not satisfied: %#v", item)
		}
	}
}

func TestSharedRenameCanonicalResolverRejectsIncompleteShare(t *testing.T) {
	item := &api.DriveItem{}
	item.ShareID.ShareName = "share"
	item.ShareID.RecordName = "record"
	if hasCompleteSharedShareID(item) {
		t.Fatal("incomplete ShareID accepted")
	}
}

func TestSharedRenameCanonicalHierarchyResolvesWithoutParentCache(t *testing.T) {
	root := &api.DriveItem{Itemid: "root-item", Drivewsid: "SHARED_FOLDER::com.apple.CloudDocs::root-doc", Type: "FOLDER"}
	root.ShareID.ShareName = "share"
	root.ShareID.RecordName = "record"
	root.ShareID.ShareChangeTag = "change"
	root.ShareID.ZoneID.ZoneName = "com.apple.CloudDocs"
	root.ShareID.ZoneID.OwnerRecordName = "owner"
	root.Items = []*api.DriveItem{{
		Itemid:    "source-item",
		Drivewsid: "FOLDER_IN_SHARED_FOLDER::com.apple.CloudDocs::source-doc",
		Docwsid:   "source-doc",
		Etag:      "fresh",
		Name:      "renamed",
		Type:      "FOLDER",
	}}

	got := findSharedCanonicalHierarchyItem(root, "source-item")
	if got == nil {
		t.Fatal("canonical source was not found")
	}
	if got.Drivewsid == "SHARED::source-item" || got.Drivewsid != "FOLDER_IN_SHARED_FOLDER::com.apple.CloudDocs::source-doc" {
		t.Fatalf("drivewsid = %q", got.Drivewsid)
	}
	if got.Etag != "fresh" || !hasCompleteSharedShareID(got) {
		t.Fatalf("canonical source context incomplete: %#v", got)
	}
}

func TestSharedRenameCanonicalHierarchyResolvesFileIdentity(t *testing.T) {
	root := &api.DriveItem{Itemid: "root-item", Drivewsid: "SHARED_FOLDER::com.apple.CloudDocs::root-doc", Type: "FOLDER"}
	root.ShareID.ShareName = "share"
	root.ShareID.RecordName = "record"
	root.ShareID.ShareChangeTag = "change"
	root.ShareID.ZoneID.ZoneName = "com.apple.CloudDocs"
	root.ShareID.ZoneID.OwnerRecordName = "owner"
	root.Items = []*api.DriveItem{{Itemid: "file-item", Drivewsid: "FILE_IN_SHARED_FOLDER::com.apple.CloudDocs::file-doc", Etag: "fresh", Type: "FILE"}}
	got := findSharedCanonicalHierarchyItem(root, "file-item")
	if got == nil || got.Drivewsid != "FILE_IN_SHARED_FOLDER::com.apple.CloudDocs::file-doc" || !hasCompleteSharedShareID(got) {
		t.Fatalf("file canonical context = %#v", got)
	}
}

func TestSharedRenameCanonicalHierarchyRejectsMissingSource(t *testing.T) {
	root := &api.DriveItem{Itemid: "root-item", Drivewsid: "SHARED_FOLDER::com.apple.CloudDocs::root-doc"}
	if got := findSharedCanonicalHierarchyItem(root, "missing"); got != nil {
		t.Fatalf("unexpected source: %#v", got)
	}
}

func TestSharedRenameCanonicalHierarchyRejectsIncompleteRootShare(t *testing.T) {
	root := &api.DriveItem{Itemid: "root-item", Drivewsid: "SHARED_FOLDER::com.apple.CloudDocs::root-doc", Items: []*api.DriveItem{{Itemid: "source", Drivewsid: "FOLDER_IN_SHARED_FOLDER::com.apple.CloudDocs::doc", Etag: "fresh"}}}
	got := findSharedCanonicalHierarchyItem(root, "source")
	if got == nil || hasCompleteSharedShareID(got) {
		t.Fatalf("incomplete share context was accepted: %#v", got)
	}
}

func TestSharedRenameCanonicalSourceFromRetrieveHierarchyDirectChild(t *testing.T) {
	root := &api.DriveItem{Drivewsid: "FOLDER::com.apple.CloudDocs::root", Items: []*api.DriveItem{{
		Drivewsid: "SHARED_FOLDER::com.apple.CloudDocs::shared-root",
		Items: []*api.DriveItem{{
			Itemid:    "source-item",
			Drivewsid: "FOLDER_IN_SHARED_FOLDER::com.apple.CloudDocs::source-doc",
			Docwsid:   "source-doc",
			Etag:      "fresh",
			Type:      "FOLDER",
		}},
	}}}
	root.Items[0].ShareID.ShareName = "share"
	root.Items[0].ShareID.RecordName = "record"
	root.Items[0].ShareID.ZoneID.ZoneName = "com.apple.CloudDocs"
	root.Items[0].ShareID.ZoneID.OwnerRecordName = "owner"
	got := findSharedCanonicalHierarchyItem(findSharedRootHierarchyItem(root), "source-item")
	if got == nil || got.Itemid != "source-item" || got.Drivewsid != "FOLDER_IN_SHARED_FOLDER::com.apple.CloudDocs::source-doc" || got.Docwsid != "source-doc" || got.Etag != "fresh" {
		t.Fatalf("direct-child canonical source = %#v", got)
	}
}

func TestSharedRenameCanonicalSourceFromRetrieveHierarchyNestedFile(t *testing.T) {
	root := &api.DriveItem{Drivewsid: "SHARED_FOLDER::com.apple.CloudDocs::shared-root", Items: []*api.DriveItem{{
		Itemid: "nested-folder", Drivewsid: "FOLDER_IN_SHARED_FOLDER::com.apple.CloudDocs::nested-folder", Items: []*api.DriveItem{{
			Itemid: "nested-file", Drivewsid: "FILE_IN_SHARED_FOLDER::com.apple.CloudDocs::nested-file", Docwsid: "nested-file", Etag: "fresh-file", Type: "FILE",
		}},
	}}}
	got := findSharedCanonicalHierarchyItem(findSharedRootHierarchyItem(root), "nested-file")
	if got == nil || got.Itemid != "nested-file" || got.Drivewsid != "FILE_IN_SHARED_FOLDER::com.apple.CloudDocs::nested-file" || got.Etag != "fresh-file" {
		t.Fatalf("nested canonical file = %#v", got)
	}
}

func TestSharedRenameDirMoveUsesItemIDFromNormalizedCacheIdentity(t *testing.T) {
	got := sharedRenameItemID("SHARED::source-item#fresh-etag")
	if got != "source-item" {
		t.Fatalf("normalized Shared cache identity resolved to item_id %q, want %q", got, "source-item")
	}
}
