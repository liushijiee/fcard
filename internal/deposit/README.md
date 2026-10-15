# TRON 资金证据基础（仅 Nile）

`CreateWorker` 提供一次性创建，已 claim 的订单永不自动重新 POST；失联只冻结，不清 token。`Service/Runtime` 已接管理员恢复/核验重排、到期、周期补偿、扫描就绪准入、Gin 与 Vue3 页面。默认 runtime/create 双开关关闭；真实 Nile 资产、钱包、HTTPS 与网关启用尚未验收，详见 `docs/Epusdt-plan.md` 第20节及 `docs/Nile-test-runbook.md`。

这是独立链核验、PostgreSQL 链证据存储/扫描及业务订单层。已实现受控 server 注入，但本机未启用或运行实际收款扫描。确认仍只是 receipt，不增加余额、卡额度或发起 Airwallex 请求。下列历史阶段记录按当时数据层边界描述，当前进度以计划第20节为准。

## 订单与确认数据层（迁移 8）

- `OrderStore.Prepare` 在同一事务内保存用户归属、幂等键、整数金额、链/合约/PID/URL/时间政策快照、固定网关单号、CREATE 任务和审计。重新锁定用户/凭据/有效会话，并核对会话 auth_version；同用户/键/金额重复返回原单，金额不同拒绝。`Get` 仅允许当前有效认证的订单所有者访问。
- `StartCreation` 只允许一次持久化 claim，并在 claim 前再次检查用户/凭据及订单授权版本；没有执行实际 HTTP POST。响应不明时 `CreateFailed(..., true)` 进入 CREATE_UNKNOWN/人工任务状态，不允许重新 claim、换单号自动创建。claim 后崩溃但没有回写状态的 CREATING 单同样不能再次 claim，需要后续人工恢复流程。
- `BindQuote` 核对原单、实际整数金额和上游尾差上限、Base58Check 地址、USDT、固定 HTTPS 网关来源及最长付款期限。保存原始 Base58 地址的规范化报价和十六进制链地址；重复绑定须完全一致。地址/金额 reservation 的事务 advisory lock 防止并发漏检；已失败/过期/已确认订单的历史隔离窗口也参与冲突判断，不自动释放旧归属。
- `InboxRecorder.Record` 重新验证原始 payload，拒绝与已验签事实不一致的调用参数；一个事务保存首次 payload/摘要/密钥版本、去重任务及首次审计，commit 后才返回 nil。密钥轮换或等值 payload 不覆盖首次证据；仅递增 deliveries。未知/早到/错配订单通知也保存，不受新单启用开关控制；ACK 不代表入金确认。
- `OrderStore.Confirm` 是内部数据层入口，不接受用户上传的 proof。它只引用已保存的 chain_transfers 和完整 block batch，重新验证版本化证据及摘要，再检查全交易 Transfer、精确金额、付款窗口、历史 reservation 和用户状态。缺证据返回 `ErrDepositProofPending`；早/晚付、歧义、错配、窗口重叠或禁用用户持久化 REVIEW_REQUIRED，不生成 receipt。
- 同环境/创世块/network/合约范围有已 claim 但尚未绑定报价的 CREATING/CREATE_UNKNOWN 单时，Confirm 返回 `ErrDepositAttributionPending`，不猜未知钱包/金额，也不生成 receipt。StartCreation/BindQuote/CreateFailed/Confirm 使用同一事务 advisory lock，避免未知占用检查和 claim/报价保存竞态。这是保守的范围级确认阻断，会影响其他用户的确认可用性；后续 runner 必须告警、施加创建背压并提供受控恢复，不能跳过该检查。
- 自动确认在同一事务内保存唯一 receipt、CONFIRMED 状态、任务完成和审计；同一引用重复确认返回原 receipt。数据库唯一键禁止一个 tx/log 归属多单，延迟约束要求状态与 receipt/链事件/报价一致，receipt 和审计 append-only。任何中间失败整体回滚；**确认没有增加余额、卡额度或发起 Airwallex 请求**。

