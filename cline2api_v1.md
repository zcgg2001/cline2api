# Cline2API v1 版本变更记录

> 本文件是 Cline2API v1 的发布说明总表。每次推送版本标签时，在文件顶部新增一个版本小节，记录该版本真实包含的功能、修复、兼容性变化和验证结果。
>
> 当前工作区整理版本：`v1.0.0`
>
> 整理日期：`2026-09-19`
>
> 详细功能设计与测试清单：[`CHANGELOG_FEATURES.md`](CHANGELOG_FEATURES.md)

---

## v1.0.0 — 后台管理、账号分组与登录订阅识别

### Added

#### 1. 后台用户管理与访问门禁

- 增加 `AdminUser` 数据结构，支持用户名、密码哈希、盐值、角色和创建时间持久化。
- 首次启动自动创建默认管理员；旧版 `AdminPasswordHash` 配置会迁移为管理员账号密码。
- 后台接口支持登录、退出、用户列表、创建用户、删除用户和重置密码。
- 管理后台增加全屏登录门禁、用户管理页面、中英文文案和会话 Cookie。
- 删除用户时保护最后一个管理员，避免后台被锁死。

#### 2. 固定 Free / Pass 两组账号模型

- `Account.subscription` 支持三种状态：
  - `free`：Free 账号
  - `pass`：ClinePass 账号
  - `unknown`：待确认
- 旧账号迁移为 `unknown`，不自动猜测真实订阅类型。
- `unknown` 账号不参与分组调度，避免错误使用受限账号。
- API Key 支持授权 Free、Pass 或两组账号。
- 旧版 Key 配置自动兼容为两组授权；新建 Key 支持明确指定授权范围。
- 每个授权范围独立维护轮询游标，避免不同 Key 之间互相影响。

#### 3. 登录后的 Free / Pass 自动识别

- OAuth 登录完成后调用 Cline：

  ```text
  GET /api/v1/users/me/plan
  ```

- 能识别 ClinePass 计划、Free 计划和 `cline_pass` entitlement。
- ClinePass 自动保存为 `pass`，无有效订阅自动保存为 `free`。
- Cline 接口异常、返回结构无法识别或 Token 无效时保留 `unknown`，不会错误授予 Pass 权限。
- 后台 OAuth 登录结果直接显示识别出的分组。
- CLI 设备登录也会输出识别到的订阅类型。
- 管理员手动指定 Free/Pass 时，手动选择优先于自动识别。

#### 4. 分组调度与权限隔离

- Free-only Key 只能使用 Free 账号，并禁止调用非免费模型。
- Pass-only Key 只能使用 Pass 账号，但可以调用免费模型。
- 双组 Key 调用免费模型时可使用两组账号；调用非免费模型时只使用 Pass 账号。
- 调度、Token 刷新、网络失败重试、账号切换、模型冷却和免费模型降级都继承当前 Key 的授权范围。
- 授权组内没有可用账号时返回明确错误，不会越权切换到其他组。
- 模型权限不足返回 `403`；授权组无可用账号返回 `503`；免费降级链耗尽返回 `429`。

#### 5. 三种协议和模型列表隔离

分组权限统一应用于以下接口：

- OpenAI Chat Completions：`/v1/chat/completions`
- Anthropic Messages：`/v1/messages`
- OpenAI Responses：`/v1/responses`
- 无 `/v1` 前缀的兼容路由
- `/v1/models` 和 `/models`

模型列表会根据 API Key 的授权范围过滤。无法识别成本的模型按非免费模型保守处理。

#### 6. 导入、导出与统计

- 账号列表支持按 Free、Pass、待确认筛选。
- 支持单个账号设置订阅类型和勾选后批量修改。
- OAuth、Token、批量导入支持携带 `subscription` 字段。
- 账号导出保留 `subscription`，可以跨设备导入导出。
- 请求日志保存调用发生时的订阅快照，账号后续重新分类不会改写历史记录。
- 后台统计增加 Free、Pass、待确认分组的账号数、活跃数、请求数和 Token 用量。
- “删除全部账号”只删除账号，不清空用户、API Key、模型和监听配置。

#### 7. 并发安全与持久化

