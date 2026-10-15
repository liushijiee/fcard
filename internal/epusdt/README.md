# Epusdt 契约基础（仅 Nile）

`Config.CheckoutOrigin` 可显式区分私网 API 地址和公开 HTTPS checkout 来源；省略时保留原先与 BaseURL 同源的行为。显式来源必须是 HTTPS 根地址，返回 checkout 必须严格同源；`MatchesMerchant` 用于创建 claim 前核对环境、PID 和公开来源，不输出密钥。反向代理/网关实际公开 URL 配置仍需联调验证，本轮未修改 Docker 或调用真实网关。

该 package 是协议验证基础，不是已上线的入金服务。`internal/deposit` 已提供独立订单、PostgreSQL inbox、链证据 receipt 与默认关闭的 Gin/Runtime 接入；本机未启用收款，也没有增加余额/卡额度。当前实施与实际 Nile 验收门槛见 `docs/Epusdt-plan.md` 第20节。

## 创建与 UNKNOWN

`Client.Create` 固定 TRON/USDT，金额使用十进制字符串和整数最小单位；不自动重试 POST，也不跟随重定向。网络失败、响应丢失、重复单号和无法信任的成功响应返回 `Failure{Ambiguous: true}`。调用方必须先持久化订单/幂等键，再进行网络请求；UNKNOWN 时不能换单号重建。

固定上游没有商户签名的按 order_id 查单路由。当前恢复方案仅为管理面人工核查，详见 `docs/Epusdt-plan.md` 第 12.3 节；尚未实现自动恢复 worker，不将后台管理员 JWT 加进普通创建客户端。

## 验签与报价匹配

- `CallbackVerifier.Verify` 只验证固定回调结构、PID 和 HMAC；返回的是经过认证的**通知线索**，不是链上资金事实。
- JSON 最多 16 KiB；拒绝重复/未知字段、尾随 JSON、字符串金额和非法类型；回调字段按锁定 v2.0.0 契约处理，升级必须回归，不能忽略未知字段或回退 MD5。
- 签名数字复现上游 `FormatFloat('f', -1, 64)`；金额同时用精确有理数核对整数最小单位，拒绝“不同十进制值舍入成相同 float64”的别名攻击。`100.00`、`1e2` 等等值合法表示可验签，存储不使用 float64。
- 最多配置两把具有唯一版本标识的密钥，逐把常量时间比较，并返回实际 `KeyVersion`。旧密钥的有效窗口、撤销和配置注入仍由后续服务实现，不能永久保留旧 key。
- `EventID` 为环境及规范化签名参数的 SHA256，不包含 signature/密钥版本；相同事件在数字表示变化或密钥轮换后保持同一身份。它不是链上 tx/log 去重键，不能代替 receipt 唯一约束。
- 结构合法、验签通过但币种/报价不一致的通知仍交给 recorder 保存，后续用 `MatchNotification` 和本地报价严格比较并进入异常处理。过期报价也保留线索，晚付是否归属必须按窗口/隔离期和链上证据判断。
- `Notification.Environment` 来自本地 Nile 配置，并不是上游证明的链环境；回调没有 network/currency/contract/event_index/最终性，必须另行核验。

## 持久化后 ACK 的接口契约

`NewCallbackHandler` 是尚未挂到应用路由的 `net/http` handler。要求 POST JSON、不接受压缩体；坏签名 401、非法结构 400、过大 413、不支持格式 415；recorder 失败返回 503，仅成功后回复 HTTP200、`text/plain`、正文 `ok`。

生产 `NotificationRecorder.Record` 必须满足：

1. 在一个数据库事务中持久保存事件身份、最小原始 payload、密钥版本和后续处理任务，事务 **commit 成功** 后才能返回 nil；commit 不确定或失败必须返回错误。
2. 相同 EventID 已可靠落库时幂等返回 nil；使用数据库 UNIQUE，而不是进程内 map/mutex。回放可更新观测信息，不覆盖首条事实。原始 payload 有保留期限，不打印签名、密钥或完整交易请求。
3. 回调早于创建响应时仍保存到预存 order_id 的 inbox；未知/不匹配订单进入待核查，不直接生成 receipt，不因为新单开关关闭而丢通知。
4. 遵守 context 的取消/截止时间；不得启动一个未经等待的异步写入后直接返回 nil。
5. 后续独立 worker 才做报价归属、链上成功执行/Transfer/合约/地址/整数金额/固化核验及 receipt 唯一确认。ACK 只表示可靠接收，不表示付款确认。

handler 只能检查 recorder 非 nil，不能替你证明其持久化正确。此 package 单测中的 recorder 是内存 mock，**禁止把该 mock 注册到 Gin 或当成实际 PostgreSQL 幂等验收**。实际实现为 `deposit.NewInboxRecorder`：验签后重新核对事实，一个 pgx 事务保存首条 payload/密钥版本、VERIFY_CALLBACK 任务和审计，提交成功才 ACK；实际临时 PostgreSQL 验收见 `internal/deposit/order_store_integration_test.go`。`deposit.CallbackWorker` 已实现租约下的报价关联、待审/有界等待及原子去重 VERIFY_CHAIN 任务，不把通知当成付款证据；`deposit.ChainWorker` 另以完整已保存链证据、窗口/归属和原子租约约束确认一次事实，`ChainCandidatePublisher` 支持无 callback 候选补偿。上述模块未注册 Gin 或注入 server，Fcard 收款未启用；实际 CREATE 发送/人工恢复/钱包白名单仍待完成。

## 验证

```powershell
go test ./internal/epusdt -count=1 -cover
go test ./internal/epusdt -run '^$' -fuzz '^FuzzNotificationVerification$' -fuzztime=5s -parallel=2
```

`contract_test.go` 模拟网关已提交但响应断开，以及回调在创建响应之前到达；`callback_test.go` 验证等待写入结果、失败不 ACK、签名后业务错配保留、并发重放、取消请求。所有这些场景都不执行真实链转账。真实网关拒绝契约另用 `scripts/test-epusdt-testnet.ps1`，其收款仍保持关闭。