当前 `SchemaVersion=10`，只在临时数据库验收，没有自动升级现有应用库。迁移 8 down 在任何订单或 inbox 存在时拒绝执行；迁移 CLI 拒绝可能留下 dirty 标记，不能自动 Force 绕过。租约、回调/链核验消费与扫描候选补偿已实现；CREATE 专用消费器、UNKNOWN 人工管理接口、订单到期任务和运行配置尚未完成。不能把这些数据层方法直接挂到公开路由来替代后续服务编排。

`order_store_test.go` 验证整数金额和报价快照；`order_store_integration_test.go` 使用 PostgreSQL 临时库验证并发幂等、认证/会话版本、一次 claim、UNKNOWN 禁止重建、报价重放/隔离、回调持久化 ACK/故障回滚/密钥轮换、已保存 v2 链证据确认、歧义/晚付/错配/禁用用户、审计失败回滚及数据库约束。所有币/钱包/交易仍为合成 fixture，不是用户真实测试付款。

## 核验任务租约与回调编排（迁移 9）

- `JobLeaseStore` 只领取 VERIFY_CALLBACK/VERIFY_CHAIN，CREATE 明确拒绝：后者仍由订单的一次性 claim 防止重发，本轮没有增加真实网关 POST。核验任务以 SKIP LOCKED 领取，提交 lease_token/lease_until 和 attempts 后才返回；续租/完成/重试先锁任务，再由数据库 clock_timestamp 检查 token 和有效期。过期或旧 token 不能写回。
- 核验任务指数退避有上限，最多1..20次；过期租约恢复和耗尽处理按 kind 隔离，每批至多100条并跳过行锁，重建 store 可从持久状态恢复。达到预算转 MANUAL_REVIEW，保留原始 inbox；last_error 只保存受约束的短代码，不存秘密或原始错误字符串。Claim/Commit 失败不能先执行任务，也不能假设未提交。
- `CallbackWorker.Process/ScanOnce/Run` 已实现独立事务编排：早到/未知订单有界等待报价，错配、损坏 inbox、无报价失败单或待审单记录人工复核；合法回调核对不可变报价，按订单+规范化 tx hash 创建去重 VERIFY_CHAIN 任务，并原子保存审计和回调任务结果。EXPIRED/禁用用户线索仍进入链核验队列，不增加金额；付款窗口、当前用户状态和唯一归属仍由后续链证据 Confirm 判断。
- 已验签原始 payload/密钥版本保留，worker 使用已持久认证事实与双摘要核对，不要求旧密钥一直存在才能处理历史 inbox。大小写 tx hash 在签名/EventID 中保持原表示，存储/链任务使用小写；规范化不改变原始 payload。不同通知事件可触发同一个链任务，但不能生成多个 receipt。
- 原订单确认时仅完成该订单+该交易的链任务并清除租约；旧链任务 owner 随即失去写回权限，同订单其他交易线索不自动 DONE。回调 worker 不选择 log index、不查询真实节点、不直接 Confirm；VERIFY_CHAIN 由下一节的 `ChainWorker` 独立复核已保存的完整链证据。
- Run 逐个处理，单步截止时间、空闲/失败轮询和连续故障预算有界；取消时退出，数据库持续故障返回错误供宿主告警。模块尚未注入 server，未启动实际后台任务，也未注册 Gin 路由或启用网关收款。

迁移9 up 遇到旧版非 CREATE 的 IN_FLIGHT 单拒绝，必须人工核查，不能自动赋予新 lease；down 在存在活动租约或已使用核验领取/恢复状态时拒绝丢失防重放信息。测试使用原始 SQL 的拒绝不代表 migration CLI 不会设置 dirty。UNKNOWN 对同资产范围确认的阻断仍然存在；早到任务达到预算后即使后来绑定报价，也不会被重复 callback 自动重置，需后续受控、审计的人工恢复流程和告警页面。

`job_leases_test.go` 与 `job_leases_integration_test.go` 验证退避/配置、并发独占领取、续租/过期/重启恢复/旧 owner、跳过锁行与预算耗尽、CREATE 排除、早到/未知/错配/过期/禁用用户回调、摘要损坏、事务故障回滚、规范化交易去重、确认后任务 fencing、其他交易线索保留及迁移安全拒绝。全部为合成回调和临时 PostgreSQL，不是实际钱包付款或外部网关联调。

## 链任务与无回调候选补偿（迁移10）

