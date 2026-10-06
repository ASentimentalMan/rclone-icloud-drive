package iclouddrive

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rclone/rclone/backend/iclouddrive/api"
	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/object"
	"github.com/rclone/rclone/lib/dircache"
	"github.com/rclone/rclone/lib/pacer"
	"github.com/rclone/rclone/lib/rest"
)

const sharedIdentityTestRootID = "FOLDER::com.apple.CloudDocs::root"

func newSharedDirectoryIdentityService(t *testing.T, handler http.HandlerFunc, withValidateCookie bool) (*api.DriveService, func()) {
	t.Helper()
	server := httptest.NewServer(handler)
	session := api.NewSession()
	if withValidateCookie {
		session.Cookies = []*http.Cookie{{Name: "X-APPLE-WEBAUTH-VALIDATE", Value: "v=1:t=synthetic-token"}}
	}
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
	client, err := api.New("", "", "", "", nil, nil, "shared-directory-identity-test", "")
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

func newSharedIdentityTestFs(service *api.DriveService) *Fs {
	f := &Fs{
		rootID:        sharedIdentityTestRootID,
		service:       service,
		sharedItemIDs: make(map[string]string),
		sharedItems:   make(map[string]*api.DriveItem),
		pacer:         fs.NewPacer(context.Background(), pacer.NewDefault()),
	}
	f.dirCache = dircache.New("", f.rootID, f)
	return f
}

func addSharedIdentityShare(item *api.DriveItem) {
	item.ShareID.ShareName = "synthetic-share"
	item.ShareID.RecordName = "synthetic-share-record"
	item.ShareID.ShareChangeTag = "synthetic-share-change"
	item.ShareID.ZoneID.ZoneName = "com.apple.CloudDocs"
	item.ShareID.ZoneID.OwnerRecordName = "synthetic-owner"
	item.ShareID.ZoneID.ZoneType = "REGULAR_CUSTOM_ZONE"
}

func sharedIdentityTree(fileName string, fileSize int64) (*api.DriveItem, *api.DriveItem) {
	file := &api.DriveItem{
		Drivewsid: "FILE_IN_SHARED_FOLDER::com.apple.CloudDocs::uploaded-document",
		Docwsid:   "uploaded-document",
		Itemid:    "uploaded-item",
		Name:      strings.TrimSuffix(fileName, ".txt"),
		Extension: "txt",
		Etag:      "uploaded-etag",
		ParentID:  "FOLDER_IN_SHARED_FOLDER::com.apple.CloudDocs::saved-document",
		Type:      "FILE",
		Size:      fileSize,
	}
	saved := &api.DriveItem{
		Drivewsid: "FOLDER_IN_SHARED_FOLDER::com.apple.CloudDocs::saved-document",
		Docwsid:   "saved-document",
		Itemid:    "saved-item",
		Name:      "saved",
		Etag:      "saved-etag",
		Type:      "FOLDER",
		Items:     []*api.DriveItem{file},
	}
	zjzxwia := &api.DriveItem{
		Drivewsid: "FOLDER_IN_SHARED_FOLDER::com.apple.CloudDocs::zjzxwia-document",
		Docwsid:   "zjzxwia-document",
		Itemid:    "zjzxwia-item",
		Name:      "Zjzxwia",
		Etag:      "zjzxwia-etag",
		Type:      "FOLDER",
		Items:     []*api.DriveItem{saved},
	}
	xarchive := &api.DriveItem{
		Drivewsid: "FOLDER_IN_SHARED_FOLDER::com.apple.CloudDocs::xarchive-document",
		Docwsid:   "xarchive-document",
		Itemid:    "xarchive-item",
		Name:      "X-Archive",
		Etag:      "xarchive-etag",
		Type:      "FOLDER",
		Items:     []*api.DriveItem{zjzxwia},
	}
	sharedRoot := &api.DriveItem{
		Drivewsid: "SHARED_FOLDER::com.apple.CloudDocs::shared-root-document",
		Docwsid:   "shared-root-document",
		Itemid:    "shared-root-item",
		Name:      "Shared",
		Etag:      "shared-root-etag",
		Type:      "FOLDER",
		Items:     []*api.DriveItem{xarchive},
	}
	addSharedIdentityShare(sharedRoot)
	for _, item := range []*api.DriveItem{xarchive, zjzxwia, saved, file} {
		item.ShareID = sharedRoot.ShareID
	}
	root := &api.DriveItem{Drivewsid: sharedIdentityTestRootID, Type: "FOLDER", Items: []*api.DriveItem{sharedRoot}}
	return root, saved
}

func writeSharedIdentityTreeJSON(w http.ResponseWriter, fileName string, fileSize int64) {
	root, _ := sharedIdentityTree(fileName, fileSize)
	_ = json.NewEncoder(w).Encode([]*api.DriveItem{root})
}

func writeSharedIdentityChildren(w http.ResponseWriter, items []*api.DriveItem) {
	raw := make([]*api.DriveItemRaw, 0, len(items))
	for _, item := range items {
		raw = append(raw, &api.DriveItemRaw{ItemID: item.Itemid, ItemInfo: &api.DriveItemRawInfo{
			Name: item.FullName(), Type: item.Type, Size: item.Size, Version: item.Etag,
		}})
	}
	_ = json.NewEncoder(w).Encode(struct {
		Items []*api.DriveItemRaw `json:"drive_item"`
	}{Items: raw})
}

func writeSharedIdentityRawItem(w http.ResponseWriter, itemID string, item *api.DriveItem) {
	_ = json.NewEncoder(w).Encode(&api.DriveItemRaw{ItemID: itemID, ItemInfo: &api.DriveItemRawInfo{
		Name: item.FullName(), Type: item.Type, Size: item.Size, Version: item.Etag,
	}})
}

func TestListAllSharedDirectoryIDsUseSharedEnumeration(t *testing.T) {
	canonicalID := "FOLDER_IN_SHARED_FOLDER::com.apple.CloudDocs::parent-document"
	child := &api.DriveItem{
		Drivewsid: "FOLDER_IN_SHARED_FOLDER::com.apple.CloudDocs::child-document",
		Docwsid:   "child-document",
		Itemid:    "child-item",
		Name:      "child",
		Etag:      "child-etag",
		Type:      "FOLDER",
	}
	parent := &api.DriveItem{
		Drivewsid: canonicalID,
		Docwsid:   "parent-document",
		Itemid:    "parent-item",
		Name:      "parent",
		Etag:      "parent-etag",
		Type:      "FOLDER",
		Items:     []*api.DriveItem{child},
	}
	addSharedIdentityShare(parent)
	child.ShareID = parent.ShareID
	var retrieveCalls, enumerateCalls int
	service, closeServer := newSharedDirectoryIdentityService(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/retrieveItemDetailsInFolders":
			retrieveCalls++
			http.Error(w, "unexpected personal metadata lookup", http.StatusBadRequest)
		case "/v1/enumerate/parent-item":
			enumerateCalls++
			writeSharedIdentityChildren(w, []*api.DriveItem{child})
		case "/v1/item/child-item":
			writeSharedIdentityRawItem(w, "child-item", child)
		default:
			http.NotFound(w, r)
		}
	}, false)
	defer closeServer()

	for _, test := range []struct {
		name string
		id   string
	}{
		{name: "synthetic cache identity", id: "SHARED::parent-item"},
		{name: "canonical nested folder identity", id: canonicalID},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newSharedIdentityTestFs(service)
			f.sharedItems[test.id] = parent
			got, err := f.listAll(context.Background(), test.id+"#parent-etag")
			if err != nil {
				t.Fatalf("listAll returned error: %v", err)
			}
			if len(got) != 1 || got[0].Itemid != "child-item" || got[0].Drivewsid != child.Drivewsid {
				t.Fatalf("listAll result = %#v", got)
			}
		})
	}
	if retrieveCalls != 0 {
		t.Fatalf("personal metadata endpoint called %d times", retrieveCalls)
	}
	if enumerateCalls != 2 {
		t.Fatalf("Shared enumerate calls = %d, want 2", enumerateCalls)
	}
}

