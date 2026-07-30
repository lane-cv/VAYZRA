# HappyLearn Phase 5 运维、备份与灾难恢复手册

本手册适用于 Phase 5 已实现的教师运维控制台、加密备份编排器和空环境恢复验证器。示例固定使用 Compose 项目 `happylearn-dev`，备份数据固定位于 `/var/lib/happylearn/backup`。所有 `<OPERATOR_VALUE>` 都必须由当班操作员从获批的变更单或离线托管介质填写；它不是可直接使用的值。

先在维护窗口记录值班人、工单、开始时间和预期结束时间。生产主机安装、systemd 单元、真实域名和公开 TLS 属于 Phase 6，不在本手册中自动执行。除特别注明的空环境恢复验证外，以下步骤都不得停止、替换或删除现有生产卷。

## 所有者专用密钥与权限检查

以部署账号执行日常备份。首次准备目录时，由主机管理员把显式目录交给获批的部署账号；不要把部署账号加入无关组，也不要把密钥放入仓库、镜像、数据库或命令参数。

```bash
export HAPPYLEARN_DEPLOY_USER='<OPERATOR_VALUE>'
export HAPPYLEARN_DEPLOY_GROUP='<OPERATOR_VALUE>'
sudo install -d -m 0700 -o "$HAPPYLEARN_DEPLOY_USER" -g "$HAPPYLEARN_DEPLOY_GROUP" \
  /var/lib/happylearn/backup/secrets \
  /var/lib/happylearn/backup/repository \
  /var/lib/happylearn/backup/state \
  /var/lib/happylearn/restore/control \
  /var/lib/happylearn/restore/reports \
  /var/lib/happylearn/app-secrets \
  /var/lib/happylearn/licenses
unset HAPPYLEARN_DEPLOY_USER HAPPYLEARN_DEPLOY_GROUP
```

切换到部署账号后设置 `umask`。数据库密码通过无回显提示写入，不出现在 shell 历史中。本地 Restic 密码由系统直接生成。`local_repository` 是容器内固定路径 `/repository`，不是任意主机路径。

```bash
umask 077
read -rs -p '数据库密码（<OPERATOR_VALUE>）: ' HAPPYLEARN_OPERATOR_DATABASE_PASSWORD
printf '%s' "$HAPPYLEARN_OPERATOR_DATABASE_PASSWORD" \
  > /var/lib/happylearn/backup/secrets/database_password
unset HAPPYLEARN_OPERATOR_DATABASE_PASSWORD
printf '%s' '/repository' \
  > /var/lib/happylearn/backup/secrets/local_repository
openssl rand -base64 48 \
  > /var/lib/happylearn/backup/secrets/local_password
chmod 0400 \
  /var/lib/happylearn/backup/secrets/database_password \
  /var/lib/happylearn/backup/secrets/local_repository \
  /var/lib/happylearn/backup/secrets/local_password
```

在与应用主机分离、受控且有备份的离线挂载介质 `<OPERATOR_VALUE>` 上生成 Age X25519 identity。身份文件绝不能回传应用主机；应用主机只保存公开 recipient。若组织已经托管身份，则 `<OPERATOR_VALUE>` 是经过双人核对的介质挂载路径。

```bash
export HAPPYLEARN_OFFLINE_MEDIA='<OPERATOR_VALUE>'
test -d "$HAPPYLEARN_OFFLINE_MEDIA"
age-keygen -o "$HAPPYLEARN_OFFLINE_MEDIA/age-identity.txt"
age-keygen -y "$HAPPYLEARN_OFFLINE_MEDIA/age-identity.txt" \
  > /var/lib/happylearn/backup/secrets/age-recipient
chmod 0400 \
  "$HAPPYLEARN_OFFLINE_MEDIA/age-identity.txt" \
  /var/lib/happylearn/backup/secrets/age-recipient
unset HAPPYLEARN_OFFLINE_MEDIA
```

