package iclouddrive

import (
	"testing"

	"github.com/rclone/rclone/backend/iclouddrive/api"
)

func TestSharedReparentedItemUsesDestinationMetadataWithoutRefresh(t *testing.T) {
	source := &api.DriveItem{
		Drivewsid: "FILE_IN_SHARED_FOLDER::com.apple.CloudDocs::file",
		Docwsid:   "file",
		Itemid:    "item-file",
		ParentID:  "SHARED_FOLDER::com.apple.CloudDocs::root",
		Etag:      "hvob",
		Size:      17,
		Type:      "FILE",
	}
	destination := &api.DriveItem{
		Drivewsid: "FOLDER_IN_SHARED_FOLDER::com.apple.CloudDocs::child",
		Docwsid:   "child",
		Itemid:    "item-child",
		ShareID:   source.ShareID,
	}

	got := sharedReparentedItem(source, destination)
	if got == source {
		t.Fatal("result must not alias source metadata")
	}
	if got.ParentID != destination.Drivewsid {
		t.Fatalf("parent = %q, want %q", got.ParentID, destination.Drivewsid)
	}
	if got.Docwsid != source.Docwsid || got.Itemid != source.Itemid || got.Size != 17 {
		t.Fatalf("source metadata was not preserved: %#v", got)
	}
}
