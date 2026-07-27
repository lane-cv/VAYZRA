# Phase 4 AI 答疑运维手册

本手册覆盖 AI 供应商密钥、排队与运行、额度账本、附件和回滚。所有排查只使用 UUID、时间、状态、稳定错误类别和聚合计数。不得把学生问题、AI 回答、提示词、附件名称、对象地址、Cookie、连接串或任何密钥复制到终端历史、工单和日志。

下列命令固定使用 Compose 项目 `happylearn-dev`。生产环境应替换为受控的部署命令和只读诊断身份，不得直接套用开发凭据。

## 环境与密钥注入

AI 运行在 `app` 服务内；通用文件处理 `worker` 不解密供应商密钥，也不需要任何 `HAPPYLEARN_AI_*` 环境变量。服务端真实默认值如下：

```text
HAPPYLEARN_AI_MASTER_KEY_VERSION=1
HAPPYLEARN_AI_BUSINESS_TIMEZONE=Asia/Shanghai
HAPPYLEARN_AI_GLOBAL_CONCURRENCY=2
HAPPYLEARN_AI_PER_STUDENT_CONCURRENCY=1
HAPPYLEARN_AI_ALLOW_PRIVATE_PROVIDER=false
```

生产环境必须显式提供 `HAPPYLEARN_AI_MASTER_KEY`。它是标准 Base64 编码的 32 个随机字节。生成命令直接写入权限为 `0600` 的文件，不向标准输出打印值：

```bash
install -d -m 0700 .secrets
umask 077
openssl rand -base64 32 > .secrets/ai-master-key
chmod 0600 .secrets/ai-master-key
test "$(wc -c < .secrets/ai-master-key)" -eq 45
```

把文件交给部署平台的文件型 Secret 或凭据管理器，并由入口脚本读取后导出给 `app`；不要把文件挂入 Web 根目录、镜像或 Git。Compose 本地开发可把值写入权限为 `0600` 的 `.env`，不要执行会把值显示到屏幕的检查命令：

```bash
install -m 0600 /dev/null .env
printf '%s' 'HAPPYLEARN_AI_MASTER_KEY=' >> .env
tr -d '\n' < .secrets/ai-master-key >> .env
printf '\n%s\n' \
  'HAPPYLEARN_AI_MASTER_KEY_VERSION=1' \
  'HAPPYLEARN_AI_BUSINESS_TIMEZONE=Asia/Shanghai' \
  'HAPPYLEARN_AI_GLOBAL_CONCURRENCY=2' \
  'HAPPYLEARN_AI_PER_STUDENT_CONCURRENCY=1' \
  'HAPPYLEARN_AI_ALLOW_PRIVATE_PROVIDER=false' >> .env
chmod 0600 .env
```

开发模式在密钥为空时会使用固定的一次性密钥，仅方便首次启动，不得把这种状态带到共享或生产环境。供应商服务地址没有环境默认值，必须经教师控制台配置。生产必须保持私网供应商开关为 `false`。

### 主密钥轮换

当前进程只解密当前版本的密文，因此轮换是一次受控的短暂停机，不是简单地替换环境变量：

1. 在“AI 管理 → 额度策略”记录现有全局额度及学生覆盖项，把四项全局额度全部设为“停用”，并逐一把所有学生覆盖项设为“继承”或“停用”，阻止新运行；等待 `queued` 和 `streaming` 计数归零，必要时让学生在详情页显式取消运行。
2. 备份数据库与对象存储，并保留旧主密钥的受控恢复副本。不要运行 down migration。
3. 用上面的方式生成新文件，将版本加一，原子更新部署 Secret，然后只重启 `app`。
4. 登录教师控制台，在“AI 管理 → 供应商配置”逐个编辑供应商，选择“替换 API Key”，重新输入对应上游凭据并保存。此操作使用新主密钥和新版本重新加密；已完成历史不需要解密。
5. 为每个供应商执行“测试连接”，确认文本/视觉模型和数学/物理提示词配置后，将目标供应商设为当前。
6. 恢复轮换前的全局额度和学生覆盖项，提交一个合成问题并检查运行、用量和日志。验证后按密钥管理制度销毁旧主密钥。

轮换前仍在队列中的运行会因供应商密钥版本变化以稳定类别 `provider_key_rotated` 失败，因此第一步必须清空活动运行。

## 在教师控制台配置供应商

1. 打开 `/admin/ai` 的“供应商配置”，选择“新建供应商”，填写名称、HTTPS 服务地址、协议模式（Chat Completions 或 Responses）和上游 API Key。服务地址不得带查询参数；不要把凭据放进地址。
2. 在“模型路由”为该供应商各保留一个已启用的文本模型和视觉模型，填写上下文、最大输出、图片折算 Token、价格与四类超时。
3. 在“提示词”分别保存数学、物理的当前版本；在“额度策略”明确设置全局额度和必要的学生覆盖项。
4. 回到“供应商配置”点“测试连接”。测试结果只应包含成功标志、协议、延迟和稳定错误类别；密钥不会回显。测试通过后点“设为当前”。
5. 用合成学生账号分别提交数学文本和物理图片问题，再到 `/admin/ai-usage` 按状态、学生、模型和上海日期核对记录。

