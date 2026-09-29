# Cline Free / Pass 分组改进方案

## 1. 目标与范围

本方案只针对 Cline，上游统一使用 Cline 官方接口，不引入其他反代项目或其他模型提供商。

目标：

1. 增加并稳定维护两个逻辑分组：`free` 与 `pass`。
2. 按 Cline 官方模型目录的响应分组确定模型归属，而不是仅凭模型名、价格或前缀猜测。
3. 保留 Cline 原有登录、Token 刷新、流式转发和模型同步能力。
4. Free Key 与 Pass Key 做权限隔离；Pass 账号可以调用免费模型，但免费账号不能调用 Pass 模型。
5. 兼容现有配置、API Key、账号数据和历史请求日志。

本方案暂不处理：Cline 以外的反代接入、Cline Cloud、其他上游协议、计费购买和自动订阅。

## 2. 现状与主要问题

当前项目已经有账号订阅状态、Key 分组和调度隔离，主要代码位于：

- `cmd/cline-proxy/groups.go`
- `cmd/cline-proxy/models_sync.go`
- `cmd/cline-proxy/pool.go`
- `cmd/cline-proxy/proxy.go`
- `cmd/cline-proxy/auth.go`

需要改进的地方：

1. 模型同步把非免费模型统一视为 `pass`，但官方 `recommended[]` 只是推荐模型，不等同于 ClinePass 套餐模型。
2. 当前分组主要表达“账号订阅状态”，模型目录和额度池的身份没有完全分离。
3. 免费模型可能使用普通供应商 ID，不能只通过 `cline-free/` 前缀判断。
4. 上游模型目录会变化，静态模型和远程模型之间需要更清晰的缓存与回退规则。
5. 上游请求头、错误分类和模型通道前缀需要集中管理，避免版本升级时散落修改。

## 3. 目标数据模型

### 3.1 账号分组

继续使用账号的订阅状态，但明确其语义：

```text
unknown  未确认，不参与需要明确权益的调度
free     未确认 ClinePass，允许免费模型
pass     已确认 ClinePass，允许免费模型和 Pass 模型
```

`unknown` 不应被静默当作 `free` 或 `pass`。手动导入 Token 时默认保持 `unknown`，OAuth 登录或显式后台确认后再设置实际状态。

### 3.2 模型分组

远程目录解析为：

```text
free[]       -> modelGroup = free
clinePass[]  -> modelGroup = pass
recommended[] -> modelGroup = recommended
clineCloud[] -> 暂不纳入
```

`recommended` 只表示官方推荐位，不自动加入 Pass 组。第一阶段可将其作为只读展示或暂不对外广告，避免错误宣称套餐权益。

模型记录建议增加明确字段：

```go
type Model struct {
    ID          string
    Provider    string
    Group       string // free / pass / recommended
    UpstreamID  string // 实际发送给 Cline 的完整 ID
    Source      string // remote / builtin / custom
    Status      string
}
```

如果为了兼容现有结构暂时不增加字段，则至少让 `Cost` 不再承担“订阅权益”语义，并在同步阶段单独保存模型组信息。

## 4. 模型同步方案

### 4.1 请求接口

固定请求：

```text
GET https://api.cline.bot/api/v1/ai/cline/recommended-models
```

请求头集中定义，至少包含：

```text
Accept: application/json
X-CLIENT-TYPE: cline-sdk
User-Agent: Cline/<version>
X-CLIENT-VERSION: <version>
```

版本号可以使用配置值或固定兼容值，但不得在多个文件中重复硬编码。

### 4.2 解析规则

按响应数组解析，规则如下：

1. 先处理 `free`，写入 `Group=free`。
2. 再处理 `clinePass`，写入 `Group=pass`。
3. 最后处理 `recommended`，写入 `Group=recommended`。
4. 以完整 `id` 去重，不因不同数组重复出现而覆盖已有明确分组。
5. `clineCloud` 暂不纳入模型列表。
6. 空 ID、非法条目和空响应返回同步失败，不覆盖上一次成功数据。

重要原则：

- 模型归组以数组来源为准。
- `cline-free/`、`cline-pass/` 是上游通道标识，转发时必须保留。
- 不能用 `pricing == 0`、`:free` 后缀或供应商前缀替代官方数组分组。

### 4.3 缓存与回退

采用三层数据优先级：

1. 本次成功远程同步结果。
2. 上一次成功同步并持久化的结果。
3. 最小内置兜底表。