func TestListAllCanonicalSharedFolderResolvesWithoutCacheWarm(t *testing.T) {
	canonicalID := "FOLDER_IN_SHARED_FOLDER::com.apple.CloudDocs::saved-document"
	root, saved := sharedIdentityTree("uploaded.txt", 5)
	var nestedDriveIDSentToMetadata bool
	service, closeServer := newSharedDirectoryIdentityService(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/retrieveItemDetailsInFolders":
			body, _ := io.ReadAll(r.Body)
			nestedDriveIDSentToMetadata = strings.Contains(string(body), canonicalID)
			_ = json.NewEncoder(w).Encode([]*api.DriveItem{root})
		case "/v1/enumerate/saved-item":
			writeSharedIdentityChildren(w, saved.Items)
		case "/v1/item/uploaded-item":
			writeSharedIdentityRawItem(w, "uploaded-item", saved.Items[0])
		default:
			http.NotFound(w, r)
		}
	}, false)
	defer closeServer()
	f := newSharedIdentityTestFs(service)
	// Deliberately leave both Shared caches empty. The canonical ID must be
	// resolved from the authenticated Shared hierarchy before enumeration.
	got, err := f.listAll(context.Background(), canonicalID)
	if err != nil {
		t.Fatalf("listAll returned error: %v", err)
	}
	if len(got) != 1 || got[0].Itemid != "uploaded-item" {
		t.Fatalf("listAll result = %#v", got)
	}
	if nestedDriveIDSentToMetadata {
		t.Fatal("nested FOLDER_IN_SHARED_FOLDER ID was sent to retrieveItemDetailsInFolders")
	}
}

