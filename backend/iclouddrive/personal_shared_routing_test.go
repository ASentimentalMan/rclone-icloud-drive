package iclouddrive

import "testing"

func TestPersonalFileToSharedUsesSharedContextRoute(t *testing.T) {
	if !isPersonalFileToSharedMove("FILE::com.apple.CloudDocs::source", true) {
		t.Fatal("Personal FILE to Shared did not select the Shared-context route")
	}
	for _, test := range []struct {
		name              string
		id                string
		destinationShared bool
	}{
		{name: "Personal file to Personal", id: "FILE::com.apple.CloudDocs::source", destinationShared: false},
		{name: "Shared file to Shared", id: "FILE_IN_SHARED_FOLDER::com.apple.CloudDocs::source", destinationShared: true},
		{name: "Personal folder to Shared", id: "FOLDER::com.apple.CloudDocs::source", destinationShared: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			if isPersonalFileToSharedMove(test.id, test.destinationShared) {
				t.Fatal("non-Personal-FILE route selected")
			}
		})
	}
}

func TestPersonalFolderToSharedUsesSharedContextRoute(t *testing.T) {
	if !isPersonalFolderToSharedMove("FOLDER::com.apple.CloudDocs::source-folder", true) {
		t.Fatal("Personal FOLDER to Shared did not select the Shared-context route")
	}
	for _, test := range []struct {
		name              string
		id                string
		destinationShared bool
	}{
		{name: "Personal folder to Personal", id: "FOLDER::com.apple.CloudDocs::source-folder", destinationShared: false},
		{name: "Shared folder to Shared", id: "FOLDER_IN_SHARED_FOLDER::com.apple.CloudDocs::source-folder", destinationShared: true},
		{name: "Personal file to Shared", id: "FILE::com.apple.CloudDocs::source", destinationShared: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			if isPersonalFolderToSharedMove(test.id, test.destinationShared) {
				t.Fatal("non-Personal-FOLDER route selected")
			}
		})
	}
}

func TestSharedObjectsToPersonalUseSourceSharedContextRoute(t *testing.T) {
	for _, test := range []struct {
		name     string
		id       string
		itemID   string
		parentID string
	}{
		{
			name:     "Shared file",
			id:       "FILE_IN_SHARED_FOLDER::com.apple.CloudDocs::source-file",
			itemID:   "source-file-item",
			parentID: "SHARED::shared-root",
		},
		{
			name:     "Shared folder",
			id:       "SHARED::source-folder-item",
			parentID: "SHARED::shared-root",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if !isSharedObjectToPersonalMove(test.id, test.itemID, test.parentID, false) {
				t.Fatal("Shared object to Personal did not select the source-Shared-context route")
			}
			if isSharedObjectToPersonalMove(test.id, test.itemID, test.parentID, true) {
				t.Fatal("Shared object to Shared selected the Personal cross-zone route")
			}
		})
	}
	if isSharedObjectToPersonalMove("FILE::com.apple.CloudDocs::personal-file", "", "FOLDER::com.apple.CloudDocs::root", false) {
		t.Fatal("Personal object selected the Shared-to-Personal route")
	}
}

func TestDirMoveSourceEtagUsesAuthenticatedFolderMetadata(t *testing.T) {
	const (
		sourceID   = "FOLDER::com.apple.CloudDocs::source"
		folderEtag = "folder-current"
		parentEtag = "parent-current"
	)
	got := selectDirMoveSourceEtag(sourceID+"#"+folderEtag, parentEtag, sourceID, folderEtag, nil)
	if got != folderEtag {
		t.Fatalf("selected etag = %q, want authenticated source folder etag %q", got, folderEtag)
	}
}