核对公开 recipient 已写入应用主机后，立即卸载离线介质并归还密钥托管；不要让 identity 随日常备份目录被复制。

内部 metrics bearer、主机采样 HMAC、AIStor license、恢复验证用教师凭据也必须是当前部署账号拥有的普通文件，且模式为 `0400`，不能是符号链接。教师凭据只用于隔离恢复环境的授权抽样，不得复用生产会话 Cookie。

```bash
openssl rand -base64 48 > /var/lib/happylearn/app-secrets/metrics-bearer
openssl rand -hex 32 > /var/lib/happylearn/app-secrets/host-metrics-hmac
read -rs -p '恢复验证教师凭据（<OPERATOR_VALUE>）: ' HAPPYLEARN_RESTORE_TEACHER_VALUE
printf '%s' "$HAPPYLEARN_RESTORE_TEACHER_VALUE" \
  > /var/lib/happylearn/restore/control/teacher-credential
unset HAPPYLEARN_RESTORE_TEACHER_VALUE
chmod 0400 \
  /var/lib/happylearn/app-secrets/metrics-bearer \
  /var/lib/happylearn/app-secrets/host-metrics-hmac \
  /var/lib/happylearn/licenses/minio.license \
  /var/lib/happylearn/restore/control/teacher-credential
stat -c '%a %U:%G %n' \
  /var/lib/happylearn/backup/secrets \
  /var/lib/happylearn/backup/repository \
  /var/lib/happylearn/backup/state \
  /var/lib/happylearn/backup/secrets/database_password \
  /var/lib/happylearn/backup/secrets/local_repository \
  /var/lib/happylearn/backup/secrets/local_password \
  /var/lib/happylearn/backup/secrets/age-recipient \
  /var/lib/happylearn/app-secrets \
  /var/lib/happylearn/app-secrets/metrics-bearer \
  /var/lib/happylearn/app-secrets/host-metrics-hmac \
  /var/lib/happylearn/licenses/minio.license \
  /var/lib/happylearn/restore/control/teacher-credential
```

目录必须显示 `700`，密钥文件必须显示 `400`，所有者和组必须是获批部署账号。任一检查不符就停止，不要用放宽权限来绕过脚本。为当前受保护的操作员 shell 设置非密钥路径和公开标识：

```bash
export HAPPYLEARN_AISTOR_LICENSE_FILE=/var/lib/happylearn/licenses/minio.license
export HAPPYLEARN_BACKUP_SECRET_DIRECTORY=/var/lib/happylearn/backup/secrets
export HAPPYLEARN_BACKUP_REPOSITORY_DIRECTORY=/var/lib/happylearn/backup/repository
export HAPPYLEARN_BACKUP_STATE_DIRECTORY=/var/lib/happylearn/backup/state
export HAPPYLEARN_BACKUP_AGE_RECIPIENT="$(
  sed -n '1p' /var/lib/happylearn/backup/secrets/age-recipient
)"
export HAPPYLEARN_BACKUP_ENCRYPTION_KEY_ID='<OPERATOR_VALUE>'
```

`HAPPYLEARN_BACKUP_AGE_RECIPIENT` 是公钥；Age identity 与 Restic 密码仍只存在于各自的受控文件中。

## 本地与可选 S3 仓库初始化

不要直接执行裸 `restic init`。`scripts/phase5-backup.sh` 先检查仓库；只有 Restic 明确报告仓库不存在时才初始化，随后重新读取配置、完成一致性快照和完整性校验。第一次受控备份就是本地仓库初始化与首个恢复点创建，路径仍由上节固定变量提供。

可选远端必须一次配齐四个文件，否则视为配置错误并停止。生产 endpoint 必须是 HTTPS；`remote_repository` 格式为 `s3:https://<OPERATOR_VALUE>`，不得包含用户信息、查询串或关闭 TLS 校验的参数。

