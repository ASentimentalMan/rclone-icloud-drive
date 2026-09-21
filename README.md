# rclone iCloud Drive Shared Folders

An unofficial rclone fork focused on reliable iCloud Drive Shared Folder
operations, including nested Shared folders and shared subdirectories.

## About this fork

This is an unofficial fork of [rclone/rclone](https://github.com/rclone/rclone),
based on rclone v1.75.1 at upstream commit
`687d264b689b8c49a67e2e52a8a5e0caa01c04ce`. The work is concentrated in the
`iclouddrive` backend and is published on the `icloud-drive` branch. It is not
an official rclone or Apple release, and upstream rclone does not necessarily
include these changes.

## Why this fork?

The goal is to make rclone more useful with iCloud Drive Shared folders:
listing nested Shared folders, reading and writing Shared content, moving files
between Personal and Shared areas, and handling Recently Deleted items. The
implementation follows observed iCloud web behavior, which Apple may change
without notice. Related upstream issue: [rclone/rclone#9477](https://github.com/rclone/rclone/issues/9477).

## Quick Start

Configure an `icloud` remote with `rclone config`, then use the normal rclone
commands:

```bash
rclone lsf icloud:
rclone lsf icloud:Shared
rclone lsf "icloud:Shared/example-folder" -R
rclone copy icloud:Shared/example-folder ./example-folder -P
```

See the [complete iCloud Drive command reference](docs/rclone-icloud-drive-commands.md)
(English) and the [Chinese command reference](docs/rclone-icloud-commands-CN.md).

## iCloud Drive Commands

| Operation | Command |
|---|---|
| List Personal | `rclone lsf icloud:` |
| List Shared | `rclone lsf icloud:Shared` |
| List nested Shared content | `rclone lsf "icloud:Shared/<folder>" -R` |
| Show metadata | `rclone stat icloud:path/to/file --metadata` |
| Show size | `rclone size icloud:path` |
| Create a folder | `rclone mkdir icloud:folder/subfolder` |
| Upload | `rclone copy local:path icloud:destination -P` |
| Download | `rclone copy icloud:source ./destination -P` |
| Personal server-side copy | `rclone copyto icloud:source-file icloud:destination-file` |
| Move or reparent | `rclone moveto icloud:source icloud:destination` |
| Rename | `rclone moveto icloud:old icloud:new` |
| List Recently Deleted | `rclone backend recently-deleted icloud:` |
| Recover selected items | `rclone backend recover icloud: name-or-drivewsid` |
| Recover All | `rclone backend recover icloud:` |
| Empty Trash | `rclone cleanup icloud:` |

For full parameters, Personal/Shared examples, cross-area moves, Recently
Deleted, Duplicate behavior, and safety limits, see the [complete command
reference](docs/rclone-icloud-drive-commands.md).

## Capability Matrix

| Capability | Status |
|---|---|
| Personal listing | Supported |
| Shared listing | Supported |
| Nested Shared listing | Supported |
| Personal download | Supported |
| Shared download | Supported |
| Personal upload | Supported |
| Shared upload | Supported |
| Personal mkdir | Supported |
| Shared mkdir | Supported |
| Personal rename | Supported |
| Shared rename | Supported |
| Personal move | Supported |
| Shared → Shared move/reparent | Supported |
| Personal → Shared move | Supported |
| Shared → Personal move | Supported |
| Shared folder move/reparent | Supported |
| Recently Deleted listing | Supported |
| Recover selected items | Supported |
| Recover All | Supported |
| Permanent Delete | Supported |
| Empty Trash | Supported |
| Personal file Duplicate | Supported |
| Shared file Duplicate | Unsupported |
| Folder Duplicate | Unsupported |

## Known Limitations

- Shared file Duplicate is unsupported.
- Personal folder Duplicate and Shared folder Duplicate are unsupported; the
  corresponding Web UI operations are not available.
- This backend depends on private iCloud web behavior rather than a stable,
  official Apple API.

## Documentation

- [Complete iCloud Drive command reference](docs/rclone-icloud-drive-commands.md)
- [Chinese command reference](docs/rclone-icloud-commands-CN.md)
- [Capability matrix](docs/icloud-drive/CAPABILITY-MATRIX.md)
- [Implementation reconstruction](docs/icloud-drive/RECONSTRUCTION.md)
- [Sanitized protocol fixture guide](backend/iclouddrive/testdata/protocol/README.md)

## Upstream

This project is based on [rclone](https://github.com/rclone/rclone), an
open-source command-line program for managing files across cloud storage
providers. Upstream attribution and the MIT license remain unchanged. This
fork is not an official rclone release.

## Storage providers

- 1Fichier [:page_facing_up:](https://rclone.org/fichier/)
- Akamai Netstorage [:page_facing_up:](https://rclone.org/netstorage/)
- Alibaba Cloud (Aliyun) Object Storage System (OSS) [:page_facing_up:](https://rclone.org/s3/#alibaba-oss)
- Amazon S3 [:page_facing_up:](https://rclone.org/s3/)
- ArvanCloud Object Storage (AOS) [:page_facing_up:](https://rclone.org/s3/#arvan-cloud-object-storage-aos)
- Bizfly Cloud Simple Storage [:page_facing_up:](https://rclone.org/s3/#bizflycloud)
- Backblaze B2 [:page_facing_up:](https://rclone.org/b2/)
- Box [:page_facing_up:](https://rclone.org/box/)
- Ceph [:page_facing_up:](https://rclone.org/s3/#ceph)
- China Mobile Ecloud Elastic Object Storage (EOS) [:page_facing_up:](https://rclone.org/s3/#china-mobile-ecloud-eos)
- Citrix ShareFile [:page_facing_up:](https://rclone.org/sharefile/)
- Cloudflare R2 [:page_facing_up:](https://rclone.org/s3/#cloudflare-r2)
- Cloudinary [:page_facing_up:](https://rclone.org/cloudinary/)
- Cubbit DS3 [:page_facing_up:](https://rclone.org/s3/#Cubbit)
- DigitalOcean Spaces [:page_facing_up:](https://rclone.org/s3/#digitalocean-spaces)
- Digi Storage [:page_facing_up:](https://rclone.org/koofr/#digi-storage)
- Dreamhost [:page_facing_up:](https://rclone.org/s3/#dreamhost)
- Drime [:page_facing_up:](https://rclone.org/s3/#drime)
- Dropbox [:page_facing_up:](https://rclone.org/dropbox/)
- Enterprise File Fabric [:page_facing_up:](https://rclone.org/filefabric/)
- Exaba [:page_facing_up:](https://rclone.org/s3/#exaba)
- Fastly Object Storage [:page_facing_up:](https://rclone.org/s3/#fastly)
- Fastmail Files [:page_facing_up:](https://rclone.org/webdav/#fastmail-files)
- FileLu [:page_facing_up:](https://rclone.org/filelu/)
- Filen [:page_facing_up:](https://rclone.org/filen/)
- Files.com [:page_facing_up:](https://rclone.org/filescom/)
- FlashBlade [:page_facing_up:](https://rclone.org/s3/#pure-storage-flashblade)
- FTP [:page_facing_up:](https://rclone.org/ftp/)
- GoFile [:page_facing_up:](https://rclone.org/gofile/)
- Google Cloud Storage [:page_facing_up:](https://rclone.org/googlecloudstorage/)
- Google Drive [:page_facing_up:](https://rclone.org/drive/)
- Google Photos [:page_facing_up:](https://rclone.org/googlephotos/)
- HDFS (Hadoop Distributed Filesystem) [:page_facing_up:](https://rclone.org/hdfs/)
- Hetzner Object Storage [:page_facing_up:](https://rclone.org/s3/#hetzner)
- Hetzner Storage Box [:page_facing_up:](https://rclone.org/sftp/#hetzner-storage-box)
- HiDrive [:page_facing_up:](https://rclone.org/hidrive/)
- Hitachi Content Platform (HCP) [:page_facing_up:](https://rclone.org/s3/#hcp)
- HTTP [:page_facing_up:](https://rclone.org/http/)
- Huawei Cloud Object Storage Service(OBS) [:page_facing_up:](https://rclone.org/s3/#huawei-obs)
- Huawei Drive [:page_facing_up:](https://rclone.org/huaweidrive/)
- iCloud Drive [:page_facing_up:](https://rclone.org/iclouddrive/)
- ImageKit [:page_facing_up:](https://rclone.org/imagekit/)
- Internet Archive [:page_facing_up:](https://rclone.org/internetarchive/)
- Internxt [:page_facing_up:](https://rclone.org/internxt/)
- Jottacloud [:page_facing_up:](https://rclone.org/jottacloud/)
- IBM COS S3 [:page_facing_up:](https://rclone.org/s3/#ibm-cos-s3)
- Impossible Cloud [:page_facing_up:](https://rclone.org/s3/#impossible-cloud)
- Intercolo Object Storage [:page_facing_up:](https://rclone.org/s3/#intercolo)
- IONOS Cloud [:page_facing_up:](https://rclone.org/s3/#ionos)
- Koofr [:page_facing_up:](https://rclone.org/koofr/)
- Leviia Object Storage [:page_facing_up:](https://rclone.org/s3/#leviia)
- Liara Object Storage [:page_facing_up:](https://rclone.org/s3/#liara-object-storage)
- Linkbox [:page_facing_up:](https://rclone.org/linkbox)
- Linode Object Storage [:page_facing_up:](https://rclone.org/s3/#linode)
- Magalu Object Storage [:page_facing_up:](https://rclone.org/s3/#magalu)
- Mail.ru Cloud [:page_facing_up:](https://rclone.org/mailru/)
- Memset Memstore [:page_facing_up:](https://rclone.org/swift/)
- MEGA [:page_facing_up:](https://rclone.org/mega/)
- MEGA S4 Object Storage [:page_facing_up:](https://rclone.org/s3/#mega)
- Memory [:page_facing_up:](https://rclone.org/memory/)
- Microsoft Azure Blob Storage [:page_facing_up:](https://rclone.org/azureblob/)
- Microsoft Azure Files Storage [:page_facing_up:](https://rclone.org/azurefiles/)
- Microsoft OneDrive [:page_facing_up:](https://rclone.org/onedrive/)
- Minio [:page_facing_up:](https://rclone.org/s3/#minio)
- Nextcloud [:page_facing_up:](https://rclone.org/webdav/#nextcloud)
- Blomp Cloud Storage [:page_facing_up:](https://rclone.org/swift/)
- OpenDrive [:page_facing_up:](https://rclone.org/opendrive/)
- OpenStack Swift [:page_facing_up:](https://rclone.org/swift/)
- Oracle Cloud Storage [:page_facing_up:](https://rclone.org/swift/)
- Oracle Object Storage [:page_facing_up:](https://rclone.org/oracleobjectstorage/)
- Outscale [:page_facing_up:](https://rclone.org/s3/#outscale)
- OVHcloud Object Storage (Swift) [:page_facing_up:](https://rclone.org/swift/)
- OVHcloud Object Storage (S3-compatible) [:page_facing_up:](https://rclone.org/s3/#ovhcloud)
- ownCloud [:page_facing_up:](https://rclone.org/webdav/#owncloud)
- pCloud [:page_facing_up:](https://rclone.org/pcloud/)
- Petabox [:page_facing_up:](https://rclone.org/s3/#petabox)
- PikPak [:page_facing_up:](https://rclone.org/pikpak/)
- Pixeldrain [:page_facing_up:](https://rclone.org/pixeldrain/)
- premiumize.me [:page_facing_up:](https://rclone.org/premiumizeme/)
- put.io [:page_facing_up:](https://rclone.org/putio/)
- Proton Drive [:page_facing_up:](https://rclone.org/protondrive/)
- QingStor [:page_facing_up:](https://rclone.org/qingstor/)
- Qiniu Cloud Object Storage (Kodo) [:page_facing_up:](https://rclone.org/s3/#qiniu)
- Rabata Cloud Storage [:page_facing_up:](https://rclone.org/s3/#Rabata)
- Quatrix [:page_facing_up:](https://rclone.org/quatrix/)
- Rackspace Cloud Files [:page_facing_up:](https://rclone.org/swift/)
- RackCorp Object Storage [:page_facing_up:](https://rclone.org/s3/#RackCorp)
- rsync.net [:page_facing_up:](https://rclone.org/sftp/#rsync-net)
- Scaleway [:page_facing_up:](https://rclone.org/s3/#scaleway)
- Scality (RING / ARTESCA) [:page_facing_up:](https://rclone.org/s3/#scality)
- Seafile [:page_facing_up:](https://rclone.org/seafile/)
- Seagate Lyve Cloud [:page_facing_up:](https://rclone.org/s3/#lyve)
- SeaweedFS [:page_facing_up:](https://rclone.org/s3/#seaweedfs)
- Selectel Object Storage [:page_facing_up:](https://rclone.org/s3/#selectel)
- Servercore Object Storage [:page_facing_up:](https://rclone.org/s3/#servercore)
- SFTP [:page_facing_up:](https://rclone.org/sftp/)
- Shade [:page_facing_up:](https://rclone.org/shade/)
- SMB / CIFS [:page_facing_up:](https://rclone.org/smb/)
- Spectra Logic [:page_facing_up:](https://rclone.org/s3/#spectralogic)
- Storj [:page_facing_up:](https://rclone.org/storj/)
- SugarSync [:page_facing_up:](https://rclone.org/sugarsync/)
- Synology C2 Object Storage [:page_facing_up:](https://rclone.org/s3/#synology-c2)
- Tencent Cloud Object Storage (COS) [:page_facing_up:](https://rclone.org/s3/#tencent-cos)
- Uloz.to [:page_facing_up:](https://rclone.org/ulozto/)
- US3 Object Storage [:page_facing_up:](https://rclone.org/s3/#us3)
- Wasabi [:page_facing_up:](https://rclone.org/s3/#wasabi)
- WebDAV [:page_facing_up:](https://rclone.org/webdav/)
- Yandex Disk [:page_facing_up:](https://rclone.org/yandex/)
- Zadara Object Storage [:page_facing_up:](https://rclone.org/s3/#zadara)
- Zero Services (ZERO-Z3) [:page_facing_up:](https://rclone.org/s3/#zero-z3)
- Zoho WorkDrive [:page_facing_up:](https://rclone.org/zoho/)
- Zata.ai [:page_facing_up:](https://rclone.org/s3/#Zata)
- The local filesystem [:page_facing_up:](https://rclone.org/local/)

Please see [the full list of all storage providers and their features](https://rclone.org/overview/)

### Virtual storage providers

These backends adapt or modify other storage providers

- Alias: rename existing remotes [:page_facing_up:](https://rclone.org/alias/)
- Archive: read archive files [:page_facing_up:](https://rclone.org/archive/)
- Cache: cache remotes (DEPRECATED) [:page_facing_up:](https://rclone.org/cache/)
- Chunker: split large files [:page_facing_up:](https://rclone.org/chunker/)
- Combine: combine multiple remotes into a directory tree [:page_facing_up:](https://rclone.org/combine/)
- Compress: compress files [:page_facing_up:](https://rclone.org/compress/)
- Crypt: encrypt files [:page_facing_up:](https://rclone.org/crypt/)
- Hasher: hash files [:page_facing_up:](https://rclone.org/hasher/)
- Union: join multiple remotes to work together [:page_facing_up:](https://rclone.org/union/)

## Features

- MD5/SHA-1 hashes checked at all times for file integrity
- Timestamps preserved on files
- Partial syncs supported on a whole file basis
- [Copy](https://rclone.org/commands/rclone_copy/) mode to just copy new/changed
  files
- [Sync](https://rclone.org/commands/rclone_sync/) (one way) mode to make a directory
  identical
- [Bisync](https://rclone.org/bisync/) (two way) to keep two directories in sync
  bidirectionally
- [Check](https://rclone.org/commands/rclone_check/) mode to check for file hash
  equality
- Can sync to and from network, e.g. two different cloud accounts
- Optional large file chunking ([Chunker](https://rclone.org/chunker/))
- Optional transparent compression ([Compress](https://rclone.org/compress/))
- Optional encryption ([Crypt](https://rclone.org/crypt/))
- Optional FUSE mount ([rclone mount](https://rclone.org/commands/rclone_mount/))
- Multi-threaded downloads to local disk
- Can [serve](https://rclone.org/commands/rclone_serve/) local or remote files
  over HTTP/WebDAV/FTP/SFTP/DLNA

## Installation & documentation

Please see the [rclone website](https://rclone.org/) for:

- [Installation](https://rclone.org/install/)
- [Documentation & configuration](https://rclone.org/docs/)
- [Changelog](https://rclone.org/changelog/)
- [FAQ](https://rclone.org/faq/)
- [Storage providers](https://rclone.org/overview/)
- [Forum](https://forum.rclone.org/)
- ...and more

## Downloads

- <https://rclone.org/downloads/>

## License

This is free software under the terms of the MIT license (check the
[COPYING file](/COPYING) included in this package).
