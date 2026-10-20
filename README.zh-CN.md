# Readest Tiny 后端

[English](README.md) | [简体中文](README.zh-CN.md)

一个兼容当前 `readest-main` 客户端的自托管后端，使用 Go、Gin、GORM、SQLite 和本地对象存储，内嵌基于 React、TypeScript 和 Ant Design 的管理员面板。

已实现的客户端接口：

- `GET /runtime-config.js`：供 Readest 的自定义服务器地址表单读取运行时配置。
- `/auth/v1`：兼容 Supabase Auth 的邮箱密码登录、会话刷新、用户更新、退出登录及密码恢复；禁用公开注册。
- `GET/POST /api/sync`：同步书籍、阅读配置、书签和批注，支持删除墓碑、服务端游标、分页拉取、最后写入者胜出（LWW）冲突处理，以及封面、阅读状态和元数据的字段级合并。
- `GET/PUT /api/privacy-sync`：按账号同步经过端到端加密的隐私设置，使用原子的最后写入者胜出操作处理冲突。
- `/api/storage/upload`、`/download`、`/list` 和 `/delete`：通过短期有效的签名 URL 上传、下载和管理本地文件。
- `/admin/`：仅管理员可访问，用于创建和删除账号、查看用量统计及设置每个账号的存储配额。
- 全服务器加密备份到 WebDAV 或 S3 兼容存储，并由管理员在内嵌面板中恢复。

运行时配置中的 `objectStorageType: "r2"` 表示客户端的对象键命名策略，文件仍保存在服务器本地。配置还会声明禁用公开注册、邮箱修改立即生效，以及同步请求的大小限制。更新后的客户端会在连接服务器时保存这些设置；已有客户端升级后，请重新连接一次服务器地址以加载新增配置。

书籍拉取游标采用可无损往返的毫秒精度，并按账号单调递增，阅读进度更新也会推进游标。旧的亚毫秒记录会返回向上取整的游标，并补齐边界组中的记录。客户端应将返回的游标原样用于下一次严格大于该游标的拉取，并同时按照服务端声明的记录数和 UTF-8 请求体字节数限制分批推送。

## 快速开始

需要 Go 1.25 和 C 编译器，因为 GORM 官方 SQLite 驱动依赖 CGO。仅在重新构建管理员面板时需要 Node.js 24。较旧的 Go 安装若启用了自动工具链选择，可通过 `GOPROXY` 下载所需工具链。

PowerShell：

```powershell
$env:GOPROXY = 'https://goproxy.cn,direct'
go mod download
Set-Location web/admin
npm ci --registry=https://registry.npmmirror.com
npm run build
Set-Location ../..
go run ./cmd/readest-server
```

服务默认启动于 `http://127.0.0.1:8080`，根路径会跳转到管理员面板 `http://127.0.0.1:8080/admin/`。在 Readest 登录页的 **Server endpoint（服务器地址）** 中填写 `http://127.0.0.1:8080`。

首次启动时，服务会创建 `data/readest.db`、`data/objects`、私有随机密钥 `data/jwt-secret` 和管理员账号。默认管理员邮箱为 `admin@readest.local`，随机生成的密码会写入仅文件所有者可访问的 `data/initial-admin-credentials.json`：

```powershell
Get-Content .\data\initial-admin-credentials.json
```

登录管理员面板后，请修改生成的密码。管理员密码修改成功后，凭据文件会被删除。也可以在首次启动前设置 `INITIAL_ADMIN_EMAIL` 和长度为 12～72 字节的 `INITIAL_ADMIN_PASSWORD`；显式设置的密码不会写入磁盘。初始化设置不会重置已有管理员。

公开账号注册始终禁用。请通过管理员面板创建阅读账号，再将登录凭据交给使用者。

请备份整个 `data` 目录。丢失 `jwt-secret` 会导致所有客户端退出登录；丢失数据库或对象目录会丢失同步元数据或书籍。

Docker：

```powershell
$env:NODE_IMAGE = 'your-trusted-registry/library/node:24-alpine' # 可选
$env:GO_IMAGE = 'your-trusted-registry/library/golang:1.25-alpine' # 可选
$env:ALPINE_IMAGE = 'your-trusted-registry/library/alpine:3.21' # 可选
$env:GOPROXY = 'https://goproxy.cn,direct'
docker compose up --build

docker compose exec readest-backend cat /app/data/initial-admin-credentials.json
```