```bash
umask 077
read -rs -p '远端 Restic 仓库（s3:https://<OPERATOR_VALUE>）: ' \
  HAPPYLEARN_OPERATOR_REMOTE_REPOSITORY
read -rs -p '远端 Restic 密码（<OPERATOR_VALUE>）: ' \
  HAPPYLEARN_OPERATOR_REMOTE_PASSWORD
read -rs -p 'S3 access key ID（<OPERATOR_VALUE>）: ' \
  HAPPYLEARN_OPERATOR_REMOTE_ACCESS_KEY_ID
read -rs -p 'S3 secret access key（<OPERATOR_VALUE>）: ' \
  HAPPYLEARN_OPERATOR_REMOTE_SECRET_ACCESS_KEY
printf '%s' "$HAPPYLEARN_OPERATOR_REMOTE_REPOSITORY" \
  > /var/lib/happylearn/backup/secrets/remote_repository
printf '%s' "$HAPPYLEARN_OPERATOR_REMOTE_PASSWORD" \
  > /var/lib/happylearn/backup/secrets/remote_password
printf '%s' "$HAPPYLEARN_OPERATOR_REMOTE_ACCESS_KEY_ID" \
  > /var/lib/happylearn/backup/secrets/remote_access_key_id
printf '%s' "$HAPPYLEARN_OPERATOR_REMOTE_SECRET_ACCESS_KEY" \
  > /var/lib/happylearn/backup/secrets/remote_secret_access_key
unset HAPPYLEARN_OPERATOR_REMOTE_REPOSITORY \
  HAPPYLEARN_OPERATOR_REMOTE_PASSWORD \
  HAPPYLEARN_OPERATOR_REMOTE_ACCESS_KEY_ID \
  HAPPYLEARN_OPERATOR_REMOTE_SECRET_ACCESS_KEY
chmod 0400 /var/lib/happylearn/backup/secrets/remote_repository \
  /var/lib/happylearn/backup/secrets/remote_password \
  /var/lib/happylearn/backup/secrets/remote_access_key_id \
  /var/lib/happylearn/backup/secrets/remote_secret_access_key
stat -c '%a %U:%G %n' /var/lib/happylearn/backup/secrets/remote_*
```

不使用远端仓库时，四个 `remote_*` 文件必须全部不存在；不要留下半套配置。仓库首次初始化后，在 `/admin/backups` 核对本地组件的校验时间、到期时间和容量。配置远端时还要核对远端组件；界面不会显示仓库路径或凭据。

## 每日计划与手动备份

教师先在 `/admin/settings` 核对 `03:00`、`Asia/Shanghai` 和设置版本。Phase 5 的 `scheduled` 触发器以当日上海日期为幂等键；在 Phase 6 调度器上线前，只允许获批的主机调度设施每天在设定时间调用一次以下固定命令。不要在本任务里安装临时 cron 或 systemd 单元。

```bash
scripts/phase5-backup.sh --project happylearn-dev --trigger scheduled
```

计划执行后在 `/admin/backups` 确认当天只有一个 `scheduled` 运行。需要人工恢复点时，在已公告的维护窗口运行：

```bash
scripts/phase5-backup.sh --project happylearn-dev --trigger manual
```

该命令会进入排空和备份模式、等待活动任务结束、创建 PostgreSQL 与 AIStor 的同批次恢复点、校验、可选同步并执行保留策略。不要同时启动第二个备份，也不要在脚本外停 AIStor 或清理临时转储。

Phase 5 后续验收任务会提供以下名称，便于工单和 CI 提前统一；在对应 Task 2、Task 3 和 Task 4 完成前不得执行或声称它们可用：

```bash
HAPPYLEARN_E2E_GROUP=recovery make e2e-phase5
bash scripts/e2e-phase5_failure_matrix.sh
```

Task 5 不创建这些 harness，也不实现其故障注入或资源采样。

## healthy、degraded 与 failed 判读

