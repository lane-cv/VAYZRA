# Phase 6 本地生产验收

本流程只证明仓库拥有的生产拓扑、发布与恢复逻辑，不替代真实服务器验收。Prerequisites（前置条件）是 Docker Engine、Compose v5.3、Go 1.26.5、Node 24.18、pnpm 11.9、ShellCheck、govulncheck、Trivy，以及绝对路径、仅所有者可读的有效 AIStor `minio.license`。

```bash
export HAPPYLEARN_AISTOR_LICENSE_FILE=/absolute/path/minio.license
HAPPYLEARN_E2E_GROUP=install make e2e-phase6
HAPPYLEARN_E2E_GROUP=regression make e2e-phase6
HAPPYLEARN_E2E_GROUP=mobile make e2e-phase6
HAPPYLEARN_E2E_GROUP=recovery make e2e-phase6
HAPPYLEARN_E2E_GROUP=release make e2e-phase6
HAPPYLEARN_E2E_GROUP=rollback make e2e-phase6
HAPPYLEARN_E2E_GROUP=failure-matrix make e2e-phase6
HAPPYLEARN_E2E_GROUP=restart make e2e-phase6
HAPPYLEARN_E2E_GROUP=security make e2e-phase6
HAPPYLEARN_E2E_GROUP=resources make e2e-phase6
HAPPYLEARN_E2E_GROUP=all make e2e-phase6
```

每次运行创建唯一的 `happylearn_phase6_<nonce>_prod` 项目、回环端口、临时根、OCI registry 和两组本地摘要镜像。普通组有界于一小时；`resources` 固定采样 30 分钟。`HUP`、`INT`、`TERM` 和普通退出都进入同一清理陷阱。可安全按 Ctrl-C 取消，脚本会先保留首个失败状态，再删除精确项目的容器、网络、卷、临时 tag、registry 和秘密根，最后执行零残留证明；禁止使用全局 prune。

`resources` 在同一个 30 分钟窗口内运行 Phase 1–5 浏览器回归，以产生课程读取、安全文件、通知、老师/AI 答疑 SSE、运维读取和 Caddy 流量；同时向真实私有 host-samples 端点提交一份签名主机样本。采样证据包含逐服务及聚合 CPU、工作集内存、重启数、健康状态和无 URL/请求体的延迟桶。浏览器负载结束后，由真实 Phase 5 主机协调器处理 UI 创建的 manual backup；采样器必须实际观察到 worker 已停止且 backup 容器正在运行，否则整个组失败。

本机使用 AIStor Free license 时，`compose.prod.local.yml` 仅在一次性本地验收栈中设置 `HAPPYLEARN_LOCAL_OBJECTSTORE_SKIP_LIFECYCLE_BOOTSTRAP=true`，因为该 license 不提供生产生命周期管理能力。这个开关只跳过生命周期规则的启动写入，不跳过对象存储认证、桶访问、上传下载、备份或恢复验证；基础 `compose.prod.yml` 不包含它，`--mode server` 的所有生产脚本也会拒绝任何 `HAPPYLEARN_LOCAL_*` 变量。真实服务器必须使用具备生命周期能力的获批配置并保持启动时 fail closed。

唯一可上传诊断是 `test-results/phase6/<nonce>/containers.log`。它只含容器名、生命周期状态、退出码、OOM 标记和省略行计数。Artifact sanitizer `sanitize-e2e-artifacts.sh` 在发布前扫描密码、令牌、Authorization/Cookie、数据库或 Redis URL、查询参数、对象键、请求体、Age 私钥和备份内容；发现任何疑似秘密便删除整组材料并失败。浏览器 trace、截图、原始日志、Compose 展开、license、环境和秘密文件永不上传。

通过证据包括：安装与 TLS 边界、桌面/移动回归、加密备份与空卷恢复、错误密钥、篡改 Restic pack 和真实缺失 AIStor 对象的关闭式拒绝、发布 manifest 晋升、15 项真实 `failure-matrix`、全部 16 个持久化状态的信号中断恢复、自动回滚且不恢复数据库、逐服务与整项目重启、依赖与镜像扫描、30 分钟资源采样、清理零残留。失败安全状态查看 `release-state.json` 中的非敏感 state/result/traceId；不得复制整个临时目录。复现时只重跑对应组，秘密会重新生成，不能保留旧秘密。

常见关闭式失败包括 license 不可读、镜像摘要不匹配、目录所有权错误、备份证据过期、迁移兼容区间错误、维护页非 503、私有端口发布、扫描器超时和残留资源。修复原因后重新运行，不得跳过 gate 或手工伪造证据。

Phase 6 repository production-ready; real-server acceptance pending.

该句只允许在仓库门禁全部通过后用于边界说明；它不表示 Phase 6 最终完成。未经另行授权不得创建 `v1.0.0-rc.1` 或 `v1.0.0`。
