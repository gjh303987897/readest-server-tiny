# Readest 前后端接口核对

核对日期：2026-10-20。后端：`D:\workspace\readest-server-tiny`；客户端：`D:\workspace\readest\apps\readest-app`。

以下为初次审计记录；随后已按用户要求完成修复，状态见本节。原始问题和复现过程保留在后文，便于追溯。

## 修复结果（2026-10-20）

- CORS 已允许 Supabase SDK 的版本请求头。
- 书籍游标已统一为毫秒，按账号递增；旧亚毫秒记录向上取整并补齐边界组。客户端后续请求不再额外加 1。
- SyncClient 按服务端声明的条数和 UTF-8 请求体字节上限分批，并合并权威响应；支持书籍、进度配置和含删除墓碑的书签。
- 后端改为声明 r2 文件命名策略，本地物理存储方式不变；前端兼容旧 local 值，持久化并优先使用所选服务器的存储和配额配置。
- 业务鉴权失败保留 `error` 字段；前端统一解析状态码、错误码和消息，401 能进入已有重新登录流程，普通 403 权限/配额错误不会误判为会话失效。
- 后端声明禁用注册、邮箱立即修改；前端相应隐藏注册入口、保留密码恢复入口，并显示立即生效的邮箱更新提示。普通 Supabase 服务器省略这些能力字段时，保留原行为。
- 前端 API 文档和后端 README 已更新。已有客户端需在登录页重新连接一次服务器地址，以刷新之前未保存的附加配置。

验证：已通过后端 `go test ./...`、`go test -race ./...` 和 `go vet ./...`；前端 17 个测试文件的 115 项相关测试、全项目 TypeScript 检查和修改文件的 Biome 检查通过。另启动了隔离的临时 Go 服务，使用实际 Supabase SDK 和现有前端函数验证登录、SDK 预检头、1001 本书推送及完整分页拉取、1001 个书签、进度、文件上传/下载/删除、隐私关闭墓碑、邮箱修改、刷新、退出和会话失效处理。未执行完整桌面/移动端打包和人工 UI 验收。

原始审计结论：主应用的接口路径、HTTP 方法和主要数据字段基本对齐，但存在以下兼容性问题。

## 范围与验证方式

- 阅读 Go 服务的路由、中间件、认证、同步合并、隐私同步、对象存储、配额和管理接口，以及相关现有测试。
- 对照主应用的真实调用链：自定义后端配置 → Supabase SDK → AuthContext → SyncClient/useSync/useBooksSync → 数据转换；另检查书签、隐私和文件传输调用。
- 阅读 `readest/API` 文档，同时以实际调用代码为准。KOReader、Calibre 和浏览器扩展未做完整兼容性审计。
- 使用本地安装的 Supabase SDK（2.105.4）捕获实际生成的认证请求头；用 TypeScript 编译器加载客户端现有函数，替换网络和环境依赖，复现配置持久化、文件名生成、配额计算、分页和大批量推送行为。
- 分页复现中的服务端查询按 Go 源码的严格大于及同时间戳补齐规则模拟，未启动真实 Go HTTP 服务。当前终端找不到 Go，标准安装路径也不存在，故未能执行 `go test ./...`、真实数据库集成或浏览器端到端测试。

## 已对齐的主体合同

| 功能 | 客户端请求 | 后端实现 | 核对结果 |
| --- | --- | --- | --- |
| 连接探测 | `GET /runtime-config.js?format=json` | 返回 Supabase URL、anon key、API 根地址 | 核心连接字段匹配，附加配置存在下述问题 |
| 密码登录 / 刷新 | `POST /auth/v1/token?grant_type=password/refresh_token` | 同名端点及 session/user 字段 | 请求和成功响应外形匹配，跨域头有阻断问题 |
| 用户 / 退出 | `GET/PUT /auth/v1/user`、`POST /auth/v1/logout` | 已实现，支持退出 scope | 路径和字段匹配；修改邮箱语义有差异 |
| 密码恢复 | `POST /auth/v1/recover` → 邮件 verify → fragment 回调 → 更新密码 | 已实现恢复令牌、重定向和密码更新 | 静态合同匹配；需要 SMTP 和对应回调白名单，尚未实测 |
| 书籍 / 进度 / 书签 | `GET/POST /api/sync` | `books/configs/notes`，按用户隔离 | camelCase 请求、snake_case 响应匹配；分页及推送上限有问题 |
| 隐私同步 | `GET/PUT /api/privacy-sync` | `{record}`、`{envelope, updatedAt}`，原子 LWW | 字段、毫秒时间戳、AES-GCM 信封结构和关闭墓碑匹配 |
| 文件上传 | `POST /api/storage/upload` → 签名 URL `PUT` | `{fileName,fileSize,bookHash}` → `{uploadUrl}` | 匹配；Tauri 上传显式设置 Content-Length，Web 上传 File |
| 文件下载 | `GET/POST /api/storage/download` | `{downloadUrl}` / `{downloadUrls}` | 单个及批量字段匹配；主书库每批 20 个封面，低于后端 100 个上限 |
| 文件删除 | `DELETE /api/storage/delete?fileKey=...` | 同名端点 | 用户前缀和 `Readest/Books/<hash>/...` 路径匹配 |