- `chain_scan_transactions` 给完整已保存 journal 的每笔交易建立定位索引，包括无 Transfer/native/失败交易；由 chain store 同块/日志/游标一起提交，历史重放检查索引一致。迁移回填兼容 v1 空块的 Transactions=null，旧证明必须按原版本再验证；非空索引拒绝 down，不能自动删除或 Force 绕过。
- `ChainWorker.Process/ScanOnce/Run` 只读可信扫描存储，不发 RPC/网关 POST；任务必须绑定原单/去重键/规范化 tx，不能指定日志索引来绕过全交易歧义检查。核对原版本完整批次、索引/journal 元数据和 SHA256、所有目标 Transfer，再计算唯一事件与业务窗口/归属。缺 proof 或 UNKNOWN 范围阻断有界等待；无入金/歧义/错配/窗口外/禁用用户/历史重叠待审。
- 事务内部 helper 保存 receipt/CONFIRMED 或待审事实、资金与任务审计，**最后**以仍有效的 lease token/数据库时钟 CAS 完成任务，再提交。过期或旧 owner 的所有财务写入回滚；另一交易不能替换同订单已有 receipt。原有内部 Confirm 不用于 leased worker，否则其独立提交会绕过资金/租约的原子约束。
- `ChainCandidatePublisher.ScanOnce` 按已保存同范围地址/实际金额/历史 reservation 窗口关联最多100个候选；每单锁后原子去重任务/审计，可在完全没有 callback 时处理合法收入。多个日志由完整交易核验，不挑第一条；晚付/重叠/其他交易线索保留待审。DONE/MANUAL_REVIEW 不自动复活。不同金额、早付、隔离外或未知归属仍留在 chain_transfers，人工处理 UI 尚未实现。
- 发布器周期调度、scanner/worker 进程启动、公开路由与真实钱包白名单均未注入/启用。本轮仅合成证据和 PostgreSQL 临时库验证，不增加余额或卡额度。精确金额+窗口是本地归属规则，不证明真实付款者身份；有限隔离后复用可能误归属极晚旧款，生产前仍需更严格地址/复用政策。

`chain_worker_test.go`/`chain_worker_integration_test.go` 验证任务绑定、完整证据与无日志定位、租约过期前后财务写回回滚、外层审计失败回滚、缺证据/UNKNOWN 等待、无回调候选幂等及两笔交易竞争同单。迁移/大规模回填、实际 SIGKILL/网络提交不确定性、未知 native 类型与单 RPC 信任仍不能视为已生产验收。

## 查询与核验

`TronRPC` 只查询 `/walletsolidity/gettransactioninfobyid`、`getnowblock`、`getblockbynum`；线上 URL 仅允许 `https://nile.trongrid.io`，不跟随重定向、不自动重试。HTTP loopback 仅可显式开启 `AllowLocalTestServer` 供 mock 使用，不能在正式配置中打开。

RPC 响应最多 4 MiB，拒绝重复 JSON key（包含嵌套/转义 key）、非法 UTF-8、尾随 JSON、过深结构、错误类型和节点 Error envelope；未知非重复字段可忽略，以兼容正常节点附加信息。错误不返回原始节点 payload。

`NewTronVerifier` 需要本地已审核的 Nile 创世块 pin、测试合约和 decimals=6。传入一个字符串或数字 **不等于代码已经验证合约真实性/decimals**；链上代码、代币元数据和管理审批仍需在受控配置启用前验证。

`ReadSolidTransaction` 检查：

- 独立 RPC 的创世块与配置 pin 一致；交易 hash 格式及 receipt ID 一致。
- receipt 包含区块高度/时间，合约 `receipt.result` 必须明确 `SUCCESS`；缺失不能默认成功。官方 protobuf 顶层成功枚举拼作 `SUCESS`，可省略默认值；顶层 `FAILED` 或合约失败均不能作为付款证明。
- receipt 所在高度不超出 Solidity 固化头；按高度重新查询的块包含该交易，区块高度/ID 前八字节/parent 高度/时间一致；头部与目标块同高或相邻时检查 ID/parent 一致。
- 仅解码已配置合约的标准 Transfer 日志，保留原始 log index。检查 32 字节 topics/data、地址 ABI 零填充；收款地址支持 Base58Check 或 `41` hex，不能只依靠字符串正则。
- 转账量按 uint256 精确十进制字符串保存，不经 float64/uint64 截断。超大、零值、非匹配金额仍可保留为待核查线索，不直接成为付款。

