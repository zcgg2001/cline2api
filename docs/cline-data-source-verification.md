# Cline 额度、费用与健康指标核查

核查时间：2026-09-19。官方仓库版本：`2755adfa463fdebde5510378a14b8bcc919e6295`。证据来自官方开源客户端、仓库内官方文档，以及当天公开可下载的官方订阅后台 JavaScript；没有登录或发送模型推理请求。

> 本文初次核查为上面的无账号阶段。现已完成用户授权的真实账号只读验证与界面接入，最新结果见文末“已接入与真实账号验证”。

## 结论修正

此前“Cline 没有可靠来源的额度百分比、费用和健康评分”的表述过于笼统，应修正为：

| 指标 | 核查结论 | 当前证据边界 |
| --- | --- | --- |
| ClinePass 额度百分比 | 有官方后台接口和展示逻辑，可列入接入方案 | 已确认接口、字段和三个窗口；未用真实账号验证返回数值 |
| 免费模型剩余额度百分比 | 尚未确认通用查询接口 | 已确认免费模型有限额及超限错误；不能据此推算完整剩余百分比 |
| 个人/组织余额 | 官方客户端与文档均有接口 | 需要账号身份及相应组织权限，当前项目尚未接入 |
| 按量消费明细 | 官方类型含 costUsd、creditsUsed、generationId 和 token 数 | 需要认证和时间范围/分页完整性核对；不能把全账号消费都归因于本代理 |
| 单次推理费用 | 官方 Chat Completions 文档定义 usage.cost，单位 USD | 是否返回取决于响应和计费模式；缺失不能当作零 |
| Cline 官方账号健康分 | 本次检索范围内未发现 | 可做本地观测指标，但需明确是本项目统计，不是上游提供的评分 |

初次核查时本地账号池为 0 个账号，未取得真实账户余额、额度百分比或账单；后续验证结果单独记录于文末。

## 1. ClinePass 百分比：已经找到官方来源