func TestFindNestedSharedPathWithoutCacheWarming(t *testing.T) {
	root, saved := sharedIdentityTree("uploaded.txt", 5)
	service, closeServer := newSharedDirectoryIdentityService(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/retrieveItemDetailsInFolders":
			_ = json.NewEncoder(w).Encode([]*api.DriveItem{root})
		case "/v1/enumerate/shared-root-item":
			writeSharedIdentityChildren(w, root.Items[0].Items)
		case "/v1/enumerate/xarchive-item":
			writeSharedIdentityChildren(w, root.Items[0].Items[0].Items)
		case "/v1/enumerate/zjzxwia-item":
			writeSharedIdentityChildren(w, root.Items[0].Items[0].Items[0].Items)
		case "/v1/item/xarchive-item":
			writeSharedIdentityRawItem(w, "xarchive-item", root.Items[0].Items[0])
		case "/v1/item/zjzxwia-item":
			writeSharedIdentityRawItem(w, "zjzxwia-item", root.Items[0].Items[0].Items[0])
		case "/v1/item/saved-item":
			writeSharedIdentityRawItem(w, "saved-item", saved)
		default:
			http.NotFound(w, r)
		}
	}, false)
	defer closeServer()
	f := newSharedIdentityTestFs(service)

	id, _, err := f.FindDir(context.Background(), "Shared/X-Archive/Zjzxwia/saved", false)
	if err != nil {
		t.Fatalf("FindDir returned error: %v", err)
	}
	if id != "SHARED::saved-item" {
		t.Fatalf("nested Shared directory ID = %q", id)
	}
}

func TestCreateSharedFolderWithCanonicalNestedParent(t *testing.T) {
	sharedRoot, _ := sharedWriteParentFixture()
	parentID := "FOLDER_IN_SHARED_FOLDER::com.apple.CloudDocs::destination-doc"
	parent := &api.DriveItem{
		Drivewsid: parentID,
		Docwsid:   "destination-doc",
		Itemid:    "destination-item",
		Etag:      "destination-etag",
		Type:      "FOLDER",
		ShareID:   sharedRoot.ShareID,
	}
	state := &sharedWriteParentTestState{}
	service, closeServer := newSharedWriteParentTestService(t, sharedRoot, state)
	defer closeServer()
	f := sharedWriteParentFs(service, parent)
	f.sharedItemIDs = make(map[string]string)
	f.sharedItems[parentID] = parent
	f.sharedItemIDs[parentID] = parent.Itemid

	got, err := f.CreateDir(context.Background(), parentID+"#destination-etag", "child")
	if err != nil {
		t.Fatalf("CreateDir returned error: %v", err)
	}
	if got != "FOLDER_IN_SHARED_FOLDER::com.apple.CloudDocs::created-doc#created-etag" {
		t.Fatalf("created directory ID = %q", got)
	}
	if state.createRequests != 1 || state.createParent != parentID {
		t.Fatalf("CreateSharedFolder parent = %q in %d requests", state.createParent, state.createRequests)
	}
}