同步失败时保留旧数据，并在管理接口中返回失败原因；不能用空结果覆盖已有模型，也不能每次启动都恢复过期的完整硬编码名单。

## 5. 账号与 Key 调度

权限矩阵：

| Key 允许组 | Free 模型 | Pass 模型 |
|---|---:|---:|
| `free` | 允许 | 拒绝 |
| `pass` | 允许 | 允许 |
| `free,pass` | 允许 | 允许 |

账号选择矩阵：

| 账号状态 | Free 模型 | Pass 模型 |
|---|---:|---:|
| `free` | 可选 | 不可选 |
| `pass` | 可选 | 可选 |
| `unknown` | 默认不选 | 不选 |

调度流程：

1. 先根据 API Key 解析允许分组。
2. 再根据模型记录解析目标分组。
3. 过滤账号状态、启用状态、冷却状态和 Key 权限。
4. 无候选账号时返回明确错误，不扩大授权范围。
5. Free 模型的重试和降级必须继续留在原授权范围内，不能从 Free Key 切到 Pass 组。

## 6. Cline 转发层改进

### 6.1 请求构造

统一构造 Cline 请求：

- URL 使用 `/api/v1/chat/completions`。
- `Authorization: Bearer <token>`，保留 `workos:` 前缀。
- 保留完整 `UpstreamID`，不剥离 `cline-free/` 或 `cline-pass/`。
- 统一附加 `X-CLIENT-TYPE` 和必要客户端标识。
- 继续支持流式响应，并确保上游返回的模型名不会错误覆盖客户端请求模型名。

### 6.2 错误处理

建议明确区分：

- `401`：Token 失效，尝试刷新一次后重试。
- `403 ENTITLEMENT_ERROR`：账号没有 Pass 权益，不应把账号标记为临时限流。
- `403 product surface`：免费模型缺少产品标识或模型不允许当前通道，返回可诊断错误。
- `429`：按上游限流语义进入冷却。
- 网络错误：进入短时冷却并切换同组账号。
- 模型不存在：不自动替换为其他模型。

## 7. 账号订阅识别

分两阶段实现：

### 第一阶段：保持安全默认值

- 手动导入账号：`unknown`。
- 旧账号迁移：保留原状态；无法确认时设为 `unknown`。
- 管理后台允许管理员显式设置 `free/pass/unknown`。

### 第二阶段：登录后自动识别

OAuth 或设备授权成功后，使用 Cline 只读计划接口查询订阅状态：

- 查询成功且明确为 ClinePass：设置 `pass`。
- 明确未订阅：设置 `free`。
- 接口 404、字段缺失、网络错误或权限错误：保留 `unknown`，不能猜测。

订阅识别失败不应阻塞账号导入，但 Pass 模型调度必须拒绝 `unknown` 账号。

## 8. 兼容性与数据迁移

启动迁移需要保证：

1. 旧 `keys` 数组继续可读。
2. 旧 Key 没有分组时，按兼容策略初始化为 `free,pass` 或由管理员确认；不要在不说明的情况下扩大已有 Key 权限。
3. 旧账号没有订阅字段时设为 `unknown`。
4. 旧模型 `Cost` 字段保留读取能力，但新同步结果以 `Group` 为准。
5. 迁移前生成 `.bak` 备份；解析失败时不得覆盖原文件。
6. 历史请求日志保存当时的账号订阅快照，不能因后续重新分类而改写历史统计。

## 9. 管理接口与界面

建议新增或调整：

- `GET /admin/api/models`：返回模型的 `group`、`source`、`upstreamId`。
- `POST /admin/api/models/sync`：同步官方目录，返回 added/removed/updated 和失败原因。
- `POST /admin/api/accounts/subscription`：批量设置 `unknown/free/pass`。
- `POST /admin/api/keys/groups`：设置 Key 允许的 `free/pass` 组。

界面要求：

- 模型列表显示 Free、Pass、Recommended 三种来源标签；第一阶段只把 Free/Pass 作为可调度组。
- 账号列表显示订阅状态和状态来源（自动识别 / 管理员设置 / 未确认）。
- Key 列表显示允许组，并在修改时明确提示权限影响。
- 同步失败时保留旧模型列表并展示上次成功时间。

## 10. 实施顺序

### 阶段 A：模型数据层

1. 增加模型组字段或独立组映射。
2. 重写 `models_sync.go` 的分组解析。
3. 增加远程缓存和最小兜底表。
4. 为 Free/Pass/Recommended 重复模型补充单元测试。

