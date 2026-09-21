package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/lib/rest"
)

const (
	defaultZone        = "com.apple.CloudDocs"
	trashRootID        = "TRASH_ROOT"
	statusOk           = "OK"
	statusEtagConflict = "ETAG_CONFLICT"
)

// DriveService represents an iCloud Drive service.
type DriveService struct {
	icloud       *Client
	RootID       string
	endpoint     string
	docsEndpoint string
}

// SharedReparentFile moves a file inside an iCloud Shared folder by updating
// its CloudDocs documentStructure parent record. This is a server-side move;
// file content is neither uploaded nor downloaded.
func (d *DriveService) SharedReparentFile(ctx context.Context, item, destination *DriveItem) error {
	if item == nil || destination == nil || item.Docwsid == "" {
		return fmt.Errorf("missing Shared file or destination identity")
	}
	shareZone := destination.ShareID.ZoneID
	if shareZone.ZoneName == "" || shareZone.OwnerRecordName == "" || shareZone.ZoneType == "" {
		shareZone = item.ShareID.ZoneID
	}
	zone := sharedCloudKitZone(shareZone.ZoneName, shareZone.OwnerRecordName, shareZone.ZoneType)
	parent, err := sharedDestinationParent(destination)
	if err != nil {
		return err
	}
	lookup := map[string]any{
		"zoneID":  zone,
		"records": []map[string]any{{"recordName": "documentStructure/" + item.Docwsid}},
	}
	var found struct {
		Records []struct {
			RecordName      string         `json:"recordName"`
			RecordChangeTag string         `json:"recordChangeTag"`
			Fields          map[string]any `json:"fields"`
		} `json:"records"`
	}
	if err := d.cloudDocsRequest(ctx, "records/lookup", lookup, &found); err != nil {
		return err
	}
	if len(found.Records) != 1 || found.Records[0].RecordChangeTag == "" {
		return fmt.Errorf("documentStructure record not found for %s", item.Docwsid)
	}
	fields := sharedModifyFields(found.Records[0].Fields, parent, zone)
	record := map[string]any{
		"recordName":      "documentStructure/" + item.Docwsid,
		"recordType":      "structure",
		"recordChangeTag": found.Records[0].RecordChangeTag,
		"fields":          fields,
		"parent":          map[string]any{"recordName": parent},
	}
	modify := map[string]any{"atomic": true, "zoneID": zone, "operations": []map[string]any{{"operationType": "update", "record": record}}}
	var response any
	return d.cloudDocsRequest(ctx, "records/modify", modify, &response)
}

// SharedReparentDirectory moves a Shared folder by updating its CloudDocs
// directory/<uuid> record. Directory records use the same CloudKit
// "structure" reparent envelope as documentStructure records, but their name
// fields do not have a file extension.
func (d *DriveService) SharedReparentDirectory(ctx context.Context, item, destination *DriveItem, name string) error {
	if item == nil || destination == nil || item.Docwsid == "" {
		return fmt.Errorf("missing Shared directory or destination identity")
	}
	if name == "" {
		name = item.Name
	}
	if name == "" {
		return fmt.Errorf("missing Shared directory name")
	}
	shareZone := destination.ShareID.ZoneID
	if shareZone.ZoneName == "" || shareZone.OwnerRecordName == "" || shareZone.ZoneType == "" {
		shareZone = item.ShareID.ZoneID
	}
	zone := sharedCloudKitZone(shareZone.ZoneName, shareZone.OwnerRecordName, shareZone.ZoneType)
	parent, err := sharedDestinationParent(destination)
	if err != nil {
		return err
	}
	recordName := sharedDirectoryRecordName(item.Docwsid)
	lookup := map[string]any{
		"zoneID":  zone,
		"records": []map[string]any{{"recordName": recordName}},
	}
	var found struct {
		Records []struct {
			RecordName      string         `json:"recordName"`
			RecordChangeTag string         `json:"recordChangeTag"`
			Fields          map[string]any `json:"fields"`
		} `json:"records"`
	}
	if err := d.cloudDocsRequest(ctx, "records/lookup", lookup, &found); err != nil {
		return err
	}
	if len(found.Records) != 1 || found.Records[0].RecordChangeTag == "" {
		return fmt.Errorf("directory record not found for %s", item.Docwsid)
	}
	fields := sharedDirectoryModifyFields(found.Records[0].Fields, name, parent, zone)
	record := map[string]any{
		"recordName":      recordName,
		"recordType":      "structure",
		"recordChangeTag": found.Records[0].RecordChangeTag,
		"fields":          fields,
		"parent":          map[string]any{"recordName": parent},
	}
	modify := map[string]any{"atomic": true, "zoneID": zone, "operations": []map[string]any{{"operationType": "update", "record": record}}}
	var response any
	return d.cloudDocsRequest(ctx, "records/modify", modify, &response)
}

func sharedDirectoryRecordName(docwsid string) string {
	return "directory/" + strings.ToUpper(docwsid)
}

func sharedCloudKitZone(zoneName, ownerRecordName, zoneType string) map[string]any {
	if zoneName == "" {
		zoneName = defaultZone
	}
	if zoneType == "" {
		zoneType = "REGULAR_CUSTOM_ZONE"
	}
	zone := map[string]any{
		"zoneName": zoneName,
		"zoneType": zoneType,
	}
	if ownerRecordName != "" {
		zone["ownerRecordName"] = ownerRecordName
	}
	return zone
}

func sharedDirectoryModifyFields(source map[string]any, name, parent string, zone map[string]any) map[string]any {
	fields := make(map[string]any, 3)
	for _, key := range []string{"basehash", "encryptedBasename"} {
		if value, ok := source[key]; ok {
			fields[key] = value
		}
	}
	if _, ok := fields["basehash"]; !ok {
		sum := sha256.Sum256([]byte(name))
		fields["basehash"] = map[string]any{"value": base64.StdEncoding.EncodeToString(sum[:]), "type": "BYTES"}
	}
	if _, ok := fields["encryptedBasename"]; !ok {
		fields["encryptedBasename"] = map[string]any{"value": base64.StdEncoding.EncodeToString([]byte(name)), "type": "ENCRYPTED_BYTES"}
	}
	fields["parent"] = map[string]any{"value": map[string]any{
		"recordName": parent, "action": "VALIDATE", "zoneID": zone,
	}, "type": "REFERENCE"}
	return fields
}

func sharedDestinationParent(destination *DriveItem) (string, error) {
	if destination == nil || destination.ShareID.ShareName == "" {
		return "", fmt.Errorf("destination Shared item has no share identity")
	}
	// The Shared root is represented by SHARED_FOLDER. A child directory is
	// represented by FOLDER_IN_SHARED_FOLDER and must use its document record
	// as the CloudKit directory parent. Do not classify it by shareName alone.
	if strings.HasPrefix(destination.Drivewsid, "FOLDER_IN_SHARED_FOLDER::") || strings.HasPrefix(destination.ParentID, "SHARED_FOLDER::") {
		if destination.Docwsid == "" {
			return "", fmt.Errorf("destination Shared directory has no document identity")
		}
		return "directory/" + destination.Docwsid, nil
	}
	return "directory/" + destination.ShareID.ShareName, nil
}

