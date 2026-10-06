//go:build !plan9 && !solaris

// Package iclouddrive implements the iCloud Drive backend
package iclouddrive

import (
	"bytes"
	"context"
	"path"

	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/config/configmap"
	"github.com/rclone/rclone/fs/fserrors"

	"github.com/rclone/rclone/backend/iclouddrive/api"
	"github.com/rclone/rclone/fs/hash"
	"github.com/rclone/rclone/lib/dircache"
	"github.com/rclone/rclone/lib/encoder"
	"github.com/rclone/rclone/lib/pacer"

	"golang.org/x/text/unicode/norm"
)

/*
- dirCache operates on relative path to root
- path sanitization
	- rule of thumb: sanitize before use, but store things as-is
	- the paths cached in dirCache are after sanitizing
	- the remote/dir passed in aren't, and are stored as-is
*/

const (
	configAppleID    = "apple_id"
	configPassword   = "password"
	configClientID   = "client_id"
	configCookies    = "cookies"
	configTrustToken = "trust_token"

	minSleep      = 10 * time.Millisecond
	maxSleep      = 2 * time.Second
	decayConstant = 2
)

// Options defines the configuration for this backend
type Options struct {
	AppleID    string               `config:"apple_id"`
	Password   string               `config:"password"`
	TrustToken string               `config:"trust_token"`
	Cookies    string               `config:"cookies"`
	ClientID   string               `config:"client_id"`
	Enc        encoder.MultiEncoder `config:"encoding"`
}

// Fs represents a remote icloud drive
type Fs struct {
	name          string // name of this remote
	root          string // the path we are working on.
	rootID        string
	opt           Options            // parsed config options
	m             configmap.Mapper   // config map for persisting auth state
	features      *fs.Features       // optional features
	dirCache      *dircache.DirCache // Map of directory path to directory id
	icloud        *api.Client
	service       *api.DriveService
	sharedItemIDs map[string]string
	sharedItems   map[string]*api.DriveItem
	pacer         *fs.Pacer // pacer for API calls
}

// Object describes an icloud drive object
type Object struct {
	fs          *Fs       // what this object is part of
	remote      string    // The remote path (relative to the fs.root)
	size        int64     // size of the object (on server, after encryption)
	modTime     time.Time // modification time of the object
	createdTime time.Time // creation time of the object
	driveID     string    // item ID of the object
	docID       string    // document ID of the object
	itemID      string    // item ID of the object
	etag        string
	downloadURL string
	shared      bool
}

// find item by path. Will not return any children for the item
func (f *Fs) findItem(ctx context.Context, dir string) (item *api.DriveItem, found bool, err error) {
	var resp *http.Response
	if err = f.pacer.Call(func() (bool, error) {
		item, resp, err = f.service.GetItemByPath(ctx, path.Join(f.root, dir))
		return shouldRetry(ctx, resp, err)
	}); err != nil {
		if item == nil && resp.StatusCode == 404 {
			return nil, false, nil
		}
		return nil, false, err
	}

	return item, true, nil
}

func (f *Fs) findLeafItem(ctx context.Context, pathID string, leaf string) (item *api.DriveItem, found bool, err error) {
	items, err := f.listAll(ctx, pathID)
	if err != nil {
		return nil, false, err
	}
	for _, item := range items {
		if item.Itemid != "" && item.Drivewsid != "" {
			f.sharedItemIDs[item.Drivewsid] = item.Itemid
			f.sharedItems[item.Drivewsid] = item
		}
		// iCloud returns file names in NFD Unicode normalization, so normalized to NFC for consistent comparison
		if strings.EqualFold(norm.NFC.String(item.FullName()), leaf) {
			return item, true, nil
		}
	}

	return nil, false, nil

}

// FindLeaf finds a directory of name leaf in the folder with ID pathID
func (f *Fs) FindLeaf(ctx context.Context, pathID string, leaf string) (pathIDOut string, found bool, err error) {
	item, found, err := f.findLeafItem(ctx, pathID, leaf)

	if err != nil {
		return "", found, err
	}

	if !found {
		return "", false, err
	}

	if !item.IsFolder() {
		return "", false, fs.ErrorIsFile
	}

	if item.Itemid != "" && (item.ShareID.ShareName != "" || isSharedDirectoryID(item.Drivewsid)) {
		key := "SHARED::" + item.Itemid
		f.sharedItems[key] = item
		return f.IDJoin(key, item.Etag), true, nil
	}
	return f.IDJoin(item.Drivewsid, item.Etag), true, nil
}

// Features implements fs.Fs.
func (f *Fs) Features() *fs.Features {
	return f.features
}

// Hashes are not exposed anywhere
func (f *Fs) Hashes() hash.Set {
	return hash.Set(hash.None)
}

func (f *Fs) purgeCheck(ctx context.Context, dir string, check bool) error {
	root := path.Join(f.root, dir)
	if root == "" {
		return errors.New("can't purge root directory")
	}

	directoryID, etag, err := f.FindDir(ctx, dir, false)
	if err != nil {
		return err
	}

	if check {
		item, found, err := f.findItem(ctx, dir)
		if err != nil {
			return err
		}

		if found && item.DirectChildrenCount > 0 {
			return fs.ErrorDirectoryNotEmpty
		}
	}

	var _ *api.DriveItem
	var resp *http.Response
	if err = f.pacer.Call(func() (bool, error) {
		_, resp, err = f.service.MoveItemToTrashByID(ctx, directoryID, etag, true)
		return retryResultUnknown(ctx, resp, err)
	}); err != nil {
		return err
	}

	// flush everything from the left of the dir
	f.dirCache.FlushDir(dir)

	return nil
}

// Purge all files in the directory specified
//
// Implement this if you have a way of deleting all the files
// quicker than just running Remove() on the result of List()
//
// Return an error if it doesn't exist
func (f *Fs) Purge(ctx context.Context, dir string) error {
	if dir == "" {
		return fs.ErrorCantPurge
	}
	return f.purgeCheck(ctx, dir, false)
}

func (f *Fs) recentlyDeleted(ctx context.Context) ([]*api.DriveItem, error) {
	var items []*api.DriveItem
	var resp *http.Response
	var err error
	if err = f.pacer.Call(func() (bool, error) {
		items, resp, err = f.service.GetRecentlyDeleted(ctx)
		return shouldRetry(ctx, resp, err)
	}); err != nil {
		return nil, err
	}
	return items, nil
}

func (f *Fs) selectRecentlyDeleted(items []*api.DriveItem, selectors []string, allowAll bool) ([]*api.DriveItem, error) {
	if len(selectors) == 0 {
		if !allowAll {
			return nil, fmt.Errorf("at least one Recently Deleted item name or drivewsid is required")
		}
		return append([]*api.DriveItem(nil), items...), nil
	}
	selected := make([]*api.DriveItem, 0, len(selectors))
	seen := make(map[string]struct{}, len(selectors))
	for _, selector := range selectors {
		selector = strings.TrimSpace(selector)
		if selector == "" {
			return nil, fmt.Errorf("Recently Deleted selector must not be empty")
		}
		var matches []*api.DriveItem
		for _, item := range items {
			if item == nil {
				continue
			}
			name := norm.NFC.String(f.opt.Enc.ToStandardName(item.FullName()))
			if selector == item.Drivewsid || norm.NFC.String(selector) == name {
				matches = append(matches, item)
			}
		}
		if len(matches) == 0 {
			return nil, fmt.Errorf("Recently Deleted item %q not found", selector)
		}
		if len(matches) != 1 {
			return nil, fmt.Errorf("Recently Deleted item %q is ambiguous; use its drivewsid", selector)
		}
		item := matches[0]
		if _, ok := seen[item.Drivewsid]; ok {
			return nil, fmt.Errorf("Recently Deleted item %q selected more than once", selector)
		}
		seen[item.Drivewsid] = struct{}{}
		selected = append(selected, item)
	}
	return selected, nil
}

func (f *Fs) mutateRecentlyDeleted(ctx context.Context, items []*api.DriveItem, recover bool) error {
	if len(items) == 0 {
		return nil
	}
	requested := make(map[string]struct{}, len(items))
	for _, item := range items {
		if item == nil || item.Drivewsid == "" {
			return fmt.Errorf("Recently Deleted item identity unavailable")
		}
		requested[item.Drivewsid] = struct{}{}
	}
	var mutationErr error
	// The callback always returns retry=false. Apple writes must be submitted
	// once, then resolved using an independent authenticated READ.
	_ = f.pacer.Call(func() (bool, error) {
		if recover {
			_, mutationErr = f.service.RecoverItems(ctx, items)
		} else {
			_, mutationErr = f.service.PermanentlyDeleteItems(ctx, items)
		}
		return false, mutationErr
	})
	remaining, readErr := f.recentlyDeleted(ctx)
	if readErr != nil {
		if mutationErr != nil {
			return fmt.Errorf("Recently Deleted mutation outcome is ambiguous (%v); reconciliation failed: %w", mutationErr, readErr)
		}
		return fmt.Errorf("Recently Deleted mutation returned success but reconciliation failed: %w", readErr)
	}
	var unresolved []string
	for _, item := range remaining {
		if item != nil {
			if _, ok := requested[item.Drivewsid]; ok {
				unresolved = append(unresolved, item.FullName())
			}
		}
	}
	if len(unresolved) != 0 {
		if mutationErr != nil {
			return fmt.Errorf("Recently Deleted mutation failed and items remain present: %w", mutationErr)
		}
		return fmt.Errorf("Recently Deleted mutation did not reconcile; %d item(s) remain present", len(unresolved))
	}
	if mutationErr != nil {
		fs.Debugf(f, "Recently Deleted mutation returned an error but authenticated READ confirmed the requested transition: %v", mutationErr)
	}
	return nil
}

