# Fcard 接入 Epusdt 收 U 实施计划

核对日期：2026-10-13（Asia/Shanghai）。依据实际代码与官方固定标签源码；本文不是功能实现、生产安全认证或业务合规结论。

当前实施进度以第22节为准：SchemaVersion=10；实际 Nile 连续扫描、测试资产/钱包允许列表、HTTPS、进程启动与创建准入已受控启用，本机8081的收款模块已就绪。尚未创建付款订单或验收用户实际测试币转账；没有主网、余额或卡额度联动。全新环境仍默认关闭，不能复制本机启用声明替代资产/起点审核。历史章节中6/7/8/9是当时迁移里程碑，不是当前部署版本。

## 1. 结论与范围

采用 **Fcard 业务后端 + 独立 Epusdt 服务 + Fcard 链上核验/补偿 worker**，不把 Echo/GORM 全局状态、后台和表并入 Gin/pgx/sqlc。

第一轮执行明确为 **Nile 测试网 / 测试 USDT（TRC20）**，不连接主网收款、不使用真实资金。使用独立测试钱包，只导入观察地址，不复用主网私钥。“充值”先指归属某用户的已核验入金记录，**不是可消费余额**。提现、归集、私钥托管、USDT 换 USD、Airwallex 充值/额度联动、消费扣账均不在本期。

部署和业务接入分开执行：Docker 只负责运行 Epusdt，Fcard 仍须实现订单、签名客户端、回调 inbox、链上核验、补偿与页面。先固定原版验证契约；只有明确需要时才最小化修改上游，不整体合并源码。执行记录见第 11 节。

关键发现：

- 原 `assimon/epusdt` 现在重定向到 `GMWalletApp/epusdt`，项目称 GM Pay，不能照搬旧教程。
- 核对时 GitHub `/releases/latest` 实际跳转 **v2.0.0**，页面标记 Latest；发布于 **2026-07-23T06:04:47Z**（北京时间 2026-07-23 14:04:47）。标签提交 **`619cecb11d6bbd1dcdd1b698ee08b37fc722d3e3`**。它是本次查到的最新正式发布，不等于已证明适合生产资金。
- 创建路由是 `/payments/gmpay/v1/order/create-transaction`，签名 **HMAC-SHA256**，不是旧 `/api/v1/...` + MD5。标签内 `wiki/LEGACY_API_MIGRATION.md` 仍写 `/payments/epusdt/...`，与实际路由及 `wiki/API.md` 冲突，以固定源码为准。
- PostgreSQL 可作网关主库，但预约锁仍在独立 SQLite，多副本安全不能由主库类型推导。
- TRON 自动扫描从当前块启动、进度只在内存；顶层 transfer 解析未覆盖全部 receipt 事件，自动路径未见等待固化/配置确认数。**网关成功回调不能直接作为真实入账依据。**
- Fcard 当前无入金、余额或支付账本，Airwallex 是固定额度沙盒发卡，不代表客户资金余额。

## 2. 检索边界

阅读 Fcard `cmd/`、`internal/`、全部 5 组迁移、sqlc、Vue 路由/页面/API/session、Go/Vitest/Playwright 测试与脚本，不用 Spec 替代实现。官方 v2.0.0 源码只下载至临时目录，审阅接口、模型、钱包、监听、匹配、回调、测试和部署，不复制进本项目。

`git ls-remote --tags` 核对标签；master HEAD 为 `aed4a970a28d734c8a35499496604b868a24ef7f`，与 release 不同，本文未逐项审计 master 差异。GitHub REST releases API 限流，改用官方 latest HTML 重定向、Latest 标记、发布时刻和 Git 标签交叉确认。旧 v0.0.8 拉取遇到 TLS 错误，未完成其逐行比较。

**初次调研未运行 Go、前端或 Epusdt 测试，测试结论只是源码覆盖范围，不报告通过率。** 初次调研未部署、开卡或转账。后续执行结果另记第 11 节，不与调研结论混淆。官方固定链接见第 10 节；E01–E10 对应其中证据分组。

## 3. Fcard 实际现状与集成位置

| 领域 | 本地实际证据 | 实施影响 |
| --- | --- | --- |
| Go/Gin | `go.mod:3`：Go1.26.0、Gin1.12.0、pgx/v5 5.11.0、migrate4.20.1；`cmd/server/main.go:39` 装配 pool/服务/worker | 新增独立 `internal/epusdt` 和 `internal/deposit`，依赖注入，不换框架 |
| 路由 | `internal/http/router.go:48` 的 New：admin/user 两域、health live/ready，卡路由要session/改密/cards enabled | 收款放 user 域，不挂 requireCards；回调独立服务签名，不要求用户Bearer |
| HTTP | requestContext统一10秒、request_id、no-store；TrustedProxies=nil，main配置读写超时 | 链上核验放worker；回调快速落inbox；不能任意信任转发IP头 |
| 用户认证 | `internal/userauth/service.go:96` Login：bcrypt、32字节随机Bearer、SHA256摘要入库；`internal/storage/queries/user_auth.sql:31` 校验ACTIVE/未撤销/过期/auth_version；`internal/http/user_auth.go:13`、`:31` | 复用身份，禁止请求指定user_id；创建事务重读/锁用户凭证，参考 `internal/card/service.go:138` |
| 管理认证 | `internal/auth/service.go`、router authenticate，管理/用户会话隔离 | 网关后台是另一套管理身份，不向浏览器给商户secret/后台token |
| PostgreSQL | `internal/storage/storage.go:10` 当前 SchemaVersion=10（最初调研为5）、pool10、UTC、statement_timeout10000，Ready精确检查版本和dirty；第14至18节记录链证据、版本化扫描、业务订单、租约及交易定位索引 | 新迁移同步升版本；worker不抢满连接；确认事实/审计同事务 |
| sqlc | `sqlc.yaml:1`：migrations、queries→internal/storage/db、pgx/v5；`scripts/check-generated.ps1` 固定sqlc1.31.1 | 写SQL后生成，不手改.sql.go；USDT优先整数单位，numeric须验证pgtype映射 |
| 迁移 | `internal/storage/migrate.go` iofs+pgx stdlib+postgres driver，`cmd/migrate`显式执行，000001–000005 | 新增第6组，不改历史、不共享网关GORM表；有资金事实后down不可静默删除 |
| Airwallex | `internal/airwallex/client.go:16` 固定sandbox；config只APP_ENV=sandbox、版本2026-08-21、COMMERCIAL、额度1–10000美分 | 此处记录代码不是验证最新Airwallex API；收U不能自动增USD卡额度 |
| 发卡任务 | 000004公司身份、000005 holds_card_slot；`internal/card/service.go:175` 输入hash/原请求/幂等，worker保留UNKNOWN，SQL Claim用SKIP LOCKED | 借鉴任务/未知结果，不将入金塞进provider_operations |
| 现存限制 | `internal/card/worker.go` 有暂停holder.Status!=READY检查注释，实际仍核对ID/INDIVIDUAL/legal name | 沙盒开卡不能认定生产KYC通过，本次不改无关代码 |
| Vue | package.json：Vue3.5.43/Router4.6.4/Vite8.3.4/Vitest5.0.3；`frontend/src/router.ts:1` 无充值页 | 新增 `/portal/deposits`，不直连网关管理API |
| API/session | `frontend/src/lib/api.ts:1` 解包Fcard data/error/request_id，支持user realm/Idempotency-Key/15秒；user-session独立sessionStorage | 服务端适配网关status_code/message/data；金额string、回跳查本地状态 |
| 资金缺口 | `frontend/src/views/PortalHomeView.vue:1` 明示入金/消费未接入；迁移无余额账本 | 新建入金事实模型，不是更新不存在的balance字段 |

### 3.1 测试已读范围

- `internal/http/router_test.go`：公共/受保护路由、坏JSON、分页；user_auth_integration_test：生命周期、限流隔离、事务/事件回滚、并发改密、迁移保留用户。
- cards_integration_test：公司身份、归属、幂等、并发、UNKNOWN、响应不匹配、核查/回滚；card_slots_integration_test：释放名额、历史/迁移、并发替换。
- `internal/airwallex/client_test.go`：httptest假供应商、token缓存/并发、错误不泄密/不自动重发。
- `internal/http/integration_test.go:30`：缺TEST_DATABASE_URL会skip，随机临时PG库、要求CREATEDB、结束删除；有skip的go test成功不证明DB集成通过。
- 前端src/lib的api/session/user-session/cards测试：身份隔离、过期、旧响应竞态、幂等；e2e/admin/portal/cards，cards用API拦截，不是实际开卡联调。
- 脚本test-integration.ps1/test-frontend-integration.ps1/check-generated.ps1；mock和实际后端集成应分开报告。

## 4. Epusdt v2.0.0 实际行为

### 4.1 目录与部署（E02/E09/E10）

`src/main.go`、`command/`、`bootstrap/` 为入口；Go1.25.0、Echo v4、GORM。`route/controller` 为API，`model/service` 为业务/核验，`model/data` 为数据访问，`model/mdb` 为模型；`task` 为链监听/RPC健康任务，`mq` 为进程内调度器加数据库状态，非外部消息代理；`src/www` 附静态后台/收银台，`wiki`/`sdk/python` 为文档示例。

主库PostgreSQL/MySQL/SQLite可选；RuntimeDB固定SQLite保存TransactionLock/EvmScanCursor，启动AutoMigrate。默认db_type=sqlite、runtime_sqlite_filename=epusdt-runtime.db、order_expiration_time=10分钟、order_notice_max_retry=0、callback_retry_base_seconds=5、queue_concurrency=10。首轮网关独立 SQLite 主库/运行时库用于部署和契约验证，不触碰现有 PostgreSQL；Fcard 入金业务仍使用 PostgreSQL/pgx/sqlc。网关 PostgreSQL 兼容性和迁移另验，不能将本轮 SQLite 结果视为已通过。

根compose使用 `gmwallet/epusdt:latest` 且仅挂 `./env:/app/.env`，**不可原样当生产部署**：固定构建SHA/镜像digest，持久化实际runtime_root_path/SQLite/日志和主库；校验release SHA256SUMS/provenance，演练恢复。Fcard/网关使用独立库/账号，网关先单实例。

### 4.2 API和签名（E03/E04/E08）

| API | 用途/限制 |
| --- | --- |
| POST `/payments/gmpay/v1/order/create-transaction` | 服务端签名JSON/form创建；pid/order_id/currency/amount/token/network/notify_url/redirect_url/name/signature，固定usdt/tron，不用Epay |
| GET `/payments/gmpay/v1/config` | 公开能力配置，不据此自动开放用户链/币 |
| GET `/pay/checkout-counter/:trade_id` | 跳转 `/cashier/:trade_id`，使用前验证固定链约束 |
| GET `/pay/checkout-counter-resp/:trade_id` | 无签名收银台数据，不是强认证商户查单 |
| GET `/pay/check-status/:trade_id` | 无签名状态提示；HTTP200不代表到账，读data.status，不单独入账 |
| POST `/pay/submit-tx-hash/:trade_id` | 路由实际存在，人工hash走链核验；用户提交hash不是付款证明 |
| POST `/pay/switch-network` | 无签名，可创建子单/补全占位单；本期公开入口封禁 |
| `/admin/api/v1/wallets`、`/:id`、`/:id/status`、`/batch-import` | 后台JWT地址增删/启停/导入，仅管理网络访问 |
| `/admin/api/v1/api-keys` 及相关接口 | PID/secret/IP白名单，只限运营管理 |

签名：排除signature、null/空字符串，非空key=value按ASCII排序以&连接，secret做HMAC-SHA256，小写64位hex；pid和非空payment_type参与。**不是原始JSON HMAC，也不是URL编码后的HMAC**。数字按FormatFloat('f',-1,64)规范化，例如100.00→100。要验证固定向量、非整数金额、JSON/form，不用普通JSON序列化代替。

order_id最大32字符，不能直接传36字符带连字符UUID，建议UUID的32位hex并保留本地UUID。HTTP请求amount约束gt:0.01，服务层最小值比较存在边界差异，以HTTP实测为准。

创建响应有trade_id/order_id/amount/currency/actual_amount/receive_address/token/status/expiration_time/payment_url，**没有network**，须保存受控请求快照。状态1等待/2成功/3过期/4等待选通道；token/network同时省略生成4，Fcard必须同时指定并拒绝占位响应。

GMPay POST JSON成功回调：pid、trade_id、order_id、amount、actual_amount、receive_address、token、block_transaction_id、signature、status=2。**没有network/currency/event_index/确认数/时间戳/nonce**；block_transaction_id是tx hash，不是高度。按同样规范化验签；ACK必须HTTP200+纯文本ok或success（忽略大小写并trim）。默认Fcard JSON envelope不是正确ACK。

EPay另用MD5/GET回调，OkPay为第三方托管提供方，本期均不接。

### 4.3 钱包/私钥（E06）

WalletAddress只有network/address/status/remark/source等，唯一network+address，支持启停/批量地址导入/软删除。AdminBatchImportRequest明确observation-only，不支持私钥导入；所审收款路径无HD用户子地址、签名出金/自动归集实现。

因此是**共用地址池+金额预约**，不是每用户生成钱包。外部生成并安全保管私钥，不把助记词/keystore导入网关或Fcard。非托管软件不等于平台业务非托管：平台若控制收款私钥仍有客户资金责任。地址变更需要格式/持有证明、双人复核、白名单和审计；停用不得抹掉历史或停止核验已发出付款指令；用户不可指定收款地址。

### 4.4 匹配与金额（E05/E09/E10）

1. 查order_id，重复返回OrderAlreadyExists，不返回原结果；订单号主库全局唯一，不按PID分区。
2. 查启用地址，预约network/address/token/amount；冲突累加10^-amount_precision，最多100次尝试。
3. **精度默认2，允许2–6**，并非固定0.0001。虽有decimal，订单amount/actual_amount仍float64；SQLite锁保存scaled int64/文本/预约精度。
4. 主库订单与RuntimeDB预约分两库，不是同一事务，崩溃恢复可能不一致。
5. TRON按链/地址/币/金额找活跃锁，再核对订单网络/币、拒绝早于建单的区块时间。
6. OrderProcessing使用进程内mutex、事务查tx是否使用和条件更新；orders.block_transaction_id普通索引，**不是unique**，不能声称跨实例exactly-once。

必须展示actual_amount而非按amount随意付款，订单可能要求20.00/20.01。错链/错币/少付/多付/分笔/晚付不按浮点容差就近匹配，不按发送钱包认定用户。拟本地存USDT六位整数最小单位/API字符串；可设网关六位精度，但先验证交易所提币精度和手续费净额，不能收款中途随意改配置。

直接收U：GetRateForCoin在coin==base返回1，currency仅required，故**currency=USDT/token=USDT从源码看可绕过法币换算，但须API实测**。不要用USD→USDT默认1宣称真实兑换或USDT永远等于USD；法币计价另定义汇率/报价期/差额。

### 4.5 监听和安全缺口（E07/E09）

TRON已核实：

