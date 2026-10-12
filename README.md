# Fcard Backend

M1 / M1.5 后端及 M2 首个沙盒开卡切片：管理员与用户独立会话、用户管理、权限控制、审计和健康检查；管理员绑定已有 Airwallex 沙盒持卡人，用户申请、查询本人虚拟卡。开卡默认关闭，不包含资金账本、交易回调或真实链上入金。

已新增 Vue 3 + TypeScript + Vite 管理前端，详见 `frontend/README.md`。后端启动后，在 `frontend` 目录执行 `npm ci`、`npm run dev`，访问 `http://127.0.0.1:5173`。

## 技术栈

- Go 1.26、Gin、pgx v5 + sqlc、PostgreSQL。
- golang-migrate 管理 SQL 迁移；slog 输出结构化日志。
- 管理员使用不透明 Bearer 会话，密码 bcrypt 哈希，数据库仅保存令牌 SHA-256 哈希。
- 业务设计详见 `后端Spec.md`；链上收款后续通过适配层接入独立开源项目。

## 本地启动（PowerShell）

在项目根目录操作。数据库使用已有 `fcard` 容器，映射 `127.0.0.1:56034`，凭证位于不提交版本控制的 `.env.postgres`。

```powershell
docker start fcard
go mod download
Copy-Item .env.backend.example .env.backend
go run ./cmd/migrate
```

只在首次配置时复制示例，避免覆盖自己的 `.env.backend`。程序依次读取 `.env.backend`、`.env.postgres`，已有环境变量优先；不读取支付测试脚本的 `.env`。也可以直接设置 `DATABASE_URL` 覆盖数据库配置。

服务不自动迁移数据库。迁移成功后，显式初始化管理员；密码要求 12–72 字节，没有默认账号或默认密码：

```powershell
$secret = Read-Host '管理员密码' -AsSecureString
$env:ADMIN_PASSWORD = [System.Net.NetworkCredential]::new('', $secret).Password
try {
    go run ./cmd/admin --email admin@example.com
} finally {
    Remove-Item Env:\ADMIN_PASSWORD -ErrorAction SilentlyContinue
    Remove-Variable secret -ErrorAction SilentlyContinue
}
go run ./cmd/server
```

默认监听 `127.0.0.1:8081`。若端口已占用，在 `.env.backend` 修改 `HTTP_ADDR`。开发环境仅本机 HTTP；任何远程开放都需配置 HTTPS、受控反向代理和访问策略。本轮不提供生产部署。

需要停用管理员时，使用本地维护命令 `go run ./cmd/admin --email admin@example.com --disable`，会在同一事务停用账号并永久撤销已有会话。该命令依赖本地数据库访问权限，不是公开管理 API，也不要求被停用账号的密码。

## API 示例

```powershell
$base = 'http://127.0.0.1:8081'
Invoke-RestMethod "$base/health/ready"

$secret = Read-Host '管理员密码' -AsSecureString
$password = [System.Net.NetworkCredential]::new('', $secret).Password
try {
    $body = @{email='admin@example.com'; password=$password} | ConvertTo-Json
    $login = Invoke-RestMethod "$base/api/v1/admin/auth/login" -Method Post -ContentType 'application/json' -Body $body
} finally {
    Remove-Variable password,body,secret -ErrorAction SilentlyContinue
}
$headers = @{Authorization="Bearer $($login.data.access_token)"}
$body = @{email='test@example.com'; display_name='Test User'} | ConvertTo-Json
$created = Invoke-RestMethod "$base/api/v1/admin/users" -Method Post -Headers $headers -ContentType 'application/json' -Body $body
Invoke-RestMethod "$base/api/v1/admin/users?page=1&page_size=20" -Headers $headers
$id = $created.data.id
Invoke-RestMethod "$base/api/v1/admin/users/$id" -Method Patch -Headers $headers -ContentType 'application/json' -Body '{"status":"DISABLED"}'
Invoke-RestMethod "$base/api/v1/admin/audit-logs" -Headers $headers
Invoke-RestMethod "$base/api/v1/admin/auth/logout" -Method Post -Headers $headers
Remove-Variable login,headers -ErrorAction SilentlyContinue
```

成功返回 `{ "data": ..., "request_id": "..." }`，失败返回 `{ "error": { "code": "...", "message": "..." }, "request_id": "..." }`。请求可传合法 UUID 格式的 `X-Request-ID`，否则服务生成新 ID。

