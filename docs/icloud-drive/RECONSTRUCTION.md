# iCloud Drive implementation reconstruction specification

This document specifies the custom iCloud Drive implementation on the
`icloud-drive` branch. It is intended to be sufficient, together with the
exact upstream baseline and the sanitized fixtures, to recreate a functionally
and protocol-equivalent source tree. The behavior is reverse-engineered from
iCloud web/CloudDocs traffic and disposable-object validation. It is not an
official Apple API.

## A. Baseline

| Property | Value |
|---|---|
| Upstream repository | `https://github.com/rclone/rclone.git` |
| Upstream tag | `v1.75.1` |
| Exact upstream commit | `687d264b689b8c49a67e2e52a8a5e0caa01c04ce` |
| Go toolchain | Go 1.27.1 |
| Release target | `linux/amd64`, static (`CGO_ENABLED=0`) |

The authoritative reconstructed tree is the exact commit above plus
`rclone-v1.75.1-icloud-final.patch`. Do not substitute a later v1.75 branch
commit or another tag with the same version string.

## B. Identity model

Apple exposes overlapping identities. They are not interchangeable.

| Object class | Canonical DriveWS form | Notes |
|---|---|---|
| Personal file | `FILE::<zone>::<document_id>` | `item_id` addresses Documents item endpoints; `document_id`/`docwsid` forms DriveWS identity. |
| Personal folder | `FOLDER::<zone>::<document_id>` | Folder destination and parent identity. |
| Shared root | `SHARED_FOLDER::com.apple.CloudDocs::<id>` | Carries root ShareID and zone context. |
| Shared file | `FILE_IN_SHARED_FOLDER::com.apple.CloudDocs::<document_id>` | CloudKit structure record is `documentStructure/<docwsid>`. |
| Shared folder | `FOLDER_IN_SHARED_FOLDER::com.apple.CloudDocs::<document_id>` | CloudKit structure record is `directory/<UPPERCASE-docwsid>`. |

`drivewsid` is the DriveWS identity sent to create, rename, delete, and move
operations. `item_id` addresses `/v1/item` and `/v1/enumerate`; it bridges
Documents reads to DriveWS metadata. `document_id` is the Documents response
name; `docwsid` is its DriveWS representation. `etag` is the current DriveWS
item version. `clientId` is copied from the source `drivewsid` for observed
move/delete schemas, or is a fresh temporary folder ID for create.

Share context is a nested identity, not decoration:

- `ShareID.shareName` identifies the share in DriveWS envelopes.
- `ShareID.recordName` names the CloudKit share record.
- `ShareID.shareChangeTag` is the current mutable share record tag obtained by
  authenticated `records/lookup` before operations that require it.
- `zoneID.zoneName` is normally `com.apple.CloudDocs`.
- `zoneID.ownerRecordName` selects the Shared owner's CloudKit zone and is
  essential; it must not be omitted or inferred.
- `zoneID.zoneType` is `REGULAR_CUSTOM_ZONE` in CloudKit/Shared write shapes.
- A CloudKit structure record's `recordChangeTag` is distinct from DriveWS
  `etag` and ShareID `shareChangeTag`.

Values are canonical only when they came from a current authenticated response.
The internal `SHARED::<item_id>` value is a dircache lookup key, never an Apple
write identity. Opaque download values are not decoded into identities.

Shared `FILE_IN_SHARED_FOLDER` content reads use the authenticated metadata
`urls.url_download` when supplied, which resolves through the observed
`/v1/download/` endpoint. They must not be forced through the Personal
`/download/by_id` path. Download URL acquisition errors are returned directly;
they must not be masked by a subsequent `DownloadFile` call with an empty URL.

## C. Authenticated metadata resolution

`retrieveItemDetailsInFolders` with `includeHierarchy=true` supplies Shared root
and child DriveWS metadata. `/v1/enumerate/<item_id>` lists Shared children;
`/v1/item/<item_id>` and `/ws/com.apple.CloudDocs/list/lookup_by_id` enrich an
abbreviated listing with document identity, etag, parent, type, size, and owner
metadata. CloudKit `records/lookup` supplies current record change tags.

Root and nested lookup differ. The Shared root may embed direct hierarchy
items, while a nested folder may require an authenticated document lookup and
inherit the root's zone-scoped ShareID. Resolution therefore has separate
source and destination entry points:

- source resolution starts from the source filesystem (`srcFs`), source parent,
  cache key, and item ID;
- destination resolution starts from the destination filesystem and resolves a
  canonical directory plus complete ShareID;