连接测试失败时先核对 HTTPS、DNS、出口防火墙和协议模式。不要在工单中粘贴请求或响应正文。

## 聚合状态与资源

以下查询只返回类别与计数：

```bash
docker compose -p happylearn-dev -f deploy/compose.dev.yml exec -T postgres \
  psql -U happylearn -d happylearn -c \
  "SELECT status,count(*) FROM ai_runs GROUP BY status ORDER BY status;"

docker compose -p happylearn-dev -f deploy/compose.dev.yml exec -T postgres \
  psql -U happylearn -d happylearn -c \
  "SELECT error_code,count(*) FROM ai_runs WHERE error_code IS NOT NULL GROUP BY error_code ORDER BY error_code;"

docker compose -p happylearn-dev -f deploy/compose.dev.yml exec -T postgres \
  psql -U happylearn -d happylearn -c \
  "SELECT usage_source,count(*) FROM ai_runs WHERE usage_source IS NOT NULL GROUP BY usage_source ORDER BY usage_source;"
```

第三个查询中 `usage_source='unknown'` 的计数就是未知用量数。教师控制台 `/admin/ai-usage` 也提供请求、成功、失败、输入/输出 Token、费用、未知用量和延迟的聚合视图。

Compose 的稳态限制为 3200 MiB、2.0 CPU；其中 `app` 为 256 MiB/0.2 CPU，`worker` 为 1792 MiB/1.0 CPU。数据初始化容器只在启动前短暂运行。查看当前用量：

```bash
docker stats --no-stream happylearn-dev-app-1 happylearn-dev-worker-1
docker compose -p happylearn-dev -f deploy/compose.dev.yml ps
```

若容器名因部署平台而不同，先用 `docker compose ... ps` 确认该项目的 `app` 与 `worker`，不要扩大到其他 Docker 项目。

## 按运行 UUID 排查

只接受授权工单中的规范 UUID，不得按学生姓名或正文搜索：

```bash
export AI_RUN_ID=11111111-1111-4111-8111-111111111111
case "$AI_RUN_ID" in
  ????????-????-4???-[89ab]???-????????????) ;;
  *) printf '%s\n' 'invalid run UUID' >&2; exit 2 ;;
esac
```

查看排队时间、runner 丢失和超时类别：

```bash
docker compose -p happylearn-dev -f deploy/compose.dev.yml exec -T postgres \
  psql -U happylearn -d happylearn -v run_id="$AI_RUN_ID" -c \
  "SELECT id,status,error_code,created_at,started_at,updated_at,completed_at,heartbeat_at,lease_expires_at FROM ai_runs WHERE id=:'run_id';"
```

- `queued` 长时间不变：检查 `app` 健康状态和全局并发计数；runner 就在 `app`，不要重启文件处理 `worker` 代替。
- `runner_lost`：说明 30 秒租约过期后被协调器终结，额度已释放；先检查 `app` 重启、OOM 和数据库连通性，再由学生显式重试。
- `timeout` 或 `timeout_total`：按模型 UUID 在教师控制台核对连接、响应头、流空闲和总超时；确认供应商延迟后再调整，不要直接改运行行。

查看附件处理状态，只返回版本 UUID、用途、状态、错误类别和时间：

```bash
docker compose -p happylearn-dev -f deploy/compose.dev.yml exec -T postgres \
  psql -U happylearn -d happylearn -v run_id="$AI_RUN_ID" -c \
  "SELECT f.id,f.purpose,f.processing_state,f.failure_category,f.created_at FROM ai_runs r JOIN ai_message_files a ON a.message_id=r.trigger_message_id JOIN file_versions f ON f.id=a.file_version_id WHERE r.id=:'run_id' ORDER BY f.created_at,f.id;"
```