// CleanUp permanently deletes every item currently present in Recently
// Deleted. The standard rclone cleanup command intentionally has account-wide
// trash semantics even when this Fs has a non-root path.
func (f *Fs) CleanUp(ctx context.Context) error {
	items, err := f.recentlyDeleted(ctx)
	if err != nil {
		return err
	}
	return f.mutateRecentlyDeleted(ctx, items, false)
}

func (f *Fs) listAll(ctx context.Context, dirID string) (items []*api.DriveItem, err error) {
	id, _ := f.parseNormalizedID(dirID)
	if isSharedDirectoryID(id) {
		parent := f.sharedItems[id]
		itemID := ""
		if strings.HasPrefix(id, "SHARED::") {
			itemID = strings.TrimPrefix(id, "SHARED::")
		} else if parent != nil {
			itemID = parent.Itemid
		}
		if parent == nil || itemID == "" || !hasSharedShareIdentity(parent) {
			parent, err = f.resolveSharedDirectoryItem(ctx, id, false, false)
			if err != nil {
				return nil, fmt.Errorf("Shared directory listing identity unavailable: %w", err)
			}
			if strings.HasPrefix(id, "SHARED::") {
				itemID = strings.TrimPrefix(id, "SHARED::")
			} else {
				itemID = parent.Itemid
			}
		}
		if parent == nil || itemID == "" {
			return nil, fmt.Errorf("Shared directory listing item identity unavailable")
		}
		raw, _, err := f.service.GetItemsInFolder(ctx, itemID, 5000)
		if err != nil {
			return nil, err
		}
		var canonical []*api.DriveItem
		if parent != nil {
			canonical = parent.Items
			// A Shared directory obtained from the DriveWS response may not
			// have its children embedded. Enrich from the same READ-only
			// DriveWS metadata endpoint when the parent has a canonical ID;
			// enumeration remains the source of the child listing.
			if len(canonical) == 0 && strings.HasPrefix(parent.Drivewsid, "SHARED_FOLDER::") {
				if details, _, readErr := f.service.GetItemByDriveID(ctx, parent.Drivewsid, true); readErr == nil && details != nil {
					canonical = details.Items
				}
			}
		}
		for _, r := range raw {
			full, _, e := f.service.GetItemRawByItemID(ctx, r.ItemID)
			if e != nil {
				return nil, e
			}
			v := full.IntoDriveItem()
			mergeSharedCanonicalMetadata(v, findSharedCanonicalItem(canonical, r.ItemID))
			if parent != nil {
				v.ParentID = parent.Drivewsid
				v.ShareID = parent.ShareID
			}
			items = append(items, v)
		}
		return items, nil
	}
	var item *api.DriveItem
	var resp *http.Response

	if err = f.pacer.Call(func() (bool, error) {
		id, _ := f.parseNormalizedID(dirID)
		item, resp, err = f.service.GetItemByDriveID(ctx, id, true)
		return shouldRetry(ctx, resp, err)
	}); err != nil {
		return nil, err
	}

	items = item.Items

	for i, item := range items {
		item.Name = f.opt.Enc.ToStandardName(item.Name)
		item.Extension = f.opt.Enc.ToStandardName(item.Extension)
		items[i] = item
	}

	return items, nil
}

// findSharedCanonicalItem finds the same object in the authoritative
// DriveWS metadata response. item_id is the protocol identity shared by the
// Documents enumerate response and DriveWS metadata; no opaque-token parsing
// or identifier synthesis is involved.
func findSharedCanonicalItem(items []*api.DriveItem, itemID string) *api.DriveItem {
	for _, item := range items {
		if item != nil && item.Itemid == itemID {
			return item
		}
	}
	return nil
}

// mergeSharedCanonicalMetadata preserves enumerate fields while carrying
// canonical DriveWS/CloudKit identity forward. The synthetic SHARED cache ID
// is deliberately kept separate from these backend metadata fields.
func mergeSharedCanonicalMetadata(item, canonical *api.DriveItem) {
	if item == nil || canonical == nil {
		return
	}
	item.Drivewsid = canonical.Drivewsid
	item.Docwsid = canonical.Docwsid
	item.Zone = canonical.Zone
	item.ParentID = canonical.ParentID
	item.Etag = canonical.Etag
	item.ShareID = canonical.ShareID
	if item.Name == "" {
		item.Name = canonical.Name
	}
	if item.Type == "" {
		item.Type = canonical.Type
	}
}

// List implements fs.Fs.
func (f *Fs) List(ctx context.Context, dir string) (entries fs.DirEntries, err error) {
	dirRemoteID, err := f.dirCache.FindDir(ctx, dir, false)
	if err != nil {
		return nil, err
	}

	entries = make(fs.DirEntries, 0)
	items, err := f.listAll(ctx, dirRemoteID)

	if err != nil {
		return nil, err
	}

	for _, item := range items {
		id := item.Drivewsid
		name := item.FullName()
		remote := path.Join(dir, name)
		if item.IsFolder() {
			if item.Itemid != "" && (item.ShareID.ShareName != "" || isSharedDirectoryID(item.Drivewsid)) {
				id = "SHARED::" + item.Itemid
				f.sharedItems[id] = item
			}
			jid := f.putFolderCache(id, item.Etag, remote)
			d := fs.NewDir(remote, item.DateModified).SetID(jid).SetSize(item.AssetQuota)
			entries = append(entries, d)
		} else {
			o, err := f.NewObjectFromDriveItem(ctx, remote, item)
			if err != nil {
				return nil, err
			}
			entries = append(entries, o)
		}
	}

	return entries, nil
}

// Mkdir implements fs.Fs.
func (f *Fs) Mkdir(ctx context.Context, dir string) error {
	_, _, err := f.FindDir(ctx, dir, true)
	return err
}

// Name implements fs.Fs.
func (f *Fs) Name() string {
	return f.name
}

// Precision implements fs.Fs.
func (f *Fs) Precision() time.Duration {
	return time.Second
}

// Copy src to this remote using server-side copy operations.
//
// This is stored with the remote path given.
//
// It returns the destination Object and a possible error.
//
// Will only be called if src.Fs().Name() == f.Name()
//
// If it isn't possible then return fs.ErrorCantCopy
//
//nolint:all
func (f *Fs) Copy(ctx context.Context, src fs.Object, remote string) (fs.Object, error) {
	// note: so many calls its only just faster then a reupload for big files.
	srcObj, ok := src.(*Object)
	if !ok {
		fs.Debugf(src, "Can't copy - not same remote type")
		return nil, fs.ErrorCantCopy
	}
	if srcObj.shared || !strings.HasPrefix(srcObj.driveID, "FILE::") || srcObj.itemID == "" {
		return nil, fs.ErrorCantCopy
	}

	file, pathID, _, err := f.FindPath(ctx, remote, true)
	if err != nil {
		return nil, err
	}
	if !strings.HasPrefix(pathID, "FOLDER::") {
		return nil, fs.ErrorCantCopy
	}

	var resp *http.Response
	var info *api.DriveItemRaw

	// make a copy
	if err = f.pacer.Call(func() (bool, error) {
		info, resp, err = f.service.CopyDocByItemID(ctx, srcObj.itemID)
		return false, err
	}); err != nil {
		return nil, fserrors.NoRetryError(fmt.Errorf("copy mutation outcome requires reconciliation: %w", err))
	}

	// renaming in CopyDocByID endpoint does not work :/ so do it the hard way

	// get new document
	var doc *api.Document
	if err = f.pacer.Call(func() (bool, error) {
		doc, resp, err = f.service.GetDocByItemID(ctx, info.ItemID)
		return shouldRetry(ctx, resp, err)
	}); err != nil {
		return nil, fserrors.NoRetryError(fmt.Errorf("copy succeeded but copied item lookup failed: %w", err))
	}

	// build request
	// can't use normal rename as file needs to be "activated" first

	r := api.NewUpdateFileInfo()
	r.DocumentID = doc.DocumentID
	r.Path.Path = file
	r.Path.StartingDocumentID = api.GetDocIDFromDriveID(pathID)
	r.Data.Signature = doc.Data.Signature
	r.Data.ReferenceSignature = doc.Data.ReferenceSignature
	r.Data.WrappingKey = doc.Data.WrappingKey
	r.Data.Size = doc.Data.Size
	r.Mtime = srcObj.modTime.UnixMilli()
	r.Btime = srcObj.modTime.UnixMilli()

	var item *api.DriveItem
	if err = f.pacer.Call(func() (bool, error) {
		item, resp, err = f.service.UpdateFileNoRetry(ctx, &r, api.ZoneFromDriveID(pathID))
		return false, err
	}); err != nil {
		return nil, fserrors.NoRetryError(fmt.Errorf("copy succeeded but destination update outcome requires reconciliation: %w", err))
	}

	o, err := f.NewObjectFromDriveItem(ctx, remote, item)
	if err != nil {
		return nil, fserrors.NoRetryError(fmt.Errorf("copy succeeded but destination object construction failed: %w", err))
	}
	obj := o.(*Object)

	// cheat unit tests
	obj.modTime = srcObj.modTime
	obj.createdTime = srcObj.createdTime

	return obj, nil
}