仅在无法访问 Docker Hub 时才需要覆盖基础镜像地址，请使用可信的镜像仓库。Compose 还支持 `BIND_HOST`、`PORT` 和 `READEST_IMAGE`。数据保存在名为 `readest-data` 的卷中，以保证非 root 容器的文件所有权正确。请将该卷纳入备份；`docker compose down` 会保留数据，而 `docker compose down -v` 会主动删除数据卷。

## 自动发布 Docker 镜像

向任意分支 push 都会触发 [Publish Docker image](.github/workflows/docker-publish.yml)，自动构建 `linux/amd64` 和 `linux/arm64` 镜像并推送到 `ghcr.io/<owner>/<repository>`。工作流使用仓库自带的 `GITHUB_TOKEN` 和 `packages: write` 权限，无需额外配置镜像仓库密钥，也支持在 GitHub Actions 页面手动运行。

- `branch-<分支名>`：跟随对应分支，`/` 等不受镜像标签支持的字符会替换为 `-`。
- `sha-<完整提交哈希>`：标识镜像对应的源码提交。
- `edge`：跟随默认分支。稳定版 `latest` 和版本标签仍由下方的版本发布流程维护。

同一分支的新 push 会取消尚未完成的旧镜像构建。首次发布后，请在 GitHub Packages 设置中确认镜像的可见性及仓库访问权限。认证和权限机制可参考 [GitHub 容器镜像发布指南](https://docs.github.com/en/actions/tutorials/publish-packages/publish-docker-images)。

通过 Compose 运行默认分支镜像：

```powershell
$env:READEST_IMAGE = 'ghcr.io/gjh303987897/readest-server-tiny:edge'
docker compose pull
docker compose up -d
```

## 版本发布

推送语义化版本标签会触发统一的发布工作流：先向 GHCR 发布支持 `linux/amd64` 和 `linux/arm64` 的镜像，再创建 GitHub Release，提供 Linux amd64/arm64、Windows amd64、macOS amd64/arm64 二进制文件及 SHA-256 校验和。每个二进制文件均包含管理员面板和 SQLite 支持。标签可以使用 `v` 前缀，也可以包含预发布后缀，例如 `0.0.1-alpha`。

```powershell
git tag v1.0.0
git push origin v1.0.0
```

预发布标签会创建 GitHub Pre-release 和精确版本的镜像标签，不会更新稳定版的次版本标签或 `latest` 标签：

```powershell
git tag 0.0.1-alpha
git push origin 0.0.1-alpha
```

使用现有 Compose 配置运行已发布的多架构镜像时，将 `READEST_IMAGE` 设置为固定的发布版本。Docker 会自动选择匹配的镜像架构：

```powershell
$env:READEST_IMAGE = 'ghcr.io/gjh303987897/readest-server-tiny:1.0.0'
docker compose pull
docker compose up -d
```

## 配置

参考 [`.env.example`](.env.example)，将配置写入进程环境变量，或写入由进程管理器加载的本地 `.env` 文件。服务二进制本身不会解析 `.env` 文件。

生产环境的重要配置：

- `APP_ENV=production` 要求 `PUBLIC_URL` 使用 HTTPS。
- 将服务放在 TLS 反向代理后面，由代理转发到内部 HTTP 端口。
- 持久化并备份 `DATA_DIR`，不要通过 Web 服务器暴露该目录。
- 首次生产启动前设置 `INITIAL_ADMIN_EMAIL`，并优先通过部署环境的密钥管理工具注入 `INITIAL_ADMIN_PASSWORD`。不要将密码写入会提交到仓库的 `.env` 文件。
- 将 `CORS_ALLOWED_ORIGINS` 和 `ALLOWED_REDIRECT_URLS` 设置为实际使用的来源和回调地址。
- 配置 `SMTP_HOST` 和 `SMTP_FROM` 以启用密码恢复邮件。
- 不要将 `JWT_SECRET`、SMTP 凭据、SQLite 数据库文件或存储对象提交到版本控制。

`PUBLIC_ANON_KEY` 会通过运行时配置提供给 Readest，因此它是公开值，仅用于标识 API。访问授权始终由经过签名、可撤销的 Bearer 会话决定。

## WebDAV / S3 备份与恢复

登录 `/admin/`，打开 **备份与恢复**：

1. 选择 WebDAV 或 S3，填写目标信息并保存。两种方式各自保留一套配置。AWS S3 的 Endpoint 可留空；MinIO 等兼容服务填写 Endpoint，并按服务要求启用 Path Style。
2. 测试已保存配置后，点击 **立即备份**，设置至少 12 字节的独立备份密码。服务器不会保存此密码，请妥善保管。加密文件以 `readest-<UTC 时间>-<UUID>.tar.gz.age` 命名，上传到配置的目录或对象前缀。
3. 恢复时加载备份列表，选择文件，输入备份密码、**当前**管理员登录密码，并输入 `RESTORE`。恢复会整体替换所有应用数据；请确保知道备份中管理员账号的登录密码，以便恢复后重新登录。

备份包含全部账号（包括密码哈希、管理员权限、配额）、图书元数据、阅读配置、笔记、隐私密文和所有已完成且被数据库引用的文件。不包含登录会话、密码恢复令牌和未完成上传。部署配置、JWT 密钥、SMTP 凭据和远端备份凭据也不进入备份，恢复后沿用当前部署的值。恢复要求数据库结构一致；迁移到新服务器时请使用相同服务端版本，配置备份目标后恢复。

备份使用 age 密码加密（scrypt 与认证加密）、gzip 压缩及 SHA-256 文件清单校验。遗失备份密码无法恢复。备份密码和 Readest 隐私 PIN 相互独立，恢复隐私密文后仍需在客户端输入原来的 PIN。scrypt 会临时使用约 256 MiB 内存。

生成快照和最终替换数据时，会暂停应用数据请求，以保证 SQLite 与对象文件的一致性。远端传输和暂存校验在后台执行，同一时间只允许一个备份或恢复任务。任务为手动触发，进程停止时取消，不自动重试；最近任务状态保留在内存中，重启后清空。同一数据库及对象目录只能由单个后端进程使用。

修改当前数据前，恢复流程会检查解密、文件哈希、SQLite 完整性、数据库结构、引用关系及管理员账号是否存在，并在 `DATA_DIR/backups/before-restore/` 保留一份恢复前安全备份，使用**本次恢复输入的同一个备份密码**加密。任务状态会显示其相对路径。这份备份保留在本地；需要通过面板回退时，可将其以原文件名放入已配置的远端备份目录。系统不会自动清理旧备份。校验失败不会修改当前数据。成功恢复会使全部登录会话及旧文件签名链接失效；请重新登录，并在客户端重新完整加载书库，避免旧的本地变更再次同步到恢复后的服务器。

目标凭据使用从部署 JWT 密钥派生的密钥加密，保存在 `DATA_DIR/backup-config.enc`。API 不返回已保存密码；保存时密码留空表示保留原值。保持 `jwt-secret` / `JWT_SECRET` 稳定才能读取该配置。如果密钥丢失，可删除配置文件并重新填写远端凭据；已有备份仍可通过独立备份密码解密。

WebDAV 需要 `PROPFIND`、`MKCOL`、`PUT`、`MOVE`、`GET` 和清理临时文件的 `DELETE` 权限。上传先使用临时文件名，完成后通过 `MOVE` 发布，避免未完成文件出现在备份列表。S3 需要存储桶列举、对象读写及中止分段上传权限。访问远端服务请使用 HTTPS。连接测试只验证列举能力，实际上传时才验证写入权限。

`BACKUP_MAX_BYTES` 默认 50 GiB，同时限制加密传输文件大小与数据库和对象的解压总大小；`BACKUP_TIMEOUT` 默认 `2h`。最多支持 99,999 个已完成对象。临时目录位于 `STORAGE_DIR/.backup-work-*`，请在现有数据之外预留 SQLite 快照、加密文件、解压文件和恢复前安全备份所需空间。任务结束后清理临时目录；进程异常终止留下的 `.backup-work-*` 目录，请只在服务停止时清理。

以下接口均要求管理员 Bearer 会话：

| 方法 | 路径 | 用途 |
| --- | --- | --- |
| GET / PUT | `/api/admin/backup/config` | 读取隐藏密码的配置 / 保存一个备份目标 |
| POST | `/api/admin/backup/test` | 测试已保存目标，例如 `{ "provider": "webdav" }` |
| GET | `/api/admin/backup/files?provider=webdav` | 列举加密备份，也支持 `provider=s3` |
| GET | `/api/admin/backup/status` | 最近任务、阶段、结果及安全备份路径 |
| POST | `/api/admin/backup/create` | 传入 `provider`、`passphrase` 启动任务，返回 HTTP 202 |
| POST | `/api/admin/backup/restore` | 传入 `provider`、`name`、`passphrase`、`adminPassword`、`confirmation: "RESTORE"`，返回 HTTP 202 |

通用配置字段为 `provider`、`endpoint`、`prefix`；WebDAV 另需 `username`、`password`，S3 另需 `region`、`bucket`、`accessKeyId`、`secretAccessKey`、`pathStyle`。恢复导致会话失效后，需要重新登录才能查看最终任务状态。

## 安全模型

- 密码使用 bcrypt；访问令牌为短期有效的 HS256 JWT，并通过可撤销的服务端会话进行校验。
- 公开注册接口始终返回 `403`；只有已认证且在数据库中具有管理员权限的用户才能创建账号。
- 管理员 API 每次请求都会重新读取用户记录，检查数据库中的管理员标记，且不会暴露密码哈希。
- 删除账号时会撤销会话、删除同步记录，并清理该账号受限目录内的存储对象。
- 刷新令牌随机生成，仅保存其 SHA-256 哈希，采用原子轮换，并拒绝重放。
- 所有针对用户数据的 GORM 查询都包含已认证用户 ID，忽略客户端传入的 `user_id`。
- 隐私 PIN 和隐藏书籍哈希保留在客户端加密的信封内，服务端只验证并保存不透明密文记录。
- 书签和批注使用已认证用户 ID、`book_hash` 及笔记 `id` 作为联合键；删除后保留墓碑，防止旧设备重新创建记录。
- 同步请求的数据、时间戳、JSON 大小、记录数量和文本长度均有限制。
- HTTP 请求体读写和 SMTP 操作均有超时；异常恢复不会记录请求头或带签名的查询字符串。
- 存储对象键的原始形式和解码形式都必须严格匹配 `Readest/Books/<32-hex-hash>/<supported-file>`。
- 磁盘对象名由服务端生成 UUID，不使用客户端路径；所有文件操作均限制在 `os.Root` 存储句柄内。
- 上传签名绑定操作用途、账号、记录、对象键、随机数、精确字节长度和过期时间；上传预留通过原子操作领取，防止签名 PUT 请求被并发重放。
- 每个账号的字节配额会在事务中根据已就绪文件和有效上传预留进行校验；管理员可设置自定义上限或继承服务端默认值。
- 管理员面板编译进 Go 二进制文件，使用同源请求，仅在 `sessionStorage` 中保存会话，并通过严格的内容安全策略（CSP）提供服务。
- 默认启用 JSON/API 限流、请求体大小限制、严格的代理处理、安全响应头和 CORS 处理。

内置固定窗口限流器适用于单进程 Tiny 后端。多实例部署时，还应在反向代理层配置共享限流。

## 验证

```powershell
$env:GOPROXY = 'https://goproxy.cn,direct'
npm --prefix web/admin ci --registry=https://registry.npmmirror.com
npm --prefix web/admin run typecheck
npm --prefix web/admin run build
go test ./...
go test -race ./...
go vet ./...
go mod verify
go run golang.org/x/vuln/cmd/govulncheck@v1.6.0 ./...
go run github.com/securego/gosec/v2/cmd/gosec@v2.28.0 -quiet ./...
```

测试覆盖管理员初始化、禁用注册、管理员授权、账号创建与删除、精确的账号配额执行、用量统计、兼容 Supabase 的会话生命周期、令牌重放与撤销、危险配置拒绝、租户隔离、书籍/配置/笔记的合并与游标行为、书签墓碑、旧笔记表结构迁移、路径遍历拒绝、签名上传的大小与重放校验，以及跨账号存储访问。

备份测试还覆盖本地 WebDAV/S3 模拟服务、凭据加密存储、全服务器恢复、会话及签名链接失效、错误密码、加密流截断、篡改内容、危险路径、符号链接、重复文件及解压大小限制。
