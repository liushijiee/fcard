# Nile 测试收款联调门槛

当前本机 Nile 测试收款模块已受控启用并追平，等待用户测试币转账验收；主网、余额及卡额度联动仍未开放。完整证据与剩余风险见 `Epusdt-plan.md` 第22节。下面的2026-10-13及早期2026-10-14记录保留当时状态。

2026-10-14 启用结果：`DEPOSIT_RUNTIME_ENABLED`、`DEPOSIT_CREATE_ENABLED`、`DEPOSIT_ASSET_REVIEWED`均为true；单笔申请上限 `DEPOSIT_MAX_ORDER_AMOUNT=1`，页面默认0.1。从原始固化起点 `71804184` / `000000000447a518addd01b5d20b41355a85f8301d6423fb195f44e54e8b22c3` 连续核验、保存超过4500个区块并追平；没有跳块或更换scanner。正式进程位于本机8081，日志/PID在 `.cache/nile-server/server-8081.*`。本机/公网 `/fcard/health/deposits` 的 available/ready/enabled 均为true，回调空JSON被HTTP400拒绝。尚未创建付款订单或确认入金；本轮只验证传输及拒绝规则，实际付款通知、确认和无回调补偿等待用户转账。

应用库原已为非dirty schema10，不需要再次迁移。启用前将应用 PostgreSQL、网关 SQLite 主库及runtime库备份到忽略目录 `.cache/nile-start-backup-*`。网关只启用清单中的 Nile USDT 测试合约及公共钱包，凭证保持不变；HTTPS及本地接口分别通过 `scripts/check-deposit-https.py`、`scripts/check-fcard-nile.py` 验证。用户可进入 `http://127.0.0.1:5173/portal/deposits`，先创建小额订单，等到WAITING后才按 actual_amount 及订单钱包付款；付款钱包必须选Nile与指定测试合约。

2026-10-13：已收到公共钱包，保存在 Git 忽略的根目录 `.env.deposits`；指定合约 `TXYZopYRdj2D9XRtbG411XZZ3kM5VkAeBf` 已在核对 genesis 的实际 Nile RPC 上读取到 bytecode，并只读模拟确认 decimals=6，详见计划第21节。runtime/create/asset_reviewed仍关闭，尚缺 HTTPS 来源和完整部署/连续扫描审核，先不要转账。

2026-10-14：已配置用户提供的 ngrok 域名，通过本机 `8999` 上的 Caddy 分发。Fcard 回调为 `https://flagship-antiquely-patriot.ngrok-free.dev/api/v1/deposits/notify`，Epusdt checkout origin 和 `app_uri` 为该域名根来源，内部创建 API 仍使用 `http://127.0.0.1:19800`。现有 Nile 商户凭证已在本机填入忽略的 `.env.deposits`，未输出或提交。runtime/create/asset_reviewed仍为false，扫描起点仍未填写。

公网只读检查通过：`/fcard/health/live` 和 `/fcard/health/ready` 返回JSON 200；根路径及 `/epusdt` 前缀下的公共配置均返回JSON 200；测试占位链接从 `/pay/checkout-counter/fcard_https_probe` 返回301到 `/cashier/fcard_https_probe`，后者返回HTML 200。占位链接未创建订单，不是实际可支付交易。管理接口和通道切换路径在根路径及 `/epusdt` 前缀均返回404。未启用运行模块，因此回调业务处理尚未挂载，未验证真实通知落库、付款或连续扫描。

代理模板在 `deploy/https/Caddyfile`，已备份并应用到 `D:\caddy-proxy\Caddyfile` 后热重载；Epusdt gateway 已重启读取 `app_uri`。`/fcard/*` 剥离前缀转发 Gin，`/epusdt/*` 仅转发公开付款相关路径。Epusdt 页面使用根路径 `/cashier`、`/pay`、`/assets`，必须保留这些公开路由；不要把 checkout origin 配成 `/epusdt`。未公开网关后台、创建或安装接口。

后续域名变更可运行 `powershell -NoProfile -File scripts/configure-deposit-https.ps1 -PublicOrigin https://你的域名`，然后重启网关；该脚本不改收款开关。只读验证命令为 `python scripts/check-deposit-https.py https://你的域名`。ngrok停止或域名变更会影响旧订单快照和通知地址，应在新测试单创建前完成核对。

## 用户需提供

- 专用 Nile 钱包的 **T... 公共收款地址**；不要提供私钥、助记词或钱包密码。
- 若已有测试代币，提供公共合约地址；否则后续先只读审核/选择六位小数测试资产，不猜测主网地址。
- 测试付款的钱包需测试 TRX 和相应测试代币。由用户在自己的钱包转账，Fcard/Epusdt 不导入私钥。
- 公开 HTTPS 回调域名和网关 checkout 根来源；商户凭证在本机受控文件中配置，不在聊天中共享。

