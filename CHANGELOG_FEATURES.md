# Cline2API 项目功能与修改记录文档

> v1 发布版本总表请查看 [`cline2api_v1.md`](cline2api_v1.md)。本文件保留功能设计、接口细节和测试说明。

---

## 一、 项目定位与整体功能概述

**Cline2API** 是一个基于 Go 语言开发的高性能轻量级代理服务，专为 Cline / Roo Code / OpenCode 等 AI 编码工具及兼容 OpenAI 协议的客户端设计。核心目标是将 Cline / WorkOS 官方多账号资源池化，并通过兼容标准 OpenAI API 格式（`/v1/chat/completions`）对外提供稳定、高可用的模型调用接口。

### 1. 核心功能架构

1. **OpenAI 兼容反向代理服务**
   - 暴露标准 OpenAI 接口：`POST /v1/chat/completions` 与 `GET /v1/models`。
   - 支持流式响应（Server-Sent Events, SSE）与常规非流式响应。
   - 客户端使用任意 API Key 即可接入调用。

2. **多账号池轮询与负载均衡 (Account Pool)**
   - 支持添加多个 Cline / WorkOS 账号并进行统一管理。
   - 支持多种负载均衡策略（轮询 `round_robin`、最饱优先 `fill`、随机 `random`）。
   - 自动持久化保存账号与配置到 `.cline-accounts.json`。

3. **智能 Token 刷新与生命周期管理**
   - 自动追踪账号 AccessToken 过期时间（提前 60 秒刷新）。
   - 调用 WorkOS 刷新接口自动换取新 Token，保证长时间无人值守运行。

4. **双重冷却与容错机制 (Cooldown & Fallback)**
   - **模型级冷却 (Model-level Cooldown)**：遇到 429 限制时仅冷却当前受限的模型，账号其余模型仍可继续使用。
   - **账号级冷却与自动切换**：遇到不可用或账号受限时自动将请求故障转移（Failover）至下一个健康账号。
   - **模型自动降级策略**：主模型不可用时支持自动降级至免费备用模型。

5. **多模型来源聚合 (Remote & OpenCode Zen)**
   - 自动同步 Cline 官方支持的模型列表。
   - 支持集成 OpenCode Zen 免费模型池。
   - 支持用户手动添加或调整自定义模型。

6. **Web 管理后台面板 (Admin Dashboard)**
   - 访问地址：`http://127.0.0.1:3457/admin/`
   - 提供仪表盘概览（账号总数、活跃账号、模型总数、请求用量与 Token 统计）。
   - 提供账号列表管理、模型管理、代理策略设置等功能。
   - 原生内置中文/英文双语切换能力。

---

## 二、 本次修改功能记录（用户管理与门禁鉴权）

本次根据需求，为系统增加了**后台用户管理体系**以及**强制密码登录门禁**，防止未经授权直接访问后台管理面板和 API。

### 1. 数据模型升级 (`types.go`)

在 [types.go](types.go) 中新增后台管理员/普通用户结构体，并在账号池配置中加入持久化支持：

```go
// 后台管理用户结构
type AdminUser struct {
    ID           string    `json:"id"`
    Username     string    `json:"username"`
    PasswordHash string    `json:"passwordHash"`
    PasswordSalt string    `json:"passwordSalt,omitempty"`
    Role         string    `json:"role,omitempty"` // admin / user
    CreatedAt    time.Time `json:"createdAt"`
}

// AccountPool 扩展
type AccountPool struct {
    Accounts          []*Account  `json:"accounts"`
    CurrentIdx        int         `json:"currentIdx"`
    Keys              []string    `json:"keys,omitempty"`
    Models            []Model     `json:"models,omitempty"`
    DefaultModel      string      `json:"defaultModel,omitempty"`
    AdminUsers        []AdminUser `json:"adminUsers,omitempty"` // 持久化管理用户
    ListenHost        string      `json:"listenHost,omitempty"`
    AdminPasswordHash string      `json:"adminPasswordHash,omitempty"`
    AdminPasswordSalt string      `json:"adminPasswordSalt,omitempty"`
}
```