// sharedModifyFields returns the business fields present in Apple's proven
// documentStructure update envelope. The lookup response contains additional
// response-only fields which must not be echoed in records/modify.
func sharedModifyFields(source map[string]any, parent string, zone map[string]any) map[string]any {
	fields := make(map[string]any, 4)
	for _, key := range []string{"basehash", "encryptedBasename", "extension"} {
		if value, ok := source[key]; ok {
			fields[key] = value
		}
	}
	fields["parent"] = map[string]any{"value": map[string]any{
		"recordName": parent, "action": "VALIDATE", "zoneID": zone,
	}, "type": "REFERENCE"}
	return fields
}

func (d *DriveService) cloudDocsRequest(ctx context.Context, endpoint string, body, response any) error {
	root := d.icloud.Session.AccountInfo.Webservices[WsPhotos].URL
	if root == "" {
		return fmt.Errorf("CloudKit webservice endpoint unavailable")
	}
	requestBytes, err := json.Marshal(body)
	if err != nil {
		return err
	}
	requestURL := root + "/database/1/com.apple.clouddocs/production/shared/" + endpoint + "?remapEnums=true&getCurrentSyncToken=true"
	opts := rest.Opts{
		Method:       "POST",
		RootURL:      requestURL,
		Body:         bytes.NewReader(requestBytes),
		IgnoreStatus: true,
		ExtraHeaders: d.icloud.Session.GetHeaders(map[string]string{"Content-Type": "text/plain"}),
	}
	// Use the session directly. Shared reparent writes must never be retried
	// by the 401/421 reauthentication path in Client.Request.
	var raw json.RawMessage
	resp, err := d.icloud.Session.Request(ctx, opts, nil, &raw)
	status := 0
	if resp != nil {
		status = resp.StatusCode
	}
	if err != nil {
		return err
	}
	if response != nil && len(raw) != 0 {
		if err := json.Unmarshal(raw, response); err != nil {
			return err
		}
	}
	if status < 200 || status > 299 {
		return fmt.Errorf("HTTP error %d (%s) returned body: %q", status, http.StatusText(status), string(raw))
	}
	return nil
}

// LookupSharedShareChangeTag obtains the current change tag for a Shared
// cloudkit.share record. The identity is supplied by authenticated DriveWS
// metadata; this method never derives it from an item ID or an etag.
func (d *DriveService) LookupSharedShareChangeTag(ctx context.Context, recordName, zoneName, ownerRecordName string) (string, error) {
	if recordName == "" || zoneName == "" || ownerRecordName == "" {
		return "", fmt.Errorf("Shared share record identity unavailable")
	}
	zone := map[string]any{
		"zoneName":        zoneName,
		"ownerRecordName": ownerRecordName,
		"zoneType":        "REGULAR_CUSTOM_ZONE",
	}
	lookup := map[string]any{
		"zoneID":  zone,
		"records": []map[string]any{{"recordName": recordName}},
	}
	var found struct {
		Records []struct {
			RecordName      string `json:"recordName"`
			RecordChangeTag string `json:"recordChangeTag"`
			ZoneID          struct {
				ZoneName        string `json:"zoneName"`
				OwnerRecordName string `json:"ownerRecordName"`
			} `json:"zoneID"`
		} `json:"records"`
	}
	if err := d.cloudDocsRequest(ctx, "records/lookup", lookup, &found); err != nil {
		return "", fmt.Errorf("Shared share record lookup: %w", err)
	}
	if len(found.Records) != 1 {
		return "", fmt.Errorf("Shared share record lookup returned %d records", len(found.Records))
	}
	record := found.Records[0]
	if record.RecordName != recordName || record.ZoneID.ZoneName != zoneName || record.ZoneID.OwnerRecordName != ownerRecordName {
		return "", fmt.Errorf("Shared share record lookup identity mismatch")
	}
	if record.RecordChangeTag == "" {
		return "", fmt.Errorf("Shared share record change tag missing")
	}
	return record.RecordChangeTag, nil
}

// NewDriveService creates a new DriveService instance.
func NewDriveService(icloud *Client) (*DriveService, error) {
	return &DriveService{icloud: icloud, RootID: "FOLDER::com.apple.CloudDocs::root", endpoint: icloud.Session.AccountInfo.Webservices[WsDrive].URL, docsEndpoint: icloud.Session.AccountInfo.Webservices[WsDocs].URL}, nil
}

// GetItemByDriveID retrieves a DriveItem by its Drive ID.
func (d *DriveService) GetItemByDriveID(ctx context.Context, id string, includeChildren bool) (*DriveItem, *http.Response, error) {
	items, resp, err := d.GetItemsByDriveID(ctx, []string{id}, includeChildren)
	if err != nil {
		return nil, resp, err
	}
	return items[0], resp, err
}

// GetItemsByDriveID retrieves DriveItems by their Drive IDs.
func (d *DriveService) GetItemsByDriveID(ctx context.Context, ids []string, includeChildren bool) ([]*DriveItem, *http.Response, error) {
	var err error
	_items := buildDriveItemDetailRequest(ids, includeChildren)

	var body *bytes.Reader
	var path string
	if !includeChildren {
		values := []map[string]any{{
			"items": _items,
		}}
		body, err = IntoReader(values)
		if err != nil {
			return nil, nil, err
		}
		path = "/retrieveItemDetails"
	} else {
		values := _items
		body, err = IntoReader(values)
		if err != nil {
			return nil, nil, err
		}
		path = "/retrieveItemDetailsInFolders"
	}

	opts := rest.Opts{
		Method:       "POST",
		Path:         path,
		ExtraHeaders: d.icloud.Session.GetHeaders(map[string]string{}),
		RootURL:      d.endpoint,
		Body:         body,
	}
	var items []*DriveItem
	resp, err := d.icloud.Request(ctx, opts, nil, &items)
	if err != nil {
		return nil, resp, err
	}

	return items, resp, err
}

func buildDriveItemDetailRequest(ids []string, includeHierarchy bool) []map[string]any {
	_items := make([]map[string]any, 0, len(ids))
	for _, id := range ids {
		_items = append(_items, map[string]any{
			"drivewsid":        id,
			"partialData":      false,
			"includeHierarchy": includeHierarchy,
		})
	}
	return _items
}

// GetDocByPath retrieves a document by its path.
func (d *DriveService) GetDocByPath(ctx context.Context, path string) (*Document, *http.Response, error) {
	values := url.Values{}
	values.Set("unified_format", "false")
	body, err := IntoReader(path)
	if err != nil {
		return nil, nil, err
	}
	opts := rest.Opts{
		Method:       "POST",
		Path:         "/ws/" + defaultZone + "/list/lookup_by_path",
		ExtraHeaders: d.icloud.Session.GetHeaders(map[string]string{}),
		RootURL:      d.docsEndpoint,
		Parameters:   values,
		Body:         body,
	}
	var item []*Document
	resp, err := d.icloud.Request(ctx, opts, nil, &item)
	if err != nil {
		return nil, resp, err
	}

	return item[0], resp, err
}

