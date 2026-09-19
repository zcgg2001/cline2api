<div align="center">

# Cline2API

Cline API 反向代理 · 多账号轮询 · 三协议兼容 · 桌面端

[![Go](https://img.shields.io/badge/Go-1.25-00ADD8?logo=go&logoColor=white)](https://go.dev)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)
[![Platform](https://img.shields.io/badge/Platform-Windows%20%7C%20macOS%20%7C%20Linux-blue)](#构建)

**🌐 English: [English README](README.en.md)**

</div>

---

## 简介

Cline2API 是 Cline API 的反向代理服务，支持多账号轮询、OpenAI Chat Completions / Responses 和 Anthropic Messages 三种协议、API Key 鉴权，内置中英文管理后台。提供跨平台桌面端单文件应用（Windows / macOS / Linux）。

当前版本 **v1.2.0**：新增账号计费与用量面板、批量账号管理、原子持久化，以及焕新的后台登录界面。详见 [v1.2.0 发布说明](releases/v1.2.0.md) 和 [v1.1 改进记录](cline2api_v1.1.md)。

**开发语言**：Go（后端 + 代理 + 桌面壳），HTML/CSS/JS（管理后台前端，内嵌于二进制）。

## 功能

- **三协议兼容**：支持 `/v1/chat/completions`、`/v1/responses` 和 `/v1/messages`
- **后台角色权限**：管理员管理配置；普通用户只读查看账号、统计、模型和日志，并可修改自己的密码
- **账号管理**：状态概览、邮箱/ID 搜索、组合筛选、卡片/列表与分页、详情侧栏；批量测试、刷新凭据、分组、导出和删除，逐项显示执行结果
- **官方额度与费用**：管理员可同步 ClinePass 的 5 小时/周/月已用百分比和重置时间，查看个人余额、最近 20 条账单实际扣费及订阅标价；未订阅和查询失败不会显示为 0% 额度
- **多账号轮询**：自动在多个 Cline 账号间切换负载（`round_robin` / `fill` / `random` 策略）
- **中英文管理后台**：浏览器访问 `/admin/` 管理账号、API Key、模型配置、请求头、代理设置；自动跟随浏览器语言，侧栏可手动切换
- **动态模型同步**：启动时自动拉取 Cline 官方推荐模型接口（免费/订阅模型），模型变化时弹窗提示，也可在后台手动「从 Cline 同步模型」
- **自定义模型**：后台可手动添加/删除模型 ID，并自由选择默认模型（未设置时自动回退到第一个免费模型）
- **API Key 鉴权**：保护代理端点，支持生成/删除多个 API Key
- **System Prompt 覆盖**：项目目录下放 `override.md` 则自动替换系统提示词
- **账号导入/导出**：支持 OAuth 登录、手动 Token、批量文件导入，以及跨设备导出
- **请求日志**：记录每次请求的 token 用量、耗时、TPS 等指标
- **桌面端**：单文件跨平台桌面应用（Wails v2），关闭窗口即停止服务

## 快速开始

### 方式一：桌面端（推荐，分享给他人）

从 [Releases](https://github.com/luawei1/cline2api/releases) 下载对应平台的可执行文件，双击运行即可。

> Windows 提示 SmartScreen「已保护你的电脑」是**未购买代码签名证书的正常现象**，
> 点击「更多信息 → 仍要运行」即可，不影响使用。

| 平台 | 文件 | 说明 |
|------|------|------|
| Windows x64 | `cline-proxy-desktop.exe` | Win10/11 自带 WebView2 |
| macOS Apple Silicon | `cline-proxy-desktop-darwin-arm64` | 需 Xcode CLT |
| macOS Intel | `cline-proxy-desktop-darwin-amd64` | 需 Xcode CLT |
| Linux x64 | `cline-proxy-desktop-linux-amd64` | 需 GTK3 + WebKit2GTK |

### 方式二：命令行

```bash
go build -o cline-proxy .
./cline-proxy              # 默认端口 3457
./cline-proxy -port 8080   # 指定端口
```

启动后访问 http://127.0.0.1:3457/admin/ 进入管理后台。

首次使用 `admin / admin` 从本机登录，必须先修改初始密码才能使用后台。新密码为 8–72 字节；已有用户和密码兼容迁移。服务器或容器部署可在首次启动前设置 `CLINE_ADMIN_PASSWORD`，直接初始化管理员密码。

### 方式三：Docker

```bash
export CLINE_ADMIN_PASSWORD='请替换为你自己的强密码'
docker compose up -d      # 构建并启动
docker compose logs -f     # 查看日志
docker compose down        # 停止
```

容器内监听 `0.0.0.0:3457`，Compose 默认仅映射宿主机 `127.0.0.1:3457`。配置、账号和日志统一保存在 `./data/`。容器内登录使用用户名 `admin` 和上面设置的密码。

从 v1.0 升级 Docker 前，停止旧容器并备份配置，将原有 `.cline-accounts.json`、`.cline-request-logs.json`、`.cline-config.json`、`.cline-zen.json` 和可选 `override.md` 复制到 `./data/` 后再启动。不要继续单独绑定挂载账号文件，原子替换需要挂载其所在目录。

## 使用指南

### 1. 添加 Cline 账号

在管理后台 **账号管理 → 导入账号**：

- **OAuth 浏览器登录**：点击按钮启动设备授权流程，在系统浏览器中完成登录（支持已登录 Cline 的浏览器）
- **手动输入 Token**：输入已有账号的 refreshToken
- **批量文件导入**：上传 JSON 文件或粘贴文本（每行一个 token，或 JSON 数组 `[{refreshToken, email}]`）

### 2. 配置客户端

```
Base URL: http://127.0.0.1:3457/v1
API Key:  <在管理后台生成的 Key>
Model:    cline-free/glm-5.2
```

支持 OpenAI Chat Completions / Responses 和 Anthropic Messages API。

### 3. 账号导出/导入（跨设备迁移）

- **导出**：账号管理页面点击「导出」按钮，下载 `cline-accounts-export.json`
- **导入**：在另一台设备上用「从文件导入」上传该文件
- 导出格式与批量导入格式完全兼容

### 4. System Prompt 覆盖

在 exe 同目录下创建 `override.md`，内容将替换所有客户端请求的系统提示词。

### 5. 监听地址与访问设置（局域网 / 多网卡）

默认只监听 `127.0.0.1`（仅本机可访问）。管理后台 **访问设置** 区可：

- **监听地址下拉选择**：`127.0.0.1`（仅本机）/ `0.0.0.0`（所有网卡）/ 本机检测到的 IP，保存后自动重启监听立即生效，选择会自动检测并展示本机 IP 列表
- **管理后台密码**：必须登录；点击「修改密码」更新当前用户凭据并撤销该用户全部会话。密码不能清空。会话最长 24 小时有效，管理员可管理其他用户

命令行也可指定监听地址（优先级：`-host` > 环境变量 > 后台设置 > `127.0.0.1`）：

```bash
# CLI：监听所有网卡（局域网设备可通过本机 IP 访问）
./cline-proxy -host 0.0.0.0

# 指定某个网卡的 IP
./cline-proxy -host 192.168.1.100

# 环境变量方式（桌面端同样支持）
CLINE_PROXY_HOST=0.0.0.0 ./cline-proxy
```

> 对外监听前完成初始密码设置，并创建 API Key；未配置任何 Key 时，代理接口仍兼容匿名调用。请通过防火墙限制访问，公网部署应使用 HTTPS 反向代理。

## 构建

### 桌面端（单文件跨平台）

Wails 依赖各平台原生 WebView，需在目标系统上构建：

```bash
# Windows（当前机器）
./desktop/build.sh

# macOS
xcode-select --install
./desktop/build.sh

# Linux
sudo apt install libgtk-3-dev libwebkit2gtk-4.1-dev
./desktop/build.sh
```

### CI 自动构建

推送 `v*` 标签触发 GitHub Actions 三平台自动构建并发布 Release：

```bash
git tag v1.2.0
git push origin v1.2.0
```

### 发布 zip（网盘分发推荐）

裸 exe 上传网盘后浏览器容易拦截，打包成 zip 可显著降低拦截率：

```bash
./desktop/build.sh && ./desktop/dist.sh
# 产物：desktop/dist/ccline2api-windows-amd64.zip
```

## 数据文件

程序按以下顺序查找数据文件（找到即使用）：

0. 若设置 `CLINE_PROXY_DATA_DIR`，下表文件直接使用该目录
1. 可执行文件所在目录
2. 当前工作目录
3. 用户主目录 `~/.cline2api/`

| 文件 | 说明 |
|------|------|
| `.cline-accounts.json` | 账号池、API Key、自定义模型与默认模型 |
| `.cline-request-logs.json` | 请求日志 |
| `.cline-accounts.json.bak` | 成功加载并迁移前的账号文件备份，同样含敏感凭据 |
| `.cline-config.json` / `.cline-zen.json` | 代理与 OpenCode 配置 |
| `override.md` | System Prompt 覆盖（可选）|

> ⚠️ 账号文件含明文 refreshToken，属于敏感凭据，不要放入发布包或提交到 Git。

账号文件解析失败时程序会报错退出并保留原文件；修复文件或从备份恢复后再启动。密码使用 bcrypt 保存，旧密码哈希在成功登录后自动迁移；旧密码超过 72 字节时保留兼容验证，需要主动改密。

## 可用模型

**默认动态同步**：程序启动时会自动从 Cline 官方推荐模型接口拉取最新模型（免费 / cline-pass / 推荐模型），
模型列表变化时管理后台会弹窗提示，也可在「设置 → 可用模型」点击「从 Cline 同步模型」手动刷新。

- 同步成功后，后台模型列表以**远程模型**为主（内置硬编码模型仅作为离线 fallback）
- 远程模型直接可用；后台「可用模型」区可添加/删除**自定义模型**（带 ✕ 删除按钮的是自定义项）
- 默认模型可在「代理配置 → 默认模型」下拉中设置；未设置时自动回退到第一个免费模型

> 内置 fallback 模型（离线/同步失败时兜底）：
> `cline-free/glm-5.2`、`cline-pass/glm-5.2`、`cline-pass/deepseek-v4-flash`、`cline-pass/qwen3.7-max`、`deepseek/deepseek-v4-flash`、`poolside/laguna-s-2.1:free`

## 项目结构

```
├── main.go              CLI 入口（go build .）
├── desktop_main.go      桌面端入口（go build -tags desktop）
├── proxy.go             HTTP 服务、API 路由、协议转换、SSE
├── admin.go             管理后台 REST API
├── admin_html.go        管理后台前端（内嵌）
├── auth.go              WorkOS OAuth + Token 刷新
├── pool.go              账号池管理、多位置数据查找
├── request_logs.go      请求日志
├── desktop/             桌面端构建脚本、文档、图标生成器
├── Dockerfile           Docker 构建
├── docker-compose.yml   Docker Compose
└── .github/workflows/   CI 三平台自动构建
```

## 技术栈

- [Go 1.25](https://go.dev) — 后端、代理、桌面壳
- [Wails v2](https://wails.io) — 跨平台桌面 WebView（单文件，非 Electron）
- [WebView2](https://developer.microsoft.com/microsoft-edge/webview2/) / [WebKit](https://webkit.org) / [WebKitGTK](https://webkitgtk.org) — 各平台原生 WebView

## 许可证

[MIT License](LICENSE) © 2026 [luawei1](https://github.com/luawei1)