func TestResolveSharedWriteParentCanonicalIdentity(t *testing.T) {
	sharedRoot, partial := sharedWriteParentFixture()
	canonicalID := "FOLDER_IN_SHARED_FOLDER::com.apple.CloudDocs::destination-doc"
	state := &sharedWriteParentTestState{}
	service, closeServer := newSharedWriteParentTestService(t, sharedRoot, state)
	defer closeServer()
	f := sharedWriteParentFs(service, partial)
	f.sharedItems = map[string]*api.DriveItem{canonicalID: partial}
	f.sharedItemIDs = make(map[string]string)
	f.sharedItemIDs[canonicalID] = partial.Itemid

	got, err := f.resolveSharedWriteParent(context.Background(), canonicalID)
	if err != nil {
		t.Fatalf("resolveSharedWriteParent returned error: %v", err)
	}
	if got.Drivewsid != canonicalID || got.Docwsid != "destination-doc" || got.Itemid != "destination-item" {
		t.Fatalf("resolved canonical parent = %#v", got)
	}
	if state.hierarchyReads != 1 || state.documentReads != 1 {
		t.Fatalf("canonical resolver reads = hierarchy %d, document %d; want one each", state.hierarchyReads, state.documentReads)
	}
}

func TestObjectUpdateUsesSharedProtocolForSharedDirectoryIDs(t *testing.T) {
	for _, test := range []struct {
		name string
		id   string
	}{
		{name: "synthetic Shared identity", id: "SHARED::saved-item"},
		{name: "canonical nested Shared identity", id: "FOLDER_IN_SHARED_FOLDER::com.apple.CloudDocs::saved-document"},
	} {
		t.Run(test.name, func(t *testing.T) {
			root, saved := sharedIdentityTree("upload.txt", 5)
			var sharedCreate, binaryUpload, sharedCommit int
			var personalCreate, personalCommit int
			var startingDocumentID string
			service, closeServer := newSharedDirectoryIdentityService(t, func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/ws/com.apple.CloudDocs/upload/web/synthetic-owner/synthetic-share-record":
					sharedCreate++
					_ = json.NewEncoder(w).Encode([]*api.UploadResponse{{URL: "http://" + r.Host + "/signed", DocumentID: "uploaded-document"}})
				case "/signed":
					binaryUpload++
					_, _ = io.Copy(io.Discard, r.Body)
					_, _ = io.WriteString(w, `{"singleFile":{"referenceChecksum":"reference","size":5,"fileChecksum":"signature","wrappingKey":"wrapping","receipt":"receipt"}}`)
				case "/ws/com.apple.CloudDocs/update/shared/synthetic-owner":
					sharedCommit++
					var request struct {
						Path struct {
							StartingDocumentID string `json:"starting_document_id"`
						} `json:"path"`
					}
					if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
						t.Errorf("decode Shared commit: %v", err)
					}
					startingDocumentID = request.Path.StartingDocumentID
					_, _ = io.WriteString(w, `{"status":{"status_code":0},"results":[{"status":{"status_code":0},"document":{"document_id":"uploaded-document","item_id":"uploaded-item","etag":"uploaded-etag","parent_id":"FOLDER_IN_SHARED_FOLDER::com.apple.CloudDocs::saved-document","name":"upload.txt","type":"FILE","size":5}}]}`)
				case "/ws/com.apple.CloudDocs/upload/web":
					personalCreate++
					http.Error(w, "personal upload path must not be used", http.StatusInternalServerError)
				case "/ws/com.apple.CloudDocs/update/documents":
					personalCommit++
					http.Error(w, "personal commit path must not be used", http.StatusInternalServerError)
				case "/retrieveItemDetailsInFolders":
					_ = json.NewEncoder(w).Encode([]*api.DriveItem{root})
				case "/v1/enumerate/shared-root-item":
					writeSharedIdentityChildren(w, root.Items[0].Items)
				case "/v1/enumerate/xarchive-item":
					writeSharedIdentityChildren(w, root.Items[0].Items[0].Items)
				case "/v1/enumerate/zjzxwia-item":
					writeSharedIdentityChildren(w, root.Items[0].Items[0].Items[0].Items)
				case "/v1/enumerate/saved-item":
					writeSharedIdentityChildren(w, saved.Items)
				case "/v1/item/xarchive-item":
					writeSharedIdentityRawItem(w, "xarchive-item", root.Items[0].Items[0])
				case "/v1/item/zjzxwia-item":
					writeSharedIdentityRawItem(w, "zjzxwia-item", root.Items[0].Items[0].Items[0])
				case "/v1/item/saved-item":
					writeSharedIdentityRawItem(w, "saved-item", saved)
				case "/v1/item/uploaded-item":
					writeSharedIdentityRawItem(w, "uploaded-item", saved.Items[0])
				default:
					http.NotFound(w, r)
				}
			}, true)
			defer closeServer()

			parent := saved
			f := newSharedIdentityTestFs(service)
			f.root = "Shared/X-Archive/Zjzxwia/saved"
			f.sharedItems[test.id] = parent
			f.sharedItemIDs[parent.Drivewsid] = parent.Itemid
			f.dirCache = dircache.New("", test.id, f)
			o := &Object{fs: f, remote: "upload.txt"}
			src := object.NewStaticObjectInfo("upload.txt", time.Unix(1, 0), 5, true, nil, f)
			err := o.Update(context.Background(), strings.NewReader("hello"), src)
			if err != nil {
				t.Fatalf("Object.Update returned error: %v", err)
			}
			if sharedCreate != 1 || binaryUpload != 1 || sharedCommit != 1 {
				t.Fatalf("Shared stages: create=%d binary=%d commit=%d", sharedCreate, binaryUpload, sharedCommit)
			}
			if personalCreate != 0 || personalCommit != 0 {
				t.Fatalf("Personal upload stages called: create=%d commit=%d", personalCreate, personalCommit)
			}
			wantStartingDocumentID := "saved-document"
			if test.id == "SHARED::saved-item" {
				wantStartingDocumentID = "saved-document"
			}
			if startingDocumentID != wantStartingDocumentID {
				t.Fatalf("Shared starting_document_id = %q, want %q", startingDocumentID, wantStartingDocumentID)
			}
		})
	}
}

