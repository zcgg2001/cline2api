# 文档导航

开发和发版规则、旧版本运行方法及撤销提交流程见 [开发与回退](development.md)。

项目文档按用途分组，避免发布说明、历史设计和接口参考混在根目录。

| 目录 | 内容 |
| --- | --- |
| `reference/` | 外部接口、数据来源和模型同步说明 |
| `plans/` | 优化计划和后续设计 |
| `history/` | 版本设计、功能记录和历史变更 |
| `releases/` | GitHub Release 正文（CI 从这里读取） |

最近一次修改记录：[2026-09-29 修改记录](history/changes-2026-09-29.md)

根目录的 `README.md` / `README.en.md` 只保留安装、使用和开发入口；可执行程序源码、Go 测试和嵌入式前端资源统一位于 `cmd/cline-proxy/`，构建产物位于 `build/` 和 `desktop/build/`，本地运行数据位于 `data/`。