- `healthy` 是运维仪表盘的聚合判读：应用、依赖、采样新鲜度和最新本地恢复点都正常。它不是 `backup_runs` 的状态名。
- `succeeded` 表示本地恢复点已经完整性校验；若配置了远端，远端同步也成功。只有这类结果可作为完全正常的当日证据。
- `degraded` 表示本地恢复点有效，但可选远端同步或远端保留失败。先保护本地仓库容量并处理 `backup_remote_sync` 告警；本地恢复仍可用，但异地灾备不满足目标。
- `failed` 表示没有形成可验证的本地恢复点，或完整性检查失败。立即停止依赖该运行做发布或清理，确认系统已回到 `normal`，并在下一次受控重试前处理安全错误类别。

`acknowledged` 只表示教师已知悉告警，不能把 `degraded` 或 `failed` 当作恢复。只有后续健康采样满足回稳条件，告警才会变为 `resolved`。

## 告警确认与 Webhook 测试

在 `/admin/alerts` 按严重程度、状态和类别筛选。打开目标告警，核对安全摘要、阈值、首次和最近观测时间，再选择“确认告警”。确认不会把告警设为 resolved，也不会停止后续采样；根因未消失时不得关闭工单。

Webhook URL 和可选授权头只能来自所有者专用文件。生产 URL 必须使用公开 HTTPS 目标；不得指向私网、回环、链路本地、云元数据或带查询串的地址。部署配置只传文件路径：

```text
HAPPYLEARN_WEBHOOK_URL_SECRET_FILE=/var/lib/happylearn/app-secrets/webhook-url
HAPPYLEARN_WEBHOOK_AUTHORIZATION_SECRET_FILE=/var/lib/happylearn/app-secrets/webhook-authorization
```

配置 Webhook 时用无回显提示写入，随后检查文件权限；未配置授权头时不要创建第二个文件，也不要把空值当作凭据：

```bash
umask 077
read -rs -p 'Webhook HTTPS URL（<OPERATOR_VALUE>）: ' HAPPYLEARN_OPERATOR_WEBHOOK_URL
read -rs -p 'Webhook 授权头（<OPERATOR_VALUE>，无则不创建）: ' HAPPYLEARN_OPERATOR_WEBHOOK_AUTHORIZATION
printf '%s' "$HAPPYLEARN_OPERATOR_WEBHOOK_URL" \
  > /var/lib/happylearn/app-secrets/webhook-url
if test -n "$HAPPYLEARN_OPERATOR_WEBHOOK_AUTHORIZATION"; then
  printf '%s' "$HAPPYLEARN_OPERATOR_WEBHOOK_AUTHORIZATION" \
    > /var/lib/happylearn/app-secrets/webhook-authorization
  chmod 0400 /var/lib/happylearn/app-secrets/webhook-authorization
fi
unset HAPPYLEARN_OPERATOR_WEBHOOK_URL \
  HAPPYLEARN_OPERATOR_WEBHOOK_AUTHORIZATION
chmod 0400 /var/lib/happylearn/app-secrets/webhook-url
stat -c '%a %U:%G %n' /var/lib/happylearn/app-secrets/webhook-*
```

由已登录的教师运维客户端向 `/api/v1/admin/operations/webhook-test` 发送无自定义 target、无业务 body 的 `POST`。服务只发送固定合成载荷；接收端应看到 schema 版本、测试状态和仪表盘路径，不得看到学生信息、IP、对象标识、请求正文或密钥。`webhook_not_configured` 表示先补齐文件配置；`webhook_delivery_failed` 表示保持告警持久化并按安全错误类别排查，不能临时关闭目标校验。

## 安全诊断

先记录教师页面显示的支持编号和安全错误类别。主机命令只读取固定项目的状态、聚合计数和资源摘要：