// Put in to the remote path with the modTime given of the given size
//
// When called from outside an Fs by rclone, src.Size() will always be >= 0.
// But for unknown-sized objects (indicated by src.Size() == -1), Put should either
// return an error or upload it properly (rather than e.g. calling panic).
//
// May create the object even if it returns an error - if so
// will return the object and the error, otherwise will return
// nil and the error
func (f *Fs) Put(ctx context.Context, in io.Reader, src fs.ObjectInfo, options ...fs.OpenOption) (fs.Object, error) {
	size := src.Size()
	if size < 0 {
		return nil, errors.New("file size unknown")
	}
	existingObj, err := f.NewObject(ctx, src.Remote())
	switch err {
	case nil:
		// object is found
		return existingObj, existingObj.Update(ctx, in, src, options...)
	case fs.ErrorObjectNotFound:
		// object not found, so we need to create it
		remote := src.Remote()
		size := src.Size()
		modTime := src.ModTime(ctx)

		obj, err := f.createObject(ctx, remote, modTime, size)
		if err != nil {
			return nil, err
		}
		return obj, obj.Update(ctx, in, src, options...)
	default:
		// real error caught
		return nil, err
	}
}

// DirCacheFlush resets the directory cache - used in testing as an
// optional interface
func (f *Fs) DirCacheFlush() {
	f.dirCache.ResetRoot()
}

// parseNormalizedID parses a normalized ID (may be in the form `driveID#itemID` or just `itemID`)
// and returns itemID, driveID, rootURL.
// Such a normalized ID can come from (*Item).GetID()
//
// Parameters:
// - rid: the normalized ID to be parsed
//
// Returns:
// - id: the itemID extracted from the normalized ID
// - etag: the driveID extracted from the normalized ID, or an empty string if not present
func (f *Fs) parseNormalizedID(rid string) (id string, etag string) {
	split := strings.Split(rid, "#")
	if len(split) == 1 {
		return split[0], ""
	}
	return split[0], split[1]
}

// FindPath finds the leaf and directoryID from a normalized path
func (f *Fs) FindPath(ctx context.Context, remote string, create bool) (leaf, directoryID, etag string, err error) {
	leaf, jDirectoryID, err := f.dirCache.FindPath(ctx, remote, create)
	if err != nil {
		return "", "", "", err
	}
	directoryID, etag = f.parseNormalizedID(jDirectoryID)
	return leaf, directoryID, etag, nil
}

// FindDir finds the directory passed in returning the directory ID
// starting from pathID
func (f *Fs) FindDir(ctx context.Context, path string, create bool) (pathID string, etag string, err error) {
	jDirectoryID, err := f.dirCache.FindDir(ctx, path, create)
	if err != nil {
		return "", "", err
	}
	directoryID, etag := f.parseNormalizedID(jDirectoryID)
	return directoryID, etag, nil
}

// IDJoin joins the given ID and ETag into a single string with a "#" delimiter.
func (f *Fs) IDJoin(id string, etag string) string {
	if strings.Contains(id, "#") {
		// already contains an etag, replace
		id, _ = f.parseNormalizedID(id)
	}

	return strings.Join([]string{id, etag}, "#")
}

func (f *Fs) putFolderCache(id, etag, remote string) string {
	jid := f.IDJoin(id, etag)
	f.dirCache.Put(remote, f.IDJoin(id, etag))
	return jid
}

// Rmdir implements fs.Fs.
func (f *Fs) Rmdir(ctx context.Context, dir string) error {
	directoryID, etag, err := f.FindDir(ctx, dir, false)
	if err != nil {
		return err
	}
	if isSharedDirectoryID(directoryID) {
		item, err := f.resolveSharedDirectoryItem(ctx, directoryID, true, false)
		if err != nil {
			return err
		}
		if item.DirectChildrenCount > 0 {
			return fs.ErrorDirectoryNotEmpty
		}
		resolved, err := f.resolveSharedRenameItem(ctx, item.Itemid, directoryID, etag, directoryID)
		if err != nil {
			return err
		}
		if _, _, err := f.service.DeleteSharedItem(ctx, resolved); err != nil {
			return err
		}
		f.dirCache.FlushDir(dir)
		return nil
	}
	return f.purgeCheck(ctx, dir, true)
}

// Root implements fs.Fs.
func (f *Fs) Root() string {
	return f.opt.Enc.ToStandardPath(f.root)
}

// String implements fs.Fs.
func (f *Fs) String() string {
	return f.root
}

// CreateDir makes a directory with pathID as parent and name leaf
//
// This should be implemented by the backend and will be called by the
// dircache package when appropriate.
func (f *Fs) CreateDir(ctx context.Context, pathID, leaf string) (string, error) {
	parsedID, _ := f.parseNormalizedID(pathID)
	if isSharedDirectoryID(parsedID) {
		parent, err := f.resolveSharedWriteParent(ctx, parsedID)
		if err != nil {
			return "", err
		}
		item, _, err := f.service.CreateSharedFolder(ctx, parent, f.opt.Enc.FromStandardName(leaf))
		if err != nil {
			return "", err
		}
		if f.sharedItems == nil {
			f.sharedItems = make(map[string]*api.DriveItem)
		}
		if f.sharedItemIDs == nil {
			f.sharedItemIDs = make(map[string]string)
		}
		if item.Itemid != "" {
			f.sharedItems["SHARED::"+item.Itemid] = item
			f.sharedItemIDs[item.Drivewsid] = item.Itemid
		}
		if item.Drivewsid != "" {
			f.sharedItems[item.Drivewsid] = item
		}
		return f.IDJoin(item.Drivewsid, item.Etag), nil
	}
	var item *api.DriveItem
	var err error
	var found bool
	var resp *http.Response
	if err = f.pacer.Call(func() (bool, error) {
		id, _ := f.parseNormalizedID(pathID)
		item, resp, err = f.service.CreateNewFolderByDriveID(ctx, id, f.opt.Enc.FromStandardName(leaf))

		// check if it went oke
		if requestError, ok := err.(*api.RequestError); ok {
			if requestError.Status == "unknown" {
				fs.Debugf(requestError, " checking if dir is created with separate call.")
				time.Sleep(1 * time.Second) // sleep to give icloud time to clear up its mind
				item, found, err = f.findLeafItem(ctx, pathID, leaf)
				if err != nil {
					return false, err
				}

				if !found {
					// lets assume it failed and retry
					return true, err
				}

				// success, clear err
				err = nil
			}
		}

		return ignoreResultUnknown(ctx, resp, err)
	}); err != nil {
		return "", err
	}

	return f.IDJoin(item.Drivewsid, item.Etag), err
}

// DirMove moves src, srcRemote to this remote at dstRemote
// using server-side move operations.
//
// Will only be called if src.Fs().Name() == f.Name()
//
// If it isn't possible then return fs.ErrorCantDirMove
//
// If destination exists then return fs.ErrorDirExists
func (f *Fs) DirMove(ctx context.Context, src fs.Fs, srcRemote, dstRemote string) error {
	srcFs, ok := src.(*Fs)
	if !ok {
		fs.Debugf(srcFs, "Can't move directory - not same remote type")
		return fs.ErrorCantDirMove
	}

	srcID, jsrcDirectoryID, srcLeaf, jdstDirectoryID, dstLeaf, err := f.dirCache.DirMove(ctx, srcFs.dirCache, srcFs.root, srcRemote, f.root, dstRemote)
	if err != nil {
		return err
	}

	srcDirectoryID, srcEtag := f.parseNormalizedID(jsrcDirectoryID)
	// dircache.DirMove returns the source parent ID and its etag. Resolve the
	// source directory entry as well so cross-zone folder moves carry the
	// folder's current etag, not the parent's etag.
	sourceID, sourceEtag, findErr := srcFs.FindDir(ctx, srcRemote, false)
	srcEtag = selectDirMoveSourceEtag(srcID, srcEtag, sourceID, sourceEtag, findErr)
	dstDirectoryID, _ := f.parseNormalizedID(jdstDirectoryID)
	var sourceParent *api.DriveItem
	if isSharedDirectoryID(srcDirectoryID) {
		if cached := srcFs.sharedItems[srcDirectoryID]; cached != nil {
			parentCopy := *cached
			sourceParent = &parentCopy
		} else if resolved, resolveErr := srcFs.resolveSharedDirectoryItem(ctx, srcDirectoryID, true, false); resolveErr == nil {
			sourceParent = resolved
		}
	}

	_, err = f.move(ctx, srcID, "", srcDirectoryID, srcLeaf, srcEtag, dstDirectoryID, dstLeaf, srcFs, sourceParent)
	if err != nil {
		return err
	}

	srcFs.dirCache.FlushDir(srcRemote)

	return nil
}

func selectDirMoveSourceEtag(srcID, parentEtag, sourceID, sourceEtag string, findErr error) string {
	normalizedSrcID := strings.SplitN(srcID, "#", 2)[0]
	if findErr == nil && sourceID == normalizedSrcID && sourceEtag != "" {
		return sourceEtag
	}
	return parentEtag
}