- rename/delete resolution refreshes the target etag and share change tag;
- listing merges Documents fields with the matching DriveWS child by `item_id`.

Both `type=FILE` and `type=FOLDER` returned by authenticated lookup are valid
Shared canonical identities. A historical file-only predicate rejected nested
folders and broke folder moves; it must not be restored.

## D. Routing matrix

| Source | Destination | rclone entry point | Internal route | Protocol and required context |
|---|---|---|---|---|
| Personal file | Personal | `Move` | generic Personal route | `/moveItems`; source drivewsid/etag, Personal destination drivewsid |
| Personal folder | Personal | `DirMove` | generic Personal route | `/moveItems`; source folder's own fresh etag, not source-parent etag |
| Personal file | Shared | `Move` | `movePersonalToShared` | `/moveItems`; Personal source plus complete authenticated Shared destination context |
| Personal folder | Shared | `DirMove` | `movePersonalToShared` | same family; canonical source folder etag and Shared destination context |
| Shared file | Personal | `Move` | Shared-to-Personal route | `/moveItems`; complete source ShareID on `items[0]`, Personal destination |
| Shared folder | Personal | `DirMove` | Shared-to-Personal route | same captured family; canonical source folder identity and source ShareID |
| Shared file | Shared | `Move` | Shared file reparent | CloudKit lookup/modify of `documentStructure/<docwsid>` |
| Shared folder | Shared | `DirMove` | Shared directory reparent | CloudKit lookup/modify of `directory/<UPPERCASE-docwsid>` |

`DirMove`'s `srcDirectoryID` describes the source parent. It is not the source
folder itself. Source and destination `Fs` values cannot be collapsed: each may
have a different root and dircache. `DirMove` may return the source folder ID in
normalized `drivewsid#etag` form. Compare its drivewsid component with the
authenticated `FindDir` result, then use the source folder's own etag; using the
source parent's etag causes `ETAG_CONFLICT`.

## E. `/moveItems`

Personal-to-Personal sends:

```json
{"destinationDrivewsId":"FOLDER::<zone>::<destination>","items":[{"drivewsid":"FILE::<zone>::<source>","etag":"<etag>","clientId":"FILE::<zone>::<source>"}]}
```

Personal-to-Shared adds a top-level `shareID` for the destination. It includes
`shareName`, `recordName`, current `shareChangeTag`, and `zoneID` with both
`zoneName` and `ownerRecordName`. `destinationDrivewsId` is the canonical Shared
root or nested folder ID. Files and folders share this envelope.

Shared-to-Personal instead places the source `shareID` inside each `items[]`
entry and uses an ordinary Personal `destinationDrivewsId`. Files and folders
share this confirmed protocol family.

The source etag and share change tag must be fresh. The historical generic
request for Personal-to-Shared omitted complete Shared context, especially
`ownerRecordName`, and must not be reintroduced.

## F. Shared file reparent

Shared file reparent is not `/moveItems`. It performs a Shared CloudKit
`records/lookup` for `documentStructure/<docwsid>` in the authenticated owner
zone. The response supplies `recordChangeTag` and the existing business fields.
One `records/modify` update then sends:

- `atomic: true` and the same `zoneID`;
- `recordName: documentStructure/<docwsid>`;
- `recordType: structure` and the lookup `recordChangeTag`;
- only captured mutable/business fields (`basehash`, `encryptedBasename`,
  `extension`, and the new `parent` reference);
- both the typed `fields.parent` reference and top-level record `parent`.

The destination Shared root maps to `directory/<shareName>`; a nested folder
maps to `directory/<destination-docwsid>`. A successful modify plus independent
authenticated read reconciliation establishes the result.

## G. Shared folder reparent

Shared folder reparent uses the same CloudKit lookup/modify family but addresses
`directory/<UPPERCASE-docwsid>`. Directory names have no file extension. The
modify preserves captured `basehash` and `encryptedBasename`; if the lookup
omits them, they are constructed from the requested folder name exactly as the
validated implementation does. The parent reference is the canonical Shared
root or nested directory record.

This path must not be replaced by generic `/moveItems`: that historical attempt
returned HTTP 400 and did not implement directory record semantics.

## H. Shared create and upload

Folder creation posts `/createFolders` with canonical
`destinationDrivewsId`, one `folders[]` entry containing name and a fresh
temporary `clientId`, and the authenticated ShareID/zone. The create schema does
not send `shareChangeTag`; adding unobserved fields is not permitted.

Direct Shared upload is a three-stage sequence:

1. POST `/ws/<zone>/upload/web/<ownerRecordName>/<shareRecordName>` with the
   ephemeral validation value in the `token` query and JSON containing
   `filename`, `type`, `content_type`, and `size`.