// GetItemByPath retrieves a DriveItem by its path.
func (d *DriveService) GetItemByPath(ctx context.Context, path string) (*DriveItem, *http.Response, error) {
	values := url.Values{}
	values.Set("unified_format", "true")

	body, err := IntoReader(path)
	if err != nil {
		return nil, nil, err
	}
	opts := rest.Opts{
		Method:       "POST",
		Path:         "/ws/" + defaultZone + "/list/lookup_by_path",
		ExtraHeaders: d.icloud.Session.GetHeaders(map[string]string{}),
		RootURL:      d.docsEndpoint,
		Parameters:   values,
		Body:         body,
	}
	var item []*DriveItem
	resp, err := d.icloud.Request(ctx, opts, nil, &item)
	if err != nil {
		return nil, resp, err
	}

	return item[0], resp, err
}

// GetDocByItemID retrieves a document by its item ID.
func (d *DriveService) GetDocByItemID(ctx context.Context, id string) (*Document, *http.Response, error) {
	values := url.Values{}
	values.Set("document_id", id)
	values.Set("unified_format", "false") // important
	opts := rest.Opts{
		Method:       "GET",
		Path:         "/ws/" + defaultZone + "/list/lookup_by_id",
		ExtraHeaders: d.icloud.Session.GetHeaders(map[string]string{}),
		RootURL:      d.docsEndpoint,
		Parameters:   values,
	}
	var item *Document
	resp, err := d.icloud.Request(ctx, opts, nil, &item)
	if err != nil {
		return nil, resp, err
	}

	return item, resp, err
}

// GetItemRawByItemID retrieves a DriveItemRaw by its item ID.
func (d *DriveService) GetItemRawByItemID(ctx context.Context, id string) (*DriveItemRaw, *http.Response, error) {
	opts := rest.Opts{
		Method:       "GET",
		Path:         "/v1/item/" + id,
		ExtraHeaders: d.icloud.Session.GetHeaders(map[string]string{}),
		RootURL:      d.docsEndpoint,
	}
	var item *DriveItemRaw
	resp, err := d.icloud.Request(ctx, opts, nil, &item)
	if err != nil {
		return nil, resp, err
	}

	return item, resp, err
}

// GetItemsInFolder retrieves a list of DriveItemRaw objects in a folder with the given ID.
func (d *DriveService) GetItemsInFolder(ctx context.Context, id string, limit int64) ([]*DriveItemRaw, *http.Response, error) {
	values := url.Values{}
	values.Set("limit", strconv.FormatInt(limit, 10))

	opts := rest.Opts{
		Method:       "GET",
		Path:         "/v1/enumerate/" + id,
		ExtraHeaders: d.icloud.Session.GetHeaders(map[string]string{}),
		RootURL:      d.docsEndpoint,
		Parameters:   values,
	}

	items := struct {
		Items []*DriveItemRaw `json:"drive_item"`
	}{}

	resp, err := d.icloud.Request(ctx, opts, nil, &items)
	if err != nil {
		return nil, resp, err
	}

	return items.Items, resp, err
}

// GetDownloadURLByDriveID retrieves the download URL for a file in the DriveService.
func (d *DriveService) GetDownloadURLByDriveID(ctx context.Context, id string) (string, *http.Response, error) {
	_, zone, docid := DeconstructDriveID(id)
	values := url.Values{}
	values.Set("document_id", docid)

	if zone == "" {
		zone = defaultZone
	}

	opts := rest.Opts{
		Method:       "GET",
		Path:         "/ws/" + zone + "/download/by_id",
		Parameters:   values,
		ExtraHeaders: d.icloud.Session.GetHeaders(map[string]string{}),
		RootURL:      d.docsEndpoint,
	}

	var filer *FileRequest
	resp, err := d.icloud.Request(ctx, opts, nil, &filer)

	if err != nil {
		return "", resp, err
	}

	var url string
	if filer.DataToken != nil {
		url = filer.DataToken.URL
	} else {
		url = filer.PackageToken.URL
	}

	return url, resp, err
}

// DownloadFile downloads a file from the given URL using the provided options.
func (d *DriveService) DownloadFile(ctx context.Context, url string, opt []fs.OpenOption) (*http.Response, error) {
	opts := &rest.Opts{
		Method:       "GET",
		ExtraHeaders: d.icloud.Session.GetHeaders(map[string]string{}),
		RootURL:      url,
		Options:      opt,
	}

	resp, err := d.icloud.srv.Call(ctx, opts)
	// icloud has some weird http codes
	if err != nil && resp != nil && resp.StatusCode == 330 {
		loc, err := resp.Location()
		if err == nil {
			return d.DownloadFile(ctx, loc.String(), opt)
		}
	}
	return resp, err
}

// MoveItemToTrashByItemID moves an item to the trash based on the item ID.
func (d *DriveService) MoveItemToTrashByItemID(ctx context.Context, id, etag string, force bool) (*DriveItem, *http.Response, error) {
	doc, resp, err := d.GetDocByItemID(ctx, id)
	if err != nil {
		return nil, resp, err
	}
	return d.MoveItemToTrashByID(ctx, doc.DriveID(), etag, force)
}

// MoveItemToTrashByID moves an item to the trash based on the item ID.
func (d *DriveService) MoveItemToTrashByID(ctx context.Context, drivewsid, etag string, force bool) (*DriveItem, *http.Response, error) {
	values := map[string]any{
		"items": []map[string]any{{
			"drivewsid": drivewsid,
			"etag":      etag,
			"clientId":  drivewsid,
		}}}

	body, err := IntoReader(values)
	if err != nil {
		return nil, nil, err
	}

	opts := rest.Opts{
		Method:       "POST",
		Path:         "/moveItemsToTrash",
		ExtraHeaders: d.icloud.Session.GetHeaders(map[string]string{}),
		RootURL:      d.endpoint,
		Body:         body,
	}

	item := struct {
		Items []*DriveItem `json:"items"`
	}{}
	resp, err := d.icloud.Request(ctx, opts, nil, &item)

	if err != nil {
		return nil, resp, err
	}

	if item.Items[0].Status != statusOk {
		// rerun with latest etag
		if force && item.Items[0].Status == "ETAG_CONFLICT" {
			return d.MoveItemToTrashByID(ctx, drivewsid, item.Items[0].Etag, false)
		}

		err = newRequestError(item.Items[0].Status, "unknown request status")
	}

	return item.Items[0], resp, err
}

// GetRecentlyDeleted returns the authenticated contents of iCloud Drive's
// Recently Deleted collection. Apple Web addresses this virtual folder by the
// protocol constant TRASH_ROOT.
func (d *DriveService) GetRecentlyDeleted(ctx context.Context) ([]*DriveItem, *http.Response, error) {
	roots, resp, err := d.GetItemsByDriveID(ctx, []string{trashRootID}, true)
	if err != nil {
		return nil, resp, err
	}
	if len(roots) != 1 || roots[0] == nil {
		return nil, resp, fmt.Errorf("Recently Deleted response contained no root item")
	}
	return roots[0].Items, resp, nil
}