func TestObjectUpdateKeepsPersonalUploadForPersonalDirectory(t *testing.T) {
	var createUpload, binaryUpload, updateFile int
	service, closeServer := newSharedDirectoryIdentityService(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ws/com.apple.CloudDocs/upload/web":
			createUpload++
			_ = json.NewEncoder(w).Encode([]*api.UploadResponse{{URL: "http://" + r.Host + "/signed", DocumentID: "personal-document"}})
		case "/signed":
			binaryUpload++
			_, _ = io.Copy(io.Discard, r.Body)
			_, _ = io.WriteString(w, `{"singleFile":{"referenceChecksum":"reference","size":5,"fileChecksum":"signature","wrappingKey":"wrapping","receipt":"receipt"}}`)
		case "/ws/com.apple.CloudDocs/update/documents":
			updateFile++
			_, _ = io.WriteString(w, `{"status":{"status_code":0},"results":[{"status":{"status_code":0},"document":{"document_id":"personal-document","item_id":"personal-item","etag":"personal-etag","parent_id":"FOLDER::com.apple.CloudDocs::root","name":"upload.txt","type":"FILE","size":5}}]}`)
		default:
			http.NotFound(w, r)
		}
	}, false)
	defer closeServer()
	f := newSharedIdentityTestFs(service)
	f.dirCache = dircache.New("", "FOLDER::com.apple.CloudDocs::personal-parent", f)
	o := &Object{fs: f, remote: "upload.txt"}
	src := object.NewStaticObjectInfo("upload.txt", time.Unix(1, 0), 5, true, nil, f)
	if err := o.Update(context.Background(), strings.NewReader("hello"), src); err != nil {
		t.Fatalf("Object.Update returned error: %v", err)
	}
	if createUpload != 1 || binaryUpload != 1 || updateFile != 1 {
		t.Fatalf("Personal stages: create=%d upload=%d update=%d", createUpload, binaryUpload, updateFile)
	}
}

func TestIsSharedDirectoryIDUsesOnlyDirectoryIdentityTypes(t *testing.T) {
	for _, test := range []struct {
		id   string
		want bool
	}{
		{id: "SHARED::item#etag", want: true},
		{id: "SHARED_FOLDER::com.apple.CloudDocs::root", want: true},
		{id: "FOLDER_IN_SHARED_FOLDER::com.apple.CloudDocs::nested", want: true},
		{id: "FILE_IN_SHARED_FOLDER::com.apple.CloudDocs::file", want: false},
		{id: "FOLDER::com.apple.CloudDocs::personal", want: false},
		{id: "OTHER_SHARED_FOLDER::value", want: false},
	} {
		if got := isSharedDirectoryID(test.id); got != test.want {
			t.Errorf("isSharedDirectoryID(%q) = %t, want %t", test.id, got, test.want)
		}
	}
}