```bash
docker compose -p happylearn-dev -f deploy/compose.dev.yml \
  ps --format '{{.Service}}\t{{.State}}\t{{.Health}}'
docker compose -p happylearn-dev -f deploy/compose.dev.yml \
  exec -T postgres psql -U happylearn -d happylearn --no-psqlrc \
  -c "SELECT state,count(*) FROM backup_runs GROUP BY state ORDER BY state;"
docker compose -p happylearn-dev -f deploy/compose.dev.yml \
  exec -T postgres psql -U happylearn -d happylearn --no-psqlrc \
  -c "SELECT state,count(*) FROM operational_alerts GROUP BY state ORDER BY state;"
docker stats --no-stream happylearn-dev-app-1 happylearn-dev-worker-1 \
  --format '{{.Name}}\t{{.CPUPerc}}\t{{.MemUsage}}'
```

如需日志，只允许在受控终端读取短时间窗内的结构化固定类别：

```bash
docker compose -p happylearn-dev -f deploy/compose.dev.yml \
  logs --no-color --since 15m app worker backup
```

诊断证据不得包含查询串、Cookie、授权头、Webhook URL、Restic 密码、Age identity、对象键、文件名、学生内容、原始命令输出或 PostgreSQL 转储。不要通过扩展 SQL 投影或开启 shell trace 获取“更多上下文”。保存证据前先运行项目的 fail-closed sanitizer；发现禁用字段就按泄漏事件处理，不得发布该目录。

## 空环境恢复验证

只选择 `succeeded` 或具有已校验本地组件的 `degraded` 运行 UUID。确认仓库、密钥、AIStor license、控制目录和报告目录的权限仍符合上文要求。恢复验证器会创建唯一 `happylearn-phase5-restore-` 前缀项目、空卷和无公开端口环境；它不会覆盖 `happylearn-dev`。

```bash
export HAPPYLEARN_RESTORE_CONTROL_DIRECTORY=/var/lib/happylearn/restore/control
export HAPPYLEARN_RESTORE_REPORT_DIRECTORY=/var/lib/happylearn/restore/reports
export HAPPYLEARN_RESTORE_TEACHER_CREDENTIAL_FILE=/var/lib/happylearn/restore/control/teacher-credential
```

**破坏性恢复警告：** 以下命令会把所选恢复点写入全新的临时 PostgreSQL 与 AIStor 卷。必须再次确认 UUID 和空环境前缀；绝不能把生产卷作为恢复目标。

```bash
scripts/phase5-restore-verify.sh --backup-id '<OPERATOR_VALUE>'
```

成功报告必须证明仓库完整性、migration readiness、允许列表行数、所有活动对象引用、授权抽样、恢复会话撤销以及 `session_revocation_verified=true`。脚本会清理自己创建的容器、网络和卷；失败时先保留经 sanitizer 允许的摘要，再确认无同前缀资源。不要手工扩大清理范围。

## RPO 与 RTO 测量

RPO 从事故判定时刻向前量到最近一个已校验本地恢复点的完成时间。使用只返回小时数的聚合查询；结果必须不大于 `24 小时`：

```bash
docker compose -p happylearn-dev -f deploy/compose.dev.yml \
  exec -T postgres psql -U happylearn -d happylearn --no-psqlrc \
  -c "SELECT round(EXTRACT(EPOCH FROM (clock_timestamp()-max(r.finished_at)))/3600,2) AS rpo_hours FROM backup_runs r WHERE r.state IN ('succeeded','degraded') AND EXISTS (SELECT 1 FROM backup_artifacts a WHERE a.backup_run_id=r.id AND a.repository='local' AND a.verified_at IS NOT NULL);"
```

RTO 使用空环境恢复验证的实际开始、完成时间和持久化 `rto_seconds`。结果必须不大于 `4 小时`，同时会话撤销必须为真：

```bash
docker compose -p happylearn-dev -f deploy/compose.dev.yml \
  exec -T postgres psql -U happylearn -d happylearn --no-psqlrc \
  -c "SELECT state,started_at,finished_at,rto_seconds,session_revocation_verified FROM restore_verifications ORDER BY started_at DESC,id DESC LIMIT 10;"
```

把查询时间、恢复点 UUID、报告 SHA-256、RPO 小时数、RTO 秒数和审批工单记录在受控证据中。不要记录仓库地址、snapshot ID、对象列表或凭据。