func (f *Fs) move(ctx context.Context, ID, srcItemID, srcDirectoryID, srcLeaf, srcEtag, dstDirectoryID, dstLeaf string, sourceFs *Fs, sourceParent *api.DriveItem) (*api.DriveItem, error) {
	if sourceFs == nil {
		sourceFs = f
	}
	if srcItemID == "" && strings.HasPrefix(ID, "SHARED::") && isSharedDirectoryID(srcDirectoryID) && srcDirectoryID == dstDirectoryID {
		itemID := sharedRenameItemID(ID)
		item, err := sourceFs.resolveSharedRenameItem(ctx, itemID, ID, srcEtag, srcDirectoryID)
		if err != nil {
			return nil, err
		}
		renamed, _, err := f.service.RenameSharedItem(ctx, item, dstLeaf)
		return renamed, err
	}
	if srcItemID != "" && isSharedDirectoryID(srcDirectoryID) && srcDirectoryID == dstDirectoryID {
		item, err := sourceFs.resolveSharedRenameItem(ctx, srcItemID, ID, srcEtag, srcDirectoryID)
		if err != nil {
			return nil, err
		}
		renamed, _, err := f.service.RenameSharedItem(ctx, item, dstLeaf)
		return renamed, err
	}

	sourceShared := isSharedMoveSource(ID, srcItemID, srcDirectoryID)
	destinationShared := isSharedDirectoryID(dstDirectoryID)
	if !sourceShared && isPersonalFileToSharedMove(ID, destinationShared) {
		return f.movePersonalToShared(ctx, ID, srcItemID, srcDirectoryID, srcLeaf, srcEtag, dstDirectoryID, sourceFs)
	}
	if !sourceShared && isPersonalFolderToSharedMove(ID, destinationShared) {
		return f.movePersonalToShared(ctx, ID, srcItemID, srcDirectoryID, srcLeaf, srcEtag, dstDirectoryID, sourceFs)
	}

	// Shared-to-Shared moves must use the owner's CloudDocs zone. Files use
	// documentStructure records; directories use directory/<uuid> records.
	if sourceShared && destinationShared {
		sourceItem, err := sourceFs.resolveSharedMoveSourceWithParent(ctx, ID, srcItemID, srcEtag, srcDirectoryID, sourceParent)
		if err != nil {
			return nil, err
		}
		destinationItem, err := f.resolveSharedMoveItem(ctx, dstDirectoryID)
		if err != nil {
			return nil, err
		}
		if sourceItem.IsFolder() || strings.HasPrefix(sourceItem.Drivewsid, "FOLDER_IN_SHARED_FOLDER::") || srcItemID == "" {
			if err := f.service.SharedReparentDirectory(ctx, sourceItem, destinationItem, dstLeaf); err != nil {
				return nil, err
			}
			result := sharedReparentedItem(sourceItem, destinationItem)
			result.Name = dstLeaf
			return result, nil
		}
		if err := f.service.SharedReparentFile(ctx, sourceItem, destinationItem); err != nil {
			return nil, err
		}
		return sharedReparentedItem(sourceItem, destinationItem), nil
	}

	// Personal-to-Shared uses the authenticated destination ShareID on the
	// DriveWS /moveItems envelope. This covers both files and directories and
	// never falls through to the context-free generic request.
	if !sourceShared && destinationShared {
		return f.movePersonalToShared(ctx, ID, srcItemID, srcDirectoryID, srcLeaf, srcEtag, dstDirectoryID, sourceFs)
	}

	// Shared-to-Personal uses the source ShareID inside items[0], matching the
	// successful Apple Web request. It is a true server-side move, not a
	// download/upload or copy/delete fallback.
	if isSharedObjectToPersonalMove(ID, srcItemID, srcDirectoryID, destinationShared) {
		sourceItem, err := sourceFs.resolveSharedMoveSourceWithParent(ctx, ID, srcItemID, srcEtag, srcDirectoryID, sourceParent)
		if err != nil {
			return nil, err
		}
		destinationID, _ := f.parseNormalizedID(dstDirectoryID)
		item, _, err := f.service.MoveSharedItemToPersonal(ctx, sourceItem, destinationID)
		return item, err
	}
	var resp *http.Response
	var item *api.DriveItem
	var err error

	// move
	if srcDirectoryID != dstDirectoryID {
		if err = f.pacer.Call(func() (bool, error) {
			id, _ := f.parseNormalizedID(ID)
			item, resp, err = f.service.MoveItemByDriveID(ctx, id, srcEtag, dstDirectoryID, true)
			return ignoreResultUnknown(ctx, resp, err)
		}); err != nil {
			return nil, err
		}
		ID = item.Drivewsid
		srcEtag = item.Etag
	}

	// rename
	if srcLeaf != dstLeaf {
		if err = f.pacer.Call(func() (bool, error) {
			id, _ := f.parseNormalizedID(ID)
			item, resp, err = f.service.RenameItemByDriveID(ctx, id, srcEtag, dstLeaf, true)
			return ignoreResultUnknown(ctx, resp, err)
		}); err != nil {
			return item, err
		}
	}

	return item, err
}

func isPersonalFileToSharedMove(id string, destinationShared bool) bool {
	return strings.HasPrefix(id, "FILE::") && destinationShared
}

func isPersonalFolderToSharedMove(id string, destinationShared bool) bool {
	return strings.HasPrefix(id, "FOLDER::") && destinationShared
}

func (f *Fs) movePersonalToShared(ctx context.Context, id, srcItemID, srcDirectoryID, srcLeaf, srcEtag, dstDirectoryID string, sourceFs *Fs) (*api.DriveItem, error) {
	destinationItem, err := f.resolveSharedMoveItem(ctx, dstDirectoryID)
	if err != nil {
		return nil, err
	}
	shared, err := f.service.ResolveSharedDestinationContext(ctx, destinationItem)
	if err != nil {
		return nil, err
	}
	id, _ = f.parseNormalizedID(id)
	// DirMove supplies the source parent cache ID/etag, not the source
	// directory's etag. Resolve the directory itself before constructing
	// the cross-zone request; sending the parent etag (or an empty etag)
	// produces a server-side ETAG_CONFLICT for Personal folder moves.
	moveEtag := srcEtag
	if srcItemID == "" && moveEtag == "" {
		moveEtag, err = sourceFs.resolvePersonalMoveSourceEtag(ctx, id, srcDirectoryID, srcLeaf)
		if err != nil {
			return nil, err
		}
	}
	item, _, err := f.service.MoveItemByDriveIDWithSharedContext(ctx, id, moveEtag, shared)
	return item, err
}

func isSharedDirectoryID(id string) bool {
	if index := strings.IndexByte(id, '#'); index >= 0 {
		id = id[:index]
	}
	return strings.HasPrefix(id, "SHARED::") || strings.HasPrefix(id, "SHARED_FOLDER::") || strings.HasPrefix(id, "FOLDER_IN_SHARED_FOLDER::")
}

func isSharedMoveSource(id, itemID, parentID string) bool {
	return strings.HasPrefix(id, "SHARED::") || strings.HasPrefix(id, "FILE_IN_SHARED_FOLDER::") || strings.HasPrefix(id, "FOLDER_IN_SHARED_FOLDER::") || itemID != "" && strings.HasPrefix(parentID, "SHARED::")
}

func isSharedObjectToPersonalMove(id, itemID, parentID string, destinationShared bool) bool {
	return isSharedMoveSource(id, itemID, parentID) && !destinationShared
}

// resolvePersonalMoveSourceEtag reads the source directory's current etag.
// DirMove only returns the source parent directory ID and its etag, so its
// source directory move must not reuse that parent etag in /moveItems.
func (f *Fs) resolvePersonalMoveSourceEtag(ctx context.Context, id, parentID, leaf string) (string, error) {
	sourceID, _ := f.parseNormalizedID(id)
	// The normal Personal listing response already contains the source
	// directory's current etag. Reuse that authenticated READ path; the
	// single-item /retrieveItemDetails endpoint rejects this folder lookup on
	// the current service and is not needed here.
	if parentID != "" && leaf != "" {
		item, found, err := f.findLeafItem(ctx, parentID, leaf)
		if err != nil {
			return "", fmt.Errorf("Personal move source metadata unavailable: %w", err)
		}
		if found && item != nil && item.Etag != "" {
			if parsed, _ := f.parseNormalizedID(item.Drivewsid); parsed == sourceID || item.Drivewsid == sourceID {
				return item.Etag, nil
			}
		}
	}
	// Keep a canonical hierarchy fallback for callers that do not have a
	// source path. This uses the same authenticated hierarchy endpoint as the
	// normal READ path rather than guessing an etag.
	item, _, err := f.service.GetItemByDriveID(ctx, sourceID, true)
	if err != nil {
		return "", fmt.Errorf("Personal move source metadata unavailable: %w", err)
	}
	if item == nil || item.Etag == "" {
		return "", fmt.Errorf("Personal move source etag unavailable")
	}
	return item.Etag, nil
}

// resolveSharedMoveSource is the single source-side canonical resolver for
// all Shared move directions. It keeps SHARED::<item_id> as a lookup key only.
func (f *Fs) resolveSharedMoveSource(ctx context.Context, id, itemID, etag, parentID string) (*api.DriveItem, error) {
	return f.resolveSharedMoveSourceWithParent(ctx, id, itemID, etag, parentID, nil)
}

func (f *Fs) resolveSharedMoveSourceWithParent(ctx context.Context, id, itemID, etag, parentID string, parent *api.DriveItem) (*api.DriveItem, error) {
	if itemID == "" {
		itemID = sharedRenameItemID(id)
	}
	return f.resolveSharedRenameItemWithParent(ctx, itemID, id, etag, parentID, parent)
}

