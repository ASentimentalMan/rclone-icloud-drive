package api

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestZoneFromDriveID(t *testing.T) {
	for _, test := range []struct {
		name string
		id   string
		want string
	}{
		{"app container", "FOLDER::iCloud.md.obsidian::documents#o2v", "iCloud.md.obsidian"},
		{"another app container", "FOLDER::com.apple.Pages::documents#7qt", "com.apple.Pages"},
		{"ordinary folder", "FOLDER::com.apple.CloudDocs::B847FE2D", "com.apple.CloudDocs"},
		{"file in a container", "FILE::iCloud.md.obsidian::abc123", "iCloud.md.obsidian"},
		{"no zone", "root", defaultZone},
		{"empty", "", defaultZone},
	} {
		t.Run(test.name, func(t *testing.T) {
			assert.Equal(t, test.want, ZoneFromDriveID(test.id))
		})
	}
}

func TestZoneFromDriveIDRoundTrip(t *testing.T) {
	id := ConstructDriveID("abc123", "iCloud.md.obsidian", "FILE")
	assert.Equal(t, "FILE::iCloud.md.obsidian::abc123", id)
	assert.Equal(t, "iCloud.md.obsidian", ZoneFromDriveID(id))
}

func TestBuildDriveItemDetailRequestIncludesHierarchyWhenRequested(t *testing.T) {
	items := buildDriveItemDetailRequest([]string{"SHARED_FOLDER::com.apple.CloudDocs::root"}, true)
	if len(items) != 1 {
		t.Fatalf("request item count = %d, want 1", len(items))
	}
	if got, ok := items[0]["includeHierarchy"].(bool); !ok || !got {
		t.Fatalf("includeHierarchy = %#v, want true", items[0]["includeHierarchy"])
	}
	withoutHierarchy := buildDriveItemDetailRequest([]string{"FILE::com.apple.CloudDocs::file"}, false)
	if got, ok := withoutHierarchy[0]["includeHierarchy"].(bool); !ok || got {
		t.Fatalf("non-hierarchy includeHierarchy = %#v, want false", withoutHierarchy[0]["includeHierarchy"])
	}
}

func TestSharedModifyFieldsMatchSuccessfulEnvelope(t *testing.T) {
	zone := map[string]any{
		"zoneName":        "com.apple.CloudDocs",
		"ownerRecordName": "_owner",
		"zoneType":        "REGULAR_CUSTOM_ZONE",
	}
	source := map[string]any{
		"basehash":          map[string]any{"value": "hash", "type": "BYTES"},
		"encryptedBasename": map[string]any{"value": "name", "type": "ENCRYPTED_BYTES"},
		"extension":         map[string]any{"value": "txt", "type": "STRING"},
		"hiddenExt":         map[string]any{"value": 0, "type": "INT64"},
		"birthtime":         map[string]any{"value": 1, "type": "INT64"},
		"countMetrics":      map[string]any{"value": []any{17, 1, 0, 0, 0}, "type": "INT64_LIST"},
		"executable":        map[string]any{"value": 1, "type": "INT64"},
		"writable":          map[string]any{"value": 1, "type": "INT64"},
	}
	got := sharedModifyFields(source, "directory/destination", zone)
	assert.ElementsMatch(t, []string{"basehash", "encryptedBasename", "extension", "parent"}, mapKeys(got))
	assert.Equal(t, "directory/destination", got["parent"].(map[string]any)["value"].(map[string]any)["recordName"])
}

func mapKeys(values map[string]any) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	return keys
}

func TestSharedDestinationParentUsesChildDirectoryRecord(t *testing.T) {
	child := &DriveItem{Drivewsid: "FOLDER_IN_SHARED_FOLDER::com.apple.CloudDocs::child", Docwsid: "child", Itemid: "item"}
	child.ShareID.ShareName = "share"
	parent, err := sharedDestinationParent(child)
	assert.NoError(t, err)
	assert.Equal(t, "directory/child", parent)
	child.Drivewsid = ""
	child.ParentID = "SHARED_FOLDER::com.apple.CloudDocs::root"
	parent, err = sharedDestinationParent(child)
	assert.NoError(t, err)
	assert.Equal(t, "directory/child", parent)

	root := &DriveItem{Drivewsid: "SHARED_FOLDER::com.apple.CloudDocs::root", Docwsid: "root", Itemid: "root-item"}
	root.ShareID.ShareName = "share"
	parent, err = sharedDestinationParent(root)
	assert.NoError(t, err)
	assert.Equal(t, "directory/share", parent)
}

func TestPersonalToSharedMoveRequestCarriesCompleteDestinationContext(t *testing.T) {
	gotBytes, err := buildPersonalToSharedMoveRequest("FILE::com.apple.CloudDocs::source", "5::4", SharedDestinationContext{
		DestinationDrivewsID: "SHARED_FOLDER::com.apple.CloudDocs::root",
		ShareName:            "share-record",
		RecordName:           "share-record",
		ShareChangeTag:       "fresh-share-tag",
		ZoneName:             "com.apple.CloudDocs",
		OwnerRecordName:      "owner-record",
	})
	assert.NoError(t, err)
	got := decodeFixtureRequest(t, jsonRaw(gotBytes))
	item := got["items"].([]any)[0].(map[string]any)
	assert.Equal(t, "FILE::com.apple.CloudDocs::source", item["drivewsid"])
	assert.Equal(t, "SHARED_FOLDER::com.apple.CloudDocs::root", got["destinationDrivewsId"])
	share := got["shareID"].(map[string]any)
	assert.Equal(t, "share-record", share["shareName"])
	assert.Equal(t, "share-record", share["recordName"])
	assert.Equal(t, "fresh-share-tag", share["shareChangeTag"])
	assert.Equal(t, "owner-record", share["zoneID"].(map[string]any)["ownerRecordName"])
}