这里只信任受控 RPC 提供的 Solidity 数据；不是独立共识节点、区块头签名/Merkle/SPV 验证或多节点一致性证明。创世块 pin 也不能阻止一个恶意节点伪造其余响应。生产仍需要可信节点、备用源和一致性/故障演练。

`ReceiptJSON` 与 `ReceiptHash` 是当前代码保留的**规范化最小 receipt 模型**及 SHA256，不是原始 HTTP 字节摘要，不包含节点额外字段。后续存储层必须保存可复核的块/receipt 最小证据、查询来源/时间和保留策略；完整原始证据归档尚未实现。

## 订单匹配不是资金入账

`MatchTronPayment` 按本地订单的环境/创世块/tx hash/合约/收款地址/精确整数 units/付款窗口检查。窗口含起止端点；实际业务窗口与晚付隔离政策需固化到订单快照。

- 少付、多付或没有目标 Transfer 返回 `ErrTransferMismatch`，不加总、不容差。
- 同地址多个入金事件、自转账或同一交易有该地址的非零出金，返回 `ErrAmbiguousTransfer`；保留整个交易证据待审，不能挑一条看似相符就确认。
- 窗口外的相符事件返回该 Transfer 和 `ErrPaymentWindow`，方便持久保存晚付/早付线索，不丢弃钱包实际收入。
- 接受内部调用/批量交易产生的合法日志，不依赖顶层函数是不是 `transfer`。这不代表多事件归属已经可以自动确认。

成功返回仍须业务层执行用户归属/历史窗口重叠检查，以及 PostgreSQL 事务中的 transfer/receipt UNIQUE、订单锁和审计，才能确认一次付款。此 package 不提供直接增加余额或卡额度的入口。

## 错误与补扫约束

| 错误 | 后续任务要求 |
| --- | --- |
| `ErrChainUnavailable`、`ErrNotSolidified` | 保留任务并重试，不推进失败高度的游标 |
| `ErrWrongChain`、`ErrInvalidChainEvidence` | 暂停该证据来源/块处理，告警核查，不能用另一个状态提示绕过 |
| `ErrExecutionFailed` | 不能确认；未来扫描器必须先取得完整块/失败 receipt 的可信无入金证据，不能单凭此错误跳块 |
| `ErrTransferMismatch`、`ErrAmbiguousTransfer`、`ErrPaymentWindow` | 保存交易和未匹配/待审事实，不丢记录，不猜订单，不直接增加金额 |

`TronScanner` 已实现逐块补扫循环，`TronChainStore` 提供“完整交易清单的最小证据及未匹配事件保存 + 游标 CAS 推进”的同一 pgx 事务；失败不推进，重启读取持久游标，不能按当前链头重置。运行配置、进程入口、额外 native 类型审批、多节点/原始响应归档和实际强杀恢复验收仍未完成，不能把所有 receipt 缺 `SUCCESS` 的交易简单跳过。

链事件去重应包含 chain_environment/genesis_id/network/tx_hash/event_index；业务 receipt 再约束一个订单和一个 transfer 各只确认一次。原始 uint256 使用 `NUMERIC(78,0)` 或受约束十进制文本保存，业务可确认 units 才使用有上限的 BIGINT。

## PostgreSQL 最小存储契约

迁移 `000006_chain_evidence` 创建 `chain_scan_cursors`、`chain_scan_blocks` 和 `chain_transfers`，`000007_chain_scan_v2` 允许显式 evidence_version=2；后端当前要求 SchemaVersion=10。尚未迁移现有 Fcard 应用数据库，运行新后端前需按项目流程升级，旧版本数据库会 readiness 失败。第6版 down 会删除链证据表，不能作为实际资金证据的无损回滚方案；第7版 down 在存在 v2 游标时拒绝降级。