// resolveSharedMoveItem resolves a destination directory from the current
// authenticated Shared hierarchy. The cache is consulted only as a fallback;
// it is never required to contain the ShareID context.
func (f *Fs) resolveSharedMoveItem(ctx context.Context, directoryID string) (*api.DriveItem, error) {
	return f.resolveSharedDirectoryItem(ctx, directoryID, true, false)
}

// resolveSharedWriteParent returns a canonical Shared directory suitable for
// folder creation or upload. Incomplete cached listing metadata is resolved
// from authenticated DriveWS or document metadata before it is used for writes.
func (f *Fs) resolveSharedWriteParent(ctx context.Context, directoryID string) (*api.DriveItem, error) {
	if cached := f.sharedItems[directoryID]; isCanonicalSharedWriteParent(cached) && hasSharedShareIdentity(cached) {
		copy := *cached
		return &copy, nil
	}
	item, err := f.resolveSharedDirectoryItem(ctx, directoryID, false, true)
	if err != nil {
		return nil, fmt.Errorf("Shared write parent canonical identity unavailable: %w", err)
	}
	if !isCanonicalSharedWriteParent(item) || !hasSharedShareIdentity(item) {
		return nil, fmt.Errorf("Shared write parent canonical identity unavailable")
	}
	return item, nil
}

func isCanonicalSharedWriteParent(item *api.DriveItem) bool {
	if !isCanonicalSharedDirectoryItem(item) || !hasSharedShareIdentity(item) {
		return false
	}
	if strings.HasPrefix(item.Drivewsid, "FOLDER_IN_SHARED_FOLDER::") {
		return item.Docwsid != ""
	}
	return item.ShareID.RecordName != ""
}

// resolveSharedDirectoryItem resolves a Shared directory from authenticated
// metadata. Cached metadata is used only when allowCachedFallback is true.
func (f *Fs) resolveSharedDirectoryItem(ctx context.Context, directoryID string, allowCachedFallback, requireDocumentID bool) (*api.DriveItem, error) {
	directoryID, _ = f.parseNormalizedID(directoryID)
	var cached *api.DriveItem
	if item := f.sharedItems[directoryID]; item != nil {
		copy := *item
		cached = &copy
	}
	itemID := ""
	if strings.HasPrefix(directoryID, "SHARED::") {
		itemID = strings.TrimPrefix(directoryID, "SHARED::")
	} else if cached != nil {
		itemID = cached.Itemid
	}
	if itemID == "" {
		itemID = f.sharedItemIDs[directoryID]
	}
	rootDriveID, _ := f.parseNormalizedID(f.rootID)
	roots, _, err := f.service.GetItemsByDriveID(ctx, []string{rootDriveID}, true)
	if err != nil {
		return nil, err
	}
	var sharedRoot *api.DriveItem
	for _, candidate := range roots {
		if root := findSharedRootHierarchyItem(candidate); root != nil {
			sharedRoot = root
			break
		}
	}
	if sharedRoot == nil {
		return nil, fmt.Errorf("Shared destination hierarchy unavailable")
	}
	var item *api.DriveItem
	if itemID != "" {
		item = findSharedCanonicalHierarchyItem(sharedRoot, itemID)
	}
	if item == nil && isSharedDirectoryID(directoryID) && !strings.HasPrefix(directoryID, "SHARED::") {
		item = findSharedCanonicalHierarchyDriveID(sharedRoot, directoryID)
	}
	if item != nil && (!isCanonicalSharedDirectoryItem(item) || requireDocumentID && strings.HasPrefix(item.Drivewsid, "FOLDER_IN_SHARED_FOLDER::") && item.Docwsid == "") {
		// A hierarchy entry can identify the destination as a folder while
		// omitting its canonical DriveWS/document identity.  Do not promote
		// that partial entry (or its cache key) into a write identity.  The
		// authenticated document lookup is the same identity bridge used for
		// Shared move sources and supplies the destination's fresh metadata.
		item = nil
	}
	if item == nil && itemID != "" {
		if doc, _, lookupErr := f.service.GetDocByItemID(ctx, itemID); lookupErr == nil && doc != nil && doc.DocumentID != "" {
			item = canonicalSharedItemFromDocument(doc, itemID, sharedRoot)
		}
	}
	if item == nil && allowCachedFallback {
		item = cached
	}
	if item == nil {
		if allowCachedFallback {
			return nil, fs.ErrorDirNotFound
		}
		return nil, fmt.Errorf("Shared directory canonical metadata unavailable")
	}
	if !isCanonicalSharedDirectoryItem(item) {
		return nil, fmt.Errorf("Shared move destination is not a directory")
	}
	if !hasSharedShareIdentity(item) && hasSharedShareIdentity(sharedRoot) {
		item.ShareID = sharedRoot.ShareID
	}
	if item.Drivewsid == "" {
		return nil, fmt.Errorf("Shared move destination canonical identity unavailable")
	}
	if item.ShareID.ShareName == "" || item.ShareID.RecordName == "" || item.ShareID.ZoneID.ZoneName == "" || item.ShareID.ZoneID.OwnerRecordName == "" {
		return nil, fmt.Errorf("Shared move destination share context unavailable")
	}
	return item, nil
}

func (f *Fs) resolveSharedUploadParent(ctx context.Context, directoryID string) (*api.DriveItem, string, error) {
	parent, err := f.resolveSharedWriteParent(ctx, directoryID)
	if err != nil {
		return nil, "", err
	}
	startingDocumentID, err := api.SharedUploadStartingDocumentID(parent)
	if err != nil {
		return nil, "", err
	}
	return parent, startingDocumentID, nil
}

func isCanonicalSharedDirectoryItem(item *api.DriveItem) bool {
	if item == nil || !item.IsFolder() {
		return false
	}
	return strings.HasPrefix(item.Drivewsid, "SHARED_FOLDER::") || strings.HasPrefix(item.Drivewsid, "FOLDER_IN_SHARED_FOLDER::")
}

// sharedRenameItemID extracts the item_id from the normalized Shared cache
// identity returned by dircache. The cache value may include the current etag
// after a '#'; neither the cache namespace nor that etag is part of item_id.
func sharedRenameItemID(cacheID string) string {
	if index := strings.IndexByte(cacheID, '#'); index >= 0 {
		cacheID = cacheID[:index]
	}
	if !strings.HasPrefix(cacheID, "SHARED::") {
		return ""
	}
	return strings.TrimPrefix(cacheID, "SHARED::")
}

// resolveSharedRenameItem obtains the current Shared protocol identity and
// share context from authenticated READ metadata. The dircache ID is only a
// path/cache key and is never promoted to a DriveWS identity.
func (f *Fs) resolveSharedRenameItem(ctx context.Context, itemID, cacheID, fallbackEtag, parentCacheID string) (*api.DriveItem, error) {
	return f.resolveSharedRenameItemWithParent(ctx, itemID, cacheID, fallbackEtag, parentCacheID, nil)
}

func (f *Fs) resolveSharedRenameItemWithParent(ctx context.Context, itemID, cacheID, fallbackEtag, parentCacheID string, preferredParent *api.DriveItem) (*api.DriveItem, error) {
	// The SHARED::<item_id> value is only a dircache identity. Resolve the
	// object from the authenticated retrieveItemDetailsInFolders hierarchy,
	// where DriveWS canonical identity, fresh etag, and the zone-scoped ShareID
	// arrive together. Do not use the listing/cache representation as the
	// canonical source and do not derive a DriveWS ID from the cache identity.
	rootDriveID, _ := f.parseNormalizedID(f.rootID)
	roots, _, err := f.service.GetItemsByDriveID(ctx, []string{rootDriveID}, true)
	if err != nil {
		return nil, err
	}
	var sharedRoot *api.DriveItem
	for _, candidate := range roots {
		sharedRoot = findSharedRootHierarchyItem(candidate)
		if sharedRoot != nil {
			break
		}
	}
	if sharedRoot == nil {
		return nil, fmt.Errorf("Shared rename canonical hierarchy unavailable")
	}
	item := findSharedCanonicalHierarchyItem(sharedRoot, itemID)
	if item == nil && sharedRoot.Drivewsid != "" {
		// A root response may omit embedded children. Fetch the Shared root
		// directly through the same authenticated hierarchy endpoint, without
		// consulting or warming f.sharedItems.
		if details, _, readErr := f.service.GetItemsByDriveID(ctx, []string{sharedRoot.Drivewsid}, true); readErr == nil {
			for _, detail := range details {
				if found := findSharedCanonicalHierarchyItem(detail, itemID); found != nil {
					item = found
					break
				}
			}
		}
	}
	if item == nil && parentCacheID != "" {
		// Nested Shared directories are not always embedded in the root
		// hierarchy response. Resolve the cached parent only as a READ lookup
		// key, then fetch that parent's canonical hierarchy and locate the source
		// there. The cache identity is never promoted to a protocol identity.
		parentItemID := sharedRenameItemID(parentCacheID)
		parent := preferredParent
		if parent == nil {
			if cached := f.sharedItems[parentCacheID]; cached != nil {
				parentCopy := *cached
				parent = &parentCopy
			}
		}
		if parentItemID == "" && parent != nil {
			parentItemID = parent.Itemid
		}
		if parent == nil {
			parent = findSharedCanonicalHierarchyItem(sharedRoot, parentItemID)
		}
		if parent == nil {
			// Path resolution may already have authenticated the parent and
			// retained its canonical DriveWS ID under the cache key. Use that
			// value only to select the next authenticated hierarchy READ; never
			// use the cache entry as the source protocol metadata itself.
			if cached := f.sharedItems[parentCacheID]; cached != nil {
				parentCopy := *cached
				parent = &parentCopy
			}
		}
		if parent != nil && parent.Drivewsid != "" {
			if details, _, readErr := f.service.GetItemsByDriveID(ctx, []string{parent.Drivewsid}, true); readErr == nil {
				for _, detail := range details {
					if found := findSharedCanonicalHierarchyItem(detail, itemID); found != nil {
						item = found
						break
					}
				}
			}
		}
	}
	if item == nil {
		// Shared items can be visible to /v1/enumerate before they are included
		// in the folder hierarchy response. The authenticated document lookup
		// supplies the canonical document identity; the ShareID still comes only
		// from the authenticated Shared root context.
		if doc, _, lookupErr := f.service.GetDocByItemID(ctx, itemID); lookupErr == nil && doc != nil && doc.DocumentID != "" {
			item = canonicalSharedItemFromDocument(doc, itemID, sharedRoot)
		}
	}
	if item == nil {
		return nil, fmt.Errorf("Shared rename source canonical metadata unavailable")
	}
	itemCopy := *item
	if !hasSharedShareIdentity(&itemCopy) && hasSharedShareIdentity(sharedRoot) {
		itemCopy.ShareID = sharedRoot.ShareID
	}
	if !hasSharedShareIdentity(&itemCopy) {
		return nil, fmt.Errorf("Shared rename share context unavailable")
	}
	if !strings.HasPrefix(itemCopy.Drivewsid, "FOLDER_IN_SHARED_FOLDER::") && !strings.HasPrefix(itemCopy.Drivewsid, "FILE_IN_SHARED_FOLDER::") {
		return nil, fmt.Errorf("Shared rename canonical DriveWS identity unavailable")
	}
	if itemCopy.Etag == "" {
		itemCopy.Etag = fallbackEtag
	}
	if itemCopy.Etag == "" {
		return nil, fmt.Errorf("Shared rename fresh etag unavailable")
	}
	shareChangeTag, err := f.service.LookupSharedShareChangeTag(ctx, itemCopy.ShareID.RecordName, itemCopy.ShareID.ZoneID.ZoneName, itemCopy.ShareID.ZoneID.OwnerRecordName)
	if err != nil {
		return nil, fmt.Errorf("Shared rename shareChangeTag unavailable: %w", err)
	}
	itemCopy.ShareID.ShareChangeTag = shareChangeTag
	if itemCopy.ParentID == "" {
		itemCopy.ParentID = sharedRoot.Drivewsid
	}
	_ = cacheID // cache identity is intentionally not used as protocol identity
	_ = parentCacheID
	return &itemCopy, nil
}