- GetNowBlock/GetBlockByNum用 `/wallet/getnowblock`、`/wallet/getblockbynum`，非walletsolidity固化块接口；轮询3秒。
- Scanner.Init将lastBlock设当前最新块，仅结构体内存保存；未看到TRON持久化游标，重启跳过停机区间。
- 运行中按lastBlock+1顺序扫，历史块获取失败停住，RPC有切换/统计，但不等于重启恢复。
- 只看RawData.Contract[0]的TriggerSmartContract、配置合约和a9059cbb transfer；不全面扫receipt Transfer日志，可能漏transferFrom/内部或批量付款。
- 默认status=SUCCESS，仅有Ret且明确失败才改；缺字段默认成功必须独立receipt核验兜底。
- 自动路径未见使用chains.min_confirmations等固化/确认数或处理重组，表有配置不代表监听执行。
- TryProcessTronTRC20Transfer依赖活跃锁，不匹配仅日志，非持久化未匹配资金池；业务处理失败后processBlock仍可返回、poll推进，需补偿。
- 人工manual_payment_verify另有receipt/log、确认数、收款/金额/时间核验，不能将人工路径当成自动路径具备。

EVM另有WS及RuntimeDB cursor backfill、初始回看2048块；TON/Aptos/Solana任务不同，本文未完成每链最终性审计，一期不开多链。

**上线前必需**：Fcard独立RPC核验TRON receipt Transfer事件/固化事实，持久化游标补扫观察地址、保存未匹配事件、失败不跳块。回调只提供候选tx，不是付款授权。无需一期重写整个网关，但必须补齐资金证明链路。

### 4.6 回调恢复（E08/E10）

mq按数据库paid/未ACK状态调度，进程内inflight/limiter，保存callback_num/confirm；指数退避最大5分钟。默认retry=0是首次失败无额外重试，**不是无限重试**；设可测重试预算及耗尽告警/重发/对账。

回调用订单API Key当前secret，禁用/删除会失败；轮换需旧验证窗口及未完成单清单。创建notify_url用ValidatePublicHTTPURL阻内网，发送Resty通用client；不能由创建校验推出完整防DNS rebinding/重定向SSRF，需固定域名/出口ACL。localhost联调可能被拒，使用受控HTTPS公网回调，不随意关校验。无nonce/时间戳，防重放依赖业务幂等。

## 5. Fcard拟实施设计（不是现有能力）

### 5.1 数据流

```text
认证用户 -> Fcard本地订单+创建任务 -> worker固定order_id调用Epusdt
       -> 保存地址/actual_amount/有效期快照 -> Vue付款指令
链上转账 -> Epusdt回调 -> Fcard验签/持久化inbox -> 纯文本ACK
                   -> 独立receipt/log/固化核验 -> 唯一确认事实/审计
持久化补偿扫描 -----> 入金事件/未匹配池 -> 唯一匹配或人工核查
```

网关负责报价/预约/候选通知，Fcard负责用户归属/确认事实，链上receipt为资金证据，私钥在两者之外。网关单实例；Fcard任务用pgx/sqlc+SKIP LOCKED，网络在事务外，任务有租约/恢复。

### 5.2 模块与迁移

拟模块 `internal/epusdt/{client,sign}.go`、`internal/deposit/{service,worker,verifier}.go`、`internal/http/deposits.go`、`internal/storage/queries/deposit.sql`。`000006_chain_evidence.up.sql/down.sql` 已用于最小链存储（第14节），`000007_chain_scan_v2.up.sql/down.sql` 用于版本化扫描证据（第15节），`000008_deposits.up.sql/down.sql` 已用于订单/inbox/任务/receipt（第16节），`000009_deposit_job_leases.up.sql/down.sql` 已用于核验任务租约（第17节），`000010_chain_transaction_index.up.sql/down.sql` 已用于完整交易定位（第18节）；后续 schema 扩展从未占用的新编号继续，同步更新 SchemaVersion 和 sqlc 生成，不复用 card 操作表。

| 表 | 拟字段/约束 |
| --- | --- |
| deposit_orders | UUID/user_id FK、provider/merchant、全局unique gateway_order_id(32hex)、gateway_trade_id、幂等键/input_hash、chain_environment/genesis_id/链/币/合约/decimals、requested_units/quoted_units、address、expires_at、状态/快照；unique(user_id,idempotency_key)，网关号按provider+merchant唯一 |
| deposit_jobs | 创建/核验/对账、租约、attempts/next_attempt_at、受控错误；固定输入/号，超时不另起新单 |
| deposit_callback_inbox | PID/trade/order、body摘要与最小payload、密钥版本、时间/处理状态；重复body去重、大小/保留期限制 |
| chain_transfers | chain_environment/genesis_id/network/contract/tx_hash/event_index/from/to/raw_units(NUMERIC(78,0))/安全范围内的units/block高度/hash/time/固化状态/证据/匹配；unique(chain_environment,genesis_id,network,tx_hash,event_index)，不能仅tx_hash唯一，一tx可能多事件 |
| deposit_receipts | deposit_order_id unique、chain_transfer_id unique、confirmed_units、时间/证据引用；确认只追加，纠错用反向记录不删历史 |
| chain_scan_cursors | chain_environment/genesis_id/network/scanner/已处理高度/hash/最后成功时间；事件保存与游标推进同事务、重放去重、失败不跳块 |
| deposit_events | SYSTEM/ADMIN/USER actor、动作/订单/request_id/脱敏事实，与关键转换同事务；不要假扮用户写自动到账事件 |

业务可确认金额用BIGINT最小单位、非负/上限/溢出约束；原始链上uint256须以NUMERIC(78,0)或受约束十进制文本完整保存，超出业务范围也保留未匹配事实，不能截断或丢弃。UTC，不存凭证/助记词。本期不建消费余额；未来另做分资产复式账本/冻结/兑换/消费，不能SUM receipt直接接卡额度。

### 5.3 状态与幂等

拟状态 `CREATING -> WAITING -> VERIFYING -> CONFIRMED`，未知创建 `CREATE_UNKNOWN`，报价超时 `EXPIRED`，异常 `REVIEW_REQUIRED`；receipt独立唯一事实。

- 同用户同键同输入返回原单，同键异输入冲突；任务先落库再请求。
- 超时/5xx/坏JSON不代表没创建。保留原gateway_order_id；重复报错不是查单。需最小fork提供受认证按order_id商户查单，或受控后台核查；恢复能力完成前不自动新建。
- 回调可早于创建响应，按预存order_id收inbox，待trade_id绑定，不丢弃。
- EXPIRED不代表钱包没收钱；晚付/补扫进待审，金额复用不自动归下一单。保留历史预约窗口/隔离期与重叠检测，歧义不自动确认。
- 停用/改密阻止新建/用户访问，但已入金仍保存冻结/待审事实，不依赖用户session处理资金。
- HMAC通过不直接CONFIRMED；独立核验后一个pgx事务锁订单、写unique event/receipt、更新状态和事件，崩溃/并发只一次确认。

### 5.4 回调与链上证明

拟新增 `POST /api/v1/payments/epusdt/notify`，无用户Bearer，独立签名/来源策略/限流/body限制。本轮只有未注册的 handler 契约基础，未开放该路由。

1. 限体积（如16KiB，按fixture验证），拒绝重复JSON key/非法数字/类型；保留验签原值，不先转整数再拼签名。
2. 只接受配置PID、本地keyring，HMAC常量时间比较，禁止MD5回退。
3. 按本地order_id查归属，核对trade_id/token/address/amount/quoted actual_amount；缺network/currency用快照和实际合约核验，不让浏览器补。
4. 重复合法回调ACK；合法但业务不匹配落异常inbox/告警，不生成receipt；DB不可写非200重试。
5. inbox事务成功即HTTP200纯文本ok（上游也支持success）。ACK代表可靠接收，不是资金已确认。
6. worker独立RPC查receipt，验证执行成功/TRON/允许USDT合约/Transfer事件/地址/精确units/event_index/时间/固化；未最终确认延后，多事件歧义待审。

补偿从可配历史高度扫本平台观察地址所有相关USDT事件，新地址补扫后启用；下载/receipt/落库失败分别重试，不推进失败游标。未匹配持久保存不猜用户；匹配须满足报价快照/窗口且唯一。对账比链上总入金、receipt、未匹配与网关成功单，不只统计网关状态。人工reconcile只重查链上证据，不直接改余额/confirmed=true。

### 5.5 API与Vue

- POST `/api/v1/user/deposits`：session+改密+独立flag，amount十进制字符串、Idempotency-Key；用户/链/币/地址/回调由服务端决定。
- GET `/api/v1/user/deposits`、`/:id`：按principal.user.ID过滤，跨用户not_found，完整金额/链/地址/有效期/核验状态，无secret。
- GET `/api/v1/admin/deposits`、`/:id`、POST `/:id/reconcile`：异常/证据查看和触发核验；钱包仍在受限网关后台管理，不做第二套写入口。
- 新增PortalDepositsView、types/API、导航，展示USDT·TRON/TRC20、actual_amount完整精度、复制/二维码/倒计时/历史、错链/净额/少付/晚付提示；二维码URI兼容先测，不假定支持带金额。
- 优先Fcard付款指令页；用payment_url须校验HTTPS固定host并封switch-network。redirect只提示等待查证，轮询Fcard带AbortSignal/退避，切账号不显示旧响应；超时保留原键，不重建新单。

## 6. 明确实施顺序与验收

| 阶段 | 按顺序工作 | 完成条件 |
| --- | --- | --- |
| 0 业务边界 | TRON/USDT、确认记录非余额、私钥保管人、限额/晚付规则、专业合规评估 | 范围/责任明确，非托管软件不代替业务判断 |
| 1 隔离网关 | 锁SHA/digest、独立 SQLite 主库/持久runtime、本机入口、启动前禁主网配置、Nile RPC、禁其他链币/OkPay/switch、retry预算 | 重建不丢数据；未确认测试合约/钱包时禁止收款；不改现有 PostgreSQL |
| 2 契约spike | HMAC/数字/form、currency=USDT、32字符号、尾差/精度/ACK、创建超时/回调先到 | 脱敏fixture确定，查单或明确人工恢复，不重复建单 |
| 3 资金证明 | 独立receipt/log/固化、持久补扫/未匹配池，复现停机/内部调用/缺Ret/未固化 | 单凭签名不能confirm，重启不漏不重，失败不跳块；不完成不收真实资金 |
| 4 数据/客户端 | 第6迁移/版本/sqlc/config/client/jobs，先落库后网络 | DB幂等/归属/UNKNOWN/迁移通过，不破坏认证/发卡 |
| 5 回调闭环 | 验签/inbox/ACK/核验/receipt唯一/审计/过期/对账/人工核查 | 并发重放一次确认，停用资金不丢，耗尽仍恢复 |
| 6 Vue | 收款/列表/详情、复制二维码/倒计时/异常、管理证据 | 手机完整精度、刷新/切用户/超时不重复，Vitest/Playwright通过 |
| 7 发布准备 | 全回归、升级/恢复/故障演练、小额受控链验证、告警手册 | P0关闭、资金无差额；真实转账另需授权，本次不执行 |
| 8 限额试运行 | 用户白名单/单笔/总敞口限制，每日链/网关/Fcard对账 | 每笔可追踪、停建单有效，不接提现/兑换/卡额度 |

阶段 3/4 的真实依赖：先完成独立核验和 mock 故障契约；持久补扫必须依赖阶段 4 的最小链事件/游标数据层，可以提前落该受控子集，再完成重启/事务回滚验收。其余入金建单/回调/用户 UI 不因此提前开放，阶段 3 的资金证明和补扫否决条件仍须完成。

拟配置EPUSDT_BASE_URL/PID/SECRET或密钥管理引用/NOTIFY_URL、DEPOSIT_ENABLED/NETWORK/TOKEN_CONTRACT、独立TRON RPC。flag只停新单，回调/核验/补扫继续。现APP_ENV仅sandbox，一期实验保留；真实资金模式另设受控配置审批，不能改名就上线。

### 6.1 测试网约束

- 链环境明确为 `nile`，RPC 限定 Nile 官方端点；业务订单、事件和去重键须带链环境，不能将主网/测试网都只记为 tron。
- 专用付款钱包 A、观察收款地址 B，私钥只在外部钱包，不提交仓库/日志/网关。
- 测试 TRX 用于资源费用；测试 USDT 合约须在 Nile 查询代码/decimals 后启用，不猜地址或沿用主网合约。
- 没有合约/收款地址时，只做无资金部署与签名测试，不伪造“到账成功”。
- 网关只绑定 127.0.0.1，代理封 switch-network、EPay、OkPay、安装接口；Fcard核验未完成前不开公网入口。
- notify_url拒绝localhost/私网；回调联调需要用户控制的HTTPS公网域名或受控隧道，不删除上游SSRF校验。
- 配置和目标确认后才测试币转账；先mock，Nile通过不等于生产验收。

## 7. 兼容性与安全风险

| 级别 | 风险/证据 | 应对 |
| --- | --- | --- |
| P0 | TRON内存游标/启动跳当前块/业务失败仍推进（E07） | 持久补扫/原子游标，停机和失败注入，不只等回调 |
| P0 | 顶层transfer/缺Ret默认成功/未等固化（E07） | 独立receipt事件和最终性核验，不支持转账进异常池 |
| P0 | 地址金额复用使晚付误归下一单（E05） | 精确报价/窗口/隔离期/历史重叠检测，歧义待审 |
| P0 | secret泄露伪造回调、后台泄露替换地址（E04/E06） | 轮换/管理面隔离/双人地址审批白名单/独立证据；无私钥也有欺诈风险 |
| P0 | Fcard无账本，卡额度沙盒USD | receipt非余额，不做充值即增加额度 |
| P1 | v2路由/HMAC/PID与旧教程及迁移文档冲突（E03/E04） | 固定实际GMPay契约，不回退MD5 |
| P1 | order_id全局unique且<=32、重复报错非幂等结果（E03/E05） | 32hex+原键、UNKNOWN核查、补商户查单 |
| P1 | Echo/GORM/Go1.25 vs Gin/pgx/sqlc/Go1.26 | HTTP隔离、独立库用户，不共享schema/import全局服务 |
| P1 | SQLite+主库非原子、局部mutex、tx无unique（E05/E09） | 网关单实例/一致备份；Fcard事件/receipt数据库unique；扩容另修 |
| P1 | float64/精度2–6/README增量例子非默认（E04/E10） | 范围/规范化fixture，本地整数、前端string，不容差 |
| P1 | 回调缺链/币种/确认数，公开切换子单（E03/E08） | 固定链币、封switch、快照及合约核验 |
| P1 | retry默认0、当前secret签名、公开状态查单 | 重试预算/旧key窗口/inbox+对账，状态仅提示 |
| P1 | compose latest/runtime未挂载/启动AutoMigrate | 锁SHA/digest、持久卷、一致恢复/升级演练 |
| P1 | URL创建校验不等于完整SSRF防御 | 固定HTTPS域名/出口ACL、查DNS/重定向行为 |
| P2 | sessionStorage/XSS/跨账号旧轮询/URL和tx隐私 | CSP/依赖审查/AbortSignal/身份版本、脱敏日志 |
| 待评估 | GPL-3.0及实际客户资金/发卡许可 | 专业审核使用/修改/分发和业务模式，本文不是法律结论 |

