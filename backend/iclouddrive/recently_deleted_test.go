package iclouddrive

import (
	"context"
	"errors"
	"testing"

	"github.com/rclone/rclone/backend/iclouddrive/api"
	"github.com/rclone/rclone/fs"
)

func TestSelectRecentlyDeletedSingleMultiAndAll(t *testing.T) {
	f := &Fs{}
	items := []*api.DriveItem{
		{Drivewsid: "FILE::com.apple.CloudDocs::file", Etag: "file-etag", Name: "alpha", Extension: "txt", Type: "FILE"},
		{Drivewsid: "FOLDER::com.apple.CloudDocs::folder", Etag: "folder-etag", Name: "folder", Type: "FOLDER"},
	}
	single, err := f.selectRecentlyDeleted(items, []string{"alpha.txt"}, false)
	if err != nil || len(single) != 1 || single[0] != items[0] {
		t.Fatalf("single selection = %#v, %v", single, err)
	}
	multi, err := f.selectRecentlyDeleted(items, []string{items[1].Drivewsid, "alpha.txt"}, false)
	if err != nil || len(multi) != 2 || multi[0] != items[1] || multi[1] != items[0] {
		t.Fatalf("multi selection = %#v, %v", multi, err)
	}
	all, err := f.selectRecentlyDeleted(items, nil, true)
	if err != nil || len(all) != 2 {
		t.Fatalf("all selection = %#v, %v", all, err)
	}
	if _, err := f.selectRecentlyDeleted(items, nil, false); err == nil {
		t.Fatal("permanent-delete accepted an empty selection")
	}
}

func TestSelectRecentlyDeletedRejectsAmbiguousAndMissing(t *testing.T) {
	f := &Fs{}
	items := []*api.DriveItem{
		{Drivewsid: "FILE::com.apple.CloudDocs::one", Etag: "one", Name: "same", Extension: "txt", Type: "FILE"},
		{Drivewsid: "FILE::com.apple.CloudDocs::two", Etag: "two", Name: "same", Extension: "txt", Type: "FILE"},
	}
	if _, err := f.selectRecentlyDeleted(items, []string{"same.txt"}, false); err == nil {
		t.Fatal("ambiguous name was accepted")
	}
	if _, err := f.selectRecentlyDeleted(items, []string{"missing.txt"}, false); err == nil {
		t.Fatal("missing name was accepted")
	}
}

func TestCopyRejectsNonPersonalFileBeforeMutation(t *testing.T) {
	f := &Fs{}
	for _, source := range []*Object{
		{fs: f, driveID: "FILE_IN_SHARED_FOLDER::com.apple.CloudDocs::shared", itemID: "shared", shared: true},
		{fs: f, driveID: "FOLDER::com.apple.CloudDocs::folder", itemID: "folder"},
		{fs: f, driveID: "FILE::com.apple.CloudDocs::file"},
	} {
		_, err := f.Copy(context.Background(), source, "copy")
		if !errors.Is(err, fs.ErrorCantCopy) {
			t.Fatalf("Copy error = %v, want ErrorCantCopy", err)
		}
	}
}

func TestPersonalCopyDestinationUsesFolderDocumentID(t *testing.T) {
	pathID := "FOLDER::com.apple.CloudDocs::destination-document"
	if got := api.GetDocIDFromDriveID(pathID); got != "destination-document" {
		t.Fatalf("copy starting_document_id = %q", got)
	}
}

func TestRecentlyDeletedCommandHelp(t *testing.T) {
	for _, help := range driveCommandHelp {
		if help.Name == "recently-deleted" {
			if help.Short == "" || help.Long == "" {
				t.Fatal("recently-deleted command help is incomplete")
			}
			return
		}
	}
	t.Fatal("recently-deleted command is missing from help")
}