func canonicalSharedItemFromDocument(doc *api.Document, itemID string, sharedRoot *api.DriveItem) *api.DriveItem {
	if doc == nil || sharedRoot == nil || doc.DocumentID == "" || itemID == "" {
		return nil
	}
	itemType := doc.Type
	switch itemType {
	case "FILE", "FILE_IN_SHARED_FOLDER":
		itemType = "FILE"
	case "FOLDER", "FOLDER_IN_SHARED_FOLDER":
		itemType = "FOLDER"
	default:
		return nil
	}
	zone := sharedRoot.ShareID.ZoneID.ZoneName
	if zone == "" {
		zone = "com.apple.CloudDocs"
	}
	return &api.DriveItem{
		Drivewsid: sharedRenameDriveID(itemType, zone, doc.DocumentID),
		Docwsid:   doc.DocumentID,
		Itemid:    itemID,
		Etag:      doc.Etag,
		Name:      doc.Name,
		ParentID:  doc.ParentID,
		Type:      itemType,
		Size:      doc.Size,
		ShareID:   sharedRoot.ShareID,
	}
}

func findSharedRootHierarchyItem(item *api.DriveItem) *api.DriveItem {
	if item == nil {
		return nil
	}
	if strings.HasPrefix(item.Drivewsid, "SHARED_FOLDER::") {
		return item
	}
	for _, child := range item.Items {
		if root := findSharedRootHierarchyItem(child); root != nil {
			return root
		}
	}
	return nil
}

// findSharedCanonicalHierarchyItem locates an item in the canonical Shared
// root response. ShareID is zone-scoped, so a child may inherit the root
// context when the response omits it on that child.
func findSharedCanonicalHierarchyItem(root *api.DriveItem, itemID string) *api.DriveItem {
	if root == nil {
		return nil
	}
	if root.Itemid == itemID {
		return root
	}
	for _, child := range root.Items {
		if found := findSharedCanonicalHierarchyItem(child, itemID); found != nil {
			if !hasCompleteSharedShareID(found) && hasCompleteSharedShareID(root) {
				copy := *found
				copy.ShareID = root.ShareID
				return &copy
			}
			return found
		}
	}
	return nil
}

func findSharedCanonicalHierarchyDriveID(root *api.DriveItem, drivewsid string) *api.DriveItem {
	if root == nil {
		return nil
	}
	if root.Drivewsid == drivewsid {
		return root
	}
	for _, child := range root.Items {
		if found := findSharedCanonicalHierarchyDriveID(child, drivewsid); found != nil {
			if !hasCompleteSharedShareID(found) && hasCompleteSharedShareID(root) {
				copy := *found
				copy.ShareID = root.ShareID
				return &copy
			}
			return found
		}
	}
	return nil
}

func hasCompleteSharedShareID(item *api.DriveItem) bool {
	return item != nil && item.ShareID.ShareName != "" && item.ShareID.RecordName != "" && item.ShareID.ShareChangeTag != "" && item.ShareID.ZoneID.ZoneName != "" && item.ShareID.ZoneID.OwnerRecordName != ""
}

func hasSharedShareIdentity(item *api.DriveItem) bool {
	return item != nil && item.ShareID.ShareName != "" && item.ShareID.RecordName != "" && item.ShareID.ZoneID.ZoneName != "" && item.ShareID.ZoneID.OwnerRecordName != ""
}

func sharedRenameDriveID(itemType, zone, documentID string) string {
	if zone == "" {
		zone = "com.apple.CloudDocs"
	}
	prefix := itemType
	if prefix == "FOLDER" {
		prefix = "FOLDER_IN_SHARED_FOLDER"
	} else if prefix == "FILE" {
		prefix = "FILE_IN_SHARED_FOLDER"
	}
	return prefix + "::" + zone + "::" + documentID
}

// prepareSharedRenameItem prepares a READ-enriched item for DriveWS rename.
// ID is the internal dircache identity and is not necessarily a DriveWS ID.
func prepareSharedRenameItem(item *api.DriveItem, cacheID, etag string) {
	if item.Drivewsid == "" {
		item.Drivewsid = cacheID
	}
	item.Etag = etag
}

// sharedReparentedItem carries the committed Shared location into the result
// object. It intentionally performs no network refresh; the CloudKit modify
// response plus the independent reconciliation are the authoritative result.
func sharedReparentedItem(source, destination *api.DriveItem) *api.DriveItem {
	if source == nil {
		return nil
	}
	result := *source
	if destination != nil {
		result.ParentID = destination.Drivewsid
		result.ShareID = destination.ShareID
	}
	return &result
}

// Move moves the src object to the specified remote.
func (f *Fs) Move(ctx context.Context, src fs.Object, remote string) (fs.Object, error) {
	srcObj, ok := src.(*Object)
	if !ok {
		fs.Debugf(src, "Can't move - not same remote type")
		return nil, fs.ErrorCantMove
	}

	srcLeaf, srcDirectoryID, _, err := srcObj.fs.FindPath(ctx, srcObj.remote, true)
	if err != nil {
		return nil, err
	}

	dstLeaf, dstDirectoryID, _, err := f.FindPath(ctx, remote, true)
	if err != nil {
		return nil, err
	}

	item, err := f.move(ctx, srcObj.driveID, srcObj.itemID, srcDirectoryID, srcLeaf, srcObj.etag, dstDirectoryID, dstLeaf, srcObj.fs, srcObj.fs.sharedItems[srcDirectoryID])
	if err != nil {
		return src, err
	}

	return f.NewObjectFromDriveItem(ctx, remote, item)
}

// Creates from the parameters passed in a half finished Object which
// must have setMetaData called on it
//
// Returns the object, leaf, directoryID and error.
//
// Used to create new objects
func (f *Fs) createObject(ctx context.Context, remote string, modTime time.Time, size int64) (o *Object, err error) {
	// Create the directory for the object if it doesn't exist
	_, _, _, err = f.FindPath(ctx, remote, true)
	if err != nil {
		return
	}
	// Temporary Object under construction
	o = &Object{
		fs:      f,
		remote:  remote,
		modTime: modTime,
		size:    size,
	}
	return o, nil
}

// ReadCookies parses the raw cookie string and returns an array of http.Cookie objects.
func ReadCookies(raw string) []*http.Cookie {
	header := http.Header{}
	header.Add("Cookie", raw)
	request := http.Request{Header: header}
	return request.Cookies()
}

var retryErrorCodes = []int{
	400, // icloud is a mess, sometimes returns 400 on a perfectly fine request. So just retry
	408, // Request Timeout
	409, // Conflict, retry could fix it.
	429, // Rate exceeded.
	500, // Get occasional 500 Internal Server Error
	502, // Server overload
	503, // Service Unavailable
	504, // Gateway Time-out
}

func shouldRetry(ctx context.Context, resp *http.Response, err error) (bool, error) {
	if fserrors.ContextError(ctx, &err) {
		return false, err
	}

	return fserrors.ShouldRetry(err) || fserrors.ShouldRetryHTTP(resp, retryErrorCodes), err
}