### 2. 账号初始化与向下兼容 (`pool.go`)

在 [pool.go](pool.go) 的 `loadPool()` 逻辑中加入自适应初始化机制：
- 若初次启动且不存在任何用户时，系统自动创建默认管理员：
  - **默认用户名**：`admin`
  - **默认密码**：`admin`
  - **初始角色**：`admin`
- 若检测到旧版本配置中已存在的 `AdminPasswordHash`，会自动迁移为该 `admin` 账号的初始密码，实现无缝平滑过渡。

### 3. 后端门禁鉴权与用户管理 API (`admin.go`)

在 [admin.go](admin.go) 中重构了鉴权中间件并实现用户 CRUD 接口：

1. **鉴权中间件升级 (`requireAdminAuth`)**
   - 当系统存在用户或设置了密码时，所有 `/admin/api/*` 请求必须携带有效的 24 小时 session 签名 cookie (`cline_admin_session`)。
   - 未登录请求统一拦截并返回 `401 Unauthorized` (`{"success":false,"error":"需要登录"}`)。

2. **新增/更新后台认证接口**：
   - `POST /admin/api/login`：支持用户名+密码校验，验证通过后签发 session cookie；集成防暴力破解耗时防碰撞。
   - `POST /admin/api/logout`：注销当前 session 并清理 cookie。

3. **新增用户管理 RESTful 接口**：
   - `GET /admin/api/users`：获取系统用户列表（脱敏返回 id、username、role、createdAt）。
   - `POST /admin/api/users/add`：添加新用户（支持设置用户名、密码、角色）。
   - `POST /admin/api/users/delete`：删除指定用户（内置保护逻辑：禁止删除最后一个管理员用户）。
   - `POST /admin/api/users/reset-password`：为指定用户重置登录密码。

### 4. 国际化多语言扩展 (`i18n.go`)

在 [i18n.go](i18n.go) 中补充了用户管理操作的中英文反馈字典：
- `user_required` / `user_exists`
- `user_added` / `user_deleted` / `user_not_found`
- `cannot_delete_last_user`
- `password_required`

### 5. 前端面板与全屏门禁交互 (`admin_html.go`)

在 [admin_html.go](admin_html.go) 中对 Web 界面进行了全面适配：

1. **强制登录全屏门禁 (`#loginOverlay`)**
   - 页面加载时自动调用接口验证认证状态；未登录时展示毛玻璃锁定层及登录弹窗。
   - 登录表单支持输入用户名与密码，回车快捷提交。
2. **左侧侧边栏增加导航项**
   - 新增 **「用户管理」** 导航 Tab。
   - 底部常驻 **「退出登录」** 按钮，点击即可注销并退回门禁。
3. **「用户管理」控制面板 (`#tab-users`)**
   - **创建用户区域**：支持输入用户名、密码及选择角色（管理员 / 普通用户）。
   - **用户数据表格**：展示用户列表、角色状态徽章、注册时间。
   - **快捷操作**：支持一键「重置密码」弹窗和「删除用户」二次确认。

---

## 三、 修改文件清单

| 文件路径 | 修改类型 | 说明 |
| :--- | :--- | :--- |
| [types.go](types.go) | 修改 | 新增 `AdminUser` 结构体及 `AccountPool.AdminUsers` 字段 |
| [pool.go](pool.go) | 修改 | `loadPool()` 增加自动创建默认 admin 账号及数据持久化 |
| [admin.go](admin.go) | 修改 | 增强中间件鉴权、新增登录/退出接口、新增用户增删查与改密 API |
| [i18n.go](i18n.go) | 修改 | 新增用户管理各场景的中英多语言文案 |
| [admin_html.go](admin_html.go) | 修改 | 增加全屏登录门禁、侧边栏用户管理项、用户管理交互与 JS 控制逻辑 |
| [CHANGELOG_FEATURES.md](CHANGELOG_FEATURES.md) | 新增 | 本次功能与修改记录详细文档 |