- 账号状态、Token 刷新、使用次数、冷却信息和分组配置统一通过账号池锁保护。
- 分组变更和调度并发时串行落盘，避免配置文件互相覆盖。
- OAuth 状态读取使用快照，避免后台轮询与登录协程产生数据竞争。

### Changed

- `AccountPool` 增加 `KeyGroups` 和 `GroupIndexes` 配置字段。
- API Key 生成接口支持 `allowedGroups`；未提供时保留兼容旧调用的双组默认行为。
- `GET /admin/api/keys` 在保留 `keys` 的同时增加 `keyRecords`，返回每个 Key 的授权组。
- `GET /admin/api/stats` 增加 `subscriptionGroups`。
- 请求日志增加订阅快照字段。
- 管理后台增加账号分组控制、Key 分组控制和分组用量概览。
- `version.go` 支持通过 `-ldflags "-X main.appVersion=<version>"` 注入发布版本号。

### Fixed

- 修复 OAuth 登录后账号订阅类型始终为空、无法判断 Free/Pass 的问题。
- 修复旧账号配置加载后没有明确分组状态的问题，统一迁移为待确认。
- 修复 Token 刷新、失败重试和免费模型降级可能绕过 Key 授权组的问题。
- 修复模型冷却状态和账号状态更新的并发落盘风险。
- 修复后台“删除全部账号”误清空其他系统配置的问题。
- 修复代理环境变量测试依赖标准库全局缓存、导致测试顺序不稳定的问题。

### API 变更

#### 账号分组

```http
POST /admin/api/accounts/subscription
Content-Type: application/json

{
  "accountIds": ["acc_xxx"],
  "subscription": "free"
}
```

允许值：`unknown`、`free`、`pass`。

#### Key 分组

```http
POST /admin/api/keys/groups
Content-Type: application/json

{
  "key": "cline_xxx",
  "allowedGroups": ["free", "pass"]
}
```

#### OAuth 状态

OAuth 完成后，`GET /admin/api/oauth/status` 的 `data` 增加：

```json
{
  "done": true,
  "success": true,
  "email": "user@example.com",
  "subscription": "pass"
}
```

### 数据迁移与兼容性

- 旧 `.cline-accounts.json` 可以直接启动，旧账号会被标记为 `unknown`。
- 旧 `keys` 数组会保留，同时迁移为 Free + Pass 两组授权。
- 旧请求日志没有订阅快照时按 `unknown` 统计。
- OpenCode 用量不计入 Cline Free/Pass 分组统计。
- 已有客户端无需修改 OpenAI 或 Anthropic 请求格式；只需使用后台生成的 API Key 和允许的模型。
- 旧账号如需加入调度，需要在后台账号列表中手动设置 Free 或 Pass。

### 本版本涉及文件

| 文件 | 变更内容 |
|---|---|
| `types.go` | 增加订阅分组、Key 分组游标和后台用户持久化字段 |
| `groups.go` | 分组迁移、权限判断、账号批量修改、分组统计和错误处理 |
| `auth.go` | Cline 计划接口、Free/Pass 识别和 OAuth 登录结果处理 |
| `pool.go` | 分组账号选择、分组游标、状态落盘和 CLI OAuth 账号初始化 |
| `admin.go` | 后台用户、账号分组、Key 分组、导入导出、OAuth 状态和统计 API |
| `admin_html.go` | 登录门禁、用户管理、账号分组、Key 分组和统计 UI |
| `proxy.go` | 三种协议的 API Key 分组隔离、重试、冷却和降级约束 |
| `responses.go` | Responses 协议分组调用支持 |
| `request_logs.go` | 请求日志订阅快照和分组用量统计支持 |
| `i18n.go` | 后台用户与订阅分组相关中英文文案 |
| `free_model_test.go` | 免费模型和代理环境测试兼容调整 |
| `protocol_free_model_test.go` | 三协议免费模型测试数据和分组场景 |
| `http_test.go` | 代理环境测试隔离，避免测试顺序依赖 |
| `groups_test.go` | 分组迁移、调度、协议、冷却、导入导出和并发测试 |
| `auth_subscription_test.go` | Cline 计划响应解析和手动选择优先级测试 |
| `CHANGELOG_FEATURES.md` | 详细功能、API、迁移和测试记录 |
| `cline2api_v1.md` | v1 版本发布记录和后续版本模板 |

