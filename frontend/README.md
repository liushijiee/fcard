# Fcard 管理前端与用户空间

Vue 3 + TypeScript + Vite，接入当前 M1/M1.5 后端能力。管理端与用户端同源但采用独立布局、路由、状态容器及会话令牌。页面数据来自 API，不在运行时使用模拟数据。

## 已实现

- 管理员登录、退出、刷新后会话恢复与后端身份验证。
- 工作台：用户/审计记录总数、最近用户和操作、HTTP 与数据库就绪检查。
- 用户管理：分页、创建、详情、修改资料、确认启用/停用。
- 管理员开通/重置临时密码，显示凭证开通及首次改密状态；临时密码仅存在当前表单，关闭或提交后清空，不写浏览器存储。
- `/portal/login` 用户登录；`/portal` 本人资料；`/portal/security` 首次/自主改密。改密成功需重新登录。
- 用户布局先调用后端 me 校验再展示页面，不信任缓存里的首次改密标志；403/password_change_required 引导改密。
- 两类账号可在同标签页登录，令牌存储与 401/退出清理独立；旧请求失败不能清除新会话，账号切换丢弃旧身份查询结果。
- 审计：分页查询、操作详情、请求编号和脱敏摘要。
- 错误提示、加载/空状态、重复邮箱提示、401 会话清理、桌面及移动布局。

新增 `/portal/cards` 沙盒卡片申请、本人列表、状态同步及结果未知核查；管理端 `/cardholders` 一次性配置公司统一沙盒发卡身份（无需选择客户），并按客户核查和补录开卡结果。开卡默认关闭，启用配置、增量迁移和安全边界见根 README。已关闭卡同步后释放开卡名额，旧卡保留展示；按钮依据后端 occupied_slots 而非当前分页判断，可防止在历史页误开放申请。仍不提供用户自助注册、充值、余额或链上支付页面。

先按根 README 执行版本 3 迁移并重启后端，再在“用户管理 → 开通登录”设置临时密码，人工交付给沙盒用户。普通创建资料不会自动开通登录，也没有统一默认密码。

## 开发启动

推荐 Node.js 22.22 或兼容版本。项目已锁定依赖版本，使用 lockfile 安装：

```powershell
# 终端 1，在项目根目录：先按根 README 初始化管理员
go run ./cmd/server

# 终端 2
cd frontend
npm ci
npm run dev
```

浏览器打开 `http://127.0.0.1:5173`，使用自己初始化的管理员账号。没有默认账号密码。

Vite 将同源 `/api`、`/health` 代理到 `http://127.0.0.1:8081`，后端无需为开发环境开放 CORS。端口采用 strictPort，冲突时明确报错，不静默切换。

后端地址不同可在 `frontend/.env.local` 设置：

```dotenv
BACKEND_TARGET=http://127.0.0.1:8081
```

也可设置同名进程环境变量。该变量只供 Vite 代理使用，不作为客户端业务配置注入。不要把 Airwallex Key、数据库密码或链上私钥放到前端环境文件，特别是 `VITE_` 变量。

## 会话与安全边界

Bearer 会话仅存储在当前标签页的 sessionStorage，不使用 localStorage，不保存密码。刷新后重新调用 `/auth/me` 校验，受保护请求遇到 401 清除会话并返回登录页；最终权限始终由后端执行。

这仍然是浏览器可读令牌，不能替代 XSS 防护。前端使用 Vue 文本转义，不使用 v-html 展示用户资料。远程部署必须启用 HTTPS。网络异常下未能通知后端注销时，页面允许明确选择“仅清除本地会话”，该操作不撤销服务端会话。

## 构建与测试

```powershell
npm run build
npm test
npm run test:e2e
```

`build` 先执行 vue-tsc 检查，再输出静态产物至 `frontend/dist`。`test` 验证 API 和会话逻辑。浏览器测试默认使用本机 Microsoft Edge，自动启动独立 Vite 测试服务（15173）。没有 Edge 时安装 Playwright 支持的浏览器并设置对应 `PLAYWRIGHT_CHANNEL`，或按本机环境调整配置。

默认浏览器测试使用测试专用的 API 拦截，真实 Go 联调由根目录脚本执行：

```powershell
# 在项目根目录，要求 fcard PostgreSQL 容器运行
powershell -NoProfile -File scripts/test-frontend-integration.ps1
```

脚本创建随机 `fcard_test_frontend_*` 数据库和临时管理员，在端口 18081 启动独立后端，通过 Vite 代理测试管理流程和完整用户开通/首次改密/重新登录流程，还验证同标签页会话隔离、重置/停用后的失效以及移动布局。结束后停止临时服务、清理测试数据库，恢复环境变量，不修改业务库用户。测试截图位于忽略提交的 `frontend/test-results`，不记录包含令牌的浏览器 trace。

## 静态部署注意

Vite 开发代理不包含在 dist 中。部署时由同源网关将 `/api/` 和 `/health/` 转发到 Go 后端，其余前端路由回退到 index.html；不要把 API 错误响应回退成 HTML。`npm run preview` 仅用于本地构建预览，不作为生产服务。

## 目录

```text
src/lib            类型化 API、会话、格式化
src/components     管理/用户独立布局、错误提示、分页
src/views          管理登录、工作台、用户、审计、用户登录/资料/安全、404
src/router.ts      路由与登录守卫
src/style.css      视觉系统与响应式布局
src/portal.css     用户空间布局与响应式样式
e2e                浏览器交互及真实接口测试
```
