# gitworkbench/
> L2 | 父级: ../CLAUDE.md

Git workbench 是 Solco 家族（Solco 与 Solco Git）共用的本地 Git 域服务：Runner 负责仓库读取与受控变更，界面层（Solco 的 nativeui、Solco Git 的呈现层）只能消费快照及显式执行操作。本模块不持有 Herdr Workspace/Pane 权威，也不提供展示组件。

成员清单
- `backend.go`：Runner 的可选第二实现的接缝——`Backend` 标记接口、按能力分组的小接口（现有 `BranchBackend`、`HistoryBackend`）、`NewError`/`ClassifyStderr`（让后端返回同一套 `*Error` 类别）；对应的 Runner 方法先问 Backend，没实现就走 argv 版 git；单仓库串行仍由 Runner 持有，后端不得再锁（2026-11-12）。
- `branches.go`：读取本地分支、只读远程跟踪引用，校验并安全切换/创建本地分支；`CheckoutRemoteBranch`/`RenameBranch`/`CreateBranchAt`（在指定提交处建分支）。
- `branches_test.go`：本地分支发现及当前分支测试。
- `remote_branches_test.go`：读取真实 Git refs/remotes、忽略符号 HEAD 的契约测试。
- `cache.go`：按仓库根目录缓存 Git 变更快照。
- `changes.go`：工作树变更查询入口。
- `changes_parsing.go`：Git 状态和变更输出解析。
- `changes_snapshot.go`：变更快照的构建与稳定字段；numstat 先行，超 `LargeDiffLines` 的文件排除出批量读取并延迟（Deferred）。
- `changes_untracked.go`：未跟踪文件补充与界限；超行数/4 MiB 的延迟、大文件二进制嗅探、按需整读（≤16 MiB）。
- `commit.go`：受保护的提交事务。
- `commit_prompt.go`：提交消息提示词生成规则。
- `commit_prompt_preview.go`：生成提示词的隐私限制与预览。
- `commit_prompt_preview_test.go`：提示词预览边界回归。
- `commit_prompt_test.go`：提交提示词规则测试。
- `context.go`：Git 仓库根定位和上下文边界。
- `generated.go`：仓库生成文件辅助规则。
- `helpers_test.go`：测试仓库准备工具。
- `large_diff.go`：大 diff 延迟——批量读取的 pathspec 排除（重命名双侧）、读取溢出整体延迟、延迟文件指纹戳（尺寸+时间）、`ReadFileDiff` 按需读单文件（含两侧 hunks，≤16 MiB）。
- `large_diff_test.go`：真实仓库回归——超阈值/超 4 MiB 延迟、超读取上限不再令快照失败、重命名/未跟踪/提交快照、按需读取与过大错误。
- `highlight.go`：语法高亮适配。
- `explain.go`：`Explain(err)` 把 git 失败（被拒/鉴权/网络/远端钩子/分叉…）翻成一句人话，供失败 toast/弹窗；只认 git 协议用语。
- `push_failure_test.go`：真实仓库回归（Push 被拒/钩子拒绝/无远端/upstream 跟随、remote 分支 Merge/Rebase、rebase 冲突自动中止）。
- `history_actions.go`：历史提交撤销/恢复的安全约束。
- `history_actions_test.go`：历史操作边界测试。
- `models_test.go`：领域模型约束测试。
- `mutation_matrix_test.go`：Git 操作拒绝/保护矩阵测试。
- `fork_more.go`：Fork 对齐的大件域能力（ResetTo、DeleteRemoteBranch、Set/UnsetUpstream、FormatPatch/StashPatch/WorkingPatch、CompareToWorktree、FileHistory、Blame、远程增改删、子模块列表/更新、RebasePlan/InteractiveRebase；冲突即 abort，Hard reset 仅干净树）。
- `fork_more2.go`：Fork 第三批域能力（Pull ff/merge/rebase、FetchAll(+prune)、MergeSquash、CheckoutDetached、RestoreFile、ApplyPatch、CreateTagAnnotated、DeleteRemoteTag、RemoveWorktree、UnmergedPaths/ResolveConflict、Reflog、Add/RemoveSubmodule、StashPushPaths/StashClear、ParseWebRepo/CommitURL；冲突型集成动作失败即回滚并带冲突文件，Commit 增 NoVerify/SignOff）。
- `autostash.go`：WithAutostash（Fork 的 Stash and reapply：stash -u → 动作 → pop；动作失败且不在半路则立即还原，半路/重放冲突则保留 stash）+ LocalChangesBlocked（识别"被本地改动挡住"的 Git 拒绝）
- `stash_rename.go`：StashRename——同树同父重建带新留言的 stash 提交 → `stash store` → 丢旧条目（先存后丢）；StashList 现读 reflog 留言（`%gs`），改名可见
- `operations.go`：同步、Stash（含其提交与 base 提交，`StashSnapshot` 把 tracked 变更与第三父的未跟踪文件合成一次审阅，供图谱把 stash 挂到所属提交）、Tag、Worktree、含完整父 SHA 的历史提交及 sequencer 操作，供原生历史图谱使用。
- `operations_test.go`：真实 Git 操作和保护回归。
- `line_patch.go`：按所选行构造单 hunk 补丁并经 `git apply --cached --recount [--reverse] -`（Runner.Stdin）暂存/取消暂存部分行；未选中的删除行回退为上下文、未选中的新增行丢弃。
- `line_patch_test.go`：真实 Git 回归（多 hunk 偏移、反向取消、无换行标记、空选区拒绝）。
- `blob.go`：`BlobSize`/`ReadBlob`（`cat-file -s` / `cat-file blob`，带上限、拒绝选项形态的规格），供 diff 卡片预览二进制两侧。
- `blob_test.go`：真实 Git 回归（HEAD/索引大小与字节、超限拒绝、非法规格拒绝）。
- `commit_graph.go`：`ListCommitGraph`（--branches/--remotes/--tags + HEAD、--date-order、--decorate=full 的真实 %D 装饰，隐藏 ref 走 Git 原生 --exclude，stash/notes 不入图）、`ResolveCommit`（完整 ref→提交，拒绝非 refs/ 名）、`CommitByHash`、`GraphPosition`（commit 在图谱里的行号，窗口据此精确扩大；MaxGraphLimit 2 万）、`CommitRef`。
- `commit_graph_test.go`：真实仓库回归（全分支、装饰、隐藏只去独占提交、HEAD 恒显示、守卫）。
- `commit_parents_test.go`：真实 Git 提交父 SHA 读取回归，支持历史图谱连接。
- `patch.go`：Diff/hunk 补丁构建和应用。
- `repository.go`：Git 仓库元数据读取。
- `runner.go`：带超时、错误分类与单仓库串行的 Git 命令边界。
- `history_rewrite.go`：`RewriteHistory`（reword / squash / fixup / drop / move 一段相邻提交；需要 `HistoryBackend`，没有就拒绝；先要求干净工作区与已附着分支，失败即中止变基）。与 `InteractiveRebase`（整份 todo、argv 实现）并存；`guardedRewrite` 是它与自定义补丁共用的“加锁 → 失败则中止变基并报冲突”外壳。
- `custom_patch.go`：`PatchSource` / `RewriteWithPatch`（`PatchBackend`；从提交移除 / 拆分 / 移动 / 撤回所选文件或行，校验在先、需干净工作区与已附着分支、冲突即中止，没有后端就拒绝，无 argv 回退）。
- `sequencer.go`：merge/revert/cherry-pick 状态探测和完成/中止；`RebaseOnto`（冲突即 abort 并回报冲突文件）。
- `sequencer_test.go`：sequencer 事务状态测试。
- `tree.go`：变更文件的目录树、折叠与过滤模型。
- `tree_at.go`：`ListTree`（`ls-tree -z`，某个 rev 下一层目录的条目：名/路径/模式/目录/子模块/大小，拒绝选项形态与越界路径，条目路径可原样交给 `ReadBlob`，供 Files 的"按提交看整棵树"）+ `FetchedAt`（读 `FETCH_HEAD` 的修改时间，链接 worktree 也成立；只读，从不触发网络）。
- `tree_at_test.go`：真实仓库回归（某提交的树与当前不同、选项/越界拒绝、条目经 ReadBlob 读回、FetchedAt 随 fetch 移动与链接 worktree）。
- `push_many.go`：多引用推送——`ReadPushOffer`（只读、上次 fetch 的记录、不联网：列出对该远端有东西可推或远端尚无的本地分支，含 ahead/新建/被挡原因，另列本地 tag）与 `PushMany`（对勾选的分支/tag 各发一条完整限定的 refspec，一次 `git push`，`MaxPushRefs=200`，可选给无 upstream 的分支 `-u`，**永不 force**，被挡/已不存在的引用在问 Git 之前就拒绝；失败错误由调用方交给 `Explain` 翻成人话）。
- `push_many_test.go`：真实仓库（本地 bare 远端）回归：提议内容、只推选中的引用（含 `-u`）、不覆盖已分叉的远端（拒绝而非强推）、坏输入拒绝。
- `tree_bench_test.go`：大目录树性能基线。
- `tree_empty_test.go`：空树、过滤边界测试。
- `worddiff.go`：文本差异细粒度 word ranges。

[PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
