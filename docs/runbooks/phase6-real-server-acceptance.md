# Phase 6 真实服务器验收

真实 Ubuntu 主机的第一步只能是只读盘点：发行版/内核、CPU/内存/磁盘、时钟、Docker/Compose、systemd、现有用户与组、监听端口、卷、挂载、DNS、证书、服务、timer、日志轮转、备份目录和防火墙规则。随后运行 `prod-preflight.sh --mode server`，不得先变更主机。

以下每一项都需要独立、明确的用户批准，前一项批准不蕴含后一项：

1. 安装或升级系统包、Docker、服务用户与 systemd 单元；
2. 修改 DNS；
3. 修改 firewall（防火墙）；
4. 申请或启用公网 TLS；
5. 重启主机；
6. 执行生产恢复切换；
7. 批准发布候选 `v1.0.0-rc.1`；
8. 创建最终 `v1.0.0` tag。

获得对应批准后，按顺序证明：仅 80/443 公网可达；数据库、Redis、AIStor、应用和内部监听私有；首次生产加密 backup（备份）成功且证据可验证；将两份不同且 24 小时内的恢复点之一 restore（恢复）到全新空卷，保持原卷分离，验证数据库/对象清单、授权、CSRF 和会话全部吊销；记录端到端 RTO，必须不超过四小时。

在 desktop（桌面）Chromium 和 mobile（移动）Pixel 7 约束下跑完整业务回归。逐个重启应用、Worker、Caddy、PostgreSQL、Redis、AIStor，再经单独批准 reboot（重启）Ubuntu；确认依赖顺序、持久数据、当前 manifest、normal 模式和私有边界。观察（observation）CPU、工作集、延迟桶、重启次数与健康至少 30 分钟，确认 2 CPU/4 GiB 上限、1 GiB 稳态余量、timer 实际触发、alert（告警）投递和日志无查询/PII。

在维护窗口演练成功发布、兼容 schema 的自动 rollback（回滚）、信号中断续跑和上一版本 readiness；确认预发布备份、503 静态维护、无已接受写丢失、无会话复活、无数据库自动恢复。发布后继续观察并记录安全摘要哈希。恢复切换、候选与最终 tag 必须保持三次独立批准。

Phase 6 repository production-ready; real-server acceptance pending.

在真实主机、DNS、防火墙、公网证书、服务安装、重启、生产恢复切换和发布候选均未完成前，禁止宣称“Phase 6 完成”，禁止创建 `v1.0.0`。