## Web UI 禁止破坏性恢复

教师 Web UI 不提供恢复执行入口。`/admin/backups` 只能查看安全历史、排队手动备份和读取恢复验证结果；任何浏览器请求都不得选择目标卷、执行 Restic restore、替换数据库或切换生产挂载。

真实灾难恢复只能由主机操作员在单独审批的维护窗口中执行：先恢复到新卷，通过本手册的空环境验证，再由 Phase 6 的发布/回滚流程申请切换。原生产卷保持分离且可回退，直到另一项明确批准的保留操作到期。

## 仓库凭据或 Age 身份丢失

丢失 Restic 仓库密码时立即冻结保留和仓库写入，保护现有仓库位并启动密钥托管事件。先从获批离线托管恢复同一密码，在隔离环境运行完整性与恢复验证。不要覆盖 `local_password` 或 `remote_password` 来“重置”旧仓库；新密码不能解密旧恢复点。若托管副本也丢失，记录受影响的 RPO 范围，经事故负责人批准后使用全新仓库路径创建新的恢复链，并只读保留旧数据以便后续取证。

丢失 Age X25519 identity 时，现有 Restic 仓库可能仍能由仓库密码恢复，但已加密的离线恢复说明无法解密。立即检查身份托管副本；没有副本时记录恢复说明不可用，生成新的离线 identity 与 recipient 仅用于未来恢复包。不得删除旧加密恢复包，也不得把新 identity 上传到应用主机来伪装兼容。

任一丢失事件都必须暂停发布，直到至少一个新恢复点完成空环境验证、RPO/RTO 重新测量且双人复核恢复材料。

## 清理、保留与磁盘压力

正常保留由备份编排器在完整性校验成功后执行：本地 `keep-daily 7`；远端 `keep-daily 30` 和 `keep-monthly 12`；发布前恢复点保护 30 天。有效本地恢复点的 `degraded` 运行参与本地保留，失败运行不触发破坏性 prune，最后一个成功恢复点永不删除。不要绕过编排器直接运行 `restic forget`、`prune` 或主机级清理。

磁盘用量达到 `75%` 是 warning，达到 `90%` 是 critical。先停止非必要的新备份和发布，确认最近本地恢复点完整、仓库增长来源和远端状态，再按已批准的保留策略运行下一次受控备份。不得为了消除告警扩大清理范围、缩短既定保留或删除未知临时文件。告警只会在连续健康采样后自动解决，教师确认不等于释放容量。

运维样本默认保留 7 天，告警和备份元数据保留 365 天；不可变审计至少保留 365 天。元数据清理由应用内有界任务负责，备份仓库保留由备份编排器负责，两者不得互相替代。

## 回滚至 Phase 4 且保留 Phase 5 数据

Phase 4 兼容回滚只替换应用行为，不降级数据库或删除恢复链：

1. 先运行一次 `scripts/phase5-backup.sh --project happylearn-dev --trigger pre_release`，并完成空环境恢复验证。
2. 在恢复出的临时数据库与对象副本上启动获批的 Phase 4 app/worker 镜像，验证它能容忍当前 migration 版本；失败就停止回滚。
3. 公告维护窗口，停止新的 AI、文件和通知任务，等待活动任务结束；只替换已批准的 app/worker 镜像。
4. 保留 Phase 5 schema、运维样本、告警、备份元数据、Restic 仓库、Age 加密恢复材料和 AIStor 对象。不得执行 down migration，不得删除 Phase 5 表、行、卷或仓库数据。
5. 验证 Phase 4 登录、教学、文件、人工答疑、通知与 AI；Phase 5 页面或路由不可用属于预期，但 Phase 5 数据继续保留和备份。
6. 故障解除后重新部署 Phase 5 镜像，核对 readiness、告警、最新恢复点和一次合成恢复验证。

若 Phase 4 在临时副本上不兼容，不得直接连生产数据试错；恢复当前 Phase 5 镜像并升级处理事故。