func TestPersonalFolderToSharedMoveRequestCarriesSourceEtag(t *testing.T) {
	body, err := buildPersonalToSharedMoveRequest("FOLDER::com.apple.CloudDocs::source-folder", "folder-etag", SharedDestinationContext{
		DestinationDrivewsID: "SHARED_FOLDER::com.apple.CloudDocs::shared-root",
		ShareName:            "share",
		RecordName:           "record",
		ShareChangeTag:       "change",
		ZoneName:             "com.apple.CloudDocs",
		OwnerRecordName:      "owner",
	})
	if err != nil {
		t.Fatal(err)
	}
	var request struct {
		DestinationDrivewsID string `json:"destinationDrivewsId"`
		Items                []struct {
			Drivewsid string `json:"drivewsid"`
			Etag      string `json:"etag"`
			ClientID  string `json:"clientId"`
		} `json:"items"`
		ShareID struct {
			ShareName      string `json:"shareName"`
			RecordName     string `json:"recordName"`
			ShareChangeTag string `json:"shareChangeTag"`
			ZoneID         struct {
				ZoneName        string `json:"zoneName"`
				OwnerRecordName string `json:"ownerRecordName"`
			} `json:"zoneID"`
		} `json:"shareID"`
	}
	if err := json.Unmarshal(body, &request); err != nil {
		t.Fatal(err)
	}
	if request.DestinationDrivewsID != "SHARED_FOLDER::com.apple.CloudDocs::shared-root" || len(request.Items) != 1 {
		t.Fatalf("folder move destination/items = %#v", request)
	}
	if request.Items[0].Drivewsid != "FOLDER::com.apple.CloudDocs::source-folder" || request.Items[0].ClientID != request.Items[0].Drivewsid || request.Items[0].Etag != "folder-etag" {
		t.Fatalf("folder move source = %#v", request.Items)
	}
	share := request.ShareID
	if share.ShareName != "share" || share.RecordName != "record" || share.ShareChangeTag != "change" || share.ZoneID.ZoneName != "com.apple.CloudDocs" || share.ZoneID.OwnerRecordName != "owner" {
		t.Fatalf("folder move Shared destination context = %#v", share)
	}
}

func TestSharedToPersonalMoveRequestCarriesSourceContextOnItem(t *testing.T) {
	for _, sourceID := range []string{
		"FILE_IN_SHARED_FOLDER::com.apple.CloudDocs::source-file",
		"FOLDER_IN_SHARED_FOLDER::com.apple.CloudDocs::source-folder",
	} {
		t.Run(sourceID, func(t *testing.T) {
			gotBytes, err := buildSharedToPersonalMoveRequest(sourceID, "fresh-etag", "FOLDER::com.apple.CloudDocs::root", SharedDestinationContext{
				ShareName:       "share-record",
				RecordName:      "share-record",
				ShareChangeTag:  "fresh-share-tag",
				ZoneName:        "com.apple.CloudDocs",
				OwnerRecordName: "owner-record",
			})
			assert.NoError(t, err)
			got := decodeFixtureRequest(t, jsonRaw(gotBytes))
			item := got["items"].([]any)[0].(map[string]any)
			assert.Equal(t, sourceID, item["drivewsid"])
			assert.Equal(t, "fresh-etag", item["etag"])
			assert.Equal(t, sourceID, item["clientId"])
			assert.Equal(t, "FOLDER::com.apple.CloudDocs::root", got["destinationDrivewsId"])
			share := item["shareID"].(map[string]any)
			assert.Equal(t, "share-record", share["shareName"])
			assert.Equal(t, "share-record", share["recordName"])
			assert.Equal(t, "fresh-share-tag", share["shareChangeTag"])
			assert.Equal(t, "com.apple.CloudDocs", share["zoneID"].(map[string]any)["zoneName"])
			assert.Equal(t, "owner-record", share["zoneID"].(map[string]any)["ownerRecordName"])
			_, ok := got["shareID"]
			assert.False(t, ok, "reverse move must carry source shareID on items[0]")
		})
	}
}

func TestSharedDirectoryReparentUsesDirectoryRecordAndNameFields(t *testing.T) {
	assert.Equal(t, "directory/ABC-123", sharedDirectoryRecordName("abc-123"))
	fields := sharedDirectoryModifyFields(nil, "new-folder", "directory/parent", map[string]any{
		"zoneName": "com.apple.CloudDocs",
	})
	assert.Equal(t, "directory/parent", fields["parent"].(map[string]any)["value"].(map[string]any)["recordName"])
	assert.Equal(t, "BYTES", fields["basehash"].(map[string]any)["type"])
	assert.Equal(t, "ENCRYPTED_BYTES", fields["encryptedBasename"].(map[string]any)["type"])
}

func TestSharedCloudKitZoneSuppliesCanonicalZoneType(t *testing.T) {
	zone := sharedCloudKitZone("com.apple.CloudDocs", "owner", "")
	assert.Equal(t, "REGULAR_CUSTOM_ZONE", zone["zoneType"])
	assert.Equal(t, "owner", zone["ownerRecordName"])
}

func jsonRaw(data []byte) any {
	var value any
	if err := json.Unmarshal(data, &value); err != nil {
		panic(err)
	}
	return value
}