## 8. 新增测试与故障演练

### 8.1 单元/HTTP契约

HMAC官方固定向量、100/100.00/20.010001、空值/大小写/重复字段/PID错误；32位order号、金额边界、坏JSON/Content-Type、URL/重定向/超时/5xx/错误不泄密；UNKNOWN不换号。receipt成功/失败/错合约假USDT/错地址/多付少付/未固化/早于建单/内部调用/多event/零或溢出/字段缺失。回调早到/乱序/冲突/重放/轮换/纯文本ACK/DB不可写。

### 8.2 PostgreSQL集成

token域隔离、强制改密、跨用户not_found、停用并发创建；同键同输入唯一/异输入冲突；同tx+event并发回调和补扫一次receipt，不同event不误互斥。receipt/审计/state失败全回滚，租约/重启/inbox/job/游标恢复。迁移5→6保留卡/session、空库/Ready/资金down保护/sqlc一致性。临时库执行，不拿生产库测试。

### 8.3 前端

user realm/稳定幂等键/金额string/完整精度/无secret或地址覆盖；redirect假成功无效、刷新断网原单恢复、切账号取消旧请求、倒计时只是报价过期。手机二维码和警告、所有本地状态/后台证据。mock与实际DB后端E2E分别报告。

### 8.4 上游与运行故障

v2已有order_service_test（预约/精度/占位/URL）、listen_trc20_block_test（节点切换/历史块获取失败）、mq/worker_test（backoff/重启重试/ACK/HMAC）、manual_payment_verify_test等。它们有用，**不证明自动TRON已覆盖停机补扫/固化/内部转账**。

必须演练回调断网至耗尽、停机到账、主库/runtime不同时间恢复、RPC落后/切换/坏数据、晚付与金额复用、停用地址但旧单已付、创建成功响应丢失、确认落库失败。告警包括扫描高度/游标最后成功时间、未匹配入金、VERIFYING/CREATE_UNKNOWN年龄、重试耗尽、链/网关/Fcard差额。

回归入口：`go test ./...`；`powershell -File scripts/test-integration.ps1`；`powershell -File scripts/check-generated.ps1`；frontend中 `npm test`、`npm run build`、`npm run test:e2e`；`scripts/test-frontend-integration.ps1`。最初源码调研未运行，后续本轮实测结果见第 11 节。Epusdt在固定源码src独立跑测试，不混入Fcard module；真实链小额试收单独授权。

## 9. 待验证与发布否决条件

- [ ] 部署当天重查最新正式release，升级先diff/回归；校验资产SHA256SUMS/provenance，不只看tag。
- [ ] 固定构建和runtime路径/SQLite WAL/Windows或Linux volume、一致备份恢复。
- [ ] 实测currency=USDT、最小金额、六位精度/提币净额/尾差，明确按实际核验units记录。
- [ ] 可信TRON RPC/备用节点、真实固化接口/最终性、USDT合约白名单真实性；本文未调用主网RPC验证预置合约。
- [ ] 持久扫描覆盖停机/内部/批量转账、失败不跳块、未匹配可追踪。
- [ ] 受认证order_id查单/UNKNOWN恢复/通知先到测试，不用公开check-status替代。
- [ ] switch封禁/公开收银台数据范围/非目标链币和OkPay关闭。
- [ ] 确认事务unique、重放/归属/停用/审计回滚、版本/sqlc测试通过。
- [ ] 回调重试/重发/轮换窗口、固定域名证书/SSRF出口策略演练。
- [ ] 私钥持有人/地址持有证明/双人审批、备份丢失处理、总敞口/异常冻结流程明确；提现虽后做，不能积累不可控资金。
- [ ] 专业评估适用地区资金管理、GPL修改/分发范围、未来Airwallex业务许可，不将本文当法律或供应商授权意见。

**否决条件**：任何P0未关闭、只有回调没有独立链上证据、补扫不可靠、归属或幂等失败、私钥/地址责任不明确、无差额处置能力，都不能真实收款。可以先mock/隔离环境闭环，但“能运行”不是“资金链路可上线”。

## 10. 官方可复核证据索引

除发布页外固定提交为 `619cecb11d6bbd1dcdd1b698ee08b37fc722d3e3`，后续不要替换为浮动master。

