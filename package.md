# GitHub Actions 与 GHCR 发布

本仓库通过 GitHub Actions 自动完成持续集成、安全检查和多架构镜像发布。工作流
针对当前项目的 Go 1.25、Node.js 24、CGO/SQLite 和多阶段 `Dockerfile` 配置编写。

## 文件结构

```text
.github/
|-- workflows/
|   |-- ci.yml
|   |-- security.yml
|   `-- release.yml
`-- dependabot.yml
```

| 文件 | 触发条件 | 作用 |
| --- | --- | --- |
| `ci.yml` | Pull Request、推送到 `main` | 校验 Go、管理面板和 Docker 镜像 |
| `security.yml` | Pull Request、推送到 `main`、每周定时、手动 | 执行漏洞与静态安全扫描 |
| `release.yml` | 推送 `v*` 标签 | 在一次发布流程中生成镜像、各系统二进制和 GitHub Release |
| `dependabot.yml` | 每周 | 检查 Go、npm 和 GitHub Actions 更新 |

同一分支的旧 CI 或安全扫描会被取消，发布任务不会自动取消。所有工作流默认只授予
`contents: read`；镜像 Job 单独获得 `packages: write`，GitHub Release Job 单独获得
`contents: write`，CodeQL Job 获得 `security-events: write`。

## 仓库配置

在 GitHub 仓库的 `Settings > Secrets and variables > Actions > Variables` 中可配置：

- `GOPROXY`：默认 `https://proxy.golang.org,direct`；国内环境可改为
  `https://goproxy.cn,direct`。
- `NPM_REGISTRY`：默认 `https://registry.npmjs.org`；国内环境可改为可信代理。
- `ENABLE_GHAS`：私有仓库已启用 GitHub Advanced Security 时设为 `true`。

代理地址不是凭据，因此使用 Repository Variable；Token、密码等敏感值必须使用
Repository Secret，不能写入源码、Dockerfile、Compose 或构建参数。

如果组织限制了 `GITHUB_TOKEN`，需要确认仓库允许 Actions 写入 Package。发布到当前
仓库所属账号的 GHCR 不需要额外 PAT，工作流使用短期 `GITHUB_TOKEN` 登录。

## 持续集成

`ci.yml` 包含三个并行 Job：

- `backend`：执行 `go mod verify`、`go vet ./...`、`go test ./...` 和
  `go test -race ./...`。
- `admin-ui`：使用锁文件执行 `npm ci`，然后进行 TypeScript 检查和生产构建。
- `docker`：完整构建当前 `Dockerfile`，但不登录仓库、不推送镜像。

Go 与 npm 缓存都以锁文件为键。Docker Buildx 使用 GitHub Actions 缓存，PR 构建无
发布权限，来自 Fork 的代码无法借此推送镜像。

## 安全检查

`security.yml` 始终运行以下本地扫描：

- `govulncheck`：检查代码实际可达的 Go 已知漏洞。
- `gosec`：检查常见 Go 安全问题。

Dependency Review 和 CodeQL 在公有仓库中自动运行。私有仓库需要 GitHub Advanced
Security；确认方案和组织策略支持后，将仓库变量 `ENABLE_GHAS` 设为 `true`。这样
可以避免未启用 GHAS 的私有仓库因 GitHub API 拒绝访问而产生无意义的失败。

Dependency Review 仅分析 Pull Request 新增的依赖，并在引入高危或严重漏洞时失败。
CodeQL 分别分析 Go 与 JavaScript/TypeScript，Go 使用显式构建模式以覆盖 CGO 代码。

## 单次发布流程

`release.yml` 接受带可选 `v` 前缀的稳定版或预发布 SemVer 标签。例如：

```powershell
git tag v1.0.0
git push origin v1.0.0
```

预发布版本可以使用 `0.0.1-alpha`、`v0.0.1-beta.1` 等标签。预发布流程会创建 GitHub
Pre-release 和精确版本镜像标签，但不会更新稳定版的 `<major>.<minor>` 或 `latest` 标签。

同一个 workflow run 会执行以下 Job：

