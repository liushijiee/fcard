# Epusdt Nile 隔离部署

这是 Nile 隔离测试部署。全新部署默认禁用收款；2026-10-14 已新增受控的测试资产/钱包启用流程。只有本机 `conf/nile-receiving.json` 审核清单中的合约、六位精度、公共钱包及 HTTPS 来源可以启用，启动检查拒绝其他链、资产或钱包。Fcard 扫描追平和订单创建开关独立控制，网关启用不等于立即可付款。当前记录见 `docs/Nile-test-runbook.md`；本项目不提供主网收款。

## 启动与验证

在仓库根目录 PowerShell 执行：

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File scripts/start-epusdt-testnet.ps1 -Build
powershell -NoProfile -ExecutionPolicy Bypass -File scripts/test-epusdt-testnet.ps1
powershell -NoProfile -ExecutionPolicy Bypass -File scripts/test-epusdt-testnet.ps1 -Recreate
```

入口 `http://127.0.0.1:19800`，只能本机访问。密码和商户密钥只保存于 `.cache/epusdt-testnet/conf/credentials.json`，不要提交、共享或贴进聊天。Windows 上该目录继承本地文件权限，应限制为当前用户可读。变更后台密码后保留自己的安全记录。

脚本校验源代码固定为 `619cecb11d6bbd1dcdd1b698ee08b37fc722d3e3`，缓存必须干净；构建基础镜像/代理镜像的 digest 锁保存在 `.cache/epusdt-testnet/compose.env`。默认不会重新选择镜像版本。源码缓存 `.cache/epusdt-source` 不属于 Fcard module、不进入 Git。

网关使用独立 Docker volumes `fcard-epusdt-nile_primary` 和 `fcard-epusdt-nile_runtime`，不访问现有 Fcard PostgreSQL。SQLite 只是这轮 spike 的主库选择，不能作为网关 PostgreSQL 兼容性验证。

## 安全初始化

构建原版 Epusdt 与一个独立 bootstrap 辅助程序，不修改上游业务源码。bootstrap 在 `network_mode: none` 下运行上游数据库初始化，不启动扫描/回调线程，随后禁用所有预置链、币、地址和 RPC，只配置 Nile RPC，并生成仅用于本地测试的凭证。

网关每次启动先做 fail-closed 检查：主网节点或清单外币/钱包被启用则拒绝启动。`scripts/prepare-nile-receiving.py` 只读核对实际 Nile genesis、已锁定测试合约字节码、ABI/decimals、钱包校验和及连续扫描审核起点，再生成忽略提交的清单。bootstrap 在一个 SQLite 事务中禁用其他支付通道，并应用清单中的资产/钱包。不要在管理页面手动打开额外资产；Nginx 拒绝通道切换、EPay、OkPay、安装入口。

`bootstrap/go.mod` 仅让该辅助源码不参与 Fcard module 的 `go test ./...`；Docker 将辅助源码放进固定上游 module 编译并测试。它不是给 Fcard 新增 GORM/Echo 依赖。

## 无资金契约测试

测试验证公开配置/启用代币与清单一致（没有清单时全部禁用）、管理认证、仅 Nile RPC 启用、危险接口阻断、坏 HMAC/旧签名拒绝，以及正确 HMAC 进入 URL 安全检查。正确签名 probe **故意使用 localhost notify_url，期望上游 10041 拒绝**，不产生订单、不发送回调、不转账。此隔离契约测试要求网关尚无订单；实际转账后不要把历史订单造成的断言失败理解为新创建了订单。`-Recreate` 只重建容器，不是完整备份灾难恢复验收。

## 停止与下一步

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File scripts/stop-epusdt-testnet.ps1
```

停止保留 volumes，禁止随意 `down -v` 删除数据。

真实小额测试前，应确认 Fcard `/health/deposits` 的 `available/ready/enabled` 全为true，单笔申请上限目前为1测试代币。扫描起点和游标不可为绕过未知交易而后移；未知类型或不可信 receipt 会停止运行。付款只能使用用户订单返回的实际金额和钱包，确认只生成 receipt，不增加余额或卡额度。