## 严格执行顺序

1. 审核 Nile genesis、真实合约 bytecode/接口/decimals、钱包用途、HTTPS 地址及扫描起点。起点必须在所有本次付款之前；不得因未知 native 类型跳块或重置 cursor。
2. 根据 `.env.deposits.example` 建立忽略的 `.env.deposits`。`DEPOSIT_RUNTIME_ENABLED=true` 仅用于明确配置的测试环境；**DEPOSIT_CREATE_ENABLED=false** 保持关闭。`DEPOSIT_ASSET_REVIEWED=true` 是人工审核声明，不替代链上合约检查。不要把未审核的合约当成 USDT。
3. 离线检查 `go run ./cmd/deposit-check`，不联网、不写库；配置完整后才执行显式只读 `go run ./cmd/deposit-check -rpc`。后者只检查 identity/head/checkpoint，不验证合约 bytecode、decimals、钱包控制权或网关。
4. 用真实连续 Nile 块验收 scanner 支持政策。遇未知类型立即停止，根据实际 receipt 增补最小政策和回归测试，不禁用核验、不跳块。
5. 当前 `deploy/epusdt/bootstrap` 仍禁止任何启用的代币/钱包/收款链。完成真实资产/钱包审核后再实现并验收受控网关启用与 HTTPS app_uri；不要直接在后台打开钱包或绕过 bootstrap。此阶段尚未执行。
6. 测试专用 Fcard 库先备份、显式升级到非 dirty schema10；目前未升级现有应用库。启动 runtime、关闭 create，等待扫描至少一个新块并追平，验证回调持久化、任务消费、冻结/到期/补偿、退出排空与故障告警。
7. 审核单笔小额上限后再开启创建，使用 `/portal/deposits`。付款必须使用返回的 **actual_amount** 和审核的 T 地址；不要使用申请金额替代实际金额。UNKNOWN/待审/到期不再付款，不新建替代单规避冻结。
8. 验收：正常确认、重复回调、无回调补偿、窗口内付款但延迟取证、超时/响应丢失后原报价恢复、错误金额/晚付/重复付款保持待审、容器强杀及数据库提交响应丢失。确认只有 receipt，不增加余额或卡额度，不调用 Airwallex。

## 扫描故障与登录可用性（2026-10-15）

扫描遇到未审核的交易类型时，充值 runtime 停止，准入关闭，`/health/deposits` 的 `ready` 和 `enabled` 返回 false；HTTP 服务继续提供登录、状态查询和人工核查接口。修复前的 server 会随充值 runtime 一起退出，导致 Vite 代理出现 `ECONNREFUSED 127.0.0.1:8081`；更新代码后重新运行 `go run ./cmd/server`。

区块 `71809560` 的未知类型已核对为 `UpdateBrokerageContract`，交易 `4840bbfb5a498d54caf5a6d023c9f67b7b76d804c7560cf1c1270b342cbd1f3a` 执行成功、无 TVM 日志，不涉及当前配置的收款地址。该类型只修改超级代表佣金，按已有 native success 策略核验：必须有明确的区块 SUCCESS，receipt 不得失败或含日志，不产生 TRC20 入金证据。公开区块与全部四笔回执保存在 `internal/deposit/testdata/nile-block-71809560.json`；离线回归验证完整区块可推进、异常回执不推进。

更新后重启后端，从原 checkpoint 连续追平，再检查 `/health/deposits` 的 `ready`、`enabled`。仅代码核验通过不代表实际扫描已追平；不得通过跳块、重置 checkpoint 或忽略其他未知交易恢复收款。

## 人工恢复

管理员先只读核对 Epusdt 原始交易与受控导出证据，核对商户原单号、trade_id、金额/尾差、钱包、有效期限与公开 checkout。通过 `/deposits` 使用原 claim，填写审核原因和可追溯证据引用；金额使用字符串。恢复事务保存原报价与独立 ADMIN 审计，不发上游 POST，不确认资金。

“查不到交易”不能证明未创建，不允许清 claim/换单号/重 POST。核验任务只有经审核的 MANUAL_REVIEW 可以重排；CREATE 永不能重排。未归属事件列表只供人工核查，不提供强制资金归属按钮。

## 禁止事项与遗留风险

禁止主网/真实资金、私钥托管、提现/归集、余额或卡额度联动。禁止任意用户 hash/proof/报价即确认资金的入口。当前单一 RPC 信任边界、未知 native 类型、极晚旧付款与地址金额复用、真实故障/容量/归档与告警投递均须独立验收；合成测试通过不是生产安全认证。
