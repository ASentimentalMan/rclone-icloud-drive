package iclouddrive

import (
	"testing"

	"github.com/rclone/rclone/backend/iclouddrive/api"
)

func TestMergeSharedCanonicalMetadataPreservesDocumentIdentity(t *testing.T) {
	const itemID = "item-1234567890"
	const documentID = "synthetic-document-id-123456"

	enumerated := &api.DriveItem{
		Itemid:   itemID,
		Name:     "enumerated-name.txt",
		ParentID: "enumerated-parent",
		Etag:     "enumerated-etag",
	}
	canonical := &api.DriveItem{
		Itemid:    itemID,
		Drivewsid: "FILE_IN_SHARED_FOLDER::com.apple.CloudDocs::synthetic-drive-id-123456",
		Docwsid:   documentID,
		ParentID:  "SHARED::synthetic-parent-id-123456",
		Etag:      "canonical-etag",
		Zone:      "synthetic-zone",
	}

	mergeSharedCanonicalMetadata(enumerated, findSharedCanonicalItem([]*api.DriveItem{canonical}, itemID))

	if enumerated.Docwsid != documentID {
		t.Fatalf("canonical docwsid = %q", enumerated.Docwsid)
	}
	if enumerated.Drivewsid != canonical.Drivewsid || enumerated.ParentID != canonical.ParentID {
		t.Fatalf("canonical identity = %#v", enumerated)
	}
	if enumerated.Itemid != itemID || enumerated.Name != "enumerated-name.txt" {
		t.Fatalf("enumerated fields changed = %#v", enumerated)
	}
}

func TestFindSharedCanonicalItemDoesNotSynthesizeIdentity(t *testing.T) {
	item := &api.DriveItem{Itemid: "item-1234567890", Docwsid: "synthetic-document-id-123456"}
	if got := findSharedCanonicalItem([]*api.DriveItem{item}, "different-item-id"); got != nil {
		t.Fatalf("unexpected canonical item = %#v", got)
	}
}