- `validate`：验证标签并提取版本号。
- `admin-ui`：只构建一次管理面板，供所有系统二进制复用。
- `binaries`：在对应架构的原生 GitHub runner 上并行构建 Linux、Windows 和 macOS。
- `docker`：并行构建并推送 amd64/arm64 多架构镜像。
- `release`：等待镜像和全部二进制成功，生成 SHA-256 校验文件并创建 GitHub Release。

项目使用 CGO SQLite，因此二进制不通过简单设置 `GOOS`/`GOARCH` 交叉编译，而是在
对应系统和架构的 runner 上原生构建。发布附件包括：

```text
readest-server_<version>_linux_amd64.tar.gz
readest-server_<version>_linux_arm64.tar.gz
readest-server_<version>_windows_amd64.zip
readest-server_<version>_darwin_amd64.tar.gz
readest-server_<version>_darwin_arm64.tar.gz
checksums.txt
```

所有二进制都嵌入同一次构建生成的管理面板。任何二进制或镜像构建失败时，不会创建
GitHub Release。

## 发布镜像

工作流会把 GitHub 仓库全名转换为小写，并发布到：

```text
ghcr.io/<owner>/<repository>:1.0.0
ghcr.io/<owner>/<repository>:1.0
ghcr.io/<owner>/<repository>:latest
```

构建目标为 `linux/amd64` 和 `linux/arm64`，发布为同一个多架构 manifest。Docker 或
Docker Compose 拉取镜像时会自动选择当前主机对应的镜像。项目使用 CGO SQLite，因此
arm64 镜像构建通过 QEMU 在目标架构环境中完成，没有把构建阶段强制固定为
`$BUILDPLATFORM`，从而避免缺少 ARM64 C 交叉编译器导致链接失败。

发布镜像包含 OCI 元数据、最大模式的 BuildKit 来源证明和 SBOM。镜像推送成功后，
Manifest 摘要会显示在 GitHub Actions Job Summary 中。所有第三方 Action 都固定到完整
Commit SHA，并由 Dependabot 跟踪更新，避免可移动版本标签被替换带来的供应链风险。

## GHCR 首次发布

第一次发布后，在 GitHub 的 Package 设置中检查：

- Package 已连接到正确的源码仓库并继承仓库权限。
- 可见性符合部署需求：Public、Private 或组织内可见。
- 组织关闭自动继承时，`Manage Actions access` 已授权当前仓库。

公有镜像可以匿名拉取：

```powershell
docker pull ghcr.io/<owner>/<repository>:1.0.0
```

私有镜像的部署服务器需要使用仅具备 `read:packages` 权限的 Personal Access Token
(classic) 登录：

```powershell
$env:CR_PAT | docker login ghcr.io -u <github-user> --password-stdin
docker pull ghcr.io/<owner>/<repository>:1.0.0
```

生产环境应固定不可变版本，不建议长期跟随 `latest`。当前 Compose 无需修改即可使用
发布镜像：

```powershell
$env:READEST_IMAGE = 'ghcr.io/<owner>/<repository>:1.0.0'
docker compose pull
docker compose up -d
```

## 本地检查

提交前可运行与 CI 对应的检查：

```powershell
$env:GOPROXY = 'https://goproxy.cn,direct'
make test-race
make vet
make admin-build
docker compose build
```

## 参考资料

- [GitHub：发布 Docker 镜像](https://docs.github.com/en/actions/tutorials/publish-packages/publish-docker-images)
- [GitHub：GITHUB_TOKEN 权限](https://docs.github.com/en/actions/concepts/security/github_token)
- [GitHub：Dependency Review](https://github.com/actions/dependency-review-action)
- [GitHub：CodeQL 编译型语言](https://docs.github.com/en/code-security/how-tos/find-and-fix-code-vulnerabilities/manage-your-configuration/codeql-for-compiled-languages)
- [Docker：GitHub Actions 多平台构建](https://docs.docker.com/build/ci/github-actions/multi-platform/)
- [Docker：build-push-action](https://github.com/docker/build-push-action)
