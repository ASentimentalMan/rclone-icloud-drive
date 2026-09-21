# iCloud Drive rclone 指令清单

以下命令针对已配置的 `icloud` remote。配置类型应为 `iclouddrive`，例如首次配置可运行 `rclone config`，然后创建名为 `icloud` 的 remote。

| 功能 | 命令 | 常用参数 | 说明 |
|---|---|---|---|
| 查看文件夹 | `rclone lsd icloud:` | `--max-depth N`<br>`--recursive` = `-R` | 列出文件夹。 |
| 查看文件夹/文件 | `rclone lsf icloud:` | `--dirs-only`<br>`--files-only`<br>`--recursive` = `-R`<br>`--format p` | 列出文件夹和文件；可按类型筛选；`--format p` 只显示路径。 |
| 详细列表 | `rclone ls icloud:` | `--recursive` = `-R`<br>`--max-depth N` | 列出文件大小和路径。 |
| 长格式列表 | `rclone lsl icloud:` | `--recursive` = `-R`<br>`--max-depth N` | 列出修改时间、大小和路径。 |
| 查看单个对象 | `rclone stat icloud:path/to/file` | `--json`<br>`--metadata` | 查看文件或目录的元数据；Shared 文件的大小来自 authenticated canonical metadata。 |
| 统计大小 | `rclone size icloud:path` | `--json`<br>`--max-depth N` | 统计文件数量和总大小，适用于 Personal 与 Shared 路径。 |
| 按大小比较 | `rclone check local:path icloud:path --size-only` | `--size-only`<br>`--one-way` | 使用 iCloud 的 canonical size 做仅按大小比较。同步/复制类命令也可使用 `--size-only`。 |
| 创建目录 | `rclone mkdir icloud:folder/subfolder` | `--verbose` = `-v` | 创建 Personal 或 Shared 文件夹，支持嵌套 Shared 目标。 |
| 上传文件 | `rclone copy local:path icloud:destination` | `--progress` = `-P`<br>`--transfers N`<br>`--size-only` | 上传到 Personal 或 Shared 文件夹；Shared 目标使用直接上传协议。 |
| 上传并指定目标文件名 | `rclone copyto local/file icloud:destination/name` | `--progress` = `-P`<br>`--size-only` | 上传单个文件并指定远端名称。 |
| 下载文件/目录 | `rclone copy icloud:source ./destination` | `--progress` = `-P`<br>`--transfers N`<br>`--size-only` | 从 Personal 或 Shared 路径下载到本地。 |
| 输出文件内容 | `rclone cat icloud:path/to/file` | `--head N`<br>`--tail N` | 读取文件内容到标准输出。 |
| Personal 文件服务端复制 | `rclone copyto icloud:source-file icloud:destination-file` | `--progress` = `-P` | 仅支持 Personal 文件的 server-side copy；不支持 Shared 文件或文件夹 Duplicate。 |
| 移动文件 | `rclone moveto icloud:source icloud:destination` | `--progress` = `-P`<br>`--size-only` | 支持 Personal↔Personal、Personal↔Shared、Shared↔Personal，以及 Shared 文件在 Shared 内重挂载。 |
| 移动目录 | `rclone move icloud:source-dir icloud:destination-dir` | `--progress` = `-P`<br>`--dry-run` | 支持 Personal↔Personal、Personal↔Shared、Shared↔Personal，以及 Shared 文件夹在 Shared 内重挂载。 |
| 重命名文件/目录 | `rclone moveto icloud:old icloud:new` | `--progress` = `-P` | 同一父目录下改名；Shared 文件和文件夹使用已验证的 Shared rename 协议。 |
| 删除文件（移入 Recently Deleted） | `rclone deletefile icloud:path/to/file` | `--verbose` = `-v` | 删除单个文件，进入 Recently Deleted，不是永久删除。 |
| 删除目录内容 | `rclone delete icloud:path/to/dir` | `--rmdirs`<br>`--dry-run`<br>`--verbose` = `-v` | 删除目录下文件；删除前可用 `--dry-run` 检查范围。 |
| 删除空目录 | `rclone rmdir icloud:path/to/dir` | `--dry-run`<br>`--verbose` = `-v` | 删除空目录，进入 Recently Deleted。 |
| 删除目录及全部内容 | `rclone purge icloud:path/to/dir` | `--dry-run`<br>`--verbose` = `-v` | 清空并删除指定目录；这是高风险操作。 |
| 查看 Recently Deleted | `rclone backend recently-deleted icloud:` | 无参数 | 每次执行 authenticated 读取 Recently Deleted，返回 `{count, items}`；只读，不恢复、不删除。 |
| 恢复指定项目 | `rclone backend recover icloud: name-or-drivewsid` | 可传多个精确名称或 `drivewsid` | 从 Recently Deleted 恢复一个或多个项目；名称有歧义时使用 `drivewsid`。 |
| Recover All | `rclone backend recover icloud:` | 无参数 | 恢复当前 Recently Deleted inventory 中的全部项目。 |
| Recently Deleted 永久删除 | `rclone backend permanent-delete icloud: name-or-drivewsid` | 可传多个精确名称或 `drivewsid` | 永久删除指定项目，不可恢复。 |
| Empty Trash | `rclone cleanup icloud:` | 无参数 | 永久清空当前 Recently Deleted inventory；不可恢复，执行前应先用 `backend recently-deleted` 检查。 |

