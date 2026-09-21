# iCloud Drive rclone Command Reference

These commands assume an `icloud` remote configured with type `iclouddrive`.
Run `rclone config` to create one if needed.

| Operation | Command | Common options | Notes |
|---|---|---|---|
| List directories | `rclone lsd icloud:` | `--max-depth N`, `-R` | Lists directories. |
| List files and directories | `rclone lsf icloud:` | `--dirs-only`, `--files-only`, `-R`, `--format p` | Filter and format the listing. |
| Detailed listing | `rclone ls icloud:` | `-R`, `--max-depth N` | Shows sizes and paths. |
| Long listing | `rclone lsl icloud:` | `-R`, `--max-depth N` | Shows modification times, sizes, and paths. |
| Inspect an object | `rclone stat icloud:path/to/file` | `--json`, `--metadata` | Shared file sizes use authenticated canonical metadata. |
| Calculate size | `rclone size icloud:path` | `--json`, `--max-depth N` | Works for Personal and Shared paths. |
| Compare by size | `rclone check local:path icloud:path --size-only` | `--size-only`, `--one-way` | Uses iCloud canonical sizes. |
| Create a directory | `rclone mkdir icloud:folder/subfolder` | `-v` | Supports Personal and nested Shared targets. |
| Upload a file | `rclone copy local:path icloud:destination` | `-P`, `--transfers N`, `--size-only` | Uploads to Personal or Shared folders. |
| Upload with a chosen name | `rclone copyto local/file icloud:destination/name` | `-P`, `--size-only` | Uploads one file to the specified remote name. |
| Download a file or directory | `rclone copy icloud:source ./destination` | `-P`, `--transfers N`, `--size-only` | Downloads from Personal or Shared paths. |
| Write file contents | `rclone cat icloud:path/to/file` | `--head N`, `--tail N` | Writes contents to standard output. |
| Personal file server-side copy | `rclone copyto icloud:source-file icloud:destination-file` | `-P` | Only Personal files support this copy operation. |
| Move a file | `rclone moveto icloud:source icloud:destination` | `-P`, `--size-only` | Supports Personal↔Personal, Personal↔Shared, Shared↔Personal, and Shared reparenting. |
| Move a directory | `rclone move icloud:source-dir icloud:destination-dir` | `-P`, `--dry-run` | Supports cross-area moves and Shared-folder reparenting. |
| Rename a file or directory | `rclone moveto icloud:old icloud:new` | `-P` | Renames within the same parent directory. |
| Delete a file | `rclone deletefile icloud:path/to/file` | `-v` | Moves the file to Recently Deleted. |
| Delete directory contents | `rclone delete icloud:path/to/dir` | `--rmdirs`, `--dry-run`, `-v` | Review the scope with `--dry-run` first. |
| Delete an empty directory | `rclone rmdir icloud:path/to/dir` | `--dry-run`, `-v` | Moves the directory to Recently Deleted. |
| Delete a directory and its contents | `rclone purge icloud:path/to/dir` | `--dry-run`, `-v` | High-risk operation. |
| List Recently Deleted | `rclone backend recently-deleted icloud:` | None | Read-only inventory; does not recover or delete items. |
| Recover selected items | `rclone backend recover icloud: name-or-drivewsid` | Multiple names or IDs allowed | Recovers one or more items. |
| Recover All | `rclone backend recover icloud:` | None | Recovers the current Recently Deleted inventory. |
| Permanently delete selected items | `rclone backend permanent-delete icloud: name-or-drivewsid` | Multiple names or IDs allowed | Permanent and unrecoverable. |
| Empty Trash | `rclone cleanup icloud:` | None | Permanently clears the current Recently Deleted inventory. |

## Getting `name-or-drivewsid`

First read the Recently Deleted inventory:

```bash
rclone backend recently-deleted icloud:
```

Each `items[]` entry includes fields such as:

- `name`: display name; a file name commonly combines `name` and `extension`.
- `drivewsid`: the complete iCloud Drive item identity for exact selection.
- `type`: normally `FILE` or `FOLDER`.

You can save the output locally and inspect it without publishing private IDs:

```bash
rclone backend recently-deleted icloud: > recently-deleted.json
jq '.items[] | {name, extension, type, drivewsid}' recently-deleted.json
```

Recover by exact name:

```bash
rclone backend recover icloud: "report.txt"
```

Or by the item identity returned in the inventory:

```bash
rclone backend recover icloud: "<full-drivewsid-from-items>"
```

Permanent deletion uses the same selection method:

```bash
rclone backend permanent-delete icloud: "<drivewsid>"
```

If multiple items have the same name, name selection is rejected; use the
corresponding complete item identity instead. Account-specific item identities
must not be pasted into public logs or documentation.

## Path and option conventions

| Placeholder or option | Meaning |
|---|---|
| `icloud:` | The remote name configured by `rclone config`; paths after the colon are relative to its root. |
| `icloud:path` | An iCloud Drive path, such as `icloud:Documents/report.txt` or `icloud:Shared/team/file.txt`. |
| `source` / `destination` | Source and destination paths; these may be iCloud, local, or another configured rclone remote. |
| `local:path` / `./destination` | A local file or directory path. |
| `N` | A user-selected integer, such as the depth, transfer count, or byte count. |
| `--max-depth N` | Limits traversal depth without changing remote data. |
| `--recursive` / `-R` | Recursively processes child directories. |
| `--dirs-only` / `--files-only` | Filters listing output. |
| `--format p` | Makes `lsf` output paths. |
| `--json` | Selects JSON output. |
| `--metadata` | Requests or displays object metadata. |
| `--progress` / `-P`, `--verbose` / `-v` | Controls local progress and diagnostic output. |
| `--dry-run` | Plans standard rclone writes without submitting them; it is not a general safeguard for Recently Deleted backend commands. |
| `--size-only` | Compares or transfers by size only. Shared sizes still come from authenticated backend metadata. |
| `--one-way` | Checks only from source to destination. |
| `--rmdirs` | Allows `delete` to remove empty directories when applicable. |

Authentication parameters such as Apple ID credentials, passwords, trust
tokens, and session cookies come from the rclone configuration flow. Never put
them in this command reference, shell history, or public logs.

## Unsupported Duplicate operations

The following operations are intentionally not provided:

- Shared file Duplicate.
- Personal folder Duplicate; there is no corresponding Web UI operation.
- Shared folder Duplicate; there is no corresponding Web UI operation.

Do not infer a replacement protocol through `copy` or another command.

## Safety notes

- `deletefile`, `delete`, `rmdir`, and `purge` change remote state. Review the
  target with `--dry-run` where supported or with `lsf` first.
- `backend recover`, `backend permanent-delete`, and `cleanup` directly operate
  on Recently Deleted. The latter two can be irreversible.
- If a write has an uncertain outcome, reread and reconcile remote state before
  retrying; do not blindly repeat the write.

For the Chinese version, see [rclone-icloud-commands-CN.md](rclone-icloud-commands-CN.md).