### 测试与验证

本版本已验证：

```text
go test ./...
go test -race ./...
go test -shuffle=on -count=3 ./...
go vet ./...
go build .
git diff --check
```

覆盖范围包括：

- 旧配置迁移和重新加载
- Free / Pass / 双组 Key 调度
- 三种协议及无前缀兼容路由
- 流式与非流式请求隔离
- Token 刷新、失败重试和模型冷却
- 免费模型降级范围隔离
- 组内无账号时的明确错误
- 账号批量修改和原子校验
- 账号导入导出往返
- 分组统计和请求日志快照
- OAuth 订阅计划解析
- 账号池并发读写和竞态检查

---

## 后续版本记录模板

发布新版本时复制下面的小节，填入真实版本号和日期。不要把未合入代码或未验证的功能写入已发布版本。

```markdown
## v1.x.y — 简短版本标题

发布日期：YYYY-MM-DD

### Added

- 新增功能：

### Changed

- 行为或配置变化：

### Fixed

- 修复问题：

### Compatibility / Migration

- 配置迁移、API 兼容性或升级注意事项：

### Tests

- `go test ./...`
- `go test -race ./...`
- `go vet ./...`
- `go build .`
```

## v1 版本号约定

- `v1.0.0`：当前首个完整分组版本，包含后台用户管理、Free/Pass 分组、登录订阅识别和三协议隔离。
- `v1.x.0`：新增向后兼容的功能模块。
- `v1.x.y`：修复问题、测试补充、文档更新或不改变数据格式的小改动。
- 如果数据结构、API 权限或配置格式发生不兼容变化，应进入新的主版本规划，不要只修改补丁版本号。

## 发布流程

每个版本的独立发布正文保存在 `releases/<版本标签>.md`，例如 [`releases/v1.0.0.md`](releases/v1.0.0.md)。GitHub Actions 在推送 `v*` 标签后读取对应文件，并在桌面端构建成功后创建 Release。发布说明会与该版本代码一起进入 Git 历史。

每个版本合入后，按以下顺序更新：

1. 在本文件顶部新增版本小节。
2. 新增 `releases/<版本标签>.md`，同步更新 [`CHANGELOG_FEATURES.md`](CHANGELOG_FEATURES.md) 的详细功能说明。
3. 运行完整测试和构建检查。
4. 使用版本标签构建发布包：

   ```bash
   git tag v1.0.0
   git push origin v1.0.0
   ```

5. GitHub Release 的标题、正文和标签版本保持一致。
6. 发布说明中必须写明配置迁移和旧账号处理方式，尤其是 `unknown` 账号不会被自动授权。

## 版本保留与安全回退

- 修改前基线：`pre-v1.0.0`，指向本轮修改之前的提交。
- 当前版本：`v1.0.0`，指向包含后台管理、分组及订阅识别的提交。
- 每次修改都先提交，再创建新的版本标签；已发布标签不移动、不覆盖、不删除。
- 普通分支推送保留提交历史；推送 `v*` 标签才触发版本构建和 Release。
- Git 只保留代码及已提交文档，不保留被忽略的账号凭据、配置数据和日志。升级或降级前必须另外备份这些文件。

### 从旧版本创建恢复分支

先确认工作区没有未保存修改，再从目标标签建立分支；不会删除主分支历史：

```bash
git fetch origin --tags
git switch -c recovery-pre-v1 pre-v1.0.0
```

也可以直接下载 GitHub 对应标签的源码或 Release 产物来运行旧版。

### 撤销错误提交并保留历史

当错误版本已推送到 GitHub 时，优先使用反向提交：

```bash
git switch main
git pull --ff-only
git revert <错误提交的SHA>
git push origin main
```

`git revert` 会新增一个撤销提交，保留错误版本及此前的所有版本。合并提交或连续多次修改需要先确定撤销范围，不应直接复制命令盲目执行。不要对共享主分支使用强制推送或删除历史的回退方式。

## 相关文档

- [`CHANGELOG_FEATURES.md`](CHANGELOG_FEATURES.md)：详细设计、接口和测试记录
- [`README.md`](README.md)：中文安装与使用说明
- [`README.en.md`](README.en.md)：英文安装与使用说明
- [`model-api.md`](model-api.md)：模型同步接口说明
