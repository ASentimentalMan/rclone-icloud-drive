# Sanitized iCloud Drive protocol fixtures

These fixtures preserve public-safe protocol shapes observed in authenticated
iCloud web/CloudDocs traffic and in successful disposable-object canaries. They
are reconstruction evidence, not live requests and not an API stability claim.

Sanitization replaced account-scoped IDs, names, etags/change tags, session
values, signed upload material, URLs, timestamps, and file contents with typed
synthetic placeholders. Cookies, authorization headers, bearer values, DSIDs,
Apple web-auth values, account identifiers, email addresses, private hosts,
signed URLs, keys, receipts, and reusable credentials are absent. Field names,
JSON nesting, identity classes, methods, endpoints, and representative response
structures were retained. Raw captures are intentionally not included.

| Fixture | Origin category | What it proves | What it does not prove |
|---|---|---|---|
| `personal-to-shared-move.json` | successful Web/canary move | destination ShareID is top-level and includes zone owner | current service availability |
| `shared-to-personal-move.json` | successful Web/canary move | source ShareID belongs on `items[]` | Shared-to-Shared routing |
| `shared-file-reparent.json` | successful Shared file move | CloudKit documentStructure lookup/modify and parent reference | folder record naming |
| `shared-folder-reparent.json` | successful Shared folder move | CloudKit directory lookup/modify and parent reference | file record naming |
| `shared-create-upload.json` | successful create/upload canaries | createFolders plus upload initialization, binary stage, and commit shapes | reusable upload credentials |
| `shared-rename-delete.json` | successful Shared CRUD canaries | fresh etag/share context and file/folder request variants | permanent deletion |
| `recently-deleted.json` | archived Apple Web capture and canaries | TRASH_ROOT read, delete/recover items batching, all/cleanup semantics | Shared trash behavior |
| `personal-file-copy.json` | successful Personal copy canary | copy endpoint, empty update map, representative result identity | folder or Shared copy support |
| `shared-size.json` | authenticated Shared reads | size sources consumed by list/stat/size-only | distinction between missing and explicit zero |

The fixture files must remain valid JSON. Placeholders beginning with `<` are
non-secret examples and are never valid credentials. Implementation families
and their fixtures are cross-referenced in
`docs/icloud-drive/RECONSTRUCTION.md`.
