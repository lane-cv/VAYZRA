# VAYZRA / HappyLearn

HappyLearn 是一个由 Go API、Vue 管理控制台、文件处理 Worker、PostgreSQL、Redis 和 AIStor 对象存储组成的教学平台。

当前初版 release：`v0.1.2`。

本文提供从安装到部署的最短路径：可以直接使用 Docker Compose 部署本地/测试环境，也可以安装 Go、Node.js 和 pnpm 进行源码开发。它不会执行 Phase 6 生产发布、真实服务器切换或数据恢复操作；生产部署请使用 [`docs/runbooks/phase6-real-server-acceptance.md`](docs/runbooks/phase6-real-server-acceptance.md)、[`docs/runbooks/phase6-release-rollback.md`](docs/runbooks/phase6-release-rollback.md) 和已审批的发布流程。

## 安装方式

| 场景 | 推荐方式 | 是否需要在主机安装 Go/Node.js/pnpm |
| --- | --- | --- |
| 首次体验或本地部署 | `scripts/deploy-from-github.sh` + Docker Compose | 否 |
| 日常前端/后端开发 | 源码安装 + 本地依赖 | 是 |
| 生产环境 | Ubuntu 24.04 + `compose.prod.yml` + 不可变镜像 | 构建机需要，生产机不需要 |

### 方式 A：Docker Compose 快速安装

这是最简单的安装方式。它会构建 App、Worker 与 `update-agent` 镜像，并启动 PostgreSQL、Redis、AIStor、API、管理控制台、文件处理 Worker 和 GitHub 更新代理。默认端口只绑定到 `127.0.0.1`，适合个人电脑、测试机或反向代理后的内网服务。

```bash
git clone git@github.com:lane-cv/VAYZRA.git "$HOME/apps/VAYZRA"
cd "$HOME/apps/VAYZRA"

# AIStor license 必须是仓库外的真实文件，并且仅当前用户可读。
chmod 600 /absolute/path/to/minio.license

./scripts/deploy-from-github.sh \
  --directory "$PWD" \
  --license-file /absolute/path/to/minio.license
```

没有 SSH Key 时，可将第一行替换为：

```bash
git clone https://github.com/lane-cv/VAYZRA.git "$HOME/apps/VAYZRA"
```

