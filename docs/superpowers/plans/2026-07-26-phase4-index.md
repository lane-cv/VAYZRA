# HappyLearn Phase 4 Execution Index

按以下顺序执行：

1. `docs/superpowers/plans/2026-07-26-phase4-ai-configuration-security.md`
   - AI 配置迁移、主密钥、供应商 URL/SSRF 策略、老师配置 API 和应用接线。
2. `docs/superpowers/plans/2026-07-26-phase4-ai-runtime-usage.md`
   - AI 附件处理产物、会话/运行/事件/额度账本、兼容协议、后台运行器和 SSE。
3. `docs/superpowers/plans/2026-07-26-phase4-unified-qa-console.md`
   - 统一答疑摘要、学生统一答疑中心、老师 AI 管理和用量统计。
4. `docs/superpowers/plans/2026-07-26-phase4-acceptance-operations.md`
   - 本地伪供应商、Phase 4 E2E、Docker/环境/运维文档、资源验证和最终质量门。

四份计划实现
`docs/superpowers/specs/2026-07-26-phase4-ai-qa-gateway-usage-design.md`。
不得在前三份计划的局部测试通过后宣称 Phase 4 完成；只有第四份计划的最终门通过后，才能开始 Phase 5 设计。

## 固定跨计划接口

- Go 领域包：`internal/aiqa`
- 学生 AI API 根：`/api/v1/student/ai`
- 老师 AI API 根：`/api/v1/admin/ai`
- 统一学生摘要：`GET /api/v1/student/question-summaries`
- 学生统一入口：`/student/questions`
- AI 详情：`/student/questions/ai/:threadId`
- 老师详情：`/student/questions/teacher/:threadId`
- 运行状态：`queued | streaming | succeeded | failed | cancelled`
- 协议模式：`chat_completions | responses`
- 学科：`math | physics`
- 模态：`text | vision`
- 费用单位：整数 micro-USD
- 额度周期时区：`Asia/Shanghai`

## 规格覆盖

| 已确认要求 | 负责计划 |
|---|---|
| 多配置、单主供应商、两种兼容协议 | 配置安全；运行与用量 |
| AES-256-GCM、密钥不回显 | 配置安全 |
| HTTPS、SSRF、DNS/重定向防护 | 配置安全；运行与用量 |
| 文字/图片/PDF/Word 与文本/视觉路由 | 运行与用量 |
| 持久化运行、SSE 断线恢复、不重复调用 | 运行与用量 |
| 请求/Token 每日月度额度、失败释放、费用统计 | 运行与用量 |
| AI 与老师答疑独立写模型 | 运行与用量 |
| 学生统一答疑中心 | 统一控制台 |
| 老师 AI 管理与用量页 | 统一控制台 |
| 手机、无障碍、安全 Markdown/KaTeX | 统一控制台 |
| 两协议伪供应商与端到端故障矩阵 | 验收与运维 |
| 2 核 4 GB、默认全站 2 并发/学生 1 并发 | 验收与运维 |

## Phase 4 完成门

1. 每个计划的 RED/GREEN 证据和提交均完成。
2. 运行第四份计划的完整验证矩阵。
3. 对最终 diff 做规格、安全、隐私、并发记账、运维和测试充分性复核。
4. 修复全部 Critical 和 Important 问题并重跑完整门。
5. 仓库保持干净。
