# VAYZRA / HappyLearn

HappyLearn 是一个由 Go API、Vue 管理控制台、文件处理 Worker、PostgreSQL、Redis 和 AIStor 对象存储组成的教学平台。

本文说明如何在 Linux 本机从 GitHub 拉取代码并使用 Docker Compose 部署开发环境。它不会执行 Phase 6 生产发布、真实服务器切换或数据恢复操作；生产部署请使用 [`docs/runbooks/phase6-real-server-acceptance.md`](docs/runbooks/phase6-real-server-acceptance.md) 和已审批的发布流程。

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