2. POST bytes once to the returned ephemeral signed upload URL. The response
   yields receipt, checksums/signatures, wrapping key, and size in memory.
3. POST `/ws/com.apple.CloudDocs/update/shared/<ownerRecordName>?errorBreakdown=true`
   with `command=add_file`, document ID, parent/path, flags, times, and the
   returned upload material.

For a Shared root, `path.starting_document_id` is the share record name. For a
nested Shared folder it is that folder's canonical `docwsid`. The implementation
extracts the ephemeral validation value from the authenticated session cookie
but never logs or persists it separately.

Historical failures included missing validation provenance, using a cache ID as
the nested parent, treating a signed URL as stable, and refreshing a completed
Shared upload through a Personal-only metadata path.

## I. Shared rename and delete

Rename posts `/renameItems`; delete posts `/deleteItems`. Each `items[]` entry
uses the canonical Shared `drivewsid`, fresh item `etag`, and complete ShareID
including current share change tag and owner zone. File rename also sends
`extension`; folder rename does not. Delete sends `clientId=drivewsid` and
expects `isDeleted=true`; it is a soft-delete transition, not the Personal
Recently Deleted permanent-delete operation.

Before rename/delete, resolve authenticated canonical metadata and refresh the
share record change tag with CloudKit `records/lookup`. Do not reuse stale cache
identity or invent a tag from an etag.

## J. Recently Deleted

Inventory is an authenticated POST to `/retrieveItemDetailsInFolders` with:

```json
[{"drivewsid":"TRASH_ROOT","partialData":false,"includeHierarchy":true}]
```

Permanent delete posts `/deleteItems`; each Personal file or folder entry
contains `drivewsid`, fresh `etag`, and `clientId=drivewsid`. Recovery posts
`/putBackItemsFromTrash`; each entry contains `drivewsid` and `etag` without
`clientId`.

Single and multi-selection use the same endpoints and differ only in the
`items[]` length. Recover All inventories TRASH_ROOT and sends all returned
items in one recovery request; it has no special all sentinel or endpoint.
Empty Trash inventories TRASH_ROOT and sends all returned items in one
permanent-delete request; it likewise has no special all sentinel.

Command mapping:

- `rclone backend recover remote: name...` selects exact names or drivewsids;
- `rclone backend recover remote:` recovers the complete inventory;
- `rclone backend permanent-delete remote: name...` permanently deletes an
  explicit non-empty selection;
- `rclone cleanup remote:` permanently deletes the complete inventory.

Ambiguous duplicate names require a drivewsid. Shared identities are rejected.
After the one mutation submission, an independent TRASH_ROOT read confirms the
selected identities are absent.

## K. Personal file duplicate

Personal file server-side copy posts `/v1/item/copy/<item_id>` with:

```json
{"info_to_update":{}}
```

The returned `item_id` identifies the new Personal file. The backend performs
an authenticated lookup, then a single non-retried update to place/finalize the
copy at the requested Personal destination. The source must be a Personal
`FILE::` object with a non-empty item ID, and the destination must be a Personal
`FOLDER::`. Other source types return `fs.ErrorCantCopy`; no folder or Shared
copy protocol is inferred.

## L. Shared size metadata

Documents `/v1/enumerate` and `/v1/item` responses expose `item_info.size` as a
JSON string; DriveWS hierarchy responses may expose numeric `size`. Conversion
into `DriveItem.Size` and then `Object.size` supplies list, stat, and
`--size-only`. When a Documents listing is abbreviated, the canonical DriveWS
item is merged by `item_id` without discarding the size already obtained.

The Go representation uses `int64`. Missing size and explicit zero may both map
to `0`; this is a known representational limitation and is not evidence that a
missing value was explicitly supplied by Apple.

## M. Mutation safety

For custom writes, construct and serialize the request once, submit it once,
and do not pass it through a transport path that may reauthenticate and replay
the mutation. Current etags/change tags must come from authenticated reads.

If transport or HTTP status leaves the outcome ambiguous, do not retry. Perform
an independent authenticated read and classify the result:

- requested state observed: treat the mutation as applied;
- old state observed: return the original failure;
- reconciliation read fails or cannot disambiguate: stop with an unresolved
  ambiguous outcome.

Fail closed when canonical identity, ShareID, owner zone, parent, etag, or
record change tag is absent. Never manufacture these from a UUID, opaque value,
cache key, or filename.

## N. Historical failure modes

- Missing `ownerRecordName` made a superficially complete Shared request
  address the wrong/incomplete zone.
