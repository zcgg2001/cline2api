# 程序源码

本目录是一个 Go `main` 包。CLI 和桌面版共用业务逻辑，通过 `desktop` 构建标签选择入口；测试与被测源码放在同一包中。

| 文件组 | 职责 |
| --- | --- |
| `main.go` / `desktop_main.go` | CLI / 桌面入口 |
| `proxy.go` / `responses.go` / `compact.go` | HTTP 代理、协议转换、流式响应 |
| `admin*.go` / `availability.go` / `i18n.go` | 后台 API、资源组装、可用性、翻译 |
| `auth.go` / `pool.go` / `groups.go` / `account_billing.go` | 认证、账号池、分组、用量计费 |
| `storage.go` / `request_logs.go` | 持久化与请求日志 |
| `zen*.go` / `capture.go` | OpenCode 集成与流量捕获 |
| `models_sync.go` / `http.go` / `types.go` | 模型同步、HTTP 客户端、共享类型 |
| `version.go` / `VERSION` | 嵌入版本与发布版本覆盖 |
| `*_test.go` | Go 测试 |
| `web/` | 嵌入式模板、样式、脚本、图片及前端测试 |
| `resource_windows_amd64.syso` | Windows 图标与版本资源 |

在**项目根目录**执行：

```sh
make build
make check
go run ./cmd/cline-proxy
sh desktop/build.sh
```

旧的根目录 `go run .` / `go build .` 需要改为 `go run ./cmd/cline-proxy` / `go build ./cmd/cline-proxy`。`-start` 也从项目根目录使用，产物保存在 `build/bin/`。

`wails.json` 随入口移动；若使用 Wails CLI，应在本目录调用并传入 `desktop` 构建标签。日常桌面构建推荐根目录的 `desktop/build.sh`。

不要仅为减少文件数量合并大文件，也不要直接把 Go 文件按类型分到子目录：每个子目录都是独立的 Go 包，无法直接共享未导出的函数和变量。后续确有维护需求时，再按账号管理、协议、存储等职责逐步抽取到 `internal/`。