- `NewTronChainStore` 固定 Nile/TRON/decimals=6、创世块和单一合约；scanner 名称及 evidence_version=1 固化证据处理范围。修改已存在 scanner 的合约被拒绝，不能悄悄重解释历史块。
- `Initialize` 必须提供已审核的起始高度和块 ID，指向最后已处理/明确排除的块，首次处理从下一高度开始。重复初始化只能使用原始起点，不能快进或覆盖当前游标；新 scanner 必须重新审批补扫范围。`Load` 返回已提交游标，不自动回到链头。
- `CommitSolidBlock` 仅供内部可信调用者使用。调用者仍须独立取得真实、完整的 Solidity 块并验证固化/来源；**存储层本身不访问 RPC、不证明固化、不验证共识，不能把用户提交的结构体当链证据**。批次交易证据需与块交易清单逐一、按序对应；最小 receipt ID/高度/时间/成功结果/规范化 SHA256 必须一致，Transfer 重新从 receipt 解码并与提交事件精确比较。
- 原有 v1 接口只接受空块，或全部交易都有明确合约 `SUCCESS` 的块。新的 v2 接口在独立 scanner 范围上支持下面限定的 native/失败交易证据；旧 scanner 不能静默切换处理版本。
- 游标行锁、相邻高度/parent 检查、块摘要及完整最小批次、所有事件和游标 CAS 在一个事务提交。完全相同的历史块重放返回 `committed=false`，不再次写入或回退游标；改变证据/parent/合约或跳高度返回冲突。数据库提交返回错误时不能假定失败或成功；保留原批次，重读游标及按原批次重放解决未知结果，不换起点/证据。
- Transfer 主键为环境/创世块/network/tx_hash/event_index；跨 scanner 相同事件只保留一份，不同内容拒绝并回滚。`raw_units` 使用受约束十进制 TEXT，精确保留 0..uint256 最大值，不经过浮点或 BIGINT 截断。所有事件暂时都是未归属证据，不代表订单已匹配或用户已入账；没有余额、业务 receipt、自动领取或归属接口。
- `chain_scan_blocks.evidence_json` 为内部批次 JSON 字节（receipt 字节由 JSON 编码为 Base64），上限 16 MiB；receipt 为规范化最小模型而非原始 HTTP。`created_at` 只代表数据库保存时间，RPC 查询来源/实际观测时间、原始响应归档、保留/访问/告警策略尚未实现。

显式执行 `powershell -NoProfile -ExecutionPolicy Bypass -File scripts/test-integration.ps1` 会在 PostgreSQL 创建并清理临时数据库，不迁移现有业务数据库。2026-10-13 已通过整项目数据库回归：第二事件写入失败及最终游标写入失败都回滚整块；重启/历史重放、跨 scanner 去重、8 个并发提交仅推进一次、uint256 最大值/零值、创世块隔离、取消、跳高度/错 parent/不完整证据、迁移 down/up 和旧库 readiness 拒绝。测试使用合成块、合成 receipt/合约/钱包，不等于真实转账、进程强杀或 RPC 宕机补扫验收。

## v2 逐块扫描与限定的无入金证据

`NewTronScanStore` 固定 evidence_version=2；已有 v1 scanner 保持 v1，不能复用同一范围转换配置。`NewTronBlockReader` 使用受控 `TronScanSource`（实际 `TronRPC`）读取 Genesis、Solidity head、已提交锚点、相邻目标块及每笔 receipt；校验锚点/parent、固化高度/时间与同高或相邻 head ID，再逐笔按块交易清单核验。上游 Solidity 仍是信任边界，没有共识签名/Merkle 验证。

每笔交易必须有且仅有一个 `raw_data.contract.type` 和 `ret`：

- `TransferContract`（TRX）、`TransferAssetContract`（TRC10）：块执行结果明确 SUCCESS，顶层没有失败，receipt 无日志，才记录 `native_success`。native 的 resource receipt 可省略默认 result，不能用这一规则放宽 TVM 成功要求。
- `TriggerSmartContract`、`CreateSmartContract`：块 contractRet 与 resource receipt 必须同为 SUCCESS 才解码目标合约 Transfer 并记录 `contract_success`。
- 上述 TVM 类型的已知失败码（如 REVERT/OUT_OF_ENERGY）：块 contractRet 与 receipt 失败码一致、receipt 顶层明确 FAILED、没有任何日志，才记录 `contract_failed`，保留整笔无入金证据但不产生 Transfer。不使用旧 verifier 的 `ErrExecutionFailed` 作为跳块依据。
- 缺 ret/type/receipt、未知类型、DEFAULT/UNKNOWN TVM 结果、矛盾执行结果、失败或 native 有日志都暂停/拒绝，不能删交易后推进。投票、资源委托/冻结等其他 native 类型尚未审批，因此**并不声称已可无阻塞连续处理 Nile 的所有真实区块**。

