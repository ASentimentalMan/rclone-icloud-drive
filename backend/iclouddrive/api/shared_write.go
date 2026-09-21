package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/google/uuid"
	"github.com/rclone/rclone/lib/rest"
)

// sharedWriteZoneID and sharedWriteShareID model only the share context fields
// observed in the authoritative Shared Web capture. Values are always supplied
// by a prior authenticated READ; this package never manufactures identities.
type sharedWriteZoneID struct {
	ZoneName  string `json:"zoneName"`
	OwnerName string `json:"ownerRecordName"`
	ZoneType  string `json:"zoneType,omitempty"`
}

type sharedWriteShareID struct {
	ShareName   string            `json:"shareName"`
	RecordName  string            `json:"recordName"`
	ShareChange string            `json:"shareChangeTag,omitempty"`
	Zone        sharedWriteZoneID `json:"zoneID"`
}

func sharedWriteShare(item *DriveItem) (sharedWriteShareID, error) {
	if item == nil || item.ShareID.ShareName == "" || item.ShareID.RecordName == "" || item.ShareID.ZoneID.ZoneName == "" || item.ShareID.ZoneID.OwnerRecordName == "" {
		return sharedWriteShareID{}, fmt.Errorf("Shared item has incomplete share context")
	}
	return sharedWriteShareID{
		ShareName:   item.ShareID.ShareName,
		RecordName:  item.ShareID.RecordName,
		ShareChange: item.ShareID.ShareChangeTag,
		Zone: sharedWriteZoneID{
			ZoneName:  item.ShareID.ZoneID.ZoneName,
			OwnerName: item.ShareID.ZoneID.OwnerRecordName,
			ZoneType:  item.ShareID.ZoneID.ZoneType,
		},
	}, nil
}

func (d *DriveService) sharedDriveRequest(ctx context.Context, path string, body, result any) (*http.Response, error) {
	requestBytes, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	return d.icloud.Session.Request(ctx, rest.Opts{
		Method:       "POST",
		Path:         path,
		RootURL:      d.endpoint,
		Body:         bytes.NewReader(requestBytes),
		IgnoreStatus: true,
		ExtraHeaders: d.icloud.Session.GetHeaders(map[string]string{"Content-Type": "text/plain"}),
	}, nil, result)
}

// CreateSharedFolder creates a child using the captured Shared createFolders
// envelope. The parent and share identity must come from a prior READ.
func (d *DriveService) CreateSharedFolder(ctx context.Context, parent *DriveItem, name string) (*DriveItem, *http.Response, error) {
	if parent == nil || parent.Drivewsid == "" {
		return nil, nil, fmt.Errorf("Shared folder parent identity unavailable")
	}
	share, err := sharedWriteShare(parent)
	if err != nil {
		return nil, nil, err
	}
	request := buildSharedCreateFolderRequest(parent.Drivewsid, name, "FOLDER::com.apple.CloudDocs::TempId-"+uuid.NewString(), share)
	var response CreateFoldersResponse
	resp, err := d.sharedDriveRequest(ctx, "/createFolders", request, &response)
	if err != nil {
		return nil, resp, err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, resp, fmt.Errorf("Shared create folder HTTP %d", resp.StatusCode)
	}
	if len(response.Folders) != 1 || response.Folders[0].Status != statusOk {
		return nil, resp, fmt.Errorf("Shared create folder returned no successful item")
	}
	return response.Folders[0], resp, nil
}