func buildRecentlyDeletedRequest(items []*DriveItem, includeClientID bool) (map[string]any, error) {
	if len(items) == 0 {
		return nil, fmt.Errorf("Recently Deleted mutation requires at least one item")
	}
	requestItems := make([]map[string]any, 0, len(items))
	seen := make(map[string]struct{}, len(items))
	for _, item := range items {
		if item == nil || item.Drivewsid == "" || item.Etag == "" {
			return nil, fmt.Errorf("Recently Deleted item identity unavailable")
		}
		if !strings.HasPrefix(item.Drivewsid, "FILE::") && !strings.HasPrefix(item.Drivewsid, "FOLDER::") {
			return nil, fmt.Errorf("Recently Deleted mutation only supports Personal file and folder identities")
		}
		if _, ok := seen[item.Drivewsid]; ok {
			return nil, fmt.Errorf("duplicate Recently Deleted identity")
		}
		seen[item.Drivewsid] = struct{}{}
		requestItem := map[string]any{
			"drivewsid": item.Drivewsid,
			"etag":      item.Etag,
		}
		if includeClientID {
			requestItem["clientId"] = item.Drivewsid
		}
		requestItems = append(requestItems, requestItem)
	}
	return map[string]any{"items": requestItems}, nil
}

// mutateRecentlyDeleted sends one captured Apple Web mutation exactly once.
// It deliberately bypasses Client.Request's reauthentication retry path: an
// uncertain write outcome must be resolved by an independent trash READ.
func (d *DriveService) mutateRecentlyDeleted(ctx context.Context, endpoint string, items []*DriveItem, includeClientID bool) (*http.Response, error) {
	values, err := buildRecentlyDeletedRequest(items, includeClientID)
	if err != nil {
		return nil, err
	}
	body, err := IntoReader(values)
	if err != nil {
		return nil, err
	}
	opts := rest.Opts{
		Method:       "POST",
		Path:         endpoint,
		ExtraHeaders: d.icloud.Session.GetHeaders(map[string]string{}),
		RootURL:      d.endpoint,
		Body:         body,
	}
	var response json.RawMessage
	return d.icloud.Session.Request(ctx, opts, nil, &response)
}

// PermanentlyDeleteItems applies the captured deleteItems protocol to items
// obtained from GetRecentlyDeleted.
func (d *DriveService) PermanentlyDeleteItems(ctx context.Context, items []*DriveItem) (*http.Response, error) {
	return d.mutateRecentlyDeleted(ctx, "/deleteItems", items, true)
}

// RecoverItems applies the captured putBackItemsFromTrash protocol to items
// obtained from GetRecentlyDeleted.
func (d *DriveService) RecoverItems(ctx context.Context, items []*DriveItem) (*http.Response, error) {
	return d.mutateRecentlyDeleted(ctx, "/putBackItemsFromTrash", items, false)
}

// CreateNewFolderByItemID creates a new folder by item ID.
func (d *DriveService) CreateNewFolderByItemID(ctx context.Context, id, name string) (*DriveItem, *http.Response, error) {
	doc, resp, err := d.GetDocByItemID(ctx, id)
	if err != nil {
		return nil, resp, err
	}
	return d.CreateNewFolderByDriveID(ctx, doc.DriveID(), name)
}

// CreateNewFolderByDriveID creates a new folder by its Drive ID.
func (d *DriveService) CreateNewFolderByDriveID(ctx context.Context, drivewsid, name string) (*DriveItem, *http.Response, error) {
	values := map[string]any{
		"destinationDrivewsId": drivewsid,
		"folders": []map[string]any{{
			"clientId": "FOLDER::UNKNOWN_ZONE::TempId-" + uuid.New().String(),
			"name":     name,
		}},
	}

	body, err := IntoReader(values)
	if err != nil {
		return nil, nil, err
	}

	opts := rest.Opts{
		Method:       "POST",
		Path:         "/createFolders",
		ExtraHeaders: d.icloud.Session.GetHeaders(map[string]string{}),
		RootURL:      d.endpoint,
		Body:         body,
	}
	var fResp *CreateFoldersResponse
	resp, err := d.icloud.Request(ctx, opts, nil, &fResp)
	if err != nil {
		return nil, resp, err
	}
	status := fResp.Folders[0].Status
	if status != statusOk {
		err = newRequestError(status, "unknown request status")
	}

	return fResp.Folders[0], resp, err
}

// RenameItemByItemID renames a DriveItem by its item ID.
func (d *DriveService) RenameItemByItemID(ctx context.Context, id, etag, name string, force bool) (*DriveItem, *http.Response, error) {
	doc, resp, err := d.GetDocByItemID(ctx, id)
	if err != nil {
		return nil, resp, err
	}
	return d.RenameItemByDriveID(ctx, doc.DriveID(), doc.Etag, name, force)
}

// RenameItemByDriveID renames a DriveItem by its drive ID.
func (d *DriveService) RenameItemByDriveID(ctx context.Context, id, etag, name string, force bool) (*DriveItem, *http.Response, error) {
	values := map[string]any{
		"items": []map[string]any{{
			"drivewsid": id,
			"name":      name,
			"etag":      etag,
			// "extension": split[1],
		}},
	}

	requestBytes, err := json.Marshal(values)
	if err != nil {
		return nil, nil, err
	}

	opts := rest.Opts{
		Method:       "POST",
		Path:         "/renameItems",
		ExtraHeaders: d.icloud.Session.GetHeaders(map[string]string{}),
		RootURL:      d.endpoint,
		Body:         bytes.NewReader(requestBytes),
	}
	var items *DriveItem
	resp, err := d.icloud.Request(ctx, opts, nil, &items)

	if err != nil {
		return nil, resp, err
	}

	status := items.Items[0].Status
	if status != statusOk {
		// rerun with latest etag
		if force && status == "ETAG_CONFLICT" {
			return d.RenameItemByDriveID(ctx, id, items.Items[0].Etag, name, false)
		}
		err = newRequestErrorf(status, "unknown inner status for: %s %s", opts.Method, resp.Request.URL)
	}

	return items.Items[0], resp, err
}

// MoveItemByItemID moves an item by its item ID to a destination item ID.
func (d *DriveService) MoveItemByItemID(ctx context.Context, id, etag, dstID string, force bool) (*DriveItem, *http.Response, error) {
	docSrc, resp, err := d.GetDocByItemID(ctx, id)
	if err != nil {
		return nil, resp, err
	}
	docDst, resp, err := d.GetDocByItemID(ctx, dstID)
	if err != nil {
		return nil, resp, err
	}
	return d.MoveItemByDriveID(ctx, docSrc.DriveID(), docSrc.Etag, docDst.DriveID(), force)
}