| 编号 | 官方证据 | 用途 |
| --- | --- | --- |
| E01 | [原入口](https://github.com/assimon/epusdt)、[latest](https://github.com/GMWalletApp/epusdt/releases/latest)、[v2.0.0](https://github.com/GMWalletApp/epusdt/releases/tag/v2.0.0) | 仓库迁移、发布/时间，核对日期2026-10-13 |
| E02 | [源码](https://github.com/GMWalletApp/epusdt/tree/619cecb11d6bbd1dcdd1b698ee08b37fc722d3e3)、[go.mod](https://github.com/GMWalletApp/epusdt/blob/619cecb11d6bbd1dcdd1b698ee08b37fc722d3e3/src/go.mod)、[RELEASING](https://github.com/GMWalletApp/epusdt/blob/619cecb11d6bbd1dcdd1b698ee08b37fc722d3e3/RELEASING.md) | 依赖/资产校验说明 |
| E03 | [router](https://github.com/GMWalletApp/epusdt/blob/619cecb11d6bbd1dcdd1b698ee08b37fc722d3e3/src/route/router.go)、[API](https://github.com/GMWalletApp/epusdt/blob/619cecb11d6bbd1dcdd1b698ee08b37fc722d3e3/wiki/API.md) | 商户/公开/后台路由 |
| E04 | [check_sign](https://github.com/GMWalletApp/epusdt/blob/619cecb11d6bbd1dcdd1b698ee08b37fc722d3e3/src/middleware/check_sign.go)、[sign](https://github.com/GMWalletApp/epusdt/blob/619cecb11d6bbd1dcdd1b698ee08b37fc722d3e3/src/util/sign/sign.go) | PID/HMAC/IP白名单/数字规范化 |
| E05 | [order_service](https://github.com/GMWalletApp/epusdt/blob/619cecb11d6bbd1dcdd1b698ee08b37fc722d3e3/src/model/service/order_service.go)、[order_data](https://github.com/GMWalletApp/epusdt/blob/619cecb11d6bbd1dcdd1b698ee08b37fc722d3e3/src/model/data/order_data.go) | 创建/预约/重复/支付和父子单更新 |
| E06 | [wallet_controller](https://github.com/GMWalletApp/epusdt/blob/619cecb11d6bbd1dcdd1b698ee08b37fc722d3e3/src/controller/admin/wallet_controller.go)、[wallet_model](https://github.com/GMWalletApp/epusdt/blob/619cecb11d6bbd1dcdd1b698ee08b37fc722d3e3/src/model/mdb/wallet_address_mdb.go)、[wallet_data](https://github.com/GMWalletApp/epusdt/blob/619cecb11d6bbd1dcdd1b698ee08b37fc722d3e3/src/model/data/wallet_address_data.go) | 观察地址/启停/导入/软删除 |
| E07 | [TRON scanner](https://github.com/GMWalletApp/epusdt/blob/619cecb11d6bbd1dcdd1b698ee08b37fc722d3e3/src/task/listen_trc20_block.go)、[task_service](https://github.com/GMWalletApp/epusdt/blob/619cecb11d6bbd1dcdd1b698ee08b37fc722d3e3/src/model/service/task_service.go)、[manual_verify](https://github.com/GMWalletApp/epusdt/blob/619cecb11d6bbd1dcdd1b698ee08b37fc722d3e3/src/model/service/manual_payment_verify.go) | 自动监听与人工核验差异 |
| E08 | [worker](https://github.com/GMWalletApp/epusdt/blob/619cecb11d6bbd1dcdd1b698ee08b37fc722d3e3/src/mq/worker.go)、[response](https://github.com/GMWalletApp/epusdt/blob/619cecb11d6bbd1dcdd1b698ee08b37fc722d3e3/src/model/response/order_response.go) | 回调字段/ACK/重试 |
| E09 | [runtime_sqlite](https://github.com/GMWalletApp/epusdt/blob/619cecb11d6bbd1dcdd1b698ee08b37fc722d3e3/src/model/dao/runtime_sqlite.go)、[mdb](https://github.com/GMWalletApp/epusdt/blob/619cecb11d6bbd1dcdd1b698ee08b37fc722d3e3/src/model/dao/mdb.go)、[table_init](https://github.com/GMWalletApp/epusdt/blob/619cecb11d6bbd1dcdd1b698ee08b37fc722d3e3/src/model/dao/mdb_table_init.go) | 双库/AutoMigrate/预置链合约 |
| E10 | [env](https://github.com/GMWalletApp/epusdt/blob/619cecb11d6bbd1dcdd1b698ee08b37fc722d3e3/src/.env.example)、[rate](https://github.com/GMWalletApp/epusdt/blob/619cecb11d6bbd1dcdd1b698ee08b37fc722d3e3/src/config/rate.go)、[precision](https://github.com/GMWalletApp/epusdt/blob/619cecb11d6bbd1dcdd1b698ee08b37fc722d3e3/src/model/data/settings_data.go) | 默认重试/有效期/汇率/精度 |

## 11. 本轮执行记录（2026-10-13）

### 11.1 已完成：隔离部署与无资金契约基础

| 交付/实际证据 | 结果与边界 |
| --- | --- |
| 发布核对：官方 `/releases/latest` HTTP 重定向到 `/releases/tag/v2.0.0`；本地源码 `git rev-parse HEAD` 为上述固定 SHA | 当日固定 v2.0.0，不跟随 master；GitHub API 当轮限流，改用官方发布页重定向核对 |
| `deploy/epusdt/Dockerfile`、`compose.yaml`、`entrypoint.sh` | 原版源码独立构建；基础/运行/代理镜像均固定 digest；bootstrap 在无网络下初始化；不将 Echo/GORM 加到 Fcard Go module |
| `deploy/epusdt/bootstrap/main.go`、`main_test.go` | 禁用预置主网 RPC、链、币、钱包，只允许 `https://nile.trongrid.io`；持久 Nile 标记/精度 6；启动前 fail-closed 检查；未配置私钥 |
| `deploy/epusdt/nginx.conf` | 本机入口 `http://127.0.0.1:19800`；switch-network、EPay、OkPay、安装接口实测 403；后台仍仅供本机管理，不是公网发布配置 |
| `scripts/start-epusdt-testnet.ps1`、`test-epusdt-testnet.ps1`、`stop-epusdt-testnet.ps1` | 启动/无资金探测/容器重建/停止保留卷；管理和商户凭证保存在忽略目录，不打印 secret |
| 独立 volumes `fcard-epusdt-nile_primary`、`fcard-epusdt-nile_runtime` | 容器重建后管理员、商户密钥和 Nile 配置可继续使用；不是两库一致备份/灾难恢复验收；现有 `fcard` PostgreSQL 容器未改动 |
| `internal/epusdt/sign.go`、`client.go` 及测试 | 服务端 HMAC-SHA256/固定 TRON-USDT/form；本地整数金额、32 字符单号校验；禁止自动重试/重定向；重复、超时、异常成功响应保留 UNKNOWN 语义；暂未接入 Gin 路由和应用配置 |
| `internal/epusdt/gateway_integration_test.go` | 真网关拒绝坏 HMAC/旧 32 字符签名；正确 HMAC 通过签名层后因 localhost notify_url 返回 HTTP400/10041；全程没有创建支付订单或发起转账 |

**当前可用能力：运行隔离网关、管理登录、验证签名与安全拒绝。当前不可用能力：用户充值、到账确认、账户余额、提现、卡额度增加。**

公开配置实测无 `supported_assets`、OkPay 关闭；后台启用的 RPC 只有 Nile、链代币全禁用、订单总数为 0。未验证 RPC 可持续扫描到正确块，未验证测试代币 Transfer/receipt/固化。启动成功不能替代这些验收。

### 11.2 已执行验证

| 命令 | 本轮结果 |
| --- | --- |
| `powershell -NoProfile -ExecutionPolicy Bypass -File scripts/start-epusdt-testnet.ps1 -Build` | 固定源码构建成功；Docker 内 bootstrap 单元测试通过；网关/代理健康 |
| `powershell -NoProfile -ExecutionPolicy Bypass -File scripts/test-epusdt-testnet.ps1` | 7 个真实网关子场景通过；未创建订单；凭证文件 hash 不变 |
| 同一脚本加 `-Recreate` | 强制重建两个容器后同样通过；持久配置/身份保留 |
| `go test ./... -count=1` | Fcard 默认测试通过；未设置数据库变量时 DB 集成测试按设计跳过，不能据此声称 DB 验证通过 |
| `powershell -NoProfile -ExecutionPolicy Bypass -File scripts/test-integration.ps1` | PostgreSQL 实际集成测试通过，使用临时测试数据库并清理；不调用真实 Airwallex 发卡 |
| `go test ./internal/epusdt -count=1` | 签名向量、form、参数/金额、安全 URL、异常/重复/响应错配、取消请求及无自动重试测试通过；未设置网关变量时真实网关探测跳过 |
| `go vet ./...`、`scripts/check-generated.ps1` | 通过；现有 sqlc 生成文件保持一致，未修改 `go.mod`/`go.sum` 或现有迁移 |
| frontend 中 `npm test`、`npm run build` | 4 个测试文件/26 个测试通过；类型检查和生产构建通过；未新增充值页面 |
| `go test -race ./internal/epusdt -count=1` | 未运行成功：当前 Windows Go 环境未启用 CGO，Go 拒绝 `-race`；需有 C 工具链的 CGO 环境补测 |

未执行完整上游测试套件、Playwright E2E、实际链转账、实际回调重试/ACK、补扫、双库备份恢复、PG 版 Epusdt 验收或真实资金操作。第 9 节资金发布否决条件仍有效。

### 11.3 下一步实施顺序与外部前置

1. **继续契约 spike**：mock 回调、数字规范化、ACK、响应丢失/回调先到测试和人工 UNKNOWN 恢复流程已补（第 12 节）；剩余真实成功建单/实际回调、持久 inbox 和受控查单恢复，仍不允许用户建单。
2. **确认专用 Nile 观察收款地址 B 与付款钱包 A**：仅需要公开收款地址和持有证明。私钥/助记词由外部钱包保存，不提供给仓库、网关或聊天。
3. **确认实际 Nile 测试 USDT 合约**：查询链上代码、decimals、Transfer 事件及固化 receipt，并明确其只是测试代币；未确认前不填主网合约或随便采用示例地址。
4. **准备受控 HTTPS 回调入口**：用户控制的域名或隧道；先实现 Fcard inbox/验签/持久化 ACK 和流量限制，再开放该特定回调路径，不暴露网关后台，不移除上游 SSRF 校验。
5. **资金证明和持久补扫优先**：按阶段 3 完成 receipt/log/最终性、持久游标、未匹配池和故障复现；然后阶段 4/5 落 PostgreSQL 迁移、sqlc、用户归属、幂等和核验闭环。阶段 2 的客户端原型不意味着阶段 4 已完成。
6. **受控启用测试收款**：修改当前“所有币/钱包必须禁用”的启动检查为仅允许已验证 Nile 合约/地址的白名单，新增主网拒绝测试；完成这一步后才由外部钱包发送测试币，核对链/网关/Fcard。
7. **最后补 Vue 收款/记录/异常页面和全链 E2E**：阶段 6/7 通过后再考虑主网，主网是独立上线审批，不是更换 RPC 即可。

第 2–4 项是实链联调所需外部信息，不阻止先做 mock、数据库/协议设计。当前没有进行测试币转账；也不需要用户提供任何私钥。源码、配置和凭证都在 `.cache/` 下，现有 `.gitignore` 用户改动保持原样。

## 12. 第二轮执行：回调契约与 UNKNOWN 证据（2026-10-13）

### 12.1 已实现，但未开放路由

| 代码/实际证据 | 本轮进展 |
| --- | --- |
| `internal/epusdt/notification.go`、`notification_test.go` | 严格 JSON/PID/HMAC；重复/未知 key、非法数字/类型/float 别名拒绝；整数金额；最多两把版本化 key；稳定 EventID；本地报价匹配 |
| `internal/epusdt/callback.go`、`callback_test.go` | 可注入持久 recorder 的未注册 HTTP handler；POST JSON/16KiB/超时；写入返回成功之后才 HTTP200 纯文本 ok；存储失败 503；合法签名的业务错配仍留给 inbox 处理 |
| `internal/epusdt/contract_test.go` | mock 网关“订单提交后响应连接断开”返回 UNKNOWN 且只创建一次；回调先于创建响应时保存线索，后续报价到达仍能匹配 |
| `internal/epusdt/README.md` | 明确 recorder commit/UNIQUE/未知订单/审计契约；不把 mock 或签名匹配当作到账确认 |

没有修改应用 Gin 路由、用户认证、现有数据库迁移、sqlc 或 Vue 页面；没有启用收款，也没有向真实网关发送成功支付回调。生产 PostgreSQL recorder、数据库并发幂等、配置注入、限流/公网接入、旧 key 有效期限管理、独立链上核验均待实现。

### 12.2 验证边界

- 回调向量按固定上游 `response.OrderNotifyResponse` 与 `sign.MapToParams` 复核，并用独立 Python 标准库 HMAC 计算测试向量；不是对原始 JSON 字节签名。该金额 fixture/地址/hash 都只是测试数据，不是已验证钱包或实际付款。
- 数字 `100`/`100.00`/`1e2` 的签名和事件身份一致；超过六位最小单位或仅经 float64 舍入看似一致的金额被拒绝。
- 真实 handler 的并发回放、取消/失败写入、等待返回才 ACK 已用 mock 验证。mock map 仅验证接口契约，**不证明 PostgreSQL 原子性，也不证明 exactly-once 到账**。
- `MatchNotification` 匹配的是已保存创建报价；不验证 tx 执行/合约/事件/固化，不能直接更新 CONFIRMED。验签通过但报价/币种错配的结构合法通知保留后待审，不因无法匹配而直接丢弃。
- 当轮全量 `go test ./... -count=1` 和 `go vet ./...` 通过；`go test ./internal/epusdt -count=1 -cover` 通过，package 语句覆盖率 97.4%。默认环境的 DB/真实网关集成测试仍按条件跳过，本轮没有重复上一轮 PostgreSQL/前端验收。
- `go test ./internal/epusdt -run '^$' -fuzz '^FuzzNotificationVerification$' -fuzztime=5s -parallel=2` 通过，最终轮执行 42,509 次；这是短时 parser/invariant 模糊测试，不是全面安全审计。Go 缓存位于工作区外，沙箱拒绝后已通过授权在沙箱外验证；不把沙箱构建错误视为代码回归。

### 12.3 当前 UNKNOWN 恢复流程：人工，只读核查

固定源码证据：`src/route/router.go` 的 GMPay 商户路由只有创建/配置等，无商户 HMAC 的按 order_id 查询；管理面有 GET `/admin/api/v1/orders` 和 GET `/admin/api/v1/orders/:trade_id`。`src/model/data/order_admin_data.go` 的 keyword 是 trade/order/tx hash 的 **LIKE 包含搜索**；`src/model/mdb/orders_mdb.go` 返回 `api_key_id`，不是 PID。不能把搜索命中第一条当作精确商户订单。

1. 业务接入时先持久化本地用户归属、固定 order_id、链环境、PID、请求快照和幂等键再 POST。连接超时/断开、10002 重复错误、成功响应校验失败均进入 CREATE_UNKNOWN；禁止换 order_id 自动重建，也不因用户刷新或轮询触发重发。
2. 当前只允许受控管理面只读核查；不将管理员 JWT/密码放到正常 Fcard 创建客户端，不用公开 `/pay/check-status/:trade_id` 作可信恢复或到账证据。正式自动恢复前需另做最小权限商户精确查单接口或受控恢复适配器。
3. 按原 order_id 搜索并检查所有分页候选，逐字段精确比对：order_id、`api_key_id` 与配置 PID 的管理面映射、TRON/USDT、currency=USDT、Gmpay/on_chain、无 parent/sub 归属、原始 amount、预定 notify_url/redirect_url。同时核对该独立网关实例的 Nile 标记和请求时间；管理记录本身没有可靠 chain_environment 证明。
4. 恰好一条相符才由带审计的受控恢复操作绑定 trade_id/收款地址/actual_amount/报价时间，不把网关 status=2 变为资金确认。status=1 恢复等待；status=2 只触发独立核验；status=3 保留过期/晚付核查；status=4、子单、商户或字段不一致进入 REVIEW_REQUIRED。
5. 无结果、分页不完整、查询失败、来源不确定或多个候选继续 UNKNOWN/待审；“暂时搜不到”不是订单没有提交的证明，也不授权新单。后续持久 worker、完整查单契约和可审计关闭/重建政策完成前，不开放用户充值。
6. 早到回调先收 inbox，不能仅靠回调把未知创建直接绑定为成功或覆盖原用户归属。receipt 唯一确认仍须独立链上证据。

这是一套待接入业务数据层的恢复规范，并非已实现的自动查单/重试 worker。下一阶段按第 6 节继续资金证明与持久补扫设计/测试，收款开关保持关闭。

## 13. 第三轮执行：独立 TRON receipt 核验基础（2026-10-13）

### 13.1 已实现的实际代码

| 交付 | 能力与边界 |
| --- | --- |
| `internal/deposit/tron_address.go` | Base58Check 校验/41 hex 规范化；Transfer indexed address 的 32 字节 ABI 零填充检查，不只用地址正则 |
| `internal/deposit/tron_rpc.go`、`tron_rpc_test.go` | 只允许官方 Nile HTTPS；loopback mock 须显式开启；仅调用 walletsolidity 端点；禁止重定向/自动重试；响应大小、重复/嵌套 JSON key、类型、节点错误和取消请求检查 |
| `internal/deposit/tron_verifier.go`、`tron_verifier_test.go` | 创世块 pin、显式合约执行 SUCCESS、固化头/区块高度与 ID/时间/交易归属、合约白名单 Transfer 日志、原始 event_index、uint256 精确字符串；不依赖顶层 transfer selector |
| `MatchTronPayment` | 环境/创世块/交易/合约/地址/精确 units/窗口匹配；多事件、自转账、同时进出待审；窗口外的匹配事件保留证据；没有余额或 CONFIRMED 更新 |
| `tron_nile_integration_test.go` | 默认跳过的真实只读 RPC 契约探测；显式启用后验证真实 Nile 创世块、固化头和相同高度块响应 |
| `internal/deposit/README.md` | RPC 信任边界、最小证据摘要、错误分类、后续补扫原子事务/去重/原始金额存储约束 |

本轮仍未注册应用路由或后台任务，未修改迁移、sqlc、用户认证、Vue、Airwallex，也未启用任何币/钱包。测试只读查询不是测试币付款；没有实际 USDT receipt、测试合约代码/decimals 或付款钱包验收。

### 13.2 可复核协议与实测证据

- 浏览工具未获得可引用页面内容，改为只读下载官方源文件并记录本地 SHA256；不能把失败的浏览结果当作有效引用。固定 Epusdt 原源码仍作为对照，没有修改其扫描器。
- 官方 [Tron.proto](https://github.com/tronprotocol/protocol/blob/master/core/Tron.proto) 的 `TransactionInfo`、`ResourceReceipt`、`Transaction.Result.contractResult` 说明顶层结果成功拼为 `SUCESS`、合约成功为 `SUCCESS`；实现分别处理，缺合约执行结果不默认成功。下载内容 SHA256：`6982f5177850db3c5048005f8f222827d925052fbb8c93b0d041b20ed774aed3`。
- 官方 [BlockCapsule.java](https://github.com/tronprotocol/java-tron/blob/develop/chainbase/src/main/java/org/tron/core/capsule/BlockCapsule.java) 的 BlockId 将前八字节解释为高度；实现检查 ID/height 一致。下载内容 SHA256：`992d7517db28f737134a892108fada37948b3ba3d5c40180d69a9d057a451000`。这些链接指向上游分支，复核须对照当次归档/hash，不能假设未来分支内容不变。
- 官方 [Solidity transaction info](https://developers.tron.network/reference/gettransactioninfobyid-1) 页面及源文件/响应快照归档于 `.cache/tron-evidence-sources/`。该目录忽略，不含私钥；最小语义证据和 hash 保留在本文。
- **2026-10-13 06:35:49 UTC / 14:35:49 上海时间**，对 `https://nile.trongrid.io/walletsolidity/getblockbynum` 的 num=0 只读查询观测到创世块 `0000000000000000d698d4192c56cb6be724a558448e2684802de4d6cd8690dc`。实际响应省略默认 height=0，代码只在创世块 ID 编码高度确为 0 时兼容省略，非零块缺 height 拒绝。
- 同轮 Solidity 头观测高度 `71780746`、blockID `000000000447498a8693d772af65523b6b1178c0c1153e0d58d2fc53a7e8c85b`。随后真实 Go 客户端探测在高度 `71780825` 通过创世块/头部/按高度查块一致性；高度变化是正常实时前进，不要求后续测试固定链头高度。
- 以上是单个官方 RPC 源的只读兼容性证据，**不是独立共识/多节点最终性证明，不是已经核验付款**。创世块、合约及 decimals 的受控配置注入、备用节点信任和证据归档策略仍待实现。

### 13.3 测试与剩余事项

- 离线 fixture 验证：缺 receipt/执行结果、REVERT/OUT_OF_ENERGY、ID/高度/时间/parent 错配、头部不固化、日志地址/ABI/uint256 错误均不能成为付款证明；合法内部日志、原始 log index 和超大 uint256 保留。
- 业务匹配 fixture 验证：少付/多付、错合约/地址、同地址多入金、自转账、同交易进出、早付/晚付拒绝自动确认。所有 fixture 的币/地址/交易都是合成数据，不是已验证真实钱包或资产。
- 真实只读命令：设置 `FCARD_TEST_NILE_RPC=1` 后运行 `go test ./internal/deposit -run '^TestNileSolidRPCContract$' -count=1 -v` 已通过；默认 `go test ./...` 不发出该公网探测。
- `go test ./internal/deposit -count=1 -cover` 通过，语句覆盖率 **93.2%**；`FuzzTronRPCJSON` 的 5 秒目标短测通过，执行 **35,112** 次。`go test ./... -count=1` 与 `go vet ./...` 通过；默认跳过 DB/真实网关/公网 Nile 条件测试的结果不等于这些集成验收，本轮真实 Nile 只读测试是另外显式执行的。

下一步优先落实最小 `chain_transfers`/`chain_scan_cursors`/未匹配池数据层及 pgx/sqlc 原子块提交：先指定审核过的起始高度/块 ID，逐块完整处理，证据下载/解码/保存失败不推进游标；事务回滚/进程崩溃后重放不丢不重。必须另外明确普通非合约交易/失败合约的可信无入金处理，不能直接根据 verifier 的错误跳块。金额/事件持久化前不创建 receipt，不开启用户收款。

## 14. 最小链证据持久化实施记录（2026-10-13）

### 14.1 实际代码与边界

- `migrations/000006_chain_evidence.up.sql/down.sql`：新增 `chain_scan_cursors`、`chain_scan_blocks`、`chain_transfers`，`internal/storage/storage.go` 要求 SchemaVersion=6。本轮没有执行现有 Fcard 数据库升级；数据库测试只创建/清理临时库。
- `internal/storage/queries/chain.sql` 与 `internal/storage/db/chain.sql.go`：通过项目固定 sqlc v1.31.1 生成 pgx 查询。游标范围固定环境/创世块/TRON/scanner，另外保存合约、evidence_version=1、原始起点和已处理块 ID/高度。
- `internal/deposit/chain_store.go`：`Initialize` 要求明确已审核起点（下一块开始扫描），重复初始化不重置/快进；`Load` 校验配置相容；`CommitSolidBlock` 同一事务保存块的完整最小批次、所有 Transfer 和游标，游标行锁及 CAS 防止并发跳块。相邻 height/parent 必须衔接，证据错误或保存失败不推进。
- 链事件 UNIQUE 覆盖环境/创世块/network/tx_hash/log index；跨 scanner 的相同事件去重，不同内容不覆盖而回滚。`raw_units` 为正则与 NUMERIC 上界共同约束的十进制 TEXT，精确保留 0..2^256-1；不要求先匹配本地订单，零值/超大/未知归属事件也保留，不成为充值事实。
- 批次交易证据与区块交易清单必须完整、逐一且按序对应；保存前校验最小 receipt 的 ID/高度/时间/明确成功结果、规范化字节及 SHA256，并重新解码日志与提交的 Transfer 比较。块证据保留内部批次 JSON（receipt 字节编码为 Base64），不是原始 HTTP 归档；数据库保存时间不等于 RPC 观测时间。
- **存储不是链核验器**：不访问 RPC、不验证 Solidity 固化/签名/共识，真实完整块及可信来源仍由内部扫描器提供；不能暴露为用户上传链证据后直接确认的接口。当前只支持空块或全部交易都有明确合约 SUCCESS 的块，native/失败交易缺完整可接受证据时整块拒绝，不能删掉交易清单或捕获 `ErrExecutionFailed` 后推进。
- 相同历史块完整重放不再次推进游标；改变证据摘要/字节、合约、起点或 parent 拒绝。数据库 Commit 出错视为结果待确认，保留原批次并重读/原样重放，不更换起点或猜测结果。本轮未加入消费余额、receipt、收款用户路由、回调 inbox 或持续扫描 worker；网关收款继续禁用。

### 14.2 实际验证

- `internal/deposit/chain_store_test.go`：离线检查缺失/额外交易证据、重复交易、错链/高度/时间/ID/hash、修改 amount/index、遗漏事件、native 缺合约成功结果及失败合约均拒绝。
- `internal/deposit/chain_store_integration_test.go`：真实 PostgreSQL 临时库验证第二事件插入失败、全部事件写完后游标更新失败都整体回滚；重启加载原游标及完全相同块重放；uint256 最大值和零值无损；不同证据冲突、跨 scanner 去重及不同创世块隔离；8 个并发提交同一块仅一项报告新提交；历史重放不倒退；取消、跳高度、错 parent、证据遗漏不推进；迁移降到5、readiness拒绝，再升级6通过。
- 初次全量回归发现4个既有迁移测试只回退至第5版之前而没有删除新增第6版表，导致重升时表已存在；已仅调整这些测试的降级准备，先执行第6版 down，再保持原历史用户/会话/卡及槽位断言。未改认证/开卡业务实现。
- `powershell -NoProfile -ExecutionPolicy Bypass -File scripts/test-integration.ps1` 全量通过（包括新增链存储与 HTTP 历史迁移回归）；`scripts/check-generated.ps1` 确认生成代码无漂移；`go vet ./...` 通过。
- 上述数据来自合成 TRON 块/receipt/代币/地址，不是实链支付或测试币转账；默认无 `TEST_DATABASE_URL` 时会跳过 PostgreSQL 集成用例，不得把跳过当通过。没有执行新一轮公网 RPC/真实网关资金测试，也没有声称完成强杀进程恢复演练。

### 14.3 接下来的实施顺序与兼容性风险

1. **先完善扫描输入**：明确 native/失败合约的可信无入金证据，完整验证区块及每笔 receipt，定义版本化批次/来源与观测时间；保留取证失败任务，不能删交易或跳高度。当前存储接口刻意 fail-closed，不适合直接对真实 Nile 连续开扫。
2. **再实现持久 worker**：受控注入创世块/合约/decimals/已审核起点，逐块追 Solidity 头，按持久游标恢复；实现受限重试/告警、备用节点策略、停机回放及进程强杀/Commit 结果未知演练。重放需保持最小证据版本/规范化一致，不能静默改变 scanner 合约或证据处理范围。
3. **随后落地订单/inbox/任务/receipt**：实现 pgx/sqlc 状态机、回调持久化后 ACK、UNKNOWN 人工只读恢复、窗口重叠/未匹配池及唯一归属，才接用户查询与收款页面；链事件保存不能替代业务确认事实，更不能增加余额。
4. **最后用真实测试钱包/测试合约币验收**：核验合约代码及 decimals、导入仅测试网地址（不上传私钥）、小额实际支付、晚付/少付/多付/重复回调/漏回调/停机补扫。所有安全门槛通过后才在明确授权下开启测试收款；本轮未启用任何收款地址。

运行新版后端前必须按项目迁移流程将应用库升级到6，否则 Ready拒绝旧版5；本轮不替用户自动升级现有库。第6版 down 会删除链证据表，只在可丢弃临时库测试，不是资金证据的无损回退。当前保存全部目标合约日志而非只过滤已有钱包，需在 worker 上线前明确容量/隐私/访问控制、保留/归档及新的处理范围审批；16 MiB 批次上限触发时暂停并告警，不能截断后推进。主网、多币/多合约、资金归属审计和提现仍在范围外。

## 15. 版本化逐块取证与断点扫描实施记录（2026-10-13）

### 15.1 实际实现

- `internal/deposit/tron_verifier.go`/`tron_rpc.go`：块最小交易模型新增 `ret.contractRet`、`ret.ret` 和 `raw_data.contract.type`，RPC 提供来源标识，仍只访问受控 Solidity 接口。成功支付 verifier 的严格 SUCCESS 门槛保持；失败/native disposition 不能进入付款匹配。
- `internal/deposit/tron_scan.go`：独立 `TronBlockReader` 核对创世块、固化 head、持久锚点、相邻块 parent、交易清单和每笔 receipt，再构建 v2 批次。所有原始清单中的交易都必须取得证据；任何缺失/矛盾/未知类型返回错误，不交付部分块。
- 限定无入金政策：只有 `TransferContract`/`TransferAssetContract` 的明确成功且无日志，才允许 native receipt 省略默认结果；TVM 的 Trigger/Create 必须明确 SUCCESS 才解码目标合约 Transfer。TVM 的13个已知失败枚举需块 contractRet/receipt 失败码一致、顶层明确 FAILED、没有日志，才保存失败证据而不产生入金事件。DEFAULT/UNKNOWN、多个 type/ret、其他 native 类型或失败/native 日志一律暂停；不是把所有失败错误当跳块许可。
- v2 保存来源、证据收集完成时间及当时固化 head。最小 receipt 仍不是完整原始响应；块交易 raw_data 只保留类型，不含原始 protobuf/合约参数。规范化 hex、protobuf 默认枚举和空日志；深拷贝输入，确保取证源后续更新不能改变等待提交的批次。
- v2 事实摘要只覆盖版本/区块/交易证据，不含变化的观测时间/head；重放比较事实摘要及完整事实内容，保留第一次保存的观测信息，不覆盖。这个摘要不是观测信息的独立认证或原始 HTTP 完整性证明；源信任/访问控制和原始归档仍待落地。
- `internal/deposit/tron_scanner.go`：`ScanOnce` 按持久游标仅取下一块；`Run` 顺序补扫/追平轮询，截止时间、指数退避及连续失败预算有上限。错链/无效证据/冲突/未知交易立即告警并停止；节点/数据库瞬时故障不推进，在预算内重试。Commit 响应丢失保留同一内存批次原样重放，不重新取证；重建 worker 从数据库已提交游标续扫，不重置到链头。
- `migrations/000007_chain_scan_v2.up.sql/down.sql`：允许 evidence_version 1/2，该阶段 SchemaVersion=7（现为10，见第18节）；旧 v1 scanner 不自动升级或重解释历史，`NewTronScanStore` 在明确新范围启用 v2。存在 v2 游标时拒绝第7版降级。没有修改旧第6版迁移、没有升级现有应用库，也没有启动实际扫描服务/启用收款。

### 15.2 源码与真实只读证据

除第13节已归档的官方 `Tron.proto` 外，本轮下载并核对官方 java-tron develop 分支：

| 官方文件（归档于 `.cache/tron-evidence-sources/`） | 观察到的语义 | SHA256 |
| --- | --- | --- |
| `actuator/src/main/java/org/tron/core/actuator/TransferActuator.java` | TransferContract 执行账户 TRX balance 变化并记录执行状态，不执行 TRC20 日志的 TVM 路径 | `59e388e6b4475a1c2bdc14c79084b356e85f3056477be2bdd40cfb710a2d8eab` |
| `actuator/src/main/java/org/tron/core/actuator/TransferAssetActuator.java` | TransferAssetContract 处理账户 asset/TRC10 数量与状态，区别于 TVM Transfer 日志 | `90e7acdf1d62ac1da3ecb2c5c4d9d14047d1ec67467d09b2edc4201705bf19e4` |
| `chainbase/src/main/java/org/tron/core/db/TransactionTrace.java` | needVM 只对应 Trigger/Create；非 VM 不设置同样的 resource contract result，VM 检查块 contractRet 与 receipt result 一致 | `92fdd12c38e758df23a26455f7cced529b1af503d4e5cc31b3ef616e0e9c3a61` |

来源为 `https://raw.githubusercontent.com/tronprotocol/java-tron/develop/` 下上述路径；分支可能变化，以归档 hash 复核。最初尝试的 framework TransactionTrace 路径返回404，实际取得的是表中 chainbase 文件，不把失败请求当证据。浏览工具本轮仍未返回可用正文，上述证据来自实际下载的官方文件，而非假引用。

**2026-10-13 08:15:00 UTC / 16:15:00 上海时间**：只读查询 Nile Solidity head 后，取其中一笔公开 TRX 普通转账并查 receipt，观测：

- 固化块高度 `71782721`，block ID `00000000044751412dfd16b552f057c096f48ecec4a042f3b7ecb928220439cb`。
- tx hash `5cbde0e6523e47dd14a1cae459ee7b1d57f8aaf92e1bd1ade208921e93a51761`，类型 TransferContract、块 contractRet=SUCCESS；receipt 高度/时间匹配，resource result、top result、log 字段均省略。
- 归档 `nile-native-observation.json` SHA256=`58446163cc0fcb99fd922f64a4ad5098cc9785a4e202cb511533f87933f97e20`。首次 PowerShell 展示的 LogCount=1 是 `@($null)` 的包装结果；检查实际字段后确认 log 字段不存在、实际日志数0，不据此放宽 native 无日志要求。
- 已显式设置 `FCARD_TEST_NILE_RPC=1` 运行实际 Go `TestNileNativeReceiptContract`，核对创世块、记录块成员关系、receipt 与新 native 分支，通过。测试未配置代币、不涉及用户钱包/转账，不是测试 USDT 支付验收；TRC10 和失败 TVM 的实链 receipt 兼容性尚未独立验证。

### 15.3 测试结果与仍待验证

- `tron_scan_test.go`/`tron_scanner_test.go`：混合块完整取证、无入金分支、13个明确失败码、未知类型/失败矛盾/缺 receipt、Solidity HTTP DTO、默认值规范化、源快照隔离、提交前故障和提交后响应丢失原样重放、预算耗尽/立即暂停/追平取消均通过。
- `tron_scanner_integration_test.go`：实际 PostgreSQL 临时库保存全部四笔混合证据但仅两条成功目标日志；提交已成功后模拟响应丢失，重放不重复；receipt 故障不推进，恢复补扫下一高度；新 repository/worker 接续持久游标；观测时间不同的两个并发批次仅推进一次；历史重放不回退；v1/v2 不混用；第7版原始 down SQL 的安全拒绝不删除证据。仍保留第14节的事件/游标故障整块回滚测试。
- 最终代码 `scripts/test-integration.ps1` 全项目临时数据库回归通过；`scripts/check-generated.ps1` 确认 sqlc 生成无漂移；`go vet ./...` 通过；无数据库环境的 `go test ./internal/deposit -count=1 -cover` 通过，覆盖率 **79.5%**（数据库/公网条件用例默认跳过，不等于这些验收）。额外公网 native Go 只读测试是显式单独执行。
- 原始 down SQL 拒绝测试不等于 migration CLI 失败也保持 Ready：迁移工具可能先设置 dirty，实际降级失败需受控人工核查，不可自动 Force/删除 v2 游标绕过保护。
- 当前 whitelist 不覆盖投票、冻结/资源委托等其他 native 类型，真实连续扫描可能暂停；须审阅并扩展有明确无 TRC20 语义的政策，不能简单把非 TVM/未知类型全部归入无入金。没有完成整个真实区块范围持续扫描、实际进程强杀、节点宕机/多节点切换、生产配置入口、原始证据归档及运维告警接入。

### 15.4 下一步与测试钱包提供时点

1. 订单/inbox/任务/唯一 receipt 的 PostgreSQL 基础已落实，见第16节；继续实现安全任务编排、回调关联、订单过期与 UNKNOWN 只读人工恢复，再接运行配置和路由。未匹配链事实不能直接增加余额。
2. 补受控 scanner 配置/进程入口、额外 native 类型审核、实链失败 receipt、保留/容量/访问/告警和进程强杀/节点故障演练；仍不得自动选择链头作为起点，或绕过未知类型。
3. **目前不需要用户提供钱包。** 业务订单、回调持久化、唯一入金归属/receipt、安全启用开关及页面联调准备好后，在导入测试地址及真实小额 Nile 支付前，明确通知用户提供一个专用测试钱包的 **`T...` 公共收款地址**。届时另行核验测试代币合约代码与 decimals，并由用户自行在自己的发送钱包发测试币；绝不索取私钥、助记词、密码，不把测试收款开启延伸为主网授权。

## 16. 订单、持久回调和唯一确认基础（2026-10-13）

### 16.1 实际实现证据

- `migrations/000008_deposits.up.sql/down.sql`、`internal/storage/storage.go`：SchemaVersion=8；新增 deposit_orders、deposit_callback_inbox、deposit_jobs、deposit_receipts、deposit_events。没有升级现有 Fcard 应用数据库；验收全部使用新建临时库。数据库禁止修改订单请求快照、已保存 claim 和已绑定报价，约束实际金额/最长有效期/隔离截止；receipt/审计禁止 UPDATE/DELETE。事务提交时延迟检查 CONFIRMED 与 receipt、原始 Transfer、报价及付款窗口一致。任何订单或 inbox 存在时拒绝第8版 down，避免销毁 UNKNOWN/入金证据。
- `internal/storage/queries/deposit.sql`、sqlc 生成的 `internal/storage/db/deposit.sql.go`：用户/幂等键、环境/PID/网关单号、环境/创世块/network/tx/log 唯一约束及 pgx 行锁/事务查询；所有金额为整数最小单位，不用 float64 存储或按订单删除旧隔离记录。
- `internal/deposit/order_store.go`：Prepare 同事务保存认证用户归属、金额/环境/合约/政策快照、固定网关订单 ID、CREATE 任务及审计。认证重新核对当前用户/凭据/有效会话及 auth_version，Get 仅限订单所有者。StartCreation 持久化唯一 claim，再次校验用户及订单授权版本；没有实际调用网关。响应不明进入 CREATE_UNKNOWN/人工任务，不允许重新 claim 或生成新单号。
- BindQuote 核对请求/报价、实际金额尾差 0..99 最小单位、TRON Base58Check 收款地址、USDT、固定 HTTPS checkout 来源和最长有效期。地址/金额的 advisory xact lock 与所有历史隔离窗口共同检查归属；重叠单进入 REVIEW_REQUIRED。晚返回的合法报价可保存为 EXPIRED；EXPIRED 不证明没收到钱。
- `internal/deposit/inbox.go`：Record 重新从 RawPayload 验签，并要求传入通知事实完全一致；首次 payload/hash/密钥版本、VERIFY_CALLBACK 任务和审计同事务保存，commit 后才返回 nil。签名轮换/等值表示不改变事件身份，重复只增加投递次数而保留首条证据。未知、早到、报价错配通知也可靠保存，不受新单启用开关影响。没有自动创建 receipt。
- `internal/deposit/receipt_store.go`：Confirm 只引用数据库中已保存的 Transfer 和完整块证据，重新验证 evidence_version 和摘要，再复核全交易匹配、付款窗口、历史窗口重叠及用户状态。缺证据返回待补扫；错配/多入金/自转或出金/窗口外/禁用用户/重叠进入持久人工复核。成功时唯一 receipt、订单 CONFIRMED、链任务完成和审计同事务提交；重复同一引用返回原 receipt。**不增加余额，不调用 Airwallex，不接受公开上传 proof。**
- 已 claim 但未绑定报价的 CREATING/CREATE_UNKNOWN 可能隐藏钱包/金额 reservation，因此同环境/创世块/network/合约范围自动确认保守阻断，返回 `ErrDepositAttributionPending`，不是忽略未知单继续匹配。claim、报价绑定、失败状态和确认共用范围 advisory xact lock，避免检查后出现未知占用的并发窗口。恢复报价后再按实际 reservation 判断；该阻断只涵盖本数据库已知订单，不证明外部网关没有其他未登记占用。

### 16.2 验收和故障语义

- `internal/deposit/order_store_test.go`：最小单位精确往返、金额边界及非法指数/精度；报价错订单/金额/尾差、地址校验、币种/状态、checkout 来源/凭据和付款期限边界。
- `internal/deposit/order_store_integration_test.go`：实际 PostgreSQL 临时库并发同键仅生成一个订单/CREATE 任务；跨用户拒绝；吊销/过期/版本不符会话拒绝；一次 claim、UNKNOWN 禁止重建、相同报价重放及历史窗口隔离；迁移 down 拒绝已有业务证据。
- 回调任务写入失败时 inbox 整体回滚且 handler 返回503；并发重复及轮换密钥保留首次 payload/版本，仅一条任务/审计；伪造已验签结构、取消请求不能 ACK；成功提交才200/ok，回调本身不创建 receipt。
- 使用合成成功合约交易，经 v2 block reader 和 chain store 保存完整证据后才 Confirm；并发确认仅一个 receipt/确认审计。缺证据、同交易多入金、晚付、错误金额、禁用用户、历史窗口重叠不得自动确认；审计插入失败则 receipt/状态整体回滚。数据库拒绝无 receipt 的 CONFIRMED、确认后脱离 receipt、修改 receipt 或删除审计。
- 请求/claim/报价快照在数据库层禁止篡改；损坏已保存块摘要或 Transfer 字段不能通过确认。存在其他 in-flight/UNKNOWN 未绑定单时自动确认停留待处理、不生成 receipt；恢复为不同金额的合法报价后原单可再次按链证据确认。
- 实际执行 `powershell -NoProfile -File scripts/test-integration.ps1` 全量通过，包含上述临时 PostgreSQL 测试及既有认证/开卡/迁移回归；`go test ./... -count=1`、`go vet ./...` 与 `scripts/check-generated.ps1` 通过。默认 go test 不设置 TEST_DATABASE_URL 时仍跳过数据库用例，不能替代这次单独执行的 PostgreSQL 验收。本轮没有执行真实支付、真实网关创建或公网 RPC 探测。
- Commit 错误不等于未提交：Prepare 按原用户/幂等键重读，BindQuote 按原单/claim/原报价重放，Confirm 按原单和同一 tx/log 重读；回调让上游重投。StartCreation 的 claim 结果不明必须重读，不能第二次 POST。claim 已保存但进程崩溃未记录结果的单必须进入后续人工恢复/告警流程，不能误当成安全可重试。

### 16.3 明确剩余顺序与风险

本节为第16阶段结束时的顺序；核验租约和回调关联已在第17节推进，当前剩余执行顺序以17.3为准。

1. **下一步任务编排**：先实现持久租约/领取/重试/重启恢复、创建失败分类与 claim 结果不明处理；CREATE 不作通用自动重试。实现 VERIFY_CALLBACK 关联原单/报价，早到通知等待报价，错配保留审计；回调只能触发独立链证据任务。现有任务表没有租约字段/runner，未匹配链事件没有业务归属处理器。
2. 实现 VERIFY_CHAIN 和链扫描结果关联、EXPIRED/晚付隔离、UNKNOWN 只读人工恢复管理与告警；禁止塞入 admin JWT 自动搜索并猜订单。人工复核授权/审计、未匹配资金处理及恢复策略必须单独验收。密码/会话变化影响新建授权，不抹掉已收到链事实；当前自动确认要求用户仍 ACTIVE，禁用用户仅留待审。
3. 再接安全运行配置、迁移部署检查、Gin 已认证用户接口及持久回调 handler；worker 不抢满连接，不向公网开放数据层 Confirm。完成 Vue3 订单/付款页、查询状态、过期/异常提示，做端到端无真实资金故障注入。仍须明确公开 HTTPS 回调入口、Epusdt checkout origin/反向代理路径、限流、payload 访问/保留、最小数据库权限及容量策略。
4. 全部业务链路和开关准备好后，才通知用户提供专用 **Nile `T...` 公共收款地址**，审核测试代币合约/decimals、钱包导入和小额收款验收。**此阶段仍不需要钱包；网关收款保持禁用，没有真实用户付款或主网操作。**

兼容性注意：运行新后端前应用库必须受控升级到8，Ready 拒绝旧版/dirty；本轮并未执行这一升级。历史迁移测试先移除空第8版业务表，再按第7/6版原流程回退；存在实际金融证据时禁止套用测试降级。v1/v2 链证据按原版本重新校验，不静默升级。当前链来源仍是可信 Solidity RPC 而非独立共识证明，未知 native 交易类型仍会停扫；receipt 数据层完成不代表扫描兼容所有 Nile 区块，更不代表已可上线收款。

地址+金额+时间窗口是本地归属策略，不是付款者身份的密码学证明；有界隔离期不能保证旧付款者永远不会更晚付款。隔离结束后复用相同地址/金额仍可能误归属极晚旧款，生产前必须明确更严格的复用/人工处理策略或独立收款地址方案，不能把当前24小时测试隔离默认为主网安全政策。

UNKNOWN 范围级确认阻断有明确可用性代价：一个挂起创建可能暂停同资产范围的其他订单确认。runner 上线前必须验收创建背压、挂起/恢复告警与人工处置时限，不可用“跳过 UNKNOWN”换取可用性，也不可把普通创建 worker 的超时当成明确失败来释放阻断。

部署当前新版后端前应用数据库需要受控升级到10（第14至17节记录6/7/8/9为历史阶段版本）。本轮仅临时库升级/清理，现有 Fcard 库未变更。扫描/任务模块尚未注入 server 或自动启动，Fcard 收款未启用；没有真实支付、用户余额、主网、提现功能。

## 17. 核验任务租约、恢复及回调关联（2026-10-13）

### 17.1 实际实现证据

- `migrations/000009_deposit_job_leases.up.sql/down.sql`、`internal/storage/storage.go`：SchemaVersion=9；任务增加 lease_token/lease_until/短码 last_error，约束非 CREATE 的 IN_FLIGHT 必须持有完整租约，其他状态清除租约。up 遇到旧版核验 IN_FLIGHT 拒绝自动升级；down 拒绝丢失活动租约或已使用的核验领取/恢复状态。未升级现有应用库。
- `internal/storage/queries/deposit_jobs.sql`、生成的 `internal/storage/db/deposit_jobs.sql.go`：SKIP LOCKED 领取、PostgreSQL clock_timestamp 到期判断、token CAS；恢复/耗尽按 kind 隔离，每批最多100条，跳过持锁任务。领取提交后才执行，结果不明不能假设未领取。只有 VERIFY_CALLBACK/VERIFY_CHAIN 进入该队列；CREATE 永远不进入普通租约重试。
- `internal/deposit/job_leases.go`：10秒..5分钟租约、1..20次预算、1秒起的指数退避及至多1小时退避上限；锁任务后续租/完成/重试，过期或旧 token 失去写回权。重建 store 从持久租约恢复，预算耗尽进入 MANUAL_REVIEW。仅保存受约束短码，不将网关/数据库原始错误、URL 或秘密写入 last_error。
- `internal/deposit/callback_worker.go`：Process/ScanOnce/Run 独立实现早到/未知订单等待报价、事实/原始 payload 摘要核对、不可变报价复核及 epusdt.MatchNotification。错配、无报价失败单、待审订单或损坏 inbox 持久待审；合法线索原子创建订单+tx hash 唯一 VERIFY_CHAIN 任务、审计和回调任务完成。没有给订单贴“付款成功”，没有选择 log index/查询真实节点/增加金额。
- 回调 worker 读取首次已认证的持久事实，而非要求历史密钥永久在线；保存的原始 payload/hash/密钥版本不覆盖。`internal/deposit/inbox.go` 规范化 tx hash 到小写，首次 HMAC/EventID 保持原始参数语义，避免合法大写 hash 在 PostgreSQL 约束下反复503；大小写/币种大小写不同的合法通知可对应同一个链任务，但原始事件分别保留。
- `internal/deposit/receipt_store.go` 和 FinishDepositChainJobs 现在仅完成已确认订单+已确认交易的链任务，并清除对应租约；其他交易线索不会因同订单已确认而丢弃。旧链任务 owner 后续 Retry/Finish 不得覆盖已提交确认结果。
- Run 单步超时不大于租约，空闲/失败轮询1秒..1分钟，连续故障预算1..20；父 context 取消退出，持续数据库故障返回错误供宿主告警。仍未注入 server、未运行实际扫描/回调 worker、未注册 Gin 收款路由。

### 17.2 实际验收与故障边界

- `job_leases_test.go`：退避上限、CREATE/非法 kind 排除、构造及 Run 参数校验、取消退出。
- `job_leases_integration_test.go`：PostgreSQL 临时库8并发仅一个 owner；续租不增 attempts；过期 token/恢复前和恢复后的旧 owner 无法完成/修改；新 store 恢复、kind 隔离、跳过锁行、恢复后的延迟领取及重试/过期预算耗尽。CREATE 一次 claim 不进入租约恢复或自动重新创建；已使用 lease 状态不可降级。
- 回调场景覆盖早到后报价恢复、未知订单预算待审、报价错配、EXPIRED/禁用用户仍保存链核验线索、失败无报价、摘要损坏、审计插入失败整体回滚、过期 owner 在创建任务/审计后仍无法提交、并发不同合法事件规范化为同一链任务。每个回调编排测试都断言未生成 deposit_receipts。
- 使用合成 v2 链证据的确认测试检查：真实 receipt 事务只完成本交易的 IN_FLIGHT 链任务、清除 lease 并阻止旧 owner 写回；其他交易 PENDING 线索保留。旧版无 lease 核验 IN_FLIGHT 的迁移 up 拒绝且不半改 schema；人工标记复核后允许临时库继续升级；关闭临时 pool 验证 Run 的故障预算退出。
- 本轮最初 PostgreSQL 测试因 Docker daemon 未运行失败；已启动本地 Docker Desktop 和原 PostgreSQL15测试服务，未启动 Epusdt 容器。数据库测试只建立/删除临时库，不在原 Fcard 库执行迁移。全部币/地址/回调/链证据均为合成 fixture，没有公网 RPC、真实网关创建或支付。
- 最终 `powershell -NoProfile -File scripts/test-integration.ps1` 全量通过（包含新增租约/回调/迁移测试和既有认证/开卡回归）；`go test ./... -count=1`、`go vet ./...`、`scripts/check-generated.ps1` 通过。默认无 TEST_DATABASE_URL 的 go test 会跳过数据库验收，不能替代本轮显式临时 PostgreSQL 测试。

### 17.3 后续执行顺序及待验证事项

本节为第17阶段结束时的顺序；链任务消费与候选补偿已在第18节推进，当前剩余顺序以18.3为准。

1. **下一步 VERIFY_CHAIN 消费与补偿关联**：只引用可信扫描器已保存的完整交易证据，缺证据保留任务等待补扫；检查全交易/付款窗口/UNKNOWN 范围阻断，再原子提交唯一 receipt 和该交易的任务结果。并发两个不同 tx/log 指向同一订单及同一事件指向多单、过期租约期间财务写回、无 callback 的实际链入金/晚付/未匹配池都必须独立验收。不能根据回调缺失的 event_index 猜一条日志或按用户提交 hash 直接确认。
2. 实现 CREATE 的专用非重试消费器：创建前必须已落库并取得一次性授权 claim，结果不明不第二次 POST；claim 提交结果不明只重读、不复用为新的发送许可。为 scope 级 UNKNOWN 加创建背压、告警和受控只读人工恢复；普通核验任务预算/租约不可套给 CREATE。当前并未调用 Epusdt 创建接口。
3. 再完成订单到期、人工待审/恢复授权与审计、未匹配资金展示和告警、最小运行配置及池容量策略，再注入 Gin/进程启动和 Vue3 页面。回调早到达到预算后不会因重复投递或后来绑定报价自动复活，需明确受控恢复政策；不同实例必须使用相容的同 kind 租约/预算政策，不能用调大预算或伪造 last_error 绕过人工状态。
4. 最后才做真实小额 Nile 联调：届时通知用户提供专用 **`T...` 公共收款地址**，核验测试代币合约/decimals、钱包白名单和 HTTPS 回调/checkout origin。**当前仍不需要钱包或测试币；不索取私钥、助记词或密码。**

兼容性/风险：新后端只接受 schema9且非 dirty，生产迁移拒绝可能留下 dirty，需要人工核查，禁止自动 Force/清理任务绕过。当前完成的是租约和回调提示的编排，不是实际链任务执行或已上线支付网关。Process 提交错误必须按原 job 状态/token 和去重任务重读，不能假设已回滚；旧 token 不能覆盖新 owner。Run 的过程取消/临时库重建测试不等于实际容器 SIGKILL/断电、数据库提交响应丢失或故障网络验收，后续启动真实 worker 前仍需这些测试。历史不可变数据/原始 inbox 的访问权限、保留/归档、人工资金归属与地址金额复用的极晚付款风险仍需按第16节单独落实。

## 18. 链核验任务消费与无回调候选补偿（2026-10-13）

### 18.1 实际代码证据

- `migrations/000010_chain_transaction_index.up.sql/down.sql`、`internal/storage/storage.go`：SchemaVersion=10。新增 chain_scan_transactions，按环境/创世块/network/scanner/tx_hash 定位完整原始已保存批次，引用对应 chain_scan_blocks，并保存高度/块 ID/时间/receipt 摘要。不仅索引有 Transfer 的交易，也索引已审核的 native/失败/无目标日志交易，避免“知道没有入金却只能一直等 proof”。
- 迁移从已有批次 JSON 回填索引，兼容旧 v1 空块的 Transactions=null；无效 manifest、缺 hash/重复同 scanner tx 等会拒绝。回填只是定位元数据，不是新链证明：worker 仍按原 evidence_version 重新验证完整批次/receipt 摘要及所有目标 Transfer。现有索引非空时 down 拒绝删除；CLI 失败可能留下 dirty，不能自动 Force。
- `internal/deposit/chain_store.go`、`internal/storage/queries/chain.sql`：块、所有交易定位索引、全部目标日志及游标在同一 pgx 事务提交；失败不推进。历史原样重放同时核对索引数量/内容，缺失或变动的索引不能被静默忽略；同 scanner 同 tx 出现在不同块会形成冲突，而不是覆写旧定位。
- `internal/storage/queries/deposit_chain_jobs.sql`、生成的 `internal/storage/db/deposit_chain_jobs.sql.go`：按 tx 查已保存批次，无需全表逐块解码 JSON。校验 scanner 合约和游标已覆盖高度；不同 scanner 的定位元数据不一致拒绝自动确认，v2 优先并按原版本复核。核对索引/块记录/完整交易、receipt 摘要、所有目标 Transfer 一致，缺记录返回待补扫而非编造付款证明。
- `internal/deposit/chain_worker.go`：VERIFY_CHAIN 的 Process/ScanOnce/Run 校验 job 的订单、去重键和规范化 tx hash，拒绝未知字段/event_index 指令；不根据 hint 猜 log index。先验证完整已保存交易，再用 MatchTronPayment 对全交易选择唯一符合的事件；无目标入金/native/失败、歧义、自转/出金、错金额/窗口、禁用用户或 reservation 重叠进入待审，未解析的 UNKNOWN 范围继续阻断自动确认。
- `internal/deposit/receipt_store.go`：抽出事务内部确认逻辑，保留已有内部 Confirm 语义；worker 不再调用会自行提交的 Confirm，而在同一事务完成唯一 receipt、CONFIRMED/待审状态、资金审计、链任务审计及最后的 lease token/数据库时钟 CAS。过期 owner 即使已经插入 receipt，也不能提交；所有中间财务写入整体回滚。同一已确认订单的其他 tx 只标记该任务待审，不替换 receipt 或改坏订单状态。
- `internal/deposit/chain_candidates.go`：范围固定为审核的环境/创世块/合约，每次最多100个“订单+tx”候选，按已保存 Transfer 的地址/实际金额及历史 reservation 窗口关联；每候选锁订单后去重保存任务和 SYSTEM 审计，不需要 callback/inbox。同交易多个日志仍只排一个任务，由完整交易核验决定歧义；历史已确认/待审订单的新候选也保留任务，DONE/MANUAL_REVIEW 不自动复活。此模块只有 ScanOnce，周期调用/启动/监控尚未注入运行进程。
- 真实窗口外的匹配收入保留并待审；订单 EXPIRED 仍可确认窗口内、只是迟到补扫的有效证据。没有合适候选的错金额/早付/隔离外/未知钱包日志仍完整留在 chain_transfers，本轮未实现这些事实的人工归属界面。候选关联是本地策略，不是付款者身份的密码学证明；第16节极晚旧款误归属风险依然需要更严格复用策略或独立地址方案，不能按本轮补偿模块直接启用生产收款。

### 18.2 验收证据与边界

- `chain_worker_test.go`：任务订单/去重键绑定、规范化 hash、禁止额外 event_index/外来 inbox/尾随 JSON、构造及取消 Run。
- `chain_worker_integration_test.go`：实际 PostgreSQL 临时库测试缺 proof 的预算等待/待审、有效完整 v2 证据确认、早/晚付、同 tx 多入金、自转、错误金额/合约、native/失败无目标日志、禁用用户、摘要损坏和缺 Transfer；已保存 UNKNOWN 未绑定报价先等待，恢复为不同实际金额报价后可重领原任务确认。
- 在任务已经过期，及 receipt AFTER INSERT 人为延迟至租约失效两种情形，receipt/订单状态/付款与任务审计都回滚；外层 CHAIN_VERIFIED 审计插入失败也整体回滚，然后同去重任务重试只生成一个 receipt。source 扫描证据保持已提交，不因业务回滚而丢失。
- 没有任何 callback 的真实临时库候选补偿测试：6并发发布仅一条任务/首次发布审计；窗口内有效证据确认，晚付/重叠归属不确认；同事件对应两单不自动挑一个赢家；已完成或待审任务不自动重排。两个不同有效 tx 并发指向一单仅一个 receipt/付款审计，另一任务保留为 ORDER_ALREADY_CONFIRMED 待审。
- 第10版迁移测试：已有索引拒绝 down，缺索引的历史块重放拒绝；在临时库模拟旧 schema 后从 v1 有日志块及 Transactions=null 的空块回填定位，再通过查询复核。原第6/7版块/日志/游标故障回滚、重启/重放及历史认证/开卡迁移回归仍保留。
- 全部地址/代币/回调/交易为合成 fixture，SQL 等待/损坏操作只作用于临时库。没有执行真实网关创建、公网 RPC 探测、用户转账或现有应用库迁移；没有调用 Airwallex、增加余额/卡额度、实现提现。本轮并未启动 Epusdt 或改变其收款配置，Fcard 的扫描/任务模块仍未注入 server/Gin。
- 最终 `powershell -NoProfile -File scripts/test-integration.ps1` 全量通过，包含新增链消费/候选补偿/交易索引及既有认证/开卡/扫描/迁移回归；新增缺索引历史重放断言后，又单独执行该迁移用例通过。`go test ./... -count=1`、`go vet ./...`、`scripts/check-generated.ps1` 通过。默认无 TEST_DATABASE_URL 的 Go 测试会跳过数据库，不能替代本轮显式 PostgreSQL 临时库验收。

### 18.3 下一步与待验证风险

1. **下一步专用 CREATE 消费器**：明确启用开关、冻结/创建背压、订单授权版本和 quote/钱包白名单校验；先落库、一次 claim、再网络请求，成功 quote 按原 snapshot 绑定。claim/POST/保存结果不明不第二次 POST，也不让普通租约恢复复活 CREATE；实现受控 UNKNOWN 只读人工恢复和告警。真实创建/钱包导入尚未授权启用。
2. 再做订单到期、人工待审/归属/重新排队权限与审计、未匹配/重复付款展示、健康告警和安全配置。候选补偿预算耗尽不能抹掉 source 事实；实际多实例运行需审核统一的同 kind 租约/重试政策，覆盖租约过期、已有资金事实的人工恢复、创建结果响应丢失和钱包替换事故。
3. 上线前落实进程受控启动、数据库10迁移、池容量/单步预算、周期候选补偿（当前只有 ScanOnce）、最小 DB 权限、原始证据访问/归档、公开 HTTPS 回调和 checkout 来源。再接 Gin 用户接口/持久回调与 Vue3 页面；仅内部使用验证交易事实的 worker，禁止开放任意 hash/proof 上传即确认的入口。
4. 端到端无真实资金故障注入通过后，真实小额 Nile 支付前通知用户提供专用 **T... 公共收款地址**，再审核测试合约代码/decimals/钱包白名单。**现在仍不需要钱包、测试币、私钥或助记词。**

迁移风险：对已有大规模 journal 的 SQL 回填可能增加事务时长、磁盘与锁压力，必须先测容量/维护窗口；本轮只验证小型临时库，未验证生产规模。索引不是对旧数据的重新认证，冲突/损坏不能靠删索引、重置游标或跳块修复。持续扫描的未知 native 类型、单一可信 Solidity RPC 而非独立共识证明、实际容器强杀/断电/提交响应丢失，以及隔离期后地址金额复用仍是待验证项；租约 CAS 的 SQL 延迟测试不是这些实际环境故障的替代品。

## 19. 专用 CREATE 消费器与失联 claim 冻结（2026-10-13）

### 19.1 实际实现证据

- `internal/deposit/create_worker.go`：新增 `NewCreateWorker/Process/ScanOnce/RecoverStaleClaims`。构造要求非空、最多100个、Base58Check有效的 T 开头收款地址白名单，拒绝零地址；只复制公开地址，不接收私钥。创建开关沿用 OrderStore.Enabled，当前仍未注入 server，未启用实际收款。
- `internal/deposit/order_store.go`：StartCreation 复用内部事务入口，消费器在一次性 claim 事务中校验已保存订单配置、商户/公开来源、用户当前授权版本及任务不可变请求。锁订单、范围 advisory lock 和 CREATE 任务后，要求 PENDING/attempts=0/无 claim；只有本次事务 Commit 返回成功才允许调用一次 client.Create。已有 token、提交失败或提交结果不明均不获得发送许可；普通核验租约永不接管 CREATE。
- `internal/storage/queries/deposit.sql` 与生成的 `internal/storage/db/deposit.sql.go`：新增有界待创建任务查询、CREATE 行锁、失联 claim 查询，每批最多100条。待创建任务与订单状态交叉核对，不持有数据库事务跨越 HTTP 请求。当前 schema 仍为10，无新增迁移。
- 消费器专用范围背压：同环境/创世块/网络/合约存在已 claim、未绑定报价的 CREATING 或 CREATE_UNKNOWN 时，不发送其他创建请求。这是保守的范围级串行化，不是按钱包金额精细调度；低层 StartCreation 保留原有内部数据层语义，未来运行进程必须只经消费器发送 HTTP。
- 请求结果不明、断连、取消、非白名单钱包、报价无效或绑定失败均不能重发 POST。已知明确拒绝才进入 FAILED；其他结果进入 CREATE_UNKNOWN/人工队列。HTTP context 已取消时使用独立、有界的10秒 context 保存结果，避免随请求取消丢弃冻结写入；数据库不可用时保留既有 claim，仍阻止重发。
- `RecoverStaleClaims` 要求阈值1分钟至24小时，只把过期的已 claim、未报价 CREATING 改为 CREATE_UNKNOWN，保留原 token/时间、审计和范围冻结；不清除 claim、不新建 ID、不查询或重新调用网关。即使创建开关关闭，也可执行这种只落库的安全冻结。原始报价若稍后可信地找回，仍需按原 claim 和快照处理，不能用“查不到交易”解冻或重新 POST。
- `internal/epusdt/client.go`：新增可选 CheckoutOrigin。明确区分私网 API BaseURL 与公开 HTTPS checkout 来源，显式来源只能是 HTTPS 根地址，报价必须严格同源；MatchesMerchant 校验 Nile/PID/来源，不返回密钥。省略时保留旧客户端同 BaseURL 的校验行为，创建消费器仍要求与 OrderStore 的 HTTPS 来源一致。

### 19.2 验收证据及未验证边界

- `create_worker_integration_test.go` 使用真实临时 PostgreSQL + 本地 httptest 网关计数器：6个并发消费及随后重放最多一次 POST；验证成功绑定、503、服务端接收后关闭连接、请求取消、明确拒绝、非白名单钱包、报价审计失败、claim 审计失败、已 claim 后重建消费器不发送、请求载荷额外字段、授权版本变化、关闭开关与配置漂移。结果不明和已 claim 状态阻止同范围第二单发送；所有场景均没有 receipt 或余额入账。
- 失联恢复测试：新 claim 不回收、无效阈值拒绝、恢复审计失败整体回滚、再次恢复幂等、token 保持不变且仍不能发送。测试仅在临时库暂停不可变触发器制造两分钟前的 fixture，之后立即恢复；这不是生产操作指引。
- `internal/epusdt/checkout_test.go` 验证不安全/带凭证/路径/query/fragment 的公开来源拒绝及环境/PID/来源绑定；CREATE 配置单测验证空依赖与失联阈值。
- 本轮全量 `scripts/test-integration.ps1` 通过（临时 PostgreSQL，含已有认证/开卡/迁移/链任务回归）；新增 claim 审计失败断言后又单独执行 TestCreateWorkerIntegration 通过。`go test ./... -count=1`、`go vet ./...`、`scripts/check-generated.ps1` 通过。默认 Go 测试仍不能替代显式数据库测试。
- 没有调用真实 Epusdt 创建、公开 RPC、Airwallex 或任何支付，没有导入钱包、变更 Docker 收款配置或迁移现有 Fcard 应用库。地址/金额/网关交易均为合成 fixture。未通过故障代理制造 PostgreSQL COMMIT 响应丢失，也未验收实际容器 SIGKILL/断电；claim 审计回滚及服务器断连测试不能替代这些故障。

### 19.3 后续实施顺序与兼容风险

1. **下一步受控人工恢复及可观测性**：实现 Fcard 管理员授权、独立审计与只读网关证据核对；恢复必须引用原订单/claim、经审核的原始报价与钱包配置，不引入常规客户端管理 JWT、不自动重 POST。失联冻结只是安全兜底，不代表已完成管理恢复接口或告警投递。
2. 补充创建前不匹配/失效用户的持久人工待审分流、短错误码、运维列表与告警。目前 Process 返回错误、事务回滚；ScanOnce 遇非背压错误停止并交给宿主，失败待创建任务保持原状态，不应无限快速轮询。并发扫描遇已被另一消费者 claim 的单也可能返回 InvalidState，宿主应先重读状态、不可重新发送。CREATE 尚无 Run/定时恢复调度。
3. 再落实订单到期、已有人工队列受控重排、未匹配资金展示及统一运行配置/资源预算，然后接进程生命周期、Gin 持久回调与 Vue3 页面。内部 BindQuote 仍是数据层方法，不应作为公开上传任意报价的 API；真实运行启用前必须让所有报价恢复入口执行同样的钱包审核。
4. 配置兼容：私网 HTTP API 只允许原客户端许可的本地测试主机；公开 checkout 仍强制 HTTPS。需要验证 Epusdt/反向代理返回的是配置的公开 checkout URL，不是容器内地址；本轮没有改网关代理或自动放宽 TLS。当前比原网关更严格的 scope 背压会降低并发量，不得靠清 token/跳过 UNKNOWN 提升吞吐。PID 与环境可以校验，配置中 Secret 是否属于该商户及地址是否确实由用户控制仍需部署侧人工审核。
5. 端到端无真实资金故障演练后，**实际小额 Nile 联调前再通知用户提供专用 T... 公共收款地址**，审核测试代币合约/decimals/白名单/HTTPS 及地址复用政策。现在仍不需要钱包或测试币，永不索取私钥、助记词、钱包密码。第16/18节的极晚付款误归属、未知 native 类型和单 RPC 信任边界仍未消除。

## 20. 收款应用接入与 Nile 联调门槛（2026-10-13）

### 20.1 已实现的实际证据

- `internal/deposit/service.go` 与 `internal/storage/queries/deposit_management.sql`：用户分页/本人订单查询、管理员订单/claim/审计查看、最近100条人工任务及尚无 receipt 的链事件、运行计数；普通用户 DTO 不包含 claim、商户密钥、内部任务或原始回调。公共创建最多保留每用户5个未解决订单，授权后的用户行锁下检查容量，原幂等请求不受容量上限影响。
- 管理员 `RecoverQuote` 在原 BindQuote 的同一事务内重新锁定有效 Fcard 管理员及会话，校验原 claim、环境/创世块/合约/商户/公开 checkout 与收款钱包白名单；至少8字符的原因/证据引用和 request_id/quote_hash 落独立 ADMIN 审计，失败不部分绑定。**仅保存人工审核的原始报价，不调用上游创建、不生成 receipt。** 管理员被信任为证据审核人，此入口不是上游签名查询，也没有密码学证明报价来源；禁止用户使用、禁止把猜测报价或“查无交易”当作恢复证据。
- 管理员核验重排只允许 MANUAL_REVIEW 的 VERIFY_CALLBACK/VERIFY_CHAIN，事务内保存原 attempts、原因、request_id 后重置核验预算；CREATE 永远不能重排。重排仍由完整链证据和既有归属政策决定，不提供管理员直接确认/任意 hash 上传入账入口。人工重复重排是显式授权操作，不是无限自动重试。
- `create_worker.go` 已补创建前载荷/配置/授权失效的持久待审分流：无 claim 的订单和 PENDING CREATE 在同一事务改为 REVIEW_REQUIRED/MANUAL_REVIEW，并保存短错误码与 SYSTEM 审计。已有 claim 不降级或清除；并发 ScanOnce 遇已被其他消费者 claim 的订单先重读，跳过而不再次发送。第19节关于失败待创建任务仍全部保持原状态的描述已被本阶段取代。
- `Service.Expire` 有界领取到期 WAITING/VERIFYING，再逐单锁行、重查并原子保存 EXPIRED/审计；保留报价、reservation、链任务与 source 证据。到期不是清空隔离期、取消真实已付款证据或增加余额。
- `internal/deposit/settings.go`、`.env.deposits.example`、`internal/config/config.go`：独立 runtime/create 双开关默认 false；必须明确 Nile、已审核测试合约、六位小数、有效公开钱包白名单、稳定 scanner、显式高度/块 ID 起点及 HTTPS callback/checkout。创世块固定为实际审核的 Nile ID。真实配置在忽略的 `.env.deposits` 中，不输出商户 Secret；模板不包含钱包/币/密钥默认值。
- `internal/deposit/runtime.go`：统一托管逐块 scanner、回调核验、链核验和维护四个协程，周期失联冻结/到期/候选补偿/CREATE 与安全计数告警；父 context 取消或任何 worker 预算耗尽/不可信证据使运行停止并等待所有 worker 退出。server 随支付运行失败关闭 HTTP，避免静默失效后继续接单。核验租约30秒、单步10秒、最多10次；维护/扫描单步30秒、轮询3秒、连续失败上限5，DB 池沿用10连接及10秒 statement_timeout。所有批查询最多100条；仍需实际负载测试，不能把这些参数视为生产容量保证。
- 创建准入要求运行中、scanner 已追平观察到的固化 head、最近成功追平小于30秒，且已存最后一块时间小于2分钟；启动、落后、过期或停止时关闭用户创建/CREATE 消费。初始 checkpoint 尚无已存新区块时不开放，必须先实际扫描到至少一个新块。关闭 create 开关仍保留查询、回调及核验/冻结；默认 runtime=false 则不初始化 scanner、不挂回调、不发链请求。
- `internal/http/deposits.go/router.go`：用户 `/api/v1/user/deposits/config`、`GET/POST /deposits`、`GET /deposits/:id`，沿用独立用户会话与改密要求；管理员 `/api/v1/admin/deposits`、`GET /deposits/:id`、`POST /deposits/:id/recover-quote`、`POST /deposit-jobs/:id/requeue`。公共持久回调固定 `/api/v1/deposits/notify`，验签及提交 inbox 后才返回纯文本 ok；关闭新单不丢弃合法旧通知。原 New 构造兼容既有开卡/认证，server 使用 NewWithDeposits 注入。
- Vue3 `/portal/deposits`：精确字符串金额、会话存储的原幂等提交、超时/刷新后只重放原 Fcard 请求，轮询本人状态；仅 WAITING 且未到期才展示实际金额/钱包/HTTPS checkout，客户端时钟到期自动隐藏。UNKNOWN/待审不给付款指令，CONFIRMED 明示未增加余额或卡额度。
- Vue3 `/deposits`：管理员状态计数、原 claim/审计、需明确确认与证据引用的报价恢复、仅核验任务重排、未归属事件展示；没有 CREATE 重试或自动资金归属按钮。新增导航导致低高度侧栏退出按钮溢出的回归已修为可滚动侧栏。前端金额校验用 BigInt，不用浮点乘法。
- `cmd/deposit-check`：默认只解析配置，**不联网、不写数据库**；显式 `-rpc` 才读 Nile genesis/固化 head/已审核 checkpoint，要求 create=false、固定 Nile identity、head 新鲜且 checkpoint 一致。输出仅公开链数据，不检查代币 bytecode/decimals 或钱包控制权；这些仍是下一阶段真实网络审核项目。

### 20.2 验收记录与安全边界

- `service_integration_test.go`：真实临时 PostgreSQL 的管理员会话、错误 claim/未批准钱包拒绝、恢复/重排/到期审计失败全部回滚、CREATE 不可重排、本人隔离与会话撤销；恢复原报价后仍无 receipt，只有随后保存合成完整 v2 链证据并执行 ChainWorker 才产生 CONFIRMED。验证每用户5单容量及未就绪时不产生 claim。
- `runtime_integration_test.go`：合成固化块 + 实际临时库的追平准入、create=false 保持不创建、取消后协程排空和准入关闭、未知合约交易 fail-closed 停止。`preflight_test.go/settings_test.go` 验证错误创世块/起点/陈旧 head、主网/不安全配置、create 开启时预检不发任何请求。未访问公网 RPC。
- `internal/http/deposits_integration_test.go`：真实管理员/用户会话及临时库，用户/管理员 realm 隔离、强制金额字符串、未知受控字段拒绝、原幂等键、本人404隔离与内部字段不泄漏、ADMIN 原报价恢复、HMAC 公共通知落库后 ACK、通知审计失败503且 inbox 回滚、用户退出后401。
- `frontend/src/lib/deposits.test.ts` 与 `frontend/e2e/deposits.spec.ts`：精确金额/付款展示门槛、角色令牌、无自动 POST 重试；真实浏览器的响应丢失+刷新后原键重放、WAITING 实际金额、默认禁用不读历史、无 CREATE 重试/任意未归属入账入口。
- 最终后端全量临时 PostgreSQL 回归、`go test ./... -count=1`、`go vet ./...` 和 sqlc 生成检查通过；离线 `go run ./cmd/deposit-check` 通过且报告 runtime/create 禁用。前端31个单测及构建通过；修复侧栏溢出后全量浏览器回归9通过、2跳过，其中新增收款用例3通过。真实后端代理/用户生命周期用例需要专用测试配置而跳过，不能称已验收真实浏览器到 Go 的支付链路。
- 当前仍为 schema10，本阶段未新增迁移，也未迁移现有 Fcard 应用库、启动实际支付 Runtime、创建真实 Epusdt 交易、配置测试钱包/代币或调用 Airwallex。最近全量数据库测试只创建/清理临时库。本机原 Epusdt 容器未被本轮重建或修改。
- 日志目前是结构化计数告警，不是已配置邮件/短信/告警平台；未实现人工强制归属、提现/归集、余额或卡额度。资金 source 保留、无 receipt 列表并不证明每条事件能自动归属，仍需审核晚付、重复、错误金额与地址复用。原始证据权限/归档和双人复核策略也未达到生产认证。

### 20.3 当前停止点：进入实际 Nile 测试准备

**无资金应用开发已推进到需要真实 Nile 数据/外部配置才能继续验收的位置；先收集专用 T... 公共收款地址，再进行下面流程。绝不索取私钥、助记词或钱包密码。**

1. 用户提供专用 Nile 钱包的公共收款地址。若已有测试 USDT 合约，提供公共合约地址；没有可后续只读审核/选择测试资产。需要测试钱包有测试 TRX 以支付 gas、以及对应的测试代币，转账仅由用户自己的钱包执行，不接收或保存私钥。
2. 先审核真实合约 bytecode/接口/六位 decimals、Nile genesis/固化 head、钱包用途、HTTPS callback 与 checkout 根来源；checkpoint 必须位于本次所有测试付款之前，不能为了绕过 unsupported native 随意后移。`.env.deposits` 根据模板私下填写，create 保持 false；凭证只在本机受控文件配置，不贴到聊天。
3. 执行 `go run ./cmd/deposit-check`；确认配置完整后才执行显式只读 `go run ./cmd/deposit-check -rpc`。接着对真实连续 Nile 块进行只读扫描验收。遇未知 native 类型立刻停止，依据真实 receipt 证据补充最小白名单政策和回归测试，**禁止跳块**。这一步尚未执行，可能发现真实网络兼容性问题。
4. 当前 Docker bootstrap **仍要求全部代币/钱包/链收款禁用**，不能在管理页直接启用。真实资产/钱包审核后，下一阶段才实现/验收受控网关启用配置、公开 HTTPS app_uri/checkout 与回调链路；不绕过 bootstrap、不放宽 HTTPS/主网隔离、不自动填入猜测合约。仅部署 Docker 不会让当前配置可收款。
5. 对测试专用应用库先备份并显式升级到非 dirty schema10，不自动升级用户现有库。开启 runtime、关闭 create，验证追平准入/回调/核验/停止告警，再确认开启单个小额测试收款。用户严格按返回的 actual_amount 付款；测试确认、重复回调、无回调补偿、晚到证据、超时/UNKNOWN 与原交易恢复。最后才演练容器 SIGKILL/提交响应丢失等真实故障；当前 SQL 回滚/合成链/HTTP 断连用例不是这些事故的替代品。

兼容风险：新页面依赖新增 Gin 路由，旧后端只会404；新 server 仍拒绝旧/dirty schema。runtime=true 会初始化稳定扫描起点并开始公开 Nile 只读请求，因此必须明确启用，不应在普通开卡环境随意设置。当前只允许 nile.trongrid.io 单一 Solidity RPC，尚无独立共识认证；固定30分钟最长报价/24小时历史隔离不是生产极晚付款保证。人工报价恢复是受权限控制的人工信任流程，不是自动上游对账。默认关闭和测试通过都不等于可开启主网或真实资金。

## 21. 用户地址配置与真实 Nile 合约只读观察（2026-10-13）

- 用户已提供公共收款地址和测试合约 `TXYZopYRdj2D9XRtbG411XZZ3kM5VkAeBf`。公共钱包的 Base58Check 校验通过，保存于 Git 忽略的根目录 `.env.deposits` 的 DEPOSIT_RECEIVING_WALLETS；合约保存于同文件 DEPOSIT_CONTRACT。没有存储私钥或助记词，也不把用户钱包写进跟踪文档。
- 2026-10-13 15:20:42 UTC，通过 `https://nile.trongrid.io/walletsolidity/getblockbynum` 先核对高度0的实际 genesis 与固定 Nile ID 一致，再经 `/wallet/getcontract` 读取指定合约。返回地址与请求完全一致，name=TetherToken，bytecode=15575字节，ABI包含 decimals 与 transfer。
- `/wallet/triggerconstantcontract` 对 decimals() 做只读 VM 模拟，result.result=true，唯一64位 hex返回值为6；字节码 SHA-256为 `2866816060782939f36b0f42dd695c197b72d4ea133f433a08575c9a528a93b5`。本轮未调用签名、广播、网关创建或支付接口。
- 机器可读观察保存于忽略目录 `.cache/tron-evidence-sources/nile-usdt-contract-observation.json`。这是当前 FullNode metadata/VM 模拟，不是 Solidity 固化交易证明、发行者真实性认证或钱包控制权证明；合约名称并不能证明正式 Tether 发行，仍只按用户指定的测试资产对待。
- DEPOSIT_RUNTIME_ENABLED、DEPOSIT_CREATE_ENABLED、DEPOSIT_ASSET_REVIEWED均保持false；未启用钱包/代币、未更改现有 Docker 或应用数据库。用户地址和合约已收集，但完整资产/部署审核尚未完成，不能把单次只读观察当成收款放行。
- 下一步需要公开 HTTPS callback 与 checkout 来源、明确的测试库/扫描起点，以及真实连续块兼容性和受控网关启用验收；在这些条件满足前不要求用户转账。第20节联调顺序与资金安全边界保持不变。

## 22. 实际 Nile 连续扫描、受控启用与用户转账准备（2026-10-14）

- 用户明确授权完成扫描起点/资产与钱包启用，并启动Fcard收款供其测试。`cmd/nile-review` 从付款前固化起点71804184连续只读审核，初次遇到资源委托/质押类型时停止，保存实际块和receipt；没有跳块、换scanner或后移游标。随后正式scanner从同一起点连续保存超过4500块，追平实际固化链头。
- 根据实际receipt及固定TRON协议枚举，新增明确的原生交易政策：DelegateResourceContract、UnDelegateResourceContract、FreezeBalanceV2Contract、UnfreezeBalanceV2Contract、VoteWitnessContract、AccountCreateContract、WithdrawBalanceContract、WithdrawExpireUnfreezeContract、AccountPermissionUpdateContract。只接受块中明确SUCCESS、匹配交易/块/时间且没有失败或日志的原生receipt，归类native_success，不生成TRC20入金；未知类型、缺成功、失败及异常日志继续停止。记录在忽略目录 `.cache/nile-live-review/`，类型观察不是用户付款证明。
- `TronRPC.SolidReceipts` 使用固定Nile Solidity按块receipt接口，检查重复JSON键及大小。reader要求receipt与块交易一一对应，不允许缺失、额外、重复、外来hash或错误高度。`ReadRange` 最多32块（runtime配置16块）、4个并发读取，按高度顺序完整核验；任何不可信窗口不返回部分批次。scanner逐块事务提交，提交响应丢失时重放原待提交证据，不重新取证改写事实。原单块模式保留。
- 再次只读核验Nile genesis、用户指定合约的固定bytecode SHA256、ABI和decimals=6，以及钱包Base58Check。`scripts/prepare-nile-receiving.py` 生成私有 `conf/nile-receiving.json`；钱包来自用户授权用途，没有索取或导入私钥，也没有完成独立的密码学控制权证明。bootstrap在SQLite事务内禁用其他链/资产/钱包，仅应用清单中的TRON测试合约和钱包；每次启动核对精确资产、精度、钱包集合、HTTPS来源及Nile RPC。没有清单的新环境仍禁止启用收款。
- PostgreSQL应用库原已为非dirty schema10、入金业务为空，本轮没有迁移或清库。启用前备份应用库和网关主/runtime库到 `.cache/nile-start-backup-20261014-114925/`；商户和管理员凭证保持不变。网关按受控清单重建，后台仍不经公网开放。
- Fcard先在18082关闭创建验证连续扫描，再在追平后替换原8081开发进程。当前私有配置runtime/create/asset_reviewed全true，单笔新申请上限1测试代币，前端默认0.1；同幂等键历史读取不因降低限额失败。`/health/deposits` 仅返回可用、就绪、创建、金额上限等公开运行标志；无钱包、商户或订单数据。进程日志/PID在 `.cache/nile-server/server-8081.*`。
- 本机及实际公网检查 available/ready/enabled 均true；Fcard health和网关公共配置JSON200，收银台301到/cashier并返回HTML200；无效回调空JSON返回400且未写inbox。公网后台/切通道返回404。前端31单测、构建、3个收款浏览器用例通过；最终全量临时PostgreSQL回归通过，go vet通过，Docker构建中启动政策安全测试通过；实际网关7项契约检查通过，包括审核资产、Nile RPC、HMAC拒绝及安全URL拒绝，未创建订单。
- 最终状态记录时：原scanner游标71808742，保存4558块，订单/receipt/inbox均0，schema10非dirty。这只是启动及付款前验收；用户真实小额测试转账、正常有效回调落库、付款确认、无回调补偿和真实故障注入尚待执行。用户需先在 `/portal/deposits` 创建订单，WAITING时按返回的actual_amount和钱包在Nile上付款。确认只保存receipt，不增加余额、卡额度或调用Airwallex；主网、提现、归集仍未开放。