官方订阅页：[Cline Dashboard — Subscription](https://app.cline.bot/dashboard/subscription?personal=true)。

该页面的公开前端代码调用 `getCurrentUserPlanUsageLimits()`，对应：

```text
GET https://api.cline.bot/api/v1/users/me/plan/usage-limits
```

响应解析结构是 `data.limits[]`，每个项包含：

- `type`：页面识别 `five_hour`、`weekly`、`monthly`。
- `percentUsed`：已用百分比。
- `resetsAt`：重置时间。

官方页面直接使用 percentUsed 绘制进度条。5 小时为滚动窗口；官方指南将周/月描述为自然周、自然月。实际重置点以接口时间为准，不在客户端自行猜测时区和起点。[官方 ClinePass 指南](https://github.com/cline/cline/blob/2755adfa463fdebde5510378a14b8bcc919e6295/docs/getting-started/clinepass.mdx)

当天可定位的发布资产：

- [官方 API 客户端：usage-limits 路径与响应解析](https://app.cline.bot/_next/static/chunks/4081-5ba301ec67731bde.js)
- [订阅查询 Hook：调用 getCurrentUserPlanUsageLimits](https://app.cline.bot/_next/static/chunks/6300-e451eeafd38b6d0d.js)
- [订阅页面：三个窗口与进度条展示](https://app.cline.bot/_next/static/chunks/app/dashboard/subscription/page-576b90cb48328a88.js)

发布资产地址带内容哈希，后续网站发布可能替换它们。usage-limits 在本次查阅的开源 SDK 账户服务中还没有对应封装，因此接入时应独立做字段校验、失败降级和版本兼容。

本轮以无认证 GET 请求验证该地址，返回 HTTP 401 和需要重新认证的错误。该结果符合认证接口的预期；不能据此确认当前项目的 WorkOS Bearer Token 对该接口必然可用，仍需真实登录态验证。官方网页的 API 客户端使用 credentials: include；开源账户 SDK 的其他账户接口使用 Authorization Bearer。

推荐展示：ClinePass 账号显示三条独立的“已用百分比 + 重置倒计时”，附最后同步时间。没有订阅显示“不适用”，查询失败显示“获取失败”或上次成功结果及其时间，字段缺失显示“未知”。不能将未知当作 0%，也不能将钱包余额当成订阅额度。

## 2. 免费模型：限额与 Pass 分开

官方文档确认免费模型存在使用限额；官方客户端明确区分免费模型配额与 ClinePass 配额。免费模型超限处理能读取后端“Try again in ...”提示，但本次查阅的账户 SDK、订阅页面及其 API 客户端中，未找到通用的免费模型剩余百分比查询方法。

证据：[免费模型说明](https://github.com/cline/cline/blob/2755adfa463fdebde5510378a14b8bcc919e6295/docs/getting-started/free-models.mdx)、[CLI 对两类配额的说明](https://github.com/cline/cline/blob/2755adfa463fdebde5510378a14b8bcc919e6295/apps/cli/src/tui/components/model-selector/cline-model-entries.ts)、[免费模型超限处理](https://github.com/cline/cline/blob/2755adfa463fdebde5510378a14b8bcc919e6295/apps/vscode/webview-ui/src/components/chat/ClineFreeModelLimitError.tsx)。

公开 recommended-models 接口本轮返回 recommended、free、clinePass、clineCloud 模型分类，没有账号级额度字段。模型目录只用于识别模型类别，不能用来计算剩余额度。[模型目录接口](https://api.cline.bot/api/v1/ai/cline/recommended-models)

推荐展示：最近使用成功时间、模型限流/冷却状态、上游提供的恢复时间。未限流不代表还有 100% 额度；累计 token 也不能除以一个猜测的上限来画进度条。

## 3. 余额和费用：有来源，需统一口径

官方 SDK 提供：

| 只读接口 | 可取得的信息 |
| --- | --- |
| `/api/v1/users/me` | 上游用户 ID、所属组织及活跃组织 |
| `/api/v1/users/{id}/balance` | 个人 balance |
| `/api/v1/users/{id}/usages` | 消费记录，包括 costUsd、creditsUsed、generationId、模型、时间、token 数 |
| `/api/v1/users/{id}/payments` | 充值/支付记录，包含 amountCents、credits |
| `/api/v1/users/me/plan` | 订阅计划、周期等信息，不能代替 usage-limits |
| `/api/v1/organizations/{id}/balance` | 组织余额 |
| `/api/v1/organizations/{orgId}/members/{memberId}/usages` | 对应组织成员用量 |

证据：[SDK 账户服务](https://github.com/cline/cline/blob/2755adfa463fdebde5510378a14b8bcc919e6295/sdk/packages/core/src/account/cline-account-service.ts)、[字段定义](https://github.com/cline/cline/blob/2755adfa463fdebde5510378a14b8bcc919e6295/sdk/packages/core/src/account/types.ts)、[官方账户 API 文档](https://github.com/cline/cline/blob/2755adfa463fdebde5510378a14b8bcc919e6295/docs/enterprise-solutions/api-reference.mdx)。

官方 Chat Completions 文档还将 `usage.cost` 定义为单次请求的美元费用。当前项目的 tokenUsage 和 RequestLog 没有费用字段，因此属于“尚未采集”，不能说“上游没有费用数据”。[官方 usage 定义](https://github.com/cline/cline/blob/2755adfa463fdebde5510378a14b8bcc919e6295/docs/api/chat-completions.mdx)

展示和接入注意：

1. 原始 balance/creditsUsed 不能直接加美元符号。官方 CLI 将 balance 除以 1,000,000 后显示美元，扩展消费表也以 creditsUsed / 1,000,000 显示美元；另一处以 /10,000 显示 credits。接入应明确保存原始值及单位，并与官网核对。[CLI 格式化](https://github.com/cline/cline/blob/2755adfa463fdebde5510378a14b8bcc919e6295/apps/cli/src/utils/output.ts)、[消费表](https://github.com/cline/cline/blob/2755adfa463fdebde5510378a14b8bcc919e6295/apps/vscode/webview-ui/src/components/account/CreditsHistoryTable.tsx)
2. ClinePass 属于订阅，官方参考模型单价用于理解消耗，不代表逐次额外扣款。应分开“订阅费用”“订阅额度”“按量钱包消费”，不要简单相加。[官方计费说明](https://github.com/cline/cline/blob/2755adfa463fdebde5510378a14b8bcc919e6295/docs/getting-started/clinepass.mdx)
3. 本代理请求费用与整个账号账单分别展示；同一账号可能同时在 IDE、CLI 和其他设备使用。账单和响应费用若通过 generationId 匹配，需去重，不能再次相加。
4. 聚合“今日/本月消费”前验证账单分页和时间边界。空记录、接口失败、数据不完整与实际消费为零是不同状态。
5. 一般展示只需只读 GET；不调用购买额度、变更订阅或切换活跃组织等写接口。

## 4. 健康：本地可观测，不是官方评分

本次在 Cline 账户 API、SDK 类型和订阅前端中未发现账号健康分。codex2api 的健康条由其自身保存的请求成败分桶生成，调度评分还结合本地错误、延迟等规则；它不是从上游直接读取一个官方“健康分”。[参考健康条实现](https://github.com/james-6-23/codex2api/blob/de41a5e3dfe9ff51524af864532c42e37955346f/admin/account_health.go)

本项目可以展示“本地观测：最近 N 次请求成功率、最近错误、TTFT、连续失败次数”，注明时间范围和样本数。少量或没有样本时显示“样本不足”。综合评分若增加，应公开计算规则，且不要命名为 Cline 官方健康评分。

当前日志存在保留上限，部分流的成功终态与探测计数也需要按优化方案修正。应先保证请求结果准确，再将这些数据用于自动调度。

## 5. 对优化方案的调整

可以进入下一步接入验证的功能：

- Pass 账号：5 小时、周、月三窗口用量条与重置时间。
- 按量消费：官方余额、消费明细和本代理响应中的费用，分别标明范围。
- Free 账号：模型限流与恢复时间，剩余百分比暂显示未知。
- 所有账号：明确标注“本地观测”的请求成功率和故障信息。

真实账号验证门槛：在本地管理后台导入至少一个本人 ClinePass 账号后，使用其账号认证只读查询计划/usage-limits/余额/用量；对照官网同一账号、同一组织、同一时间窗口的显示，确认字段、单位、订阅空值和失败行为。凭据不应粘贴进聊天或写进核查报告。本轮没有执行这一阶段，也没有修改运行代码或展示模拟数值。

## 6. 已接入与真实账号验证（2026-09-19 补充）

用户导入 1 个账号并授权核查与展示后，已用账号 WorkOS Bearer Token 完成验证；未发送模型推理请求，未购买额度、修改订阅或切换组织。

- `/users/me`、个人 `/balance`、`/usages?limit=20` 返回 200。
- `/users/me/plan` 与 `/users/me/plan/usage-limits` 返回 404；结合本地 Free 分组和官方后台处理方式，界面显示“未开通 ClinePass”。401/403 不作此判断。
- 个人余额原始值 500000，按官方 CLI /1,000,000 的美元口径显示 $0.50。
- 最近 20 条记录 `creditsUsed` 合计为 0，实际扣费显示 $0.00；存在 nextToken，明确标注“最近记录”，不宣称完整月账单。
- 账单实测的 `costUsd` 可返回 49770 等原始值，同时 `creditsUsed` 为 0。因此本版不把该字段直接解释为美元或实际扣款；实际扣费仅从 creditsUsed 按官方消费表口径换算。单次推理 `usage.cost` 是另一种字段，不能与账单原始 costUsd 混用。
- 查询前后，本地订阅分组、账号状态、业务请求计数保持一致。需要刷新凭据时，新 refreshToken 原子保存，串行化同账号刷新。

已实现：管理员账号页卡片/列表显示官方额度与费用，详情显示个人钱包余额、最近消费明细、Pass 订阅标价/周期和三窗口额度；2 分钟内存缓存，手动刷新至少间隔 10 秒，最多 3 个账号并发同步，部分失败保留上次有效快照并标注时间。财务数据不返回给普通只读用户，不落盘账单缓存。

Pass 百分比成功响应已按官方字段用模拟上游验证，包括 0%、超过 100%、字段缺失、同步失败；浏览器检查了三窗口进度条和账单详情。当前真实账号无 Pass，因此仍不能宣称“真实 Pass 额度成功读取”或“与该用户官网额度对账完成”。
