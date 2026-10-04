# 开发、发版与回退

## 保留检查点

每个完成且验证通过的功能使用独立提交保存。公共历史只追加提交：已经推送的变更需要撤销时，使用 `git revert <commit>` 创建撤销提交，再正常推送。不重写已发布的历史，不强推分支，不移动、删除或覆盖已有版本 tag。

## 发布新版本

1. 选择未使用的版本号，例如 `v1.3.0`。用 `git ls-remote --tags origin` 和 `gh release list` 检查远程版本。
2. 更新 `cmd/cline-proxy/VERSION`，新增 `docs/releases/<tag>.md`；不要编辑旧版本的发布说明。
3. 执行 `make check` 并提交，正常推送 `main`。
4. 用 `git tag -a <tag> -m "Release <tag>"` 创建新 tag，执行 `git push origin <tag>`。命令失败时先调查原因，不使用 `-f`。
5. 等待 Build 工作流完成，确认 GitHub Releases 有该版本说明及 Windows、macOS Apple Silicon、Linux x64 三个桌面资产。现有 Release 的重复发布会被工作流拒绝。

## 临时运行旧版本

优先从对应的 GitHub Release 下载旧版程序。停止当前程序，备份实际数据目录中的 `.cline-accounts.json`、`.cline-config.json` 和其他运行数据，再替换运行程序。保留新程序和备份，方便再次切换。

需要从源码编译旧版本时，在单独的目录检出已发布 tag，当前开发目录保持不动。例如回到本次功能之前的 `v1.2.1`：

```sh
git worktree add --detach ../cline2api-v1.2.1 v1.2.1
cd ../cline2api-v1.2.1
make build
```

使用独立的 `CLINE_PROXY_DATA_DIR` 存放备份数据的副本，验证旧版本后再决定切换线上程序。`v1.2.1` 及更早版本不识别账号的手动禁用开关，因此回退到这些版本时，禁用账号可能重新参与调度；在数据副本中移除不希望调度的账号后再运行旧版。

## 撤销主分支上的功能

先保存当前未提交的工作，再选择具体功能提交创建撤销提交：

```sh
git revert <feature-commit>
make check
git push origin main
```

如需向用户分发回退后的程序，发布一个新的版本号。旧 tag 和旧 Releases 始终保留，不将它们重新指向其他提交。