func ignoreResultUnknown(ctx context.Context, resp *http.Response, err error) (bool, error) {
	if requestError, ok := err.(*api.RequestError); ok {
		if requestError.Status == "unknown" {
			fs.Debugf(requestError, " ignoring.")
			return false, nil
		}
	}
	return shouldRetry(ctx, resp, err)
}

func retryResultUnknown(ctx context.Context, resp *http.Response, err error) (bool, error) {
	if requestError, ok := err.(*api.RequestError); ok {
		if requestError.Status == "unknown" {
			fs.Debugf(requestError, " retrying.")
			return true, err
		}
	}
	return shouldRetry(ctx, resp, err)
}

// NewFs constructs an Fs from the path, container:path
func NewFs(ctx context.Context, name, root string, m configmap.Mapper) (fs.Fs, error) {
	icloud, opt, err := newICloudClient(ctx, name, m, api.WsDrive)
	if err != nil {
		return nil, err
	}

	root = strings.Trim(root, "/")

	f := &Fs{
		name:          name,
		root:          root,
		icloud:        icloud,
		rootID:        "FOLDER::com.apple.CloudDocs::root",
		sharedItemIDs: make(map[string]string),
		sharedItems:   make(map[string]*api.DriveItem),
		opt:           *opt,
		m:             m,
		pacer:         fs.NewPacer(ctx, pacer.NewDefault(pacer.MinSleep(minSleep), pacer.MaxSleep(maxSleep), pacer.DecayConstant(decayConstant))),
	}
	f.features = (&fs.Features{
		CanHaveEmptyDirectories: true,
		PartialUploads:          false,
	}).Fill(ctx, f)

	rootID := f.rootID
	f.service, err = icloud.DriveService()
	if err != nil {
		return nil, err
	}

	f.dirCache = dircache.New(
		root,
		rootID,
		f,
	)

	err = f.dirCache.FindRoot(ctx, false)
	if err != nil {
		// Assume it is a file
		newRoot, remote := dircache.SplitPath(root)
		tempF := *f
		tempF.dirCache = dircache.New(newRoot, rootID, &tempF)
		tempF.root = newRoot
		// Make new Fs which is the parent
		err = tempF.dirCache.FindRoot(ctx, false)
		if err != nil {
			// No root so return old f
			return f, nil
		}

		_, err := tempF.NewObject(ctx, remote)
		if err != nil {
			if err == fs.ErrorObjectNotFound {
				// File doesn't exist so return old f
				return f, nil
			}

			return nil, err
		}

		f.dirCache = tempF.dirCache
		f.root = tempF.root
		// return an error with an fs which points to the parent
		return f, fs.ErrorIsFile
	}

	return f, nil
}

// NewObject creates a new fs.Object from a given remote string.
//
// ctx: The context.Context for the function.
// remote: The remote string representing the object's location.
// Returns an fs.Object and an error.
func (f *Fs) NewObject(ctx context.Context, remote string) (fs.Object, error) {
	return f.NewObjectFromDriveItem(ctx, remote, nil)
}

// NewObjectFromDriveItem creates a new fs.Object from a given remote string and DriveItem.
//
// ctx: The context.Context for the function.
// remote: The remote string representing the object's location.
// item: The optional DriveItem to use for initializing the Object. If nil, the function will read the metadata from the remote location.
// Returns an fs.Object and an error.
func (f *Fs) NewObjectFromDriveItem(ctx context.Context, remote string, item *api.DriveItem) (fs.Object, error) {
	o := &Object{
		fs:     f,
		remote: remote,
	}
	if item != nil {
		err := o.setMetaData(item)
		if err != nil {
			return nil, err
		}
	} else {
		item, err := f.readMetaData(ctx, remote)

		if err != nil {
			return nil, err
		}

		err = o.setMetaData(item)
		if err != nil {
			return nil, err
		}
	}

	return o, nil
}

func (f *Fs) readMetaData(ctx context.Context, path string) (item *api.DriveItem, err error) {
	fullPath := path
	if strings.HasPrefix(f.root, "Shared") && !strings.HasPrefix(path, "Shared") {
		fullPath = strings.TrimRight(f.root, "/") + "/" + strings.TrimLeft(path, "/")
	}
	if strings.HasPrefix(fullPath, "Shared") {
		return f.resolveSharedObject(ctx, fullPath)
	}
	leaf, ID, _, err := f.FindPath(ctx, path, false)

	if err != nil {
		if err == fs.ErrorDirNotFound {
			return nil, fs.ErrorObjectNotFound
		}
		return nil, err
	}

	item, found, err := f.findLeafItem(ctx, ID, leaf)

	if err != nil {
		return nil, err
	}

	if !found {
		return nil, fs.ErrorObjectNotFound
	}

	return item, nil
}

// resolveSharedObject resolves a Shared file strictly by parent enumeration.
// It deliberately does not use path lookup or treat the final file as a dir.
func (f *Fs) resolveSharedObject(ctx context.Context, remote string) (*api.DriveItem, error) {
	parts := strings.Split(strings.Trim(remote, "/"), "/")
	if len(parts) < 2 || parts[0] != "Shared" {
		return nil, fs.ErrorObjectNotFound
	}
	items, err := f.listAll(ctx, f.rootID)
	if err != nil {
		return nil, err
	}
	var current *api.DriveItem
	for _, item := range items {
		if item.FullName() == parts[0] {
			current = item
			break
		}
	}
	if current == nil || !current.IsFolder() {
		return nil, fs.ErrorObjectNotFound
	}
	key := "SHARED::" + current.Itemid
	f.sharedItems[key] = current
	for _, component := range parts[1:] {
		children, err := f.listAll(ctx, f.IDJoin(key, current.Etag))
		if err != nil {
			return nil, err
		}
		var found *api.DriveItem
		for _, child := range children {
			if child.FullName() == component {
				found = child
				break
			}
		}
		if found == nil {
			return nil, fs.ErrorObjectNotFound
		}
		if found.IsFolder() {
			current = found
			key = "SHARED::" + current.Itemid
			f.sharedItems[key] = current
			continue
		}
		// Shared enumerate records use item_id as their stable identity. If
		// the enumerate record is abbreviated, enrich it through the Documents
		// item endpoint. Do not use path lookup or derive an identity from the
		// opaque token.
		if found.Docwsid == "" {
			if raw, _, lookupErr := f.service.GetItemRawByItemID(ctx, found.Itemid); lookupErr == nil && raw != nil {
				enriched := raw.IntoDriveItem()
				// Preserve the parent/share context obtained from enumeration.
				enriched.ParentID = found.ParentID
				enriched.ShareID = found.ShareID
				if enriched.Docwsid != "" {
					found.Docwsid = enriched.Docwsid
				}
				if enriched.Drivewsid != "" {
					found.Drivewsid = enriched.Drivewsid
				}
				if enriched.Etag != "" {
					found.Etag = enriched.Etag
				}
			}
			// Some Shared /v1/item responses contain only item_id and an
			// opaque download token. Resolve the document identity through
			// the existing item lookup instead of decoding that token.
			if found.Docwsid == "" {
				if doc, _, lookupErr := f.service.GetDocByItemID(ctx, found.Itemid); lookupErr == nil && doc != nil {
					found.Docwsid = doc.DocumentID
					found.Drivewsid = doc.DriveID()
					found.Etag = doc.Etag
					found.Size = doc.Size
				}
			}
		}
		return found, nil
	}
	return nil, fs.ErrorObjectNotFound
}

func (o *Object) setMetaData(item *api.DriveItem) (err error) {
	if item.IsFolder() {
		return fs.ErrorIsDir
	}
	o.size = item.Size
	o.modTime = item.DateModified
	o.createdTime = item.DateCreated
	o.driveID = item.Drivewsid
	o.docID = item.Docwsid
	o.itemID = item.Itemid
	o.etag = item.Etag
	o.downloadURL = item.DownloadURL()
	o.shared = item.ShareID.ShareName != "" || strings.HasPrefix(item.Drivewsid, "FILE_IN_SHARED_FOLDER::") || strings.HasPrefix(item.ParentID, "FOLDER_IN_SHARED_FOLDER::") || strings.HasPrefix(item.ParentID, "SHARED_FOLDER::")
	return nil
}

// ID returns the ID of the Object if known, or "" if not
func (o *Object) ID() string {
	return o.driveID
}

// Fs implements fs.Object.
func (o *Object) Fs() fs.Info {
	return o.fs
}

// Hash implements fs.Object.
func (o *Object) Hash(ctx context.Context, t hash.Type) (string, error) {
	return "", hash.ErrUnsupported
}

// ModTime implements fs.Object.
func (o *Object) ModTime(context.Context) time.Time {
	return o.modTime
}

// Open implements fs.Object.
func (o *Object) Open(ctx context.Context, options ...fs.OpenOption) (io.ReadCloser, error) {
	fs.FixRangeOption(options, o.size)

	// Drive does not support empty files, so we cheat
	if o.size == 0 {
		return io.NopCloser(bytes.NewBufferString("")), nil
	}

	var resp *http.Response
	var err error

	if err = o.fs.pacer.Call(func() (bool, error) {
		url := o.downloadURL

		//var doc *api.Document
		//if o.docID == "" {
		//doc, resp, err = o.fs.service.GetDocByItemID(ctx, o.itemID)
		//}

		if url == "" {
			// Can not get the download url on a item to work, so do it the hard way.
			url, _, err = o.fs.service.GetDownloadURLByDriveID(ctx, o.driveID)
			if err != nil {
				return false, err
			}
		}

		resp, err = o.fs.service.DownloadFile(ctx, url, options)
		return shouldRetry(ctx, resp, err)
	}); err != nil {
		return nil, err
	}

	return resp.Body, err
}