两个容易误判为不一致的字段实际是兼容的：`books.progress` 返回数组，`configs.progress` 返回 JSON 字符串，客户端分别直接使用和 JSON.parse；`books.metadata` 返回 JSON 字符串，也与客户端 JSON.parse 对应。书签通过 `notes` 的 `type=bookmark`、`cfi`、`text`、`note` 映射，删除墓碑能传递。

## 需要修正的问题

### 1. P1：CORS 未放行 SDK 必带的认证请求头

后端 [server.go:281](D:/workspace/readest-server-tiny/internal/server/server.go:281) 只允许 `Authorization, Content-Type, Accept, apikey, x-client-info`。当前安装的 SDK 在每个认证请求中补入 `X-Supabase-Api-Version: 2024-01-01`，见 [SDK fetch.ts:180](D:/workspace/readest/node_modules/.pnpm/@supabase+auth-js@2.105.4/node_modules/@supabase/auth-js/src/lib/fetch.ts:180)。

实际 SDK 请求头捕获结果中，未获允许的头为：

```text
CORS_HEADERS_NOT_ALLOWED ["x-supabase-api-version"]
```

影响：前端 Web 页面或 Tauri WebView 与自定义服务器跨域时，浏览器预检不能放行认证请求，可能出现“连接探测成功，但无法登录/刷新会话”。同源管理面板或直接 HTTP 工具不会暴露此问题。反向代理另外补齐该头时才可绕开。

建议：后端允许 `X-Supabase-Api-Version`，并增加使用 SDK 实际请求头的跨域预检验证。

### 2. P1：书籍分页游标的时间精度不同，可能提前结束拉取

后端 [sync.go:286](D:/workspace/readest-server-tiny/internal/server/sync.go:286) 使用未截断的 `time.Now().UTC()` 作为整批 `synced_at`，响应通过 RFC3339Nano 格式输出。查询使用 [sync.go:323](D:/workspace/readest-server-tiny/internal/server/sync.go:323) 的 `synced_at > since`，`since` 则恢复为毫秒时间。

客户端 [useSync.ts:18](D:/workspace/readest/apps/readest-app/src/hooks/useSync.ts:18) 用 JavaScript Date 将它截断为毫秒；[useSync.ts:59](D:/workspace/readest/apps/readest-app/src/hooks/useSync.ts:59) 遇到返回页的最大时间不大于当前游标就退出分页。

复现场景：先有 1000 本书共享 `2026-10-20T00:00:00.000500Z`，再有一本更晚的书。第一页得到前 1000 条，下一页以 `.000Z` 查询，重复命中同一批 1000 条；客户端认为游标不再推进，停止分页，后一本尚未读取。

```text
expected: 1001
received: 1000
calls: 2
includesLaterBook: false
```

此外，[useSync.ts:211](D:/workspace/readest/apps/readest-app/src/hooks/useSync.ts:211) 后续普通增量请求把毫秒游标加 1，而后端本就采用严格大于比较，会跳过紧邻的部分更新。并非任何少于 1000 本的书库都会触发分页重复，但同批写入 1000 条是服务端明确支持的情况。

建议：统一游标精度、边界和推进规则，优先使用服务端返回的精确游标或不透明游标；如果保留毫秒游标，必须同时考虑同毫秒不同批次写入，不能仅靠加 1 跳过重复。补充“1000 条同批时间戳 + 后续批次”的联动测试。

### 3. P2：前端整批推送没有拆分，后端每类最多接收 1000 条

后端 [sync.go:250](D:/workspace/readest-server-tiny/internal/server/sync.go:250) 在任一类型超过 1000 条时，拒绝整个请求，返回 400。JSON 请求体另有默认 2 MiB 上限。

客户端 [useBooksSync.ts:23](D:/workspace/readest/apps/readest-app/src/app/library/hooks/useBooksSync.ts:23) 收集全部待同步书籍；[useBooksSync.ts:82](D:/workspace/readest/apps/readest-app/src/app/library/hooks/useBooksSync.ts:82) 和 [useBooksSync.ts:108](D:/workspace/readest/apps/readest-app/src/app/library/hooks/useBooksSync.ts:108) 直接整批传入。随后 [sync.ts:79](D:/workspace/readest/apps/readest-app/src/libs/sync.ts:79) 原样序列化。

实际执行现有 SyncClient，传入 1001 本书后，捕获到的单次请求仍含 1001 条。首次同步大书库或大量离线修改会整体失败。单本书的书签也在 [useBookmarkSync.ts:73](D:/workspace/readest/apps/readest-app/src/app/reader/hooks/useBookmarkSync.ts:73) 一次推送全部记录，包含删除墓碑，同样可能累积超过上限。

建议：客户端按数量和请求体大小分批，并逐批合并服务端返回的权威记录；只提高后端条数上限仍会遇到体积限制。

### 4. P2：runtime config 的 `objectStorageType: local` 超出客户端支持范围