// SharedDestinationContext is the authenticated destination context required
// by DriveWS when a personal-zone item enters a Shared zone. The values come
// from current Shared hierarchy metadata and the share-record lookup; this
// type never derives protocol identities from a synthetic cache ID.
type SharedDestinationContext struct {
	DestinationDrivewsID string
	ShareName            string
	RecordName           string
	ShareChangeTag       string
	ZoneName             string
	OwnerRecordName      string
	ZoneType             string
}

func newSharedDestinationContext(destination *DriveItem) (SharedDestinationContext, error) {
	if destination == nil || destination.Drivewsid == "" || destination.ShareID.ShareName == "" ||
		destination.ShareID.RecordName == "" || destination.ShareID.ShareChangeTag == "" ||
		destination.ShareID.ZoneID.ZoneName == "" || destination.ShareID.ZoneID.OwnerRecordName == "" {
		return SharedDestinationContext{}, fmt.Errorf("incomplete authenticated Shared destination metadata")
	}
	return SharedDestinationContext{
		DestinationDrivewsID: destination.Drivewsid,
		ShareName:            destination.ShareID.ShareName,
		RecordName:           destination.ShareID.RecordName,
		ShareChangeTag:       destination.ShareID.ShareChangeTag,
		ZoneName:             destination.ShareID.ZoneID.ZoneName,
		OwnerRecordName:      destination.ShareID.ZoneID.OwnerRecordName,
		ZoneType:             destination.ShareID.ZoneID.ZoneType,
	}, nil
}

func sharedMoveShareID(destination SharedDestinationContext) map[string]any {
	zone := map[string]any{
		"zoneName":        destination.ZoneName,
		"ownerRecordName": destination.OwnerRecordName,
	}
	return map[string]any{
		"shareName":      destination.ShareName,
		"recordName":     destination.RecordName,
		"shareChangeTag": destination.ShareChangeTag,
		"zoneID":         zone,
	}
}

func validateSharedMoveDestination(destination SharedDestinationContext) error {
	if destination.DestinationDrivewsID == "" || destination.ShareName == "" || destination.RecordName == "" ||
		destination.ShareChangeTag == "" || destination.ZoneName == "" || destination.OwnerRecordName == "" {
		return fmt.Errorf("incomplete Shared move context")
	}
	if !strings.HasPrefix(destination.DestinationDrivewsID, "SHARED_FOLDER::") &&
		!strings.HasPrefix(destination.DestinationDrivewsID, "FOLDER_IN_SHARED_FOLDER::") {
		return fmt.Errorf("Shared move destination is not a canonical Shared directory")
	}
	return nil
}

func buildPersonalToSharedMoveRequest(id, etag string, destination SharedDestinationContext) ([]byte, error) {
	if err := validateSharedMoveDestination(destination); err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{
		"items":                []map[string]any{{"drivewsid": id, "etag": etag, "clientId": id}},
		"destinationDrivewsId": destination.DestinationDrivewsID,
		"shareID":              sharedMoveShareID(destination),
	})
}

// buildSharedToPersonalMoveRequest matches the authenticated Web move shape:
// source Shared context is attached to items[0], while the destination is an
// ordinary caller-zone DriveWS directory.
func buildSharedToPersonalMoveRequest(id, etag, destinationDrivewsID string, source SharedDestinationContext) ([]byte, error) {
	if !strings.HasPrefix(id, "FILE_IN_SHARED_FOLDER::") && !strings.HasPrefix(id, "FOLDER_IN_SHARED_FOLDER::") {
		return nil, fmt.Errorf("invalid canonical Shared move source")
	}
	if etag == "" {
		return nil, fmt.Errorf("Shared move source etag unavailable")
	}
	if source.ShareName == "" || source.RecordName == "" || source.ShareChangeTag == "" || source.ZoneName == "" || source.OwnerRecordName == "" {
		return nil, fmt.Errorf("incomplete Shared source context")
	}
	if destinationDrivewsID == "" || !strings.HasPrefix(destinationDrivewsID, "FOLDER::") {
		return nil, fmt.Errorf("invalid Personal move destination")
	}
	return json.Marshal(map[string]any{
		"items": []map[string]any{{
			"drivewsid": id,
			"shareID":   sharedMoveShareID(source),
			"etag":      etag,
			"clientId":  id,
		}},
		"destinationDrivewsId": destinationDrivewsID,
	})
}

// ResolveSharedDestinationContext obtains the current Shared share record
// change tag from authenticated CloudKit metadata before a cross-zone move.
func (d *DriveService) ResolveSharedDestinationContext(ctx context.Context, destination *DriveItem) (SharedDestinationContext, error) {
	if destination == nil || destination.Drivewsid == "" || destination.ShareID.ShareName == "" || destination.ShareID.RecordName == "" || destination.ShareID.ZoneID.ZoneName == "" || destination.ShareID.ZoneID.OwnerRecordName == "" {
		return SharedDestinationContext{}, fmt.Errorf("Shared destination lacks DriveWS share identity")
	}
	tag, err := d.LookupSharedShareChangeTag(ctx, destination.ShareID.RecordName, destination.ShareID.ZoneID.ZoneName, destination.ShareID.ZoneID.OwnerRecordName)
	if err != nil {
		return SharedDestinationContext{}, err
	}
	destinationCopy := *destination
	destinationCopy.ShareID.ShareChangeTag = tag
	return newSharedDestinationContext(&destinationCopy)
}

// MoveItemByDriveIDWithSharedContext moves a caller-zone item into a Shared
// directory. It is deliberately non-retrying: callers must reconcile an
// uncertain mutation before any later submission.
func (d *DriveService) MoveItemByDriveIDWithSharedContext(ctx context.Context, id, etag string, destination SharedDestinationContext) (*DriveItem, *http.Response, error) {
	body, err := buildPersonalToSharedMoveRequest(id, etag, destination)
	if err != nil {
		return nil, nil, err
	}
	return d.moveItemRequest(ctx, body, false, true, id, etag, destination.DestinationDrivewsID)
}