| 方法 | 路径 | 认证 |
| --- | --- | --- |
| GET | `/health/live`、`/health/ready` | 无 |
| POST | `/api/v1/admin/auth/login` | 邮箱密码 |
| GET | `/api/v1/admin/auth/me` | Bearer |
| POST | `/api/v1/admin/auth/logout` | Bearer |
| POST / GET | `/api/v1/admin/users` | Bearer |
| GET / PATCH | `/api/v1/admin/users/{id}` | Bearer |
| GET | `/api/v1/admin/audit-logs` | Bearer |
| POST | `/api/v1/admin/users/{id}/password/reset` | 管理员 Bearer；目标用户 ACTIVE |
| POST | `/api/v1/user/auth/login` | 用户邮箱密码 |
| GET | `/api/v1/user/auth/me` | 用户 Bearer，含待首次改密会话 |
| POST | `/api/v1/user/auth/logout` | 用户 Bearer，含待首次改密会话 |
| POST | `/api/v1/user/auth/change-password` | 用户 Bearer + 当前密码/新密码 |

创建用户必填 `email`、`display_name`；更新支持 `email`、`display_name`、`status`（ACTIVE / DISABLED），必须至少提供一个非 null 字段。不支持删除历史记录。创建资料不自动设置密码，历史用户也没有默认密码。管理用户响应增加 `login_enabled`（是否已建立凭证）和 `must_change_password`，不返回密码或凭证哈希；账号停用状态独立于凭证是否存在。

## 开通用户登录

1. 执行 `go run ./cmd/migrate`，然后启动或重启后端。数据库预期版本为 3，增量迁移保留原有用户 ID 和管理员会话，不为旧用户初始化密码。
2. 管理端进入“用户管理”，创建用户资料，点击该用户的“开通登录”，输入并确认临时密码（12–72 字节）。已有凭证的用户显示“重置密码”。只有 ACTIVE 用户允许此操作。
3. 沙盒中通过安全方式人工交付临时密码，让用户打开前端 `/portal/login`。不开放公开注册或匿名忘记密码。
4. 用户首次登录跳转 `/portal/security`，校验当前密码后设置不同的新密码。成功后所有用户会话失效，需用新密码重新登录，进入 `/portal` 查看本人资料。

接口重置 body 为 `{"temporary_password":"..."}`；用户改密 body 为 `{"current_password":"...","new_password":"..."}`。响应不回显密码，客户端不自动重试结果未知的密码变更。重置、改密、停用、修改邮箱均在事务中撤销用户会话；重新启用不恢复旧令牌。邮箱修改保留密码，后续用新邮箱登录。

管理员和用户分别查询各自的会话表，互用令牌返回 401；前端分别使用 `fcard.admin.session` 与 `fcard.user.session`，退出一方不清除另一方。用户侧只查询本人身份，不提供用户列表或管理员代登录。后续用户业务路由须同时挂载用户会话与首次改密限制中间件，并在服务/SQL 中按受信任主体校验资源归属；本阶段没有卡片、订单或账本归属接口。

JSON 写请求要求 `Content-Type: application/json`，拒绝未知字段、多段 JSON 和超过 16 KiB 的请求体。分页参数为 `page`、`page_size`，默认 1/20，最大 page_size 100。错误状态包括 400、401、403、404、409、415、429、500；readiness 未就绪返回 503。首次改密限制使用 `403/password_change_required`，停用用户的密码重置使用 `409/invalid_state`。

登录限速使用 PostgreSQL 固定一分钟窗口：每邮箱最多 5 次、每直接客户端 IP 最多 20 次，包含成功尝试；管理员和用户使用独立键空间。服务默认不信任 X-Forwarded-For，部署反向代理前应明确可信代理范围。匿名失败事件仅存身份/来源哈希，每小时清理超过 7 天的匿名事件，保留管理员审计和已认证用户安全事件。

## 查询代码生成与测试

编辑 `internal/storage/queries/*.sql` 后执行：

```powershell
go run github.com/sqlc-dev/sqlc/cmd/sqlc@v1.31.1 generate
powershell -NoProfile -File scripts/check-generated.ps1
go test ./...
go vet ./...
```

生成文件纳入版本控制，不手动修改。`sqlc.yaml` 控制 UUID、时间类型映射。

数据库集成测试：

```powershell
powershell -NoProfile -File scripts/test-integration.ps1
```

脚本从 `.env.postgres` 构建 `TEST_DATABASE_URL`，测试在该服务器新建随机命名的 `fcard_test_*` 数据库，执行迁移和接口测试后删除该测试库，不清空或修改 `fcard` 业务库。测试账号需 CREATEDB 权限；也可提前设置独立服务器的 `TEST_DATABASE_URL`。未设置变量时，普通 `go test` 跳过数据库集成测试。