v2 批次保存 source ID、收集完成时间和当时的固化 head；区块仅保留 ID/header、交易 ID/type/ret，receipt 仍为最小模型，不保留完整 raw_data 参数/原始 HTTP。规范化省略与显式 protobuf 默认值、空日志和 hex 大小写，并深拷贝块证据，防止取证源后续修改已等待提交的批次。v2 evidence_hash 只摘要版本/块/交易内容，不包含会变化的观测时间/head；相同链事实重放保留首次完整批次/观测信息，不覆盖，也不是观测信息的独立签名或原始字节完整性证明。

`TronScanner.ScanOnce` 从持久游标读取下一块；`Run` 在落后时逐块补扫，在追平时轮询，带单批截止时间、指数退避上限和连续失败预算。错链/证据无效/冲突/未知类型立即停止并记录不含敏感 payload 的告警；超时/节点或数据库故障在预算内重试，失败不推进。数据库 Commit 结果未知时保留同一内存批次原样重放，不重新下载/改变观测时间；进程重启则按数据库已提交游标重新取证。进程入口、受控启用、强杀及故障注入运维演练尚未接入。

真实 PostgreSQL 临时库测试已验证：混合 native/成功/失败块只保存成功目标合约的事件且保存所有四笔证据；成功提交后模拟响应丢失，再提交相同批次不重复；缺 receipt 不推进且恢复后补扫；重建 repository/worker 继续下一高度；不同观测时间并发提交相同链事实仅推进一次；v1/v2 不混用；v2 游标存在时第7版原始 down SQL 拒绝且不改变表。正式 migration 工具执行失败可能把版本标为 dirty，不能把原始 SQL 的无变化测试理解成工具降级也会保持 Ready，更不能自动 Force 绕过。

2026-10-13 08:15:00 UTC 的公开只读观测与显式 `TestNileNativeReceiptContract` 通过：Nile 固化块 `71782721` 中一笔 TRX TransferContract 的 contractRet=SUCCESS，resource/top-level result 省略、log 字段省略，与 native 分支兼容。此测试没有配置代币合约、不涉及用户钱包或付款；TRC10 和失败 TVM 的实际 receipt 仍待实链只读/测试交易验收。

## 何时需要测试收款地址

现在不需要提供钱包。等业务订单、回调 inbox、唯一入金归属/receipt、安全启用开关和页面联调准备好，在导入测试收款地址及真实小额 Nile 支付前明确向用户索取：**专用 Nile 测试钱包的 `T...` 公共收款地址**，并另行核验测试代币合约/decimals。不索取、不存储私钥、助记词或钱包密码；发送测试币由用户在自己的钱包中完成。本阶段不生成/导入钱包，不启用网关收款。

## 已有测试与真实只读验证

```powershell
go test ./internal/deposit -count=1 -cover
go test ./internal/deposit -run '^$' -fuzz '^FuzzTronRPCJSON$' -fuzztime=5s -parallel=2
```

默认测试只用 mock。真实只读 Nile 契约测试需显式设置 `FCARD_TEST_NILE_RPC=1`，再运行 `go test ./internal/deposit -run '^TestNileSolidRPCContract$' -count=1 -v`；该测试查询创世块和固化块，不查用户钱包、不转账。

2026-10-13 只读观测 Nile 创世块为 `0000000000000000d698d4192c56cb6be724a558448e2684802de4d6cd8690dc`；真实 Go RPC 测试通过。此测试不涉及测试 USDT 合约、实际交易 receipt、充值匹配或停机补扫，因此仍不能宣布可以收款。

官方协议/BlockCapsule 与响应观测保存在忽略目录 `.cache/tron-evidence-sources/`，文档证据、结果和剩余风险见 `docs/Epusdt-plan.md` 第 13 节。fixture 中的合约、钱包和交易均为合成数据，不是实链已验证对象。
