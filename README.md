# VAYZRA / HappyLearn

HappyLearn 是一个由 Go API、Vue 管理控制台、文件处理 Worker、PostgreSQL、Redis 和 AIStor 对象存储组成的教学平台。

当前初版 release：`v0.1.0`。

本文提供从安装到部署的最短路径：可以直接使用 Docker Compose 部署本地/测试环境，也可以安装 Go、Node.js 和 pnpm 进行源码开发。它不会执行 Phase 6 生产发布、真实服务器切换或数据恢复操作；生产部署请使用 [`docs/runbooks/phase6-real-server-acceptance.md`](docs/runbooks/phase6-real-server-acceptance.md)、[`docs/runbooks/phase6-release-rollback.md`](docs/runbooks/phase6-release-rollback.md) 和已审批的发布流程。

## 安装方式

| 场景 | 推荐方式 | 是否需要在主机安装 Go/Node.js/pnpm |
| --- | --- | --- |
| 首次体验或本地部署 | `scripts/deploy-from-github.sh` + Docker Compose | 否 |
| 日常前端/后端开发 | 源码安装 + 本地依赖 | 是 |
| 生产环境 | Ubuntu 24.04 + `compose.prod.yml` + 不可变镜像 | 构建机需要，生产机不需要 |

### 方式 A：Docker Compose 快速安装

这是最简单的安装方式。它会构建应用和 Worker 镜像，并启动 PostgreSQL、Redis、AIStor、API、管理控制台和文件处理 Worker。默认端口只绑定到 `127.0.0.1`，适合个人电脑、测试机或反向代理后的内网服务。

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
sudo scripts/prod-preflight.sh \
  --project-dir /srv/happylearn/current \
  --env-file /etc/happylearn/production.env \
  --manifest /srv/happylearn/releases/release-input/candidate-manifest.json \
  --mode server \
  --expected-host-address '<approved-public-address>'

sudo scripts/prod-release.sh \
  --project-dir /srv/happylearn/current \
  --env-file /etc/happylearn/production.env \
  --manifest /srv/happylearn/releases/release-input/candidate-manifest.json \
  --version 0.1.0 \
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
3. 验证 Compose 配置，构建 App 与 Worker 镜像；
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

脚本只接受 fast-forward 更新。若仓库存在未提交修改、分支不是指定分支或处于 detached HEAD，会停止而不是覆盖本地内容。

部署其他分支时明确指定：

```bash
./scripts/deploy-from-github.sh \
  --directory "$PWD" \
  --ref feature/example \
  --license-file /absolute/path/to/minio.license
```

已有目录必须已经检出同名分支。该选项适合测试分支，不应替代正式发布审批。

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