覆盖迁移重复执行和旧用户升级、两类身份登录/注销、令牌过期、跨认证域拒绝、首次改密限制、两个用户身份隔离、重置/停用/改邮箱撤销、重新启用不恢复旧令牌、并发改密及登录与重置/停用/改邮箱竞争、审计/安全事件失败事务回滚、限速隔离及迁移未就绪状态。

## 当前限制

- 沙盒开卡需显式启用，仅调用固定 Airwallex sandbox 地址。每用户最多一张已创建或待核查卡片，USD 累计测试限额由后端设置；不提供真实资金流水。
- 链上收款项目尚未选型，不保存区块链私钥或接收真实 USDT。
- 登录接口不提供公网服务承诺，TLS、代理策略及生产准入后续单独设计。
- 管理前端与用户空间覆盖 M1/M1.5 及沙盒开卡切片，尚无入金、余额、消费记账、Webhook 或链上支付能力。

## 沙盒开卡（M2 首个切片）

先执行 `go run ./cmd/migrate` 升级到版本 5。原 `.env` 继续仅供 Python 消费模拟脚本使用；Go 后端不自动读取该文件。将沙盒凭证配置到不提交的 `.env.backend` 或进程环境变量，不要复制到前端。

```dotenv
AIRWALLEX_ISSUING_ENABLED=true
AIRWALLEX_CLIENT_ID=<你的沙盒Client ID>
AIRWALLEX_API_KEY=<你的沙盒API Key>
AIRWALLEX_API_VERSION=2026-08-21
AIRWALLEX_CARD_PROGRAM_PURPOSE=COMMERCIAL
AIRWALLEX_CARD_LIMIT_CENTS=1000
```

程序固定使用 `https://api.sandbox.airwallex.com`，不允许通过配置切到生产或任意地址。本切片按官方 2026-08-21 API 的 `program` 和 `authorization_controls.transaction_limits` 契约实现；如果你的账号只支持旧版 Borderless Cards / Expense Management，请先核实 Issuing 产品与版本，不要盲目切换版本。卡产品用途必须显式选定，不能把个人消费默认为企业商业用途。累计限额范围 1–10000 美分，默认 1000 美分（USD 10），只允许 USD 交易；不是用户余额。

使用流程：

1. 管理员创建用户并开通登录；用户完成首次改密。
2. 在 Airwallex 沙盒确认公司账户现有的 INDIVIDUAL 持卡人 ID。此 ID 是公司统一发卡配置，不是 Fcard 客户的身份；当前客户端仍需要该接口字段，不支持用公司账户 ID 替代。按当前测试要求，配置和后台任务暂不校验 READY 状态；仍核验 ID、类型和实名，实际发卡是否允许以供应商响应为准。
3. 管理端进入 `/cardholders` 的“公司发卡配置”，一次性输入公司现有持卡人 ID。后端核验身份并读取实名作为 created_by，保存全局唯一配置及审计。所有客户共用该配置，不再逐客户绑定；重复提交同一 ID 可成功，不同 ID 不允许覆盖。客户选择仅用于开卡结果核查。
4. 用户进入 `/portal/cards`，填写卡片名称并申请。新请求采用企业非个性化虚拟卡：program.purpose=COMMERCIAL、is_personalized=false、form_factor=VIRTUAL，复用公司现有 cardholder_id 和 created_by，不创建客户持卡人。后端从已认证主体取 user_id，保存卡片与操作，返回 202，再由数据库后台任务调用供应商。用户不能指定 owner、cardholder_id、program 或额度。既有个性化卡和已保存请求不会自动转换；重试仍使用原请求快照。
5. 页面查询本地操作结果。供应商 PENDING 不展示成 ACTIVE；成功后可手动同步供应商状态。普通接口仅返回本地卡 ID、状态、尾号、测试限额和操作编号，不返回 PAN/CVV、供应商完整响应、凭证或请求快照。