// Remote implements fs.Object.
func (o *Object) Remote() string {
	return o.remote
}

// Remove implements fs.Object.
func (o *Object) Remove(ctx context.Context) error {
	if o.itemID == "" {
		return nil
	}
	if o.shared || strings.HasPrefix(o.driveID, "FILE_IN_SHARED_FOLDER::") {
		item, err := o.fs.resolveSharedRenameItem(ctx, o.itemID, o.driveID, o.etag, "")
		if err != nil {
			return err
		}
		_, _, err = o.fs.service.DeleteSharedItem(ctx, item)
		return err
	}

	var resp *http.Response
	var err error
	if err = o.fs.pacer.Call(func() (bool, error) {
		_, resp, err = o.fs.service.MoveItemToTrashByID(ctx, o.driveID, o.etag, true)
		return retryResultUnknown(ctx, resp, err)
	}); err != nil {
		return err
	}

	return nil
}

// SetModTime implements fs.Object.
func (o *Object) SetModTime(ctx context.Context, t time.Time) error {
	return fs.ErrorCantSetModTime
}

// Size implements fs.Object.
func (o *Object) Size() int64 {
	return o.size
}

// Storable implements fs.Object.
func (o *Object) Storable() bool {
	return true
}

// String implements fs.Object.
func (o *Object) String() string {
	if o == nil {
		return "<nil>"
	}
	return o.remote
}

// Update implements fs.Object.
func (o *Object) Update(ctx context.Context, in io.Reader, src fs.ObjectInfo, options ...fs.OpenOption) error {
	size := src.Size()
	if size < 0 {
		return errors.New("file size unknown")
	}

	remote := o.Remote()
	modTime := src.ModTime(ctx)

	leaf, dirID, _, err := o.fs.FindPath(ctx, path.Clean(remote), true)
	if err != nil {
		return err
	}

	// Move current file to trash
	if o.driveID != "" {
		err = o.Remove(ctx)
		if err != nil {
			return err
		}
	}

	name := o.fs.opt.Enc.FromStandardName(leaf)
	var resp *http.Response
	if isSharedDirectoryID(dirID) {
		parent, startingDocumentID, err := o.fs.resolveSharedUploadParent(ctx, dirID)
		if err != nil {
			return err
		}
		uploadInfo, _, err := o.fs.service.CreateSharedUpload(ctx, parent, size, name)
		if err != nil {
			return err
		}
		upload, _, err := o.fs.service.UploadSharedBinary(ctx, in, size, name, uploadInfo.URL)
		if err != nil {
			return err
		}
		r := api.NewUpdateFileInfo()
		r.DocumentID = uploadInfo.DocumentID
		r.Path.Path = name
		r.Path.StartingDocumentID = startingDocumentID
		r.Data.Receipt = upload.SingleFile.Receipt
		r.Data.Signature = upload.SingleFile.Signature
		r.Data.ReferenceSignature = upload.SingleFile.ReferenceSignature
		r.Data.WrappingKey = upload.SingleFile.WrappingKey
		r.Data.Size = upload.SingleFile.Size
		r.Mtime = modTime.UnixMilli()
		r.Btime = modTime.UnixMilli()
		item, _, err := o.fs.service.UpdateSharedFile(ctx, &r, parent.ShareID.ZoneID.OwnerRecordName)
		if err != nil {
			return err
		}
		// Reconcile through the Shared READ/list path. In particular, do not
		// use the personal retrieveItemDetails refresh for a Shared object.
		reconciled, err := o.fs.NewObject(ctx, o.remote)
		if err != nil {
			return fmt.Errorf("Shared upload reconciliation: %w", err)
		}
		reconciledObject, ok := reconciled.(*Object)
		if !ok || reconciledObject.itemID != item.Itemid || reconciledObject.size != size || !strings.HasPrefix(item.Drivewsid, "FILE_IN_SHARED_FOLDER::") || item.Itemid == "" || item.Docwsid == "" {
			return fmt.Errorf("Shared upload reconciliation returned wrong identity or size")
		}
		item.ShareID = parent.ShareID
		item.ParentID = parent.Drivewsid
		if err := o.setMetaData(item); err != nil {
			return err
		}
		o.modTime = modTime
		o.size = src.Size()
		return nil
	}

	// Create document
	var uploadInfo *api.UploadResponse
	if err = o.fs.pacer.Call(func() (bool, error) {
		uploadInfo, resp, err = o.fs.service.CreateUpload(ctx, size, name, api.ZoneFromDriveID(dirID))
		return ignoreResultUnknown(ctx, resp, err)
	}); err != nil {
		return err
	}

	// Upload content
	var upload *api.SingleFileResponse
	if err = o.fs.pacer.Call(func() (bool, error) {
		upload, resp, err = o.fs.service.Upload(ctx, in, size, name, uploadInfo.URL)
		return ignoreResultUnknown(ctx, resp, err)
	}); err != nil {
		return err
	}

	//var doc *api.Document
	//if err = o.fs.pacer.Call(func() (bool, error) {
	//	doc, resp, err = o.fs.service.GetDocByItemID(ctx, dirID)
	//	return ignoreResultUnknown(ctx, resp, err)
	//}); err != nil {
	//	return err
	//}

	r := api.NewUpdateFileInfo()
	r.DocumentID = uploadInfo.DocumentID
	r.Path.Path = name
	r.Path.StartingDocumentID = api.GetDocIDFromDriveID(dirID)
	//r.Path.StartingDocumentID = doc.DocumentID
	r.Data.Receipt = upload.SingleFile.Receipt
	r.Data.Signature = upload.SingleFile.Signature
	r.Data.ReferenceSignature = upload.SingleFile.ReferenceSignature
	r.Data.WrappingKey = upload.SingleFile.WrappingKey
	r.Data.Size = upload.SingleFile.Size
	r.Mtime = modTime.Unix() * 1000
	r.Btime = modTime.Unix() * 1000

	// Update metadata
	var item *api.DriveItem
	if err = o.fs.pacer.Call(func() (bool, error) {
		item, resp, err = o.fs.service.UpdateFile(ctx, &r, api.ZoneFromDriveID(dirID))
		return ignoreResultUnknown(ctx, resp, err)
	}); err != nil {
		return err
	}

	err = o.setMetaData(item)
	if err != nil {
		return err
	}

	o.modTime = modTime
	o.size = src.Size()

	return nil
}

// Disconnect clears authentication state and removes disk caches
func (f *Fs) Disconnect(ctx context.Context) error {
	return disconnectClient(f.m, f.icloud)
}

// Command exposes the Recently Deleted inventory, recovery, and permanent-delete actions.
func (f *Fs) Command(ctx context.Context, name string, arg []string, opt map[string]string) (any, error) {
	switch name {
	case "recently-deleted":
		if len(arg) != 0 {
			return nil, fmt.Errorf("recently-deleted does not accept arguments")
		}
		items, err := f.recentlyDeleted(ctx)
		if err != nil {
			return nil, err
		}
		return map[string]any{"count": len(items), "items": items}, nil
	case "recover", "permanent-delete":
		items, err := f.recentlyDeleted(ctx)
		if err != nil {
			return nil, err
		}
		selected, err := f.selectRecentlyDeleted(items, arg, name == "recover")
		if err != nil {
			return nil, err
		}
		if err := f.mutateRecentlyDeleted(ctx, selected, name == "recover"); err != nil {
			return nil, err
		}
		return map[string]any{"action": name, "items": len(selected), "reconciled": true}, nil
	default:
		return nil, fs.ErrorCommandNotFound
	}
}

var driveCommandHelp = []fs.CommandHelp{{
	Name:  "recently-deleted",
	Short: "List Personal iCloud Drive Recently Deleted items.",
	Long: `Performs an authenticated read of the Recently Deleted inventory and returns
the current items and count. This command is read-only; it does not recover or
delete any items.`,
}, {
	Name:  "recover",
	Short: "Recover selected or all Personal iCloud Drive Recently Deleted items.",
	Long: `Pass one or more exact item names (or drivewsid values) to recover selected
items. With no arguments, this recovers every item currently returned by the
authenticated Recently Deleted inventory. Ambiguous names are rejected.`,
}, {
	Name:  "permanent-delete",
	Short: "Permanently delete selected Personal iCloud Drive Recently Deleted items.",
	Long: `Pass one or more exact item names (or drivewsid values). At least one item is
required; use rclone cleanup to empty the entire Recently Deleted collection.
Ambiguous names are rejected.`,
}}

// Check interfaces are satisfied
var (
	_ fs.Fs              = &Fs{}
	_ fs.Disconnecter    = (*Fs)(nil)
	_ fs.Mover           = (*Fs)(nil)
	_ fs.Purger          = (*Fs)(nil)
	_ fs.CleanUpper      = (*Fs)(nil)
	_ fs.DirMover        = (*Fs)(nil)
	_ fs.DirCacheFlusher = (*Fs)(nil)
	_ fs.Copier          = (*Fs)(nil)
	_ fs.Commander       = (*Fs)(nil)
	_ fs.Object          = &Object{}
	_ fs.IDer            = (*Object)(nil)
)