### 阶段 B：路由与权限

1. 将 `modelGroup()` 改为读取同步后的模型组。
2. 完成 Key 权限与账号订阅状态矩阵。
3. 检查所有重试、降级、冷却路径都不越权。

### 阶段 C：Cline 转发兼容

1. 集中管理请求头和 URL。
2. 补充 401/403/429/网络错误分类。
3. 验证流式和非流式响应。

### 阶段 D：迁移与管理界面

1. 增加配置迁移和备份。
2. 暴露模型组、账号订阅和 Key 权限。
3. 增加同步结果和失败原因展示。

## 11. 测试与验收标准

### 单元测试

- `free[]` 中无 `cline-free/` 前缀的模型仍归 Free。
- `clinePass[]` 中带前缀和不带前缀的模型均归 Pass。
- `recommended[]` 不自动归 Pass。
- 重复模型按完整 ID 去重且分组稳定。
- 未识别模型不会放大 Free Key 权限。
- unknown 账号不会被选作 Pass 模型账号。
- Key 分组修改后立即影响下一次请求。

### 集成测试

- 官方目录成功、超时、非 JSON、空列表时的缓存行为。
- Free/Pass 两组账号轮询和冷却恢复。
- 401 刷新后重试一次。
- 403 权限错误不触发错误的长时间冷却。
- 流式响应完整透传，模型字段和 usage 不丢失。
- 旧配置迁移后账号、Key、模型和日志仍可读取。

### 验收原则

1. 不引入其他上游。
2. 不把推荐模型伪装成 Pass 模型。
3. 不因同步失败清空可用模型。
4. 不因 Free/Pass 分组切换扩大 API Key 权限。
5. 不使用真实账号进行未经授权的推理或扣费测试。

## 12. 推荐的第一批代码改动

第一批只做低风险的数据层改动：

1. 在 `models_sync.go` 中按 `free[]`、`clinePass[]`、`recommended[]` 保存分组。
2. 调整 `modelGroup()`，优先读取远程分组，未知模型按非免费处理。
3. 保留现有 `groups.go` 的 Free/Pass Key 权限矩阵。
4. 增加同步失败保留旧清单的逻辑。
5. 增加对应测试和一份脱敏的官方响应 fixture。

完成并验证这一批后，再决定是否调整管理界面和账号自动订阅识别。

## 13. 第一批执行记录

已完成：

- `Model` 增加 `group` 字段，兼容旧 `cost` 字段。
- Cline 远程目录按 `free[]`、`clinePass[]`、`recommended[]` 分组保存。
- Free 数组中的无前缀模型仍保持 Free 归属。
- Recommended 模型不再因 `FREE` 标签被误判为 Free，并单独标记为 `recommended`。
- 同步结果增加 `updated`，同步写盘失败时回滚内存模型列表。
- Cline 模型目录请求补充官方客户端标识请求头。
- 可用性接口暴露模型组，并单独展示 Cline Recommended。
- 增加模型分组、重复模型、能力字段和同步失败保留旧数据测试。

验证结果：

```text
go test ./...
通过
```

## 14. 第二批执行记录

已完成：

- `/v1/models` 返回 `group` 和 `source` 字段，Free Key 不会看到 Pass/Recommended 模型。
- 管理模型接口对旧记录补齐有效 `group`/`source`，避免 `omitempty` 让前端丢失分组信息。
- 管理后台模型列表按 Cline Free、Cline Pass、Cline Recommended 分组展示，不再用旧 `cost` 字段误判推荐模型。
- 模型同步弹窗同时展示新增、更新和移除的模型。
- 手动添加模型校验 `free/pass` 分组，并同步写入 `Model.Group`。
- Cline 注册和刷新请求复用统一客户端标识请求头；WorkOS 请求保持原有头部。
- 解析上游 403 时识别 `ENTITLEMENT_ERROR`、产品面错误和限流错误；entitlement 拒绝不会进入临时冷却。
- Pass 账号收到明确 entitlement 拒绝后降为 `unknown`，并尝试同组下一个 Pass 账号；全部失败时向调用方保留明确的 403 错误。
- 增加旧模型缺失 `group` 时回退 `cost`、模型列表分组字段和 entitlement 重试/失败闭环测试。

验证结果：

```text
go test ./...
通过

go test -race ./cmd/cline-proxy -run 'Test(Cline|Group|ModelGroup|BuildRemoteModels|RemoteModel)' -count=1
通过
```