脚本会自动创建被 Git 忽略的 `.env.github-deploy`、`.secrets/github-deploy/` 和本机 AI 主密钥；不要删除或替换 AI 主密钥，否则已保存的 AI Provider 凭据将无法解密。完整选项和端口覆盖方式见下方的[首次从 GitHub 部署](#首次从-github-部署)。

### 方式 B：源码开发安装

源码开发需要以下版本：Go `1.26.5`、Node.js `24.18.0`、pnpm `11.9.0`。此外仍建议用 Docker Compose 提供 PostgreSQL、Redis 和 AIStor。安装依赖并执行基础检查：

```bash
git clone git@github.com:lane-cv/VAYZRA.git
cd VAYZRA

corepack enable
corepack prepare pnpm@11.9.0 --activate
pnpm install --frozen-lockfile
go mod download

go test ./...
pnpm test
pnpm typecheck
pnpm lint
pnpm build
```

本地环境变量、数据库迁移、管理员初始化和源码启动命令见 [`docs/runbooks/local-development.md`](docs/runbooks/local-development.md)。不要把 `.env`、`.secrets/`、AIStor license 或管理员密码提交到 Git。

### 方式 C：生产环境安装

生产环境不是 `compose.dev.yml` 的放大版。生产主机要求 Ubuntu 24.04、Docker Compose、systemd、至少 2 CPU/4 GiB 内存，并使用 `deploy/compose.prod.yml`、外部 secret 文件、不可变镜像摘要和发布前备份。生产主机不需要安装 Go、Node.js 或 pnpm。

准备生产配置的入口文件：

```bash
sudo install -d -o root -g root -m 0711 /etc/happylearn/secrets
cp deploy/production.env.example deploy/production.env
chmod 600 deploy/production.env
```

随后必须按 [`deploy/secrets/README.md`](deploy/secrets/README.md) 创建每个服务所需的 secret 文件，并将 `deploy/production.env` 中的镜像地址全部替换为已审核的 `@sha256:...` 摘要。不要直接使用示例中的占位值，也不要把密钥写入环境变量或命令行参数。

在生产维护窗口中，先执行只读预检，再按发布手册运行发布协调器：

```bash
candidate_manifest=/srv/happylearn/releases/release-input/candidate-manifest.json
candidate_version=$(jq -er '.version' "$candidate_manifest")

sudo scripts/prod-preflight.sh \
  --project-dir /srv/happylearn/current \
  --env-file /etc/happylearn/production.env \
  --manifest "$candidate_manifest" \
  --mode server \
  --expected-host-address '<approved-public-address>'

sudo scripts/prod-release.sh \
  --project-dir /srv/happylearn/current \
  --env-file /etc/happylearn/production.env \
  --manifest "$candidate_manifest" \
  --version "$candidate_version" \
  --mode server \
  --expected-host-address '<approved-public-address>' \
  --confirm-maintenance-window
```

上述命令只是发布流程的执行入口，不替代主机盘点、DNS/TLS、防火墙、systemd 安装、备份恢复验收和独立审批。首次生产部署前必须完整阅读 [`phase6-real-server-acceptance.md`](docs/runbooks/phase6-real-server-acceptance.md)；出现 trace ID 或失败安全状态时，按 [`phase6-release-rollback.md`](docs/runbooks/phase6-release-rollback.md) 处理。

## 前置条件

- Linux 或 WSL2；命令使用 Bash。
- Git、OpenSSL 和 curl。
- Docker Engine 及 `docker compose` 插件。
- 当前用户能够访问 Docker daemon。
- 已下载且可读取的 AIStor Free `minio.license` 文件。许可证应放在仓库外，不要提交到 Git。
- GitHub 私有仓库需要提前配置 SSH Key 或 HTTPS 凭据。

无需在主机安装 Go、Node.js 或 pnpm；应用和前端均在 Docker 构建阶段编译。

## 首次从 GitHub 部署

先获取仓库，再运行部署脚本。下面使用 SSH；也可以使用 HTTPS 地址 `https://github.com/lane-cv/VAYZRA.git`。

```bash
install -d "$HOME/apps"
git clone git@github.com:lane-cv/VAYZRA.git "$HOME/apps/VAYZRA"
cd "$HOME/apps/VAYZRA"

./scripts/deploy-from-github.sh \
  --directory "$PWD" \
  --license-file /absolute/path/to/minio.license
```

脚本会完成以下操作：

1. 检查当前 `master` 工作区是否干净，并以 `git pull --ff-only` 更新代码；
2. 首次运行时生成仅供本机使用的 AI 主密钥，后续部署保持同一密钥；
3. 验证 Compose 配置，构建 App、Worker 与 `update-agent` 镜像；
4. 启动或更新 `happylearn-dev`，保留已有命名数据卷；
5. 等待所有容器健康并验证应用 readiness 接口。

默认访问地址：

- Web 与 API：<http://127.0.0.1:8080>
- 内部接口：`127.0.0.1:9090`
- PostgreSQL：`127.0.0.1:54329`
- Redis：`127.0.0.1:56379`
- AIStor S3：`127.0.0.1:59000`
- AIStor 控制台：<http://127.0.0.1:59001>

所有端口只绑定本机回环地址，不对局域网或公网开放。

### Docker 镜像离线部署

如果目标服务器无法访问 Docker Hub，可以在网络正常的构建机上构建并导出镜像：

```bash
cd /path/to/VAYZRA

docker build -t happylearn-app:local -f Dockerfile .
docker build -t happylearn-worker:local -f Dockerfile.worker .
docker build -t happylearn-update-agent:local -f deploy/Dockerfile.update-agent .
docker pull 'postgres:18.4@sha256:a02db8cac496f15b094798a38254f14d6e00741f709360e5e00bb6668ea31636'
docker pull 'redis:8.8@sha256:3eafabb4c93fcb8b36b666e07a43f096cb157bc6b07dce4b2492b895c63cf37f'
docker pull 'debian:12.12-slim@sha256:d5d3f9c23164ea16f31852f95bd5959aad1c5e854332fe00f7b3a20fcc9f635c'
docker pull 'alpine:3.23.3@sha256:25109184c71bdad752c8312a8623239686a9a2071e8825f20acb8f2198c3f659'
docker pull 'quay.io/minio/aistor/minio:RELEASE.2026-06-06T02-44-06Z@sha256:5dbb753c0dbe6a987dd30ce564f66c0042e291e464d10e792443451d4fec2120'

# docker save/load 不保证保留多架构 index 的 RepoDigest；为当前目标架构创建离线专用别名。
docker tag 'postgres:18.4@sha256:a02db8cac496f15b094798a38254f14d6e00741f709360e5e00bb6668ea31636' happylearn-offline/postgres:18.4
docker tag 'redis:8.8@sha256:3eafabb4c93fcb8b36b666e07a43f096cb157bc6b07dce4b2492b895c63cf37f' happylearn-offline/redis:8.8
docker tag 'debian:12.12-slim@sha256:d5d3f9c23164ea16f31852f95bd5959aad1c5e854332fe00f7b3a20fcc9f635c' happylearn-offline/debian:12.12-slim
docker tag 'alpine:3.23.3@sha256:25109184c71bdad752c8312a8623239686a9a2071e8825f20acb8f2198c3f659' happylearn-offline/alpine:3.23.3
docker tag 'quay.io/minio/aistor/minio:RELEASE.2026-06-06T02-44-06Z@sha256:5dbb753c0dbe6a987dd30ce564f66c0042e291e464d10e792443451d4fec2120' happylearn-offline/aistor:2026-06-06

docker save \
  happylearn-app:local \
  happylearn-worker:local \
  happylearn-update-agent:local \
  happylearn-offline/postgres:18.4 \
  happylearn-offline/redis:8.8 \
  happylearn-offline/debian:12.12-slim \
  happylearn-offline/alpine:3.23.3 \
  happylearn-offline/aistor:2026-06-06 \
  | gzip > vayzra-images.tar.gz
sha256sum vayzra-images.tar.gz > vayzra-images.tar.gz.sha256
```

把 `vayzra-images.tar.gz` 和仓库目录复制到目标服务器后：

```bash
cd "$HOME/apps/VAYZRA"
sha256sum -c /path/to/vayzra-images.tar.gz.sha256
docker load -i /path/to/vayzra-images.tar.gz

./scripts/deploy-from-github.sh \
  --offline \
  --directory "$PWD" \
  --license-file /home/ubuntu/minio.license
```

`--offline` 要求目标机已有 Git checkout 和上述全部离线别名；脚本只在该模式把 Compose 切换到这些本地别名，并使用 `--no-build`，不会执行 `git pull`、构建或拉取。离线包是平台相关制品，必须在与目标机相同架构上制作并随 `.sha256` 一起传输、校验；许可证、密钥和命名数据卷仍然单独保管，不会打进镜像包。

### 存储占用显示“暂无数据”

首页的“存储占用”来自 AIStor 的容量采样。应用启动后会由运维采样器读取 AIStor 管理 API 的磁盘已用空间和总容量，并写入 PostgreSQL；首次启动通常等待一个采样周期即可显示。

如果仍显示“暂无数据”，请确认 `HAPPYLEARN_MINIO_ACCESS_KEY` 对应的账号具有 AIStor 管理 API 的存储信息读取权限；Compose 默认使用 AIStor root 账号。权限不足、AIStor 不可用或容量接口返回空数据时，系统会安全地继续显示“暂无数据”，不会猜测容量。

## 后续拉取并重新部署

在同一个部署目录重复运行相同命令即可：

```bash
cd "$HOME/apps/VAYZRA"
./scripts/deploy-from-github.sh \
  --directory "$PWD" \
  --license-file /absolute/path/to/minio.license
```

该升级路径同样会重新构建 App、Worker 与 `update-agent` 镜像，并保留已有命名数据卷。

脚本只接受 fast-forward 更新。若仓库存在未提交修改、分支不是指定分支或处于 detached HEAD，会停止而不是覆盖本地内容。

部署完成后，管理员可以在“系统运维 → 系统设置 → GitHub 版本更新”中手动检查，页面也会定期检查。更新代理以 GitHub Releases 中版本号最高、已启用 GitHub immutable protection 的 stable SemVer Release 为唯一在线发现来源；标签必须严格为 `vMAJOR.MINOR.PATCH`，draft、prerelease、mutable Release、build metadata 和其他标签都会跳过。启用本策略前，仓库管理员必须在 GitHub Settings 的 Releases 区域启用 release immutability；该策略只保护启用后创建的 Release，旧 Release 不作为 OTA 信任输入。Release 标签会先以非强制方式拉取到隔离的 Git ref：同一版本标签一旦被本机检查过，远端再移动或重签该标签都会被拒绝。代理也会把配置的远端分支非强制拉取到另一隔离 ref，要求 Release 提交可从该远端分支到达，并核对该提交最新一次 `verify.yml` push 工作流已经成功；旁支、未合并或未通过完整验证的提交不能触发 OTA。随后代理用 Git ancestor 关系确认 Release 能从当前提交 fast-forward；当前提交已经领先于 Release 时不会自动降级，历史分叉时也会停止。

从旧版 branch-pull 更新代理迁移到本策略时，必须先在每台已有本地/测试部署主机上关闭管理员在线更新入口，并由宿主机完整重跑一次 `deploy-from-github.sh`。确认 `update-agent` 已随 App、Worker 一起重建、状态接口返回 `strategy=github-release` 后，才可以创建首个启用本策略的 stable Release；旧代理无法识别下述控制面阻断规则，不能靠旧在线更新完成这次引导升级。

点击“更新并重启”后，更新代理会先从现有 App、Worker 容器的 Docker `.Image` 字段捕获两个不可变 `sha256` 镜像 ID，再以已验证的候选 commit SHA（而不是可移动 ref）创建 `/var/lib/happylearn-update` 下的临时 worktree。构建前还会检查候选 `Dockerfile` 与 `Dockerfile.worker`：已声明的内部 stage 可以继续作为 `FROM`，所有外部 `FROM` 和显式 `# syntax` frontend 必须同时包含 tag 与 `sha256` digest；裸 tag 或仅 digest 均会阻止在线更新。切换前，代理会把旧/候选 commit、四个不可变镜像 ID 和操作阶段原子、`fsync` 地写入仅代理可读的 `operation.json`，随后切换服务并等待 Compose 健康检查。新版本健康失败、切换失败或健康通过后主检出无法 fast-forward 时，代理会自动用旧镜像恢复 App 与 Worker；只有新运行态健康后，才会对主 checkout 按候选 SHA 执行 `git merge --ff-only` 并重新核对 HEAD。数据库、Redis、AIStor 的命名卷不会被删除，工作区存在未提交修改、分支不匹配或检查期间发生变化时更新会被阻止。

本地 OTA 会解释 Release 内的 App/Worker Dockerfile，因此所有外部 `FROM` 与 Dockerfile frontend 都必须使用可读标签加多架构 `sha256` 摘要；Compose 中由该部署路径直接拉取的 PostgreSQL、Redis、Debian 与 Alpine 也固定摘要。合同测试会拒绝重新引入裸标签。源码构建过程中 Debian 软件仓库本身仍可能随时间更新，所以这项本地/测试能力不等同于可复现的生产制品；生产仍只接受预构建、签名或登记过摘要的 Phase 6 镜像。

为保证自动恢复旧镜像不会遇到已由候选版本推进且不向后兼容的数据库 schema，本地 OTA 只允许 `db/migrations` 没有任何新增、修改或删除的 Release。只要 Release 包含 migration，检查结果就会阻止在线更新；必须改用 Phase 6 的不可变镜像发布、备份验收和显式回滚流程。

更新状态以原子文件方式保存在 `/var/lib/happylearn-update/status.json`，包含 Release 版本、说明、阶段和进度。若代理在切换或提交终态之间重启，它会先核对主 checkout：HEAD 仍为旧 commit 时幂等恢复并验证旧镜像，HEAD 已为候选 commit 时幂等恢复并验证候选镜像后补写成功终态；HEAD 为其他 commit、分支不匹配或工作区不干净时会保留 journal 并阻止继续自动操作，等待人工核对。只有终态成功持久化后才删除 journal，因此终态文件写入失败不会被改写成指向旧 commit 的失败结果。

界面始终报告 `canRollback=false`，回滚 API 始终返回 409。原因是显式回滚发生时可能已经越过任意后续版本、数据库 schema 或 OTA 控制面边界，仅凭本地状态无法证明反向兼容，也无法安全替换正在协调更新的旧代理；即使本地 OTA 已阻止 migration 和控制面变更，也不足以为任意未来手动回滚建立这个证明。失败操作仍会在 checkout 尚未提交候选 commit 的受控窗口内，使用 journal 记录的旧不可变镜像自动恢复；需要版本级显式回滚时必须使用 Phase 6 的已审批不可变制品、备份与恢复流程。

在线更新不会让更新代理替换自身。为避免新 App 与旧代理使用不兼容的状态/API 契约，或让候选版本借 Docker Compose 改写容器权限、宿主机挂载及运行命令，只要 Release 修改 `cmd/update-agent`、`internal/updates`、`deploy/Dockerfile.update-agent`、`deploy/compose.dev.yml`、`deploy/compose.github.yml` 或 `scripts/deploy-from-github.sh`，检查结果就会阻止 OTA；请在宿主机完整重新运行部署脚本，使 App、Worker 与 `update-agent` 一起更新。

该在线更新入口仅由 `deploy-from-github.sh` 的本地/测试部署启用，生产 `compose.prod.yml` 不挂载更新代理或 Docker socket，生产环境仍须使用审批后的不可变镜像发布流程。

仓库的 `github-release` 工作流会发布供本地/测试 OTA 发现的 GitHub Release 元数据，并附带 `VAYZRA-vX.Y.Z.tar.gz` 源码归档和 `SHA256SUMS` 校验文件；这些源码资产不是生产制品。它不会构建或推送 Phase 6 所需的不可变生产镜像、SBOM 或候选 manifest。生产发布输入仍须由独立、已审批的构建与制品登记流程生成，并按方式 C 的 Phase 6 手册验收，不能把 GitHub Release 当作生产发布闭环。

如果 GitHub 仓库为私有仓库，更新代理不能使用宿主机 SSH 私钥。请准备一个仅有仓库 `Contents: Read` 与 `Actions: Read` 权限的 fine-grained GitHub Token 文件，并以 owner-only 权限运行部署脚本：

```bash
chmod 600 /absolute/path/to/github-token
./scripts/deploy-from-github.sh \
  --directory "$PWD" \
  --license-file /absolute/path/to/minio.license \
  --github-token-file /absolute/path/to/github-token
```

更新代理通过 `https://api.github.com` 读取 Release 元数据，并通过 GitHub Smart HTTP Basic 认证拉取已验证的 Release 标签。Token 只从只读文件加载：API Bearer 头仅发送给 `api.github.com`，Git 认证头仅作用于 `https://github.com/`；Token 不写入远程 URL、Git 命令参数、状态文件、持久化 Git 配置或应用日志，也不返回给前端。`--github-token-file` 只配置容器内的更新代理，首次 clone 以及宿主机执行的 pull 仍使用宿主机已有的 SSH Key 或 HTTPS 凭据。没有 Token 时，公开仓库可以检查；使用 SSH 完成首次部署的私有仓库则会在管理员页面提示 Release 检查不可用。

部署其他分支时明确指定：

```bash
./scripts/deploy-from-github.sh \
  --directory "$PWD" \
  --ref feature/example \
  --license-file /absolute/path/to/minio.license
```

已有目录必须已经检出同名分支。在线更新仍只接受能从该分支当前提交 fast-forward、同时属于受信 `master` 发布线且该提交已通过 `master` 最新验证运行的 stable SemVer Release；feature 分支自身未经发布工作流的标签不会被接受。该选项适合在包含受信 Release 的测试分支上验证兼容性，不应替代正式发布审批。

## 处理端口冲突

端口被其他本机服务占用时，可以全部或部分覆盖。例如：

```bash
./scripts/deploy-from-github.sh \
  --directory "$PWD" \
  --license-file /absolute/path/to/minio.license \
  --app-port 18080 \
  --internal-port 19090 \
  --postgres-port 55429 \
  --redis-port 56380 \
  --aistor-api-port 59002 \
  --aistor-console-port 59003
```

此时 Web 地址为 <http://127.0.0.1:18080>。脚本会把本机配置写入被 Git 忽略的 `.env.github-deploy`，密钥保存在 `.secrets/github-deploy/`。

## 查看状态和日志

在部署目录中设置许可证路径并定义 Compose 参数：

```bash
cd "$HOME/apps/VAYZRA"
export HAPPYLEARN_AISTOR_LICENSE_FILE=/absolute/path/to/minio.license

compose=(
  docker compose
  --project-name happylearn-dev
  --project-directory "$PWD"
  --env-file "$PWD/.env.github-deploy"
  --env-file "$PWD/.secrets/github-deploy/ai.env"
  -f "$PWD/deploy/compose.dev.yml"
  -f "$PWD/deploy/compose.github.yml"
)

"${compose[@]}" ps
"${compose[@]}" logs --tail 100 app worker
curl --fail http://127.0.0.1:8080/api/v1/health/ready
```

如果部署时修改了 `--app-port`，最后一条命令也要使用对应端口。

## 停止与重新启动

停止容器但保留数据库和对象存储数据：

```bash
"${compose[@]}" stop
```

重新启动：

```bash
"${compose[@]}" up -d --wait --wait-timeout 300
```

不要在普通更新中执行 `down --volumes`、`docker volume prune` 或 `docker system prune`。这些命令可能删除数据库或对象文件。需要清理开发数据时，先阅读 [`docs/runbooks/local-development.md`](docs/runbooks/local-development.md) 的破坏性清理说明并确认目标项目。

## 本地文件与安全边界

- `.env.github-deploy`、`.secrets/`、许可证、构建输出和测试产物均被 Git 忽略。
- AI 主密钥用于解密已保存的 AI Provider 凭据。丢失或替换它会使已有密文不可用；请按本机敏感配置备份。
- 脚本不会创建管理员账号、修改远端 Git 仓库、推送代码、删除卷或自动恢复数据库。
- 真实生产环境必须使用不可变镜像清单、文件型生产密钥、发布前备份、维护窗口和 Phase 6 验收流程，不能直接复用此开发脚本。

更多开发、测试和管理员初始化步骤参见 [`docs/runbooks/local-development.md`](docs/runbooks/local-development.md)。
