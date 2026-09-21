# iCloud Drive fork capability matrix

This matrix describes the tested scope of the `icloud-drive` branch based on
rclone v1.75.1 commit `687d264b689b8c49a67e2e52a8a5e0caa01c04ce`.

| Family | Capability | Status |
|---|---|---|
| Shared CRUD | Create folder | COMPLETE |
| Shared CRUD | Rename folder | COMPLETE |
| Shared CRUD | Direct file upload | COMPLETE |
| Shared CRUD | Rename file | COMPLETE |
| Shared CRUD | Delete file | COMPLETE |
| Shared CRUD | Delete folder | COMPLETE |
| Shared reparent | File to Shared folder | COMPLETE |
| Shared reparent | Folder to Shared folder | COMPLETE |
| Cross-zone | Personal file to Shared | COMPLETE |
| Cross-zone | Personal folder to Shared | COMPLETE |
| Cross-zone | Shared file to Personal | COMPLETE |
| Cross-zone | Shared folder to Personal | COMPLETE |
| Metadata | Shared size metadata | COMPLETE |
| Metadata | `--size-only` | COMPLETE |
| Shared READ | Content read via authenticated metadata download URL | COMPLETE |
| Recently Deleted | Permanent delete file | COMPLETE |
| Recently Deleted | Permanent delete folder | COMPLETE |
| Recently Deleted | Permanent delete multi-selection | COMPLETE |
| Recently Deleted | Recover file | COMPLETE |
| Recently Deleted | Recover folder | COMPLETE |
| Recently Deleted | Recover multi-selection | COMPLETE |
| Recently Deleted | Recover All | COMPLETE |
| Recently Deleted | Empty Trash / `rclone cleanup` | COMPLETE |
| Copy | Personal file server-side copy | COMPLETE |
| Copy | Shared file duplicate | UNSUPPORTED / OUT OF SCOPE |
| Copy | Personal folder duplicate | NO WEB UI OPERATION / OUT OF SCOPE |
| Copy | Shared folder duplicate | NO WEB UI OPERATION / OUT OF SCOPE |

`COMPLETE` means the implementation is capture-backed, has targeted regression
coverage, and was exercised against disposable real iCloud Drive objects. It
does not imply that Apple exposes a stable or official public API.
