package api

import (
	"encoding/json"
	"net/url"
	"reflect"
	"strings"
	"testing"
)

func decodeFixtureRequest(t *testing.T, value any) map[string]any {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	return decoded
}

func sharedFixtureShareID() sharedWriteShareID {
	return sharedWriteShareID{
		ShareName:   "<share-record>",
		RecordName:  "<share-record>",
		ShareChange: "<share-change-tag>",
		Zone: sharedWriteZoneID{
			ZoneName:  "com.apple.CloudDocs",
			OwnerName: "<owner-record>",
			ZoneType:  "REGULAR_CUSTOM_ZONE",
		},
	}
}

func TestSharedCreateFolderRequestMatchesCapture(t *testing.T) {
	share := sharedFixtureShareID()
	share.ShareChange = ""
	got := decodeFixtureRequest(t, buildSharedCreateFolderRequest(
		"SHARED_FOLDER::com.apple.CloudDocs::<parent-id>",
		"test-folder",
		"FOLDER::com.apple.CloudDocs::<client-id>",
		share,
	))
	want := map[string]any{
		"destinationDrivewsId": "SHARED_FOLDER::com.apple.CloudDocs::<parent-id>",
		"folders": []any{map[string]any{
			"name":     "test-folder",
			"clientId": "FOLDER::com.apple.CloudDocs::<client-id>",
		}},
		"shareID": map[string]any{
			"shareName":  "<share-record>",
			"recordName": "<share-record>",
			"zoneID": map[string]any{
				"zoneName":        "com.apple.CloudDocs",
				"ownerRecordName": "<owner-record>",
				"zoneType":        "REGULAR_CUSTOM_ZONE",
			},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("createFolders request mismatch\ngot: %#v\nwant: %#v", got, want)
	}
}

func TestSharedRenameFolderRequestMatchesCapture(t *testing.T) {
	got := decodeFixtureRequest(t, buildSharedRenameRequest(
		"FOLDER_IN_SHARED_FOLDER::com.apple.CloudDocs::<folder-id>",
		"test-folder-rename", "", "<etag>", sharedFixtureShareID(),
	))
	want := map[string]any{"items": []any{map[string]any{
		"drivewsid": "FOLDER_IN_SHARED_FOLDER::com.apple.CloudDocs::<folder-id>",
		"shareID": map[string]any{
			"shareName": "<share-record>", "recordName": "<share-record>", "shareChangeTag": "<share-change-tag>",
			"zoneID": map[string]any{"zoneName": "com.apple.CloudDocs", "ownerRecordName": "<owner-record>", "zoneType": "REGULAR_CUSTOM_ZONE"},
		},
		"etag": "<etag>", "name": "test-folder-rename",
	}}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("folder rename request mismatch\ngot: %#v\nwant: %#v", got, want)
	}
}

func TestSharedRenameFileRequestMatchesCapture(t *testing.T) {
	got := decodeFixtureRequest(t, buildSharedRenameRequest(
		"FILE_IN_SHARED_FOLDER::com.apple.CloudDocs::<file-id>",
		"test-file-rename", "txt", "<etag>", sharedFixtureShareID(),
	))
	item := got["items"].([]any)[0].(map[string]any)
	if item["extension"] != "txt" {
		t.Fatalf("file rename extension = %#v, want txt", item["extension"])
	}
	if item["drivewsid"] != "FILE_IN_SHARED_FOLDER::com.apple.CloudDocs::<file-id>" {
		t.Fatalf("file rename identity = %#v", item["drivewsid"])
	}
}

func TestSharedDeleteFolderRequestMatchesCapture(t *testing.T) {
	got := decodeFixtureRequest(t, buildSharedDeleteRequest(
		"FOLDER_IN_SHARED_FOLDER::com.apple.CloudDocs::<folder-id>", "<etag>", sharedFixtureShareID(),
	))
	item := got["items"].([]any)[0].(map[string]any)
	if item["drivewsid"] != "FOLDER_IN_SHARED_FOLDER::com.apple.CloudDocs::<folder-id>" || item["clientId"] != item["drivewsid"] {
		t.Fatalf("folder delete request = %#v", item)
	}
	if _, ok := item["force"]; ok {
		t.Fatal("delete fixture must not add unobserved force field")
	}
}

func TestSharedDeleteFileRequestMatchesCapture(t *testing.T) {
	got := decodeFixtureRequest(t, buildSharedDeleteRequest(
		"FILE_IN_SHARED_FOLDER::com.apple.CloudDocs::<file-id>", "<etag>", sharedFixtureShareID(),
	))
	item := got["items"].([]any)[0].(map[string]any)
	if item["drivewsid"] != "FILE_IN_SHARED_FOLDER::com.apple.CloudDocs::<file-id>" {
		t.Fatalf("file delete request = %#v", item)
	}
}

func TestSharedUploadInitializationMatchesCapture(t *testing.T) {
	got := decodeFixtureRequest(t, buildSharedUploadInitializationRequest("test-file.txt", 5))
	want := map[string]any{
		"filename": "test-file.txt", "type": "FILE", "content_type": "text/plain", "size": float64(5),
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("upload initialization mismatch\\ngot: %#v\\nwant: %#v", got, want)
	}
}

func TestSharedUploadCommitMatchesCapture(t *testing.T) {
	got := decodeFixtureRequest(t, buildSharedUploadCommitRequest(
		"<document-id>", "<shared-owner>", "test-file.txt", 5,
		"<signature>", "<wrapping-key>", "<reference-signature>", "<receipt>", 1700000000,
	))
	want := map[string]any{
		"document_id":    "<document-id>",
		"path":           map[string]any{"starting_document_id": "<shared-owner>", "path": "test-file.txt"},
		"allow_conflict": true,
		"file_flags":     map[string]any{"is_writable": true, "is_executable": false, "is_hidden": false},
		"mtime":          float64(1700000000), "btime": float64(1700000000), "command": "add_file",
		"data": map[string]any{
			"signature": "<signature>", "wrapping_key": "<wrapping-key>",
			"reference_signature": "<reference-signature>", "receipt": "<receipt>", "size": float64(5),
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("upload commit mismatch\\ngot: %#v\\nwant: %#v", got, want)
	}
}

func TestSharedUploadCommitPathMatchesCapture(t *testing.T) {
	got := buildSharedUploadCommitPath("<owner-record-name>")
	want := "/ws/com.apple.CloudDocs/update/shared/<owner-record-name>?errorBreakdown=true"
	if got != want {
		t.Fatalf("upload commit path = %q, want %q", got, want)
	}
}

func TestSharedUploadInitializationPathUsesOnlyProvenanceFields(t *testing.T) {
	got := buildSharedUploadInitializationPath("<owner-record-name>", "<share-record>", "<temporary-upload-token>")
	if !strings.Contains(got, "/upload/web/<owner-record-name>/<share-record>") {
		t.Fatalf("upload initialization path = %q", got)
	}
	if !strings.Contains(got, "token="+url.QueryEscape("<temporary-upload-token>")) {
		t.Fatalf("upload initialization path omitted dynamic token: %q", got)
	}
}

func TestSharedUploadStartingDocumentIDUsesNestedFolderDocumentID(t *testing.T) {
	parent := &DriveItem{Drivewsid: "FOLDER_IN_SHARED_FOLDER::com.apple.CloudDocs::<folder-docwsid>", Docwsid: "<folder-docwsid>"}
	parent.ShareID.RecordName = "<share-record>"
	got, err := SharedUploadStartingDocumentID(parent)
	if err != nil {
		t.Fatal(err)
	}
	if got != "<folder-docwsid>" {
		t.Fatalf("nested Shared starting_document_id = %q, want folder docwsid", got)
	}
}

func TestSharedUploadStartingDocumentIDUsesShareRecordForRoot(t *testing.T) {
	parent := &DriveItem{Drivewsid: "SHARED_FOLDER::com.apple.CloudDocs::<root-docwsid>"}
	parent.ShareID.RecordName = "<share-record>"
	got, err := SharedUploadStartingDocumentID(parent)
	if err != nil {
		t.Fatal(err)
	}
	if got != "<share-record>" {
		t.Fatalf("Shared root starting_document_id = %q, want share record", got)
	}
}

func TestSharedUploadStartingDocumentIDRejectsNestedFolderWithoutDocwsid(t *testing.T) {
	parent := &DriveItem{Drivewsid: "FOLDER_IN_SHARED_FOLDER::com.apple.CloudDocs::<folder-docwsid>"}
	parent.ShareID.RecordName = "<share-record>"
	if _, err := SharedUploadStartingDocumentID(parent); err == nil {
		t.Fatal("expected missing nested folder document identity to fail closed")
	}
}