- Generic `/moveItems` was used where a Shared-specific route was required.
- A nested rooted-Fs Shared file could retain a raw `FILE::` drivewsid while its
  non-empty item ID and `SHARED::<parent-item-id>` identified it as Shared. A
  Personal-file shortcut that ignored this source context preempted the
  established Shared CloudKit reparent route.
- Cache-shaped `SHARED::<item_id>` identities were promoted into write fields.
- A file-only authenticated resolver rejected legitimate `type=FOLDER` data.
- Nested Shared parent resolution assumed root-shaped metadata.
- `srcFs` and destination `Fs` were interchanged during `DirMove`; source parent
  semantics and destination resolution then diverged.
- `DirMove` compared a normalized authenticated folder ID with an unparsed
  `drivewsid#etag` cache value. The comparison failed and the Personal folder to
  Shared request serialized the source parent's etag instead of the folder's
  fresh authenticated etag.
- Shared folder generic `/moveItems` returned 400; CloudKit directory reparent
  is required.
- Replaying an ambiguous mutation could duplicate or reverse user-visible
  state.
- Capture evidence was sometimes mistaken for permission to extrapolate a new
  protocol. Implementation is limited to observed request families.
- A copied Personal file was initially finalized using the wrong destination
  identity; `starting_document_id` must be the destination folder document ID.
- Shared size was lost when abbreviated enumeration metadata replaced rather
  than merged with authenticated canonical metadata.

## O. Capability matrix

The authoritative matrix is [CAPABILITY-MATRIX.md](CAPABILITY-MATRIX.md). In
summary, Shared CRUD/upload/reparent, all four Personal/Shared move directions,
Shared size/size-only, Personal Recently Deleted delete/recover/all/cleanup, and
Personal file copy are complete. Shared-file duplicate is unsupported/out of
scope. Personal- and Shared-folder duplicate have no Web UI operation and are
out of scope.

## P. Rebuild procedure

Starting from an empty working location with Git and Go 1.27.1:

```sh
git clone https://github.com/rclone/rclone.git rclone
cd rclone
git checkout --detach 687d264b689b8c49a67e2e52a8a5e0caa01c04ce
git status --short
git apply --check /path/to/rclone-v1.75.1-icloud-final.patch
git apply /path/to/rclone-v1.75.1-icloud-final.patch
git diff --check
test -z "$(gofmt -l backend/iclouddrive)"
go test -count=1 ./backend/iclouddrive/...
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o rclone-icloud-final ./
sha256sum rclone-icloud-final
```

The checkout must be clean before applying the patch. Do not copy individual
source, tests, docs, or fixtures: the single patch is the reconstruction unit.
For byte-identical binaries, use the same Go version, environment, flags, and
repository path normalization; source-tree equivalence is mandatory even if
build metadata makes binary hashes differ.

## Q. Test and canary procedure

Run `gofmt -l`, `git diff --check`, targeted API/schema/identity/routing tests,
and `go test -count=1 ./backend/iclouddrive/...`. Fixture JSON parsing is part
of the package tests.

Real validation must use a dedicated non-production account or disposable
namespace and current authenticated metadata. At minimum cover one object for
each distinct family: Shared create/upload/rename/delete; file and folder
CloudKit reparent; Personal file/folder to Shared; Shared file/folder to
Personal; Shared list/stat/size-only; one recover and permanent delete; Recover
All; cleanup; and Personal file copy. Use the smallest object count that still
exercises file/folder or source/destination distinctions.

For every mutation, record the pre-state, issue at most one logical command,
and reconcile with authenticated reads. Never blindly retry. Do not use real
user data, deploy the candidate, or treat test credentials as fixtures.

## R. Evidence map

| Implementation family | Public sanitized fixture |
|---|---|
| Personal file/folder to Shared `/moveItems` | `personal-to-shared-move.json` |
| Shared file/folder to Personal `/moveItems` | `shared-to-personal-move.json` |
| Shared file CloudKit reparent | `shared-file-reparent.json` |
| Shared folder CloudKit reparent | `shared-folder-reparent.json` |
| Shared folder create and direct upload | `shared-create-upload.json` |
| Shared file/folder rename and delete | `shared-rename-delete.json` |
| TRASH_ROOT, permanent delete, recover/all, cleanup | `recently-deleted.json` |
| Personal file copy | `personal-file-copy.json` |
| Shared size/list/stat/size-only | `shared-size.json` |

Fixtures live in `backend/iclouddrive/testdata/protocol/`. They preserve shapes
and identity classes but intentionally omit raw authenticated captures,
credentials, private account values, and stable service guarantees.
