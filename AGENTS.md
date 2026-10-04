# 开发与发版约定

- 用独立提交保存完成且验证通过的改动，保留可回退的检查点。
- 修复已推送的改动使用新的提交或 `git revert`；不得对已发布或已推送的历史做 rebase、reset 后强推。
- 不使用 `git push --force` 或 `--force-with-lease`，不删除、移动或强制覆盖已有版本 tag。
- 发版使用尚未使用的新版本号，同步更新 `cmd/cline-proxy/VERSION` 和新增的 `docs/releases/<tag>.md`，运行 `make check`。
- 创建新的版本 tag 并推送，等待 GitHub Actions 完成，确认 GitHub Releases 发布说明和三个平台的资产齐全后再报告发版成功。
- 已发布的 Release、发布说明和资产保留原样；需要修正时发布新版本。
- 回退方法见 `docs/development.md`。切换旧版本前备份运行数据，避免将账号凭据加入 Git。

<!-- CODEGRAPH_START -->
## CodeGraph

在仓库根目录存在 `.codegraph/` 时，定位或理解代码应先调用 `codegraph_explore`（传入当前项目路径）或 `codegraph explore "<符号或问题>"`，再按需搜索或读取源码。

不存在 `.codegraph/` 时跳过 CodeGraph，不主动建立索引。
<!-- CODEGRAPH_END -->