## 四、固定 Free / Pass 分组（第一版）

### 数据与兼容

- `Account.subscription`：`unknown`（待确认）、`free`、`pass`。旧账号迁移为待确认，不推断真实订阅；待确认账号不参与调度。
- `AccountPool.keyGroups`：按 Key 保存允许调度的订阅组；原 `keys: []string` 格式保留，旧 Key 迁移为两组可用。新增 Key 在后台默认仅 Free。
- `AccountPool.groupIndexes`：各授权范围独立轮询游标。
- 旧账号和手动 Token 导入默认仍为待确认，不凭空推断订阅；OAuth 新登录会查询 Cline `/api/v1/users/me/plan` 自动识别 ClinePass/Free，查询失败仍保留待确认。

### 权限与路由

- Free-only Key：仅使用 Free 账号，禁止非免费模型。
- Pass-only Key：仅使用 Pass 账号，可调用免费和非免费模型。
- 双组 Key：免费模型可使用两组账号；非免费模型只选择 Pass 账号。
- `unknown` 不可作为 Key 授权组；空数组、非法组不会扩大权限。
- Chat Completions、Messages、Responses 及无 `/v1` 前缀接口统一执行上述规则。
- `/v1/models`、`/models` 按 Key 权限过滤；未识别模型保守按非免费处理。
- Token/网络失败切换、模型冷却与 `model="free"` 降级均保留 Key 授权范围。具体模型 ID 仍不自动替换成其他模型。
- 模型权限不足返回 403；具体模型无可用账号返回 503；免费降级链耗尽返回 429。流开始后不重新选账号或模型。
- 未配置任何 Key 时保留原来的匿名调用行为（两组范围）；对外部署必须配置 Key。
- 现有 `Model.Cost=pass` 是非免费标签，不能据此证明账号拥有 Pass，也不能保证模型一定包含在 Pass 套餐内；上游仍决定实际可用性。

### 后台、导入导出及统计

- 账号页支持订阅筛选、单个设置、勾选后批量设置（全部目标验证成功才更新）。
- OAuth、Token 和批量导入支持订阅分类；导出保留 `subscription`；文件中的分类优先于后台默认值。
- Key 页支持创建和修改允许组；Key 改用加密安全随机数生成。
- 请求日志保存调用时订阅快照。分组用量从保留日志聚合（最近30天、最多5000条），不会因账号重新分类而改写历史；旧日志归为待确认，OpenCode 用量不计入 Cline 分组。
- 分组统计包含账号数量、活跃数量、请求数、输入/输出/缓存/总 Token。
- 修复“删除全部账号”清空整池的问题，现在保留用户、Key、模型和监听设置。
- 配置快照及落盘串行执行，分组修改和调度并发时不会竞态写入；Token 刷新、账号状态与计数更新也使用账号池锁。
- 用户 `Role` 的完整后台权限隔离不属于本次分组实现，仍需要单独开发。

### API

- `POST /admin/api/accounts/subscription`：`{"accountIds":["acc_..."],"subscription":"free"}`。
- `POST /admin/api/keys/groups`：`{"key":"cline_...","allowedGroups":["free","pass"]}`。
- `POST /admin/api/keys/generate` 支持 `allowedGroups`；省略时保留两组默认以兼容旧调用，后台明确提交选择。
- `GET /admin/api/keys` 保留 `keys`，新增 `keyRecords`（Key 与 `allowedGroups`）。
- `GET /admin/api/stats` 新增 `subscriptionGroups`。

### 验证

- `go test ./...`、`go test -race ./...`、`go vet ./...`、`go build .`。
- 新增分组迁移/重载、调度策略、冷却恢复、失败切换、免费降级、三协议和兼容路径、模型列表、批量原子校验、导出及用量快照测试。
- 增加三协议流式隔离、导入导出往返、Key 权限立即生效及分组编辑/调度并发落盘测试。
- 原代理环境变量测试改为独立进程，避免标准库代理环境缓存造成测试顺序依赖。