## `name-or-drivewsid` 如何获取

先读取 Recently Deleted 清单：

```bash
rclone backend recently-deleted icloud:
```

返回 JSON 中，每个 `items[]` 元素包含项目的名称和身份字段，主要使用：

- `name`：显示名称。文件名通常包含 `name` 与 `extension` 的组合。
- `drivewsid`：项目的完整 iCloud Drive 身份值，适合精确指定。
- `type`：通常为 `FILE` 或 `FOLDER`。

例如先把输出保存到本地临时文件后查看（不要把包含私有 ID 的输出公开）：

```bash
rclone backend recently-deleted icloud: > recently-deleted.json
jq '.items[] | {name, extension, type, drivewsid}' recently-deleted.json
```

然后可以按精确名称恢复：

```bash
rclone backend recover icloud: "report.txt"
```

或者直接使用 `drivewsid`：

```bash
rclone backend recover icloud: "<full-drivewsid-from-items>"
```

永久删除的参数获取方式相同：

```bash
rclone backend permanent-delete icloud: "<drivewsid>"
```

如果多个项目名称相同，名称选择会被拒绝，此时必须使用对应的完整 `drivewsid`。`drivewsid` 是账户相关的私有身份值，不应贴到公开日志或文档中。

## 命令中其他参数的来源

| 占位符/参数 | 来源和含义 |
|---|---|
| `icloud:` | `rclone config` 中配置的 remote 名称；本清单假定名称为 `icloud`。冒号后面的路径都相对于该 remote 的根目录。 |
| `icloud:path` | iCloud Drive 内的路径，例如 `icloud:Documents/report.txt` 或 `icloud:Shared/team/file.txt`。不要填写本地文件系统绝对路径。 |
| `source` / `destination` | 命令的源和目标路径；可分别是 `icloud:...`、本地路径（如 `./file`）或另一个已配置的 rclone remote。 |
| `local:path` / `./destination` | 本地文件或目录路径，由当前 shell 的工作目录和文件系统决定。 |
| `N` | 用户自行指定的整数。例如 `--max-depth 2` 表示最多遍历两层，`--transfers 4` 表示并行传输数，`--head 100` / `--tail 100` 表示读取字节数。 |
| `--max-depth N` | rclone 遍历深度限制；不改变 iCloud 数据。 |
| `--recursive` / `-R` | 是否递归处理子目录；由用户按需要指定。 |
| `--dirs-only` / `--files-only` | 列表输出筛选条件；不改变远端对象。 |
| `--format p` | `lsf` 的输出格式选项，`p` 表示路径；属于 rclone 通用列表参数。 |
| `--json` | 将命令输出改为 JSON；不改变请求协议。 |
| `--metadata` | 请求/显示 rclone 对象元数据；不改变 iCloud 身份字段。 |
| `--progress` / `-P`、`--verbose` / `-v` | 本地输出详细程度或进度显示。 |
| `--dry-run` | 只规划操作、不提交相应的标准 rclone 写操作；执行 Recently Deleted 专用 backend command 时不要把它当作通用保护开关。 |
| `--size-only` | rclone 比较/传输选项，只按大小判断，不是 iCloud 专用参数。Shared 文件大小仍由 backend 的 authenticated metadata 提供。 |
| `--one-way` | `check` 的方向选项，只检查源到目标的对象，不会改变 iCloud。 |
| `--rmdirs` | `delete` 的目录清理选项；是否删除空目录由用户明确指定。 |

认证参数（Apple ID、密码、trust token 或 session cookies）来自 rclone 配置流程和配置文件，不应写在命令清单、shell 历史或公开日志中。

## 明确不支持的 Duplicate 操作

以下操作没有列入可用命令：

- Shared file Duplicate：不支持。
- Personal folder Duplicate：Web UI 没有该操作。
- Shared folder Duplicate：Web UI 没有该操作。

这些限制是有意保留的，不应通过 `copy` 或其他命令猜测替代协议。

## 安全提示

- `deletefile`、`delete`、`rmdir`、`purge` 会改变云端状态；先用 `--dry-run`（适用时）或 `lsf` 检查目标。
- `backend recover`、`backend permanent-delete` 和 `cleanup` 是 Recently Deleted 的直接操作；其中后两者可能不可逆。
- 写操作遇到不明确结果时不要盲目重试，应先重新读取并核对远端状态。