// RenameSharedItem applies the captured folder/file renameItems envelope.
func (d *DriveService) RenameSharedItem(ctx context.Context, item *DriveItem, name string) (*DriveItem, *http.Response, error) {
	if item == nil || item.Drivewsid == "" || item.Etag == "" {
		return nil, nil, fmt.Errorf("Shared item rename identity unavailable")
	}
	share, err := sharedWriteShare(item)
	if err != nil {
		return nil, nil, err
	}
	var response struct {
		Items []*DriveItem `json:"items"`
	}
	resp, err := d.sharedDriveRequest(ctx, "/renameItems", buildSharedRenameRequest(item.Drivewsid, name, item.Extension, item.Etag, share), &response)
	if err != nil {
		return nil, resp, err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 || len(response.Items) != 1 || response.Items[0].Status != statusOk {
		return nil, resp, fmt.Errorf("Shared rename returned unsuccessful response")
	}
	return response.Items[0], resp, nil
}

// DeleteSharedItem applies the captured Shared deleteItems soft-delete
// transition. It does not claim or perform permanent deletion.
func (d *DriveService) DeleteSharedItem(ctx context.Context, item *DriveItem) (*DriveItem, *http.Response, error) {
	if item == nil || item.Drivewsid == "" || item.Etag == "" {
		return nil, nil, fmt.Errorf("Shared item delete identity unavailable")
	}
	share, err := sharedWriteShare(item)
	if err != nil {
		return nil, nil, err
	}
	var response struct {
		Items []*DriveItem `json:"items"`
	}
	resp, err := d.sharedDriveRequest(ctx, "/deleteItems", buildSharedDeleteRequest(item.Drivewsid, item.Etag, share), &response)
	if err != nil {
		return nil, resp, err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 || len(response.Items) != 1 || !response.Items[0].IsDeleted {
		return nil, resp, fmt.Errorf("Shared delete did not return isDeleted=true")
	}
	return response.Items[0], resp, nil
}

func buildSharedCreateFolderRequest(parentDrivewsID, name, clientID string, shareID sharedWriteShareID) map[string]any {
	// The captured createFolders request carries the stable share/zone identity,
	// but not the mutable shareChangeTag used by rename/delete.
	shareID.ShareChange = ""
	return map[string]any{
		"destinationDrivewsId": parentDrivewsID,
		"folders": []map[string]any{{
			"name":     name,
			"clientId": clientID,
		}},
		"shareID": shareID,
	}
}

func buildSharedRenameRequest(drivewsID, name, extension, etag string, shareID sharedWriteShareID) map[string]any {
	item := map[string]any{
		"drivewsid": drivewsID,
		"shareID":   shareID,
		"etag":      etag,
		"name":      name,
	}
	if extension != "" {
		item["extension"] = extension
	}
	return map[string]any{"items": []map[string]any{item}}
}

func buildSharedDeleteRequest(drivewsID, etag string, shareID sharedWriteShareID) map[string]any {
	return map[string]any{"items": []map[string]any{{
		"drivewsid": drivewsID,
		"shareID":   shareID,
		"etag":      etag,
		"clientId":  drivewsID,
	}}}
}

func buildSharedUploadInitializationRequest(name string, size int64) map[string]any {
	return map[string]any{
		"filename":     name,
		"type":         "FILE",
		"content_type": GetContentTypeForFile(name),
		"size":         size,
	}
}

func buildSharedUploadCommitRequest(documentID, parentID, name string, size int64, signature, wrappingKey, referenceSignature, receipt string, mtime int64) map[string]any {
	return map[string]any{
		"document_id":    documentID,
		"path":           map[string]any{"starting_document_id": parentID, "path": name},
		"allow_conflict": true,
		"file_flags":     map[string]any{"is_writable": true, "is_executable": false, "is_hidden": false},
		"mtime":          mtime,
		"btime":          mtime,
		"command":        "add_file",
		"data": map[string]any{
			"signature": signature, "wrapping_key": wrappingKey,
			"reference_signature": referenceSignature, "receipt": receipt, "size": size,
		},
	}
}

func buildSharedUploadCommitPath(ownerRecordName string) string {
	return "/ws/" + defaultZone + "/update/shared/" + ownerRecordName + "?errorBreakdown=true"
}

// SharedUploadStartingDocumentID returns the document parent used by the
// Shared add_file operation. The Shared root uses the share record; a nested
// Shared folder uses that folder's canonical document ID.
func SharedUploadStartingDocumentID(parent *DriveItem) (string, error) {
	if parent == nil {
		return "", fmt.Errorf("Shared upload parent unavailable")
	}
	switch {
	case strings.HasPrefix(parent.Drivewsid, "SHARED_FOLDER::"):
		if parent.ShareID.RecordName == "" {
			return "", fmt.Errorf("Shared upload root share record unavailable")
		}
		return parent.ShareID.RecordName, nil
	case strings.HasPrefix(parent.Drivewsid, "FOLDER_IN_SHARED_FOLDER::"):
		if parent.Docwsid == "" {
			return "", fmt.Errorf("Shared upload folder document identity unavailable")
		}
		return parent.Docwsid, nil
	default:
		return "", fmt.Errorf("Shared upload parent canonical identity unavailable")
	}
}

func buildSharedUploadInitializationPath(ownerRecordName, shareRecordName, uploadToken string) string {
	values := url.Values{}
	values.Set("token", uploadToken)
	return "/ws/" + defaultZone + "/upload/web/" + ownerRecordName + "/" + shareRecordName + "?" + values.Encode()
}

// SharedUploadStage carries only dynamic values returned by the preceding
// authenticated request. Secrets are intentionally not serialized or logged.
type SharedUploadStage struct {
	DocumentID         string
	TargetURL          string
	Receipt            string
	Signature          string
	WrappingKey        string
	ReferenceSignature string
	Size               int64
}

func (d *DriveService) CreateSharedUpload(ctx context.Context, parent *DriveItem, size int64, name string) (*UploadResponse, *http.Response, error) {
	if parent == nil || parent.ShareID.ZoneID.OwnerRecordName == "" || parent.ShareID.RecordName == "" {
		return nil, nil, fmt.Errorf("Shared upload context unavailable")
	}
	uploadToken, err := d.icloud.Session.GetValidateToken()
	if err != nil {
		return nil, nil, fmt.Errorf("Shared upload web token: %w", err)
	}
	bodyBytes, err := json.Marshal(buildSharedUploadInitializationRequest(name, size))
	if err != nil {
		return nil, nil, err
	}
	var response []*UploadResponse
	resp, err := d.icloud.Session.Request(ctx, rest.Opts{
		Method: "POST", RootURL: d.docsEndpoint,
		Path: "/ws/" + parent.ShareID.ZoneID.ZoneName + "/upload/web/" + parent.ShareID.ZoneID.OwnerRecordName + "/" + parent.ShareID.RecordName + "?" + url.Values{"token": []string{uploadToken}}.Encode(),
		Body: bytes.NewReader(bodyBytes), IgnoreStatus: true,
		ExtraHeaders: d.icloud.Session.GetHeaders(map[string]string{"Content-Type": "text/plain"}),
	}, nil, &response)
	if err != nil {
		return nil, resp, err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 || len(response) != 1 || response[0].DocumentID == "" || response[0].URL == "" {
		return nil, resp, fmt.Errorf("Shared upload initialization failed with HTTP %d", resp.StatusCode)
	}
	return response[0], resp, nil
}

func (d *DriveService) UploadSharedBinary(ctx context.Context, in io.Reader, size int64, name, targetURL string) (*SingleFileResponse, *http.Response, error) {
	if targetURL == "" {
		return nil, nil, fmt.Errorf("Shared upload target unavailable")
	}
	var response *SingleFileResponse
	resp, err := d.icloud.Session.Request(ctx, rest.Opts{
		Method: "POST", RootURL: targetURL, Body: in, ContentLength: &size,
		ContentType: GetContentTypeForFile(name), IgnoreStatus: true,
		ExtraHeaders: d.icloud.Session.GetHeaders(map[string]string{"Content-Type": "text/plain"}),
	}, nil, &response)
	if err != nil {
		return nil, resp, err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 || response == nil || response.SingleFile == nil {
		return nil, resp, fmt.Errorf("Shared binary upload failed with HTTP %d", resp.StatusCode)
	}
	return response, resp, nil
}

func (d *DriveService) UpdateSharedFile(ctx context.Context, r *UpdateFileInfo, ownerRecordName string) (*DriveItem, *http.Response, error) {
	if r == nil || ownerRecordName == "" {
		return nil, nil, fmt.Errorf("Shared upload commit context unavailable")
	}
	bodyBytes, err := json.Marshal(r)
	if err != nil {
		return nil, nil, err
	}
	endpoint := buildSharedUploadCommitPath(ownerRecordName)
	var response DocumentUpdateResponse
	resp, err := d.icloud.Session.Request(ctx, rest.Opts{
		Method: "POST", RootURL: d.docsEndpoint,
		Path: endpoint, Body: bytes.NewReader(bodyBytes), IgnoreStatus: true,
		ExtraHeaders: d.icloud.Session.GetHeaders(map[string]string{"Content-Type": "text/plain"}),
	}, nil, &response)
	if err != nil {
		return nil, resp, err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 || len(response.Results) != 1 || response.Results[0].Document == nil || response.Results[0].Status.StatusCode != 0 {
		return nil, resp, fmt.Errorf("Shared upload commit failed with HTTP %d", resp.StatusCode)
	}
	doc := response.Results[0].Document
	return &DriveItem{Drivewsid: ConstructDriveID(doc.DocumentID, defaultZone, "FILE_IN_SHARED_FOLDER"), Docwsid: doc.DocumentID, Itemid: doc.ItemID, Etag: doc.Etag, ParentID: doc.ParentID, Name: doc.Name, Type: doc.Type, Size: doc.Size}, resp, nil
}

func sharedUploadOwner(parent *DriveItem) (string, error) {
	if parent == nil || strings.TrimSpace(parent.ShareID.ZoneID.OwnerRecordName) == "" {
		return "", fmt.Errorf("Shared owner record identity unavailable")
	}
	return parent.ShareID.ZoneID.OwnerRecordName, nil
}