// MoveSharedItemToPersonal moves a Shared-zone item into a caller-zone
// directory using the source ShareID carried by the Web /moveItems schema.
func (d *DriveService) MoveSharedItemToPersonal(ctx context.Context, item *DriveItem, destinationDrivewsID string) (*DriveItem, *http.Response, error) {
	if item == nil {
		return nil, nil, fmt.Errorf("Shared source item unavailable")
	}
	source, err := newSharedDestinationContext(&DriveItem{
		Drivewsid: item.Drivewsid,
		ShareID:   item.ShareID,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("Shared source context: %w", err)
	}
	body, err := buildSharedToPersonalMoveRequest(item.Drivewsid, item.Etag, destinationDrivewsID, source)
	if err != nil {
		return nil, nil, err
	}
	return d.moveItemRequest(ctx, body, false, true, item.Drivewsid, item.Etag, destinationDrivewsID)
}

// MoveItemByDocID moves an item by its doc ID.
// func (d *DriveService) MoveItemByDocID(ctx context.Context, srcDocID, srcEtag, dstDocID string, force bool) (*DriveItem, *http.Response, error) {
// 	return d.MoveItemByDriveID(ctx, srcDocID, srcEtag, docDst.DriveID(), force)
// }

// MoveItemByDriveID moves an item by its drive ID.
func (d *DriveService) MoveItemByDriveID(ctx context.Context, id, etag, dstID string, force bool) (*DriveItem, *http.Response, error) {
	values := map[string]any{
		"destinationDrivewsId": dstID,
		"items": []map[string]any{{
			"drivewsid": id,
			"etag":      etag,
			"clientId":  id,
		}},
	}

	body, err := json.Marshal(values)
	if err != nil {
		return nil, nil, err
	}
	return d.moveItemRequest(ctx, body, force, false, id, etag, dstID)
}

func (d *DriveService) moveItemRequest(ctx context.Context, body []byte, force, noWriteRetry bool, id, etag, dstID string) (*DriveItem, *http.Response, error) {
	opts := rest.Opts{
		Method:       "POST",
		Path:         "/moveItems",
		ExtraHeaders: d.icloud.Session.GetHeaders(map[string]string{}),
		RootURL:      d.endpoint,
		Body:         bytes.NewReader(body),
	}

	var raw json.RawMessage
	var resp *http.Response
	var err error
	if noWriteRetry {
		resp, err = d.icloud.Session.Request(ctx, opts, nil, &raw)
	} else {
		resp, err = d.icloud.Request(ctx, opts, nil, &raw)
	}
	var items *DriveItem
	if len(raw) != 0 {
		if decodeErr := json.Unmarshal(raw, &items); decodeErr != nil && err == nil {
			err = decodeErr
		}
	}
	if err != nil {
		return nil, resp, err
	}
	if items == nil || len(items.Items) == 0 {
		return nil, resp, fmt.Errorf("moveItems returned no items")
	}

	status := items.Items[0].Status
	if status != statusOk {
		// rerun with latest etag
		if force && !noWriteRetry && status == "ETAG_CONFLICT" {
			return d.MoveItemByDriveID(ctx, id, items.Items[0].Etag, dstID, false)
		}
		requestURL := ""
		if resp != nil && resp.Request != nil && resp.Request.URL != nil {
			requestURL = resp.Request.URL.String()
		}
		err = newRequestErrorf(status, "unknown inner status for: %s %s", opts.Method, requestURL)
	}

	return items.Items[0], resp, err
}

// CopyDocByItemID copies a document by its item ID.
func (d *DriveService) CopyDocByItemID(ctx context.Context, itemID string) (*DriveItemRaw, *http.Response, error) {
	if itemID == "" {
		return nil, nil, fmt.Errorf("copy source item_id is required")
	}
	// putting name in info doesn't work. extension does work so assume this is a bug in the endpoint
	values := map[string]any{
		"info_to_update": map[string]any{},
	}

	body, err := IntoReader(values)
	if err != nil {
		return nil, nil, err
	}
	opts := rest.Opts{
		Method:       "POST",
		Path:         "/v1/item/copy/" + itemID,
		ExtraHeaders: d.icloud.Session.GetHeaders(map[string]string{}),
		RootURL:      d.docsEndpoint,
		Body:         body,
	}

	var info *DriveItemRaw
	// A copy is not idempotent. Bypass Client.Request's reauthentication
	// retry path so the mutation is never submitted twice.
	resp, err := d.icloud.Session.Request(ctx, opts, nil, &info)
	if err != nil {
		return nil, resp, err
	}
	if info == nil || info.ItemID == "" {
		return nil, resp, fmt.Errorf("copy response contained no item_id")
	}
	return info, resp, err
}

// CreateUpload creates an url for an upload.
func (d *DriveService) CreateUpload(ctx context.Context, size int64, name, zone string) (*UploadResponse, *http.Response, error) {
	// first we need to request an upload url
	values := map[string]any{
		"filename":     name,
		"type":         "FILE",
		"size":         strconv.FormatInt(size, 10),
		"content_type": GetContentTypeForFile(name),
	}
	body, err := IntoReader(values)
	if err != nil {
		return nil, nil, err
	}

	opts := rest.Opts{
		Method:       "POST",
		Path:         "/ws/" + zone + "/upload/web",
		ExtraHeaders: d.icloud.Session.GetHeaders(map[string]string{}),
		RootURL:      d.docsEndpoint,
		Body:         body,
	}
	var responseInfo []*UploadResponse
	resp, err := d.icloud.Request(ctx, opts, nil, &responseInfo)
	if err != nil {
		return nil, resp, err
	}
	return responseInfo[0], resp, err
}

// Upload uploads a file to the given url
func (d *DriveService) Upload(ctx context.Context, in io.Reader, size int64, name, uploadURL string) (*SingleFileResponse, *http.Response, error) {
	// TODO: implement multipart upload
	opts := rest.Opts{
		Method:        "POST",
		ExtraHeaders:  d.icloud.Session.GetHeaders(map[string]string{}),
		RootURL:       uploadURL,
		Body:          in,
		ContentLength: &size,
		ContentType:   GetContentTypeForFile(name),
		// MultipartContentName: "files",
		MultipartFileName: name,
	}
	var singleFileResponse *SingleFileResponse
	resp, err := d.icloud.Request(ctx, opts, nil, &singleFileResponse)
	if err != nil {
		return nil, resp, err
	}
	return singleFileResponse, resp, err
}

// UpdateFile updates a file in the DriveService.
//
// ctx: the context.Context object for the request.
// r: a pointer to the UpdateFileInfo struct containing the information for the file update.
// Returns a pointer to the DriveItem struct representing the updated file, the http.Response object, and an error if any.
func (d *DriveService) UpdateFile(ctx context.Context, r *UpdateFileInfo, zone string) (*DriveItem, *http.Response, error) {
	return d.updateFile(ctx, r, zone, false)
}

// UpdateFileNoRetry performs the same document update without the automatic
// reauthentication retry used by ordinary API reads. It is used to finish a
// newly copied document without risking a second mutation submission.
func (d *DriveService) UpdateFileNoRetry(ctx context.Context, r *UpdateFileInfo, zone string) (*DriveItem, *http.Response, error) {
	return d.updateFile(ctx, r, zone, true)
}

func (d *DriveService) updateFile(ctx context.Context, r *UpdateFileInfo, zone string, noRetry bool) (*DriveItem, *http.Response, error) {
	body, err := IntoReader(r)
	if err != nil {
		return nil, nil, err
	}
	opts := rest.Opts{
		Method:       "POST",
		Path:         "/ws/" + zone + "/update/documents",
		ExtraHeaders: d.icloud.Session.GetHeaders(map[string]string{}),
		RootURL:      d.docsEndpoint,
		Body:         body,
	}
	var responseInfo *DocumentUpdateResponse
	var resp *http.Response
	if noRetry {
		resp, err = d.icloud.Session.Request(ctx, opts, nil, &responseInfo)
	} else {
		resp, err = d.icloud.Request(ctx, opts, nil, &responseInfo)
	}
	if err != nil {
		return nil, resp, err
	}
	if responseInfo == nil || len(responseInfo.Results) == 0 || responseInfo.Results[0].Document == nil {
		return nil, resp, fmt.Errorf("update response contained no document")
	}

	doc := responseInfo.Results[0].Document
	item := DriveItem{
		Drivewsid:    ConstructDriveID(doc.DocumentID, zone, "FILE"),
		Docwsid:      doc.DocumentID,
		Itemid:       doc.ItemID,
		Etag:         doc.Etag,
		ParentID:     doc.ParentID,
		DateModified: time.Unix(r.Mtime, 0),
		DateCreated:  time.Unix(r.Mtime, 0),
		Type:         doc.Type,
		Name:         doc.Name,
		Size:         doc.Size,
	}

	return &item, resp, err
}

// UpdateFileInfo represents the information for an update to a file in the DriveService.
type UpdateFileInfo struct {
	AllowConflict   bool   `json:"allow_conflict"`
	Btime           int64  `json:"btime"`
	Command         string `json:"command"`
	CreateShortGUID bool   `json:"create_short_guid"`
	Data            struct {
		Receipt            string `json:"receipt,omitempty"`
		ReferenceSignature string `json:"reference_signature,omitempty"`
		Signature          string `json:"signature,omitempty"`
		Size               int64  `json:"size,omitempty"`
		WrappingKey        string `json:"wrapping_key,omitempty"`
	} `json:"data"`
	DocumentID string    `json:"document_id"`
	FileFlags  FileFlags `json:"file_flags"`
	Mtime      int64     `json:"mtime"`
	Path       struct {
		Path               string `json:"path"`
		StartingDocumentID string `json:"starting_document_id"`
	} `json:"path"`
}

// FileFlags defines the file flags for a document.
type FileFlags struct {
	IsExecutable bool `json:"is_executable"`
	IsHidden     bool `json:"is_hidden"`
	IsWritable   bool `json:"is_writable"`
}

// NewUpdateFileInfo creates a new UpdateFileInfo object with default values.
//
// Returns an UpdateFileInfo object.
func NewUpdateFileInfo() UpdateFileInfo {
	return UpdateFileInfo{
		Command:         "add_file",
		CreateShortGUID: true,
		AllowConflict:   true,
		FileFlags: FileFlags{
			IsExecutable: true,
			IsHidden:     false,
			IsWritable:   true,
		},
	}
}

// DriveItemRaw is a raw drive item.
// not suure what to call this but there seems to be a "unified" and non "unified" drive item response. This is the non unified.
type DriveItemRaw struct {
	ItemID   string            `json:"item_id"`
	ItemInfo *DriveItemRawInfo `json:"item_info"`
	UToken   string            `json:"utoken"`
}

// SplitName splits the name of a DriveItemRaw into its name and extension.
//
// It returns the name and extension as separate strings. If the name ends with a dot,
// it means there is no extension, so an empty string is returned for the extension.
// If the name does not contain a dot, it means
func (d *DriveItemRaw) SplitName() (string, string) {
	name := d.ItemInfo.Name
	// ends with a dot, no extension
	if strings.HasSuffix(name, ".") {
		return name, ""
	}
	lastInd := strings.LastIndex(name, ".")

	if lastInd == -1 {
		return name, ""
	}
	return name[:lastInd], name[lastInd+1:]
}

// ModTime returns the modification time of the DriveItemRaw.
//
// It parses the ModifiedAt field of the ItemInfo struct and converts it to a time.Time value.
// If the parsing fails, it returns the zero value of time.Time.
// The returned time.Time value represents the modification time of the DriveItemRaw.
func (d *DriveItemRaw) ModTime() time.Time {
	i, err := strconv.ParseInt(d.ItemInfo.ModifiedAt, 10, 64)
	if err != nil {
		return time.Time{}
	}
	return time.UnixMilli(i)
}

// CreatedTime returns the creation time of the DriveItemRaw.
//
// It parses the CreatedAt field of the ItemInfo struct and converts it to a time.Time value.
// If the parsing fails, it returns the zero value of time.Time.
// The returned time.Time
func (d *DriveItemRaw) CreatedTime() time.Time {
	i, err := strconv.ParseInt(d.ItemInfo.CreatedAt, 10, 64)
	if err != nil {
		return time.Time{}
	}
	return time.UnixMilli(i)
}

// DriveItemRawInfo is the raw information about a drive item.
type DriveItemRawInfo struct {
	Drivewsid string `json:"drivewsid"`
	Docwsid   string `json:"docwsid"`
	ParentID  string `json:"parentId"`
	UToken    string `json:"utoken"`
	Name      string `json:"name"`
	// Extension is absolutely borked on endpoints so dont use it.
	Extension  string `json:"extension"`
	Size       int64  `json:"size,string"`
	Type       string `json:"type"`
	Version    string `json:"version"`
	ModifiedAt string `json:"modified_at"`
	CreatedAt  string `json:"created_at"`
	Urls       struct {
		URLDownload string `json:"url_download"`
	} `json:"urls"`
	ShareInfo struct {
		Participants []struct {
			ParticipantID string `json:"participant_id"`
			Type          string `json:"type"`
		} `json:"participants"`
	} `json:"share_info"`
}

// IntoDriveItem converts a DriveItemRaw into a DriveItem.
//
// It takes no parameters.
// It returns a pointer to a DriveItem.
func (d *DriveItemRaw) IntoDriveItem() *DriveItem {
	name, extension := d.SplitName()
	item := &DriveItem{
		Drivewsid:    d.ItemInfo.Drivewsid,
		Docwsid:      d.ItemInfo.Docwsid,
		Itemid:       d.ItemID,
		Name:         name,
		Extension:    extension,
		Type:         d.ItemInfo.Type,
		Etag:         d.ItemInfo.Version,
		DateModified: d.ModTime(),
		DateCreated:  d.CreatedTime(),
		Size:         d.ItemInfo.Size,
		ParentID:     d.ItemInfo.ParentID,
		Urls:         d.ItemInfo.Urls,
	}
	for _, participant := range d.ItemInfo.ShareInfo.Participants {
		if participant.Type == "OWNER" {
			item.ShareID.ZoneID.OwnerRecordName = participant.ParticipantID
			item.ShareID.ZoneID.ZoneName = defaultZone
			item.ShareID.ZoneID.ZoneType = "REGULAR_CUSTOM_ZONE"
			break
		}
	}
	return item
}

// DocumentUpdateResponse is the response of a document update request.
type DocumentUpdateResponse struct {
	Status struct {
		StatusCode   int    `json:"status_code"`
		ErrorMessage string `json:"error_message"`
	} `json:"status"`
	Results []struct {
		Status struct {
			StatusCode   int    `json:"status_code"`
			ErrorMessage string `json:"error_message"`
		} `json:"status"`
		OperationID any       `json:"operation_id"`
		Document    *Document `json:"document"`
	} `json:"results"`
}

// Document represents a document on iCloud.
type Document struct {
	Status struct {
		StatusCode   int    `json:"status_code"`
		ErrorMessage string `json:"error_message"`
	} `json:"status"`
	DocumentID string `json:"document_id"`
	ItemID     string `json:"item_id"`
	Urls       struct {
		URLDownload string `json:"url_download"`
	} `json:"urls"`
	Etag           string       `json:"etag"`
	ParentID       string       `json:"parent_id"`
	Name           string       `json:"name"`
	Type           string       `json:"type"`
	Deleted        bool         `json:"deleted"`
	Mtime          int64        `json:"mtime"`
	LastEditorName string       `json:"last_editor_name"`
	Data           DocumentData `json:"data"`
	Size           int64        `json:"size"`
	Btime          int64        `json:"btime"`
	Zone           string       `json:"zone"`
	FileFlags      struct {
		IsExecutable bool `json:"is_executable"`
		IsWritable   bool `json:"is_writable"`
		IsHidden     bool `json:"is_hidden"`
	} `json:"file_flags"`
	LastOpenedTime   int64 `json:"lastOpenedTime"`
	RestorePath      any   `json:"restorePath"`
	HasChainedParent bool  `json:"hasChainedParent"`
}

// DriveID returns the drive ID of the Document.
func (d *Document) DriveID() string {
	if d.Zone == "" {
		d.Zone = defaultZone
	}
	return d.Type + "::" + d.Zone + "::" + d.DocumentID
}

// DocumentData represents the data of a document.
type DocumentData struct {
	Signature          string `json:"signature"`
	Owner              string `json:"owner"`
	Size               int64  `json:"size"`
	ReferenceSignature string `json:"reference_signature"`
	WrappingKey        string `json:"wrapping_key"`
	PcsInfo            string `json:"pcsInfo"`
}

// SingleFileResponse is the response of a single file request.
type SingleFileResponse struct {
	SingleFile *SingleFileInfo `json:"singleFile"`
}

// SingleFileInfo represents the information of a single file.
type SingleFileInfo struct {
	ReferenceSignature string `json:"referenceChecksum"`
	Size               int64  `json:"size"`
	Signature          string `json:"fileChecksum"`
	WrappingKey        string `json:"wrappingKey"`
	Receipt            string `json:"receipt"`
}

// UploadResponse is the response of an upload request.
type UploadResponse struct {
	URL        string `json:"url"`
	DocumentID string `json:"document_id"`
}

// FileRequestToken represents the token of a file request.
type FileRequestToken struct {
	URL                string `json:"url"`
	Token              string `json:"token"`
	Signature          string `json:"signature"`
	WrappingKey        string `json:"wrapping_key"`
	ReferenceSignature string `json:"reference_signature"`
}

// FileRequest represents the request of a file.
type FileRequest struct {
	DocumentID   string            `json:"document_id"`
	ItemID       string            `json:"item_id"`
	OwnerDsid    int64             `json:"owner_dsid"`
	DataToken    *FileRequestToken `json:"data_token,omitempty"`
	PackageToken *FileRequestToken `json:"package_token,omitempty"`
	DoubleEtag   string            `json:"double_etag"`
}

// CreateFoldersResponse is the response of a create folders request.
type CreateFoldersResponse struct {
	Folders []*DriveItem `json:"folders"`
}

// DriveItem represents an item on iCloud.
type DriveItem struct {
	DateCreated     time.Time   `json:"dateCreated"`
	Drivewsid       string      `json:"drivewsid"`
	Docwsid         string      `json:"docwsid"`
	Itemid          string      `json:"item_id"`
	Zone            string      `json:"zone"`
	Name            string      `json:"name"`
	ParentID        string      `json:"parentId"`
	Hierarchy       []DriveItem `json:"hierarchy"`
	Etag            string      `json:"etag"`
	Type            string      `json:"type"`
	IsDeleted       bool        `json:"isDeleted"`
	AssetQuota      int64       `json:"assetQuota"`
	FileCount       int64       `json:"fileCount"`
	ShareCount      int64       `json:"shareCount"`
	ShareAliasCount int64       `json:"shareAliasCount"`
	ShareID         struct {
		ShareName      string `json:"shareName"`
		RecordName     string `json:"recordName"`
		ShareChangeTag string `json:"shareChangeTag"`
		ZoneID         struct {
			ZoneName        string `json:"zoneName"`
			OwnerRecordName string `json:"ownerRecordName"`
			ZoneType        string `json:"zoneType"`
		} `json:"zoneID"`
	} `json:"shareID"`
	DirectChildrenCount int64        `json:"directChildrenCount"`
	Items               []*DriveItem `json:"items"`
	NumberOfItems       int64        `json:"numberOfItems"`
	Status              string       `json:"status"`
	Extension           string       `json:"extension,omitempty"`
	DateModified        time.Time    `json:"dateModified"`
	DateChanged         time.Time    `json:"dateChanged"`
	Size                int64        `json:"size,omitempty"`
	LastOpenTime        time.Time    `json:"lastOpenTime"`
	Urls                struct {
		URLDownload string `json:"url_download"`
	} `json:"urls"`
}

// IsFolder returns true if the item is a folder.
func (d *DriveItem) IsFolder() bool {
	return d.Type == "FOLDER" || d.Type == "APP_CONTAINER" || d.Type == "APP_LIBRARY"
}

// DownloadURL returns the download URL of the item.
func (d *DriveItem) DownloadURL() string {
	return d.Urls.URLDownload
}

// FullName returns the full name of the item.
// name + extension
func (d *DriveItem) FullName() string {
	if d.Extension != "" {
		return d.Name + "." + d.Extension
	}
	return d.Name
}

// GetDocIDFromDriveID returns the DocumentID from the drive ID.
func GetDocIDFromDriveID(id string) string {
	split := strings.Split(id, "::")
	return split[len(split)-1]
}

// DeconstructDriveID returns the document type, zone, and document ID from the drive ID.
func DeconstructDriveID(id string) (docType, zone, docid string) {
	split := strings.Split(id, "::")
	if len(split) < 3 {
		return "", "", id
	}
	return split[0], split[1], split[2]
}

// ZoneFromDriveID returns the zone a drive ID belongs to, or the default zone
// for an ID that carries none. Items in an app container -- Obsidian, Pages,
// Shortcuts -- live in that app's zone rather than com.apple.CloudDocs, and a
// write addressed to the wrong zone cannot resolve its parent.
func ZoneFromDriveID(id string) string {
	_, zone, _ := DeconstructDriveID(id)
	if zone == "" {
		return defaultZone
	}
	return zone
}

// ConstructDriveID constructs a drive ID from the given components.
func ConstructDriveID(id string, zone string, t string) string {
	return strings.Join([]string{t, zone, id}, "::")
}

// GetContentTypeForFile detects content type for given file name.
func GetContentTypeForFile(name string) string {
	// detect MIME type by looking at the filename only
	mimeType := mime.TypeByExtension(filepath.Ext(name))
	if mimeType == "" {
		// api requires a mime type passed in
		mimeType = "text/plain"
	}
	return strings.Split(mimeType, ";")[0]
}