后端 [server.go:199](D:/workspace/readest-server-tiny/internal/server/server.go:199) 返回 `local`。客户端 [storage.ts:3](D:/workspace/readest/apps/readest-app/src/utils/storage.ts:3) 仅支持 `r2/s3`，且只是类型断言，没有运行时兜底。[book.ts:25](D:/workspace/readest/apps/readest-app/src/utils/book.ts:25) 对其他值返回空远端书名。

实际执行 `getRemoteBookFilename()`，存储类型为 local 时结果为 `""`，继而书籍路径变成 `Readest/Books/`，不满足后端对象键校验，上传和下载失败。

触发条件需要区分：当前登录页“连接自定义后端”流程在 [backendEndpoint.ts:94](D:/workspace/readest/apps/readest-app/src/services/backendEndpoint.ts:94) 只保存四个地址/密钥字段，实际上丢弃了 `objectStorageType`；因此该流程通常仍使用客户端原来的 r2/s3 值，不会仅因收到 JSON 中的 local 而立即触发空书名。若通过 Web runtime 脚本或代理把该后端配置注入页面全局，则会触发。不能把所有桌面端上传都判为必然失败。

建议：接口返回客户端支持的文件命名策略（例如 r2），后端物理文件保存到本地不要求对客户端声明 local；或者在客户端完整实现 local 并持久化存储配置。

### 5. P2：业务接口的鉴权错误形状与客户端处理方式不同

后端 `/api` 同样使用 `authRequired`；失败由 [server.go:369](D:/workspace/readest-server-tiny/internal/server/server.go:369) 的 authError 输出 `{code,error_code,msg,message}`，没有 `error` 字段。

客户端 [sync.ts:57](D:/workspace/readest/apps/readest-app/src/libs/sync.ts:57) 和通用 [fetch.ts](D:/workspace/readest/apps/readest-app/src/utils/fetch.ts) 只优先读取 `error`，于是具体的“会话已失效”等说明丢失，退化为状态文本。更实际的影响是 [useSync.ts:172](D:/workspace/readest/apps/readest-app/src/hooks/useSync.ts:172) 依赖错误消息含 `Not authenticated` 才关闭 keepLogin 并跳转登录；服务端撤销会话但本地尚存 token 时，这条恢复路径无法匹配。

建议：业务接口使用一致的 `{error}` 结构，或者客户端统一保留 HTTP 状态和错误码，并根据 401/403 处理重新认证。Supabase Auth 端点仍需维持 SDK 兼容的错误格式。

### 6. P2：修改邮箱的成功语义不同

客户端 [update/page.tsx:33](D:/workspace/readest/apps/readest-app/src/app/auth/update/page.tsx:33) 调用 updateUser 后，明确显示“已向旧、新邮箱发送确认邮件”。后端 [auth.go:156](D:/workspace/readest-server-tiny/internal/server/auth.go:156) 直接更新 email，没有发送确认邮件或待确认邮箱状态。

影响：请求能成功，但用户会等待不存在的确认邮件，而且新登录邮箱已经立即生效。

建议：按自托管策略决定采用立即修改还是邮件确认，并使页面文案与实现一致。这里没有把“必须启用邮件确认”作为唯一修复方案。

## 有意的功能差异及配置缺口

1. **公开注册策略没有在客户端同步。** 后端 [server.go:164](D:/workspace/readest-server-tiny/internal/server/server.go:164) 明确禁用公开注册，README 也如此说明；但客户端 [auth/page.tsx:77](D:/workspace/readest/apps/readest-app/src/app/auth/page.tsx:77) 使用默认显示链接的 Auth 组件，保留“注册”入口，点击后必然收到 403。应保留后端管理员建号策略，隐藏注册入口或显示管理员建号说明；前端 `API/auth-and-runtime.md` 关于注册和确认邮件的要求也需要同步。这属于产品策略未对齐，并不意味着后端应开放注册。
2. **自定义后端附加配置没有持久化。** `BackendConnection` 不含 `objectStorageType/storageFixedQuota`；客户端存储和配额工具也不读这两个自定义连接字段。实际使用 1 GiB 后端配置执行连接后，保存结果只有四个地址/密钥字段；无 runtime 配额的 `getStoragePlanData()` 仍算出 500 MiB。后端 JWT 的 `storage_purchased_bytes` 又是“用户实际配额减后端基础配额”，假定双方基础值相同，因此非默认配额的计算合同有缺口。不过当前主应用没有查到调用这个函数显示配额的 UI；自定义连接后文件 API 直接调用 Go，Go 仍按数据库配额强制执行，不能据此断言当前 UI 已显示错误或能绕过配额。
3. 后端支持 reading_status、group、tags 等扩展字段，而当前主应用的 Book/DBBook 转换尚未完整消费这些字段；这属于后端能力超集。已检查的正常主应用调用不依赖这些额外字段。

## 建议实施顺序

先修 CORS，确保能登录；随后一起修同步分页游标和推送分批；然后统一 runtime 存储配置、鉴权失败处理和账户页面语义。Go 测试应补充 SDK 请求头和真正跨前后端边界的样例，现有单端测试不足以覆盖本报告复现的组合场景。