| 方法 | 路径 | 用途 |
| --- | --- | --- |
| GET / POST | `/api/v1/admin/company/cardholder` | 查看/核验公司统一配置；POST body 为 provider_cardholder_id |
| GET | `/api/v1/user/cards/config` | 开卡启用、company_configured、occupied_slots 和测试限额信息；占用数覆盖全部历史页，不暴露公司身份 |
| POST / GET | `/api/v1/user/cards` | 申请/分页查询本人卡片；POST body 为 nick_name |
| GET | `/api/v1/user/cards/{id}` | 本人卡片详情 |
| POST | `/api/v1/user/cards/{id}/sync` | 读取供应商状态，更新本人卡片 |
| GET | `/api/v1/user/card-operations/{id}` | 本人开卡操作状态 |
| POST | `/api/v1/user/card-operations/{id}/retry` | UNKNOWN 时显式重放原申请，最多五次总尝试 |
| GET | `/api/v1/admin/users/{id}/card-operations/{operation_id}` | 管理员核查目标用户操作 |
| POST | `/api/v1/admin/users/{id}/card-operations/{operation_id}/reconcile` | 验证已创建卡片后补录；body 为 provider_card_id |

申请开卡必须带 UUID 格式 `Idempotency-Key`。同用户同键同规范化名称返回同一操作，换参数返回 409；供应商 request_id 和完整请求快照在首次提交后固定，后端配置变化不会改写重试内容。卡片通过本地 user_id 与供应商 card_id 绑定，新请求仅附带 fcard_card_id/fcard_operation_id metadata，不向供应商提交客户 ID、邮箱或实名；metadata 是核查辅助，不代替本地资源归属。

操作状态为 PENDING → PROCESSING → SUCCEEDED / FAILED / UNKNOWN。超时、5xx、响应不匹配、崩溃或保存结果失败保留待核查，禁止新开卡请求绕过同时一张未关闭或待核查卡片的限制。进程中断后的 PROCESSING 超过一分钟转 UNKNOWN，不自动重发。用户可显式重放同 request_id；重试仍无法确定时，管理员在供应商找到卡片后用核查入口补录。补录要求卡片持卡人、request_id 和全部平台 metadata 匹配，不能用任意卡 ID 认领。客户端未知提交结果时将名称和幂等键保存在用户独立 sessionStorage，刷新后仍只能显式重试原申请。

迁移 000004 新增 company_issuing_profile，解除卡片与逐客户外部持卡人映射的外键；保留历史卡片、旧映射和请求快照，不从历史客户自动推断公司身份。迁移后管理员必须重新配置公司身份。已有请求仍使用原快照（包括旧 metadata）执行、重试和核查，不改写历史事实。若已有公司模式卡片无法满足旧外键，回退到版本 3 会被拒绝，不能直接降级。旧逐客户绑定接口已移除。

迁移 000005 新增 provider_operations.holds_card_slot，以独立名额占用标记替代按操作成功状态永久占位。迁移会释放明确失败操作、以及已成功开卡且本地确认 CLOSED 的卡片名额；UNKNOWN 不释放。外部注销本身不会更新本地：在 Airwallex 注销旧卡后，回到 Fcard 点击“同步供应商状态”，后端核对供应商 ID、持卡人、request_id 和 metadata，并在同一事务中保存 CLOSED、释放名额及记录安全事件。历史卡片、归属、请求快照、SUCCEEDED 操作和幂等键均保留。重复同步不会重复释放，已关闭卡不允许被旧响应重新打开；冻结、处理中、未知、过期等状态继续占位。后台创建或管理员核查直接确认 CLOSED 时也释放名额。

同一用户在释放后必须使用新的申请和新的幂等键开新卡；旧键仍返回旧操作。占用数由后端统计全部卡片，前端分页不会绕过限制。存在多次成功开卡历史时，旧版本唯一约束无法容纳这些记录，禁止直接降级至版本 4，回退迁移会失败而不是删除历史。

后台调用前重新检查账号与凭证版本。已发出的供应商请求不能通过账号停用撤回，停用账号不等于冻结卡片；本切片不提供冻结、预算、消费或退款账务，不可用于生产。完整卡号安全展示、Webhook 与交易对账需后续单独实现。

当前仍使用 AIRWALLEX_CARD_LIMIT_CENTS 的固定沙盒限额，不按客户充值金额分配；客户入金确认、余额、额度分配和消费扣账尚未实现。实现“公司统一发卡、Fcard 客户归属”不意味着可直接上线真实资金服务，也不代表已核实供应商对该业务的许可。

测试使用 httptest 供应商或浏览器 API 拦截，不调用真实 Airwallex，不生成实际沙盒卡。数据库测试在随机临时库中覆盖归属、幂等、并发限额、撤销、结果未知恢复、核查和事件失败回滚。

接入契约来源：Airwallex 官方 Cards、Cardholders、API Access Token 和 Versioning 文档，核对日期 2026-10-11。