`ATTACHMENT_NOT_READY` 发生在入队前，通常还没有运行 UUID；此时改用授权的文件版本 UUID，并复用 [Phase 3 附件排查](phase3-qa-notifications.md#stuck-attachment-processing)。不要手工修改处理状态。

查看该运行的额度预留、结算或释放：

```bash
docker compose -p happylearn-dev -f deploy/compose.dev.yml exec -T postgres \
  psql -U happylearn -d happylearn -v run_id="$AI_RUN_ID" -c \
  "SELECT id,status,reserved_request_count,reserved_token_count,usage_source,error_code,created_at,completed_at FROM ai_runs WHERE id=:'run_id';"

docker compose -p happylearn-dev -f deploy/compose.dev.yml exec -T postgres \
  psql -U happylearn -d happylearn -v run_id="$AI_RUN_ID" -c \
  "SELECT action,period_kind,request_delta,token_delta,created_at FROM ai_usage_ledger WHERE run_id=:'run_id' ORDER BY created_at,id;"
```

`QUOTA_EXCEEDED` 发生在入队前，可能没有运行 UUID；在教师控制台“额度策略”核对全局值、学生继承/覆盖值和上海业务日/月边界。额度账本不可编辑；失败或取消的运行应出现幂等 `release`，成功运行应出现幂等 `settle`。

完成排查后执行 `unset AI_RUN_ID`。

## 修正额度估算异常

当供应商报告的实际 Token 高于预留值，系统会完成该次结算，并把对应模型标记为 `quota_estimation_anomaly`，阻止它接收新运行。先按运行 UUID 核对预留与账本，再执行：

1. 在 `/admin/ai` 打开“模型路由”，定位被封锁的模型 UUID。
2. 根据已验证的供应商计数方式，增大上下文 Token、最大输出 Token 或图片配额 Token 中至少一项；不得只清除标志而不修正容量。
3. 勾选“确认调整容量后清除异常封锁”并保存，再执行供应商连接测试和合成问题。
4. 在 `/admin/ai-usage` 确认新运行只结算一次。不要用 SQL 清除封锁或改写不可变账本。

## 停止新运行但保留数据

在“AI 管理 → 额度策略”记录当前全局值和所有学生覆盖值后，把每日请求、每月请求、每日 Token、每月 Token 全部设为“停用”，并逐一把所有学生覆盖项设为“继承”或“停用”。学生的显式正数覆盖会优先于全局值，只改全局值不能保证停止全部新运行。完成这两步后，新请求会在入队前被拒绝；供应商配置、线程、消息、运行事件和用量账本不会被删除，已在运行的请求也不会被取消。等待已有运行结束，或让所属学生在详情页显式取消。恢复服务时先恢复全局值，再恢复记录的学生覆盖值；不要删除表、配置或对象。

## API 与日志泄漏检查

仅在隔离的合成账号环境执行。把专用供应商测试密钥保存到 `.secrets/provider-key`（`0600`），使用权限为 `0600` 的管理员 Cookie 文件请求只读配置 API，并把响应落到受保护文件。以下检查为 quiet 模式，发现命中时只打印固定告警，不打印命中内容：

```bash
chmod 0600 .secrets/provider-key .secrets/admin.cookies
umask 077
curl --fail --silent --show-error \
  --cookie .secrets/admin.cookies \
  http://127.0.0.1:8080/api/v1/admin/ai/providers > .secrets/providers-response.json
if grep -qFf .secrets/provider-key .secrets/providers-response.json; then
  printf '%s\n' 'secret found in provider API response' >&2
  exit 1
fi
if grep -Eqi '"[^"]*(encrypted|cipher)[^"]*"[[:space:]]*:' .secrets/providers-response.json; then
  printf '%s\n' 'secret-bearing field found in provider API response' >&2
  exit 1
fi
if docker compose -p happylearn-dev -f deploy/compose.dev.yml logs --no-color --no-log-prefix app |
  grep -qFf .secrets/provider-key; then
  printf '%s\n' 'secret found in app logs' >&2
  exit 1
fi
```

随后在浏览器开发者工具中确认读取响应只含 `hasKey` 和更新时间，不含明文或密文。`apiKey`、`Authorization`、`body_text`、`payload_text`、`object_key` 以及供应商地址查询串都禁止出现在诊断 SELECT 或日志命令中。测试结束后安全删除合成凭据和响应文件。

## 回滚到 Phase 3 镜像

回滚只替换应用行为，不回退数据库或对象：

1. 先对 PostgreSQL 与 AIStor 对象卷做同一维护窗口的一致备份；记录当前 Phase 4 与已批准 Phase 3 的 app/worker 镜像 digest。
2. 在恢复出的临时数据库/对象副本上启动 Phase 3 镜像，确认旧镜像能容忍已存在的 Phase 4 migration 版本并通过 Phase 3 冒烟测试。
3. 在生产停止新 AI 运行，等待或显式取消活动运行，然后只部署已批准的 Phase 3 app 和 worker 镜像。
4. 保留 Phase 4 schema、AI 配置、线程、消息、运行、账本和对象。不得执行 down migration、删表、删行、`down --volumes` 或对象清理。
5. 验证 Phase 3 登录、教学、文件、人工答疑和通知；Phase 4 路由不可用属于预期。持续备份保留的数据。
6. 回滚故障解除后，重新部署 Phase 4 镜像；它会复用保留的 schema/data/objects。检查 readiness、聚合计数和一个合成 AI 问题。

若临时兼容性演练失败，不得直接部署旧镜像；恢复最近的 Phase 4 镜像并升级处理事故。

## 上线核对清单

- [ ] 主密钥来自受控 Secret，文件/挂载权限为 `0600`，版本与所有供应商密文版本一致。
- [ ] 供应商、双模型、双学科提示词、额度、连接测试和当前供应商已在教师控制台核对。
- [ ] 状态、错误类别、未知用量和资源均通过聚合命令检查。
- [ ] queued、`runner_lost`、timeout、附件和额度场景均只用授权 UUID 演练。
- [ ] `quota_estimation_anomaly` 通过调整容量并在 UI 确认后解除。
- [ ] 停用新运行不会删除配置、历史、账本或对象。
- [ ] 合成密钥没有出现在 API 响应或 app 日志。
- [ ] Phase 3 镜像已在 Phase 4 数据副本上完成兼容性回滚演练，未执行任何数据降级。
