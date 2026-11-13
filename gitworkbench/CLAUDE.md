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
- `backend.go` 里还有 `StashBackend`（`StashList`，与 argv 路径逐项一致）。
- `write_backend.go`：`WriteBackend`（分支切换/创建/改名/删除、stash 推入/弹出/应用/丢弃、标签创建/删除）与 `viaWriter`：校验与参数形态留在 Runner，只把「执行那条 git 命令」交给后端；没有后端或该情形不适用时照旧 `Mutate`。
- `amend_pick.go`：`AmendTo`（`AmendBackend`；只允许暂存改动，失败后连 fixup 提交一起回收、改动仍在暂存区，无 argv 回退）与 `CherryPickCommits`（`CherryPickBackend`，无后端时回落 `git cherry-pick --allow-empty`；校验在先、拒绝合并提交、冲突留给序列器继续/中止）。
- `custom_patch.go`：`PatchSource` / `RewriteWithPatch`（`PatchBackend`；从提交移除 / 拆分 / 移动 / 撤回所选文件或行，校验在先、需干净工作区与已附着分支、冲突即中止，没有后端就拒绝，无 argv 回退）。
- `custom_patch.go` 另有 `PatchLinesFor`：把界面里选的（hunk, 行）换成 `PatchSourceLine.Index`，逐行核对种类与文本，对不上就拒绝（界面退回整文件）。`custom_patch_lines_test.go`：映射正确、读法不一致即拒绝。
- `history_preview.go`：批量历史改写的预览（2026-11-12 决定第 (12) 条）：`PreviewHistory`（纯函数；沿第一父链、所选须相邻，列出改写后的提交顺序与每行发生了什么，Squash/Fixup 并入更旧的一个；无合并、无可并入、清空分支、移出范围一律拒绝并给原因）、`PublishedCommits`（`rev-list --not --remotes` 找已在远端的提交）。`history_preview_test.go`：每个动作的结果、各类拒绝、已推送与本地分开；做过变异检查。
- `courier.go`：Change Courier 内核（2026-11-12 决定第 (13) 条）：`CourierSource`（一个文件的整个改动，或其中选中的行；来自工作区或一个提交）、`CourierPatch`（整文件用 Git 自己写的补丁：二进制、模式、改名、未跟踪都在，工作区经临时索引 `workingPatch`；按行按所选一侧切，每个 hunk 的起点按它被应用的那一侧算，合并提交与改名文件的按行被拒）、`PreviewCourier`（目标 worktree 上 `git apply --check`，干净才给文件清单与行数，否则给 Git 的原话；目标在合并 / 变基中也是不干净）、`DeliverCourier`（再预检一次，只进目标工作区、不暂存、走目标仓库的 Safety Journal，Recovery 里名为 Sent changes.patch）、`DiscardCourierLines`（Move：把源工作区里这些行反向应用掉，同样有恢复点）。`line_patch.go` 的 `hunkBody` 是它与 `LinePatch` 共用的行体。`courier_test.go`：整文件 / 未跟踪 / 改名 / 多 hunk 偏移 / 提交来源 / 对不上与合并中被拒 / 恢复点可退回 / hunk 起点；做过变异检查。
- `moved_lines.go`：`MovedLines`（2026-11-12 决定第 (14) 条）：纯函数，找出一个文件 diff 里“搬了家”的行——去空白后完全相同、至少 3 个字符、在删除侧与新增侧各自唯一、且两端不在同一段连续改动里（原地改缩进是改写不是搬家）；键是 {hunk, 行下标}，值带对侧文件行号（`moved_lines_test.go`，每条变异检查过）。
- `stash_lines.go`：`StashLines`（决定第 (14) 条）：把一个文件里选中的、未暂存的行存成原生 stash——HEAD 的树上只叠这些行（临时索引 `GIT_INDEX_FILE`，真实索引不碰），带 index 父提交，`stash store` 入栈，随后用 `DiscardCourierLines` 把这些行从工作区拿掉（走 Safety Journal，可退回）；存之前先 `apply --check --reverse` 确认拿得掉，存后拿失败就把刚存的那条 stash 撤回；未跟踪、已暂存、改名、合并 / 变基中、没选行都在存之前拒；没有 Git 身份时用中性身份（`stash_lines_test.go`，每条变异检查过）。
- `commit_filter.go`：`CommitFilter` / `FilterCommits`（决定第 (14) 条）：Git 一侧的提交查询——路径（`--literal-pathspecs`，文件夹或文件）、日期区间（`YYYY-MM-DD`，两端含当天）、范围（默认图谱的全部引用并保持被隐藏的 ref 隐藏，或仅 HEAD）、Limit；日期格式错、区间倒置、仓库外或以 `-` 开头的路径在 Git 运行前被拒（`commit_filter_test.go`，每条变异检查过）。
- `reflog_undo.go`：`PlanReplay`（只读：下一步可撤销/重做的那个动作与它的身份串）/ `Replay`（按身份串撤销或重做 HEAD 上的 checkout、commit/reset/pull、一次完整 rebase）；LazyGit z/Z 与 TanGit 的 MIT 移植，argv 实现、不写新引用或文件——撤销与重做各写一条带 `[lazygit undo]` / `[lazygit redo]` 标记的 reflog 记录（与 LazyGit 命令行互认、重启后仍平衡）；撤销 commit 是 soft（改动留暂存），重做与 rebase 的两个方向是 hard 并先把已跟踪改动 stash 起来再放回；合并/变基进行中、身份串不符（HEAD 被别的客户端动过）一律拒绝（`reflog_undo_test.go`，每条变异检查过）。
- `journal.go` / `journal_work.go` / `journal_patch.go`：Safety Journal（2026-11-12 决定第 (3)(9) 条；`Runner.Journal` 开启，默认关）：丢弃改动与删除未跟踪文件执行前，把会丢的内容写成只含这些路径的树提交（临时索引 `GIT_INDEX_FILE`，不动用户的索引与工作区），用 `refs/solcogit/journal/<id>` 钉住，并在 `<git 目录>/solcogit/journal/<id>.json`（0600，只有路径名与对象号、无内容）记一条；`Operations` 列表（新的在前，纳秒时间戳），`UndoOperation` 找回前重新校验——文件又被改过 / 路径又存在 / 钉住的引用丢失都拒绝（`ErrStaleState`），超过 `MaxRecoveryBytes`（128 MiB）或记录失败则不执行破坏性操作（fail closed），保留最新 50 条 / 14 天；停在 pending 超过 10 分钟的记录算 interrupted、仍可找回（`journal_test.go`，每条变异检查过）。第 2 批：删分支（钉住分支尖，找回 = 重建分支，同名分支已存在就拒绝，上游设置不恢复）与 drop stash（钉住 stash 提交，找回 = `stash store` 放回栈顶，含未跟踪部分）；两者在 `Journal` 开启时走无二次加锁的内核路径（有 `WriteBackend` 仍调它）；被 Git 拒绝的删除不留记录与钉住。第 3 批（`journal_patch.go`）：应用补丁（补丁涉及的路径——`apply --numstat` 只给改名的新名，旧名从补丁头的 `rename from` 行补上——在应用前的工作区状态钉成提交，应用后的树记进 `After`；找回时现状不等于 `After` 就拒绝，再把旧状态放回、补丁新建的文件删掉）与 Drop All Stashes（每个 stash 提交作为父提交挂在一个恢复提交上，一条引用钉住全部，最多 200 个；找回 = 从最旧到最新 `stash store`，顺序不变）；没有 stash 可清时不留记录。`Journal` 的全部范围到此收齐。
- `conflict.go`：冲突编辑内核（2026-11-12 决定第 (3)(10) 条）：`ReadConflict`（读 index stage 1/2/3 三份内容，用 `git merge-file -p --diff3 --marker-size=29` 让 Git 自己算合并并切成块——退出码是冲突数、由 `Error.out` 取回标准输出；29 个字符的标记让内容里的 `=======` 不会撞上；一方删除 / 二进制 / 过大 / 链接与子模块返回 `Editable=false` 与原因）、`ParseConflictMarkers` / `BuildConflictResult`（块与每块的选择 Current / Incoming / Both / Base / 未决 → 结果文本，纯函数，未决块留 7 字符标记）、`ResolveWith`（写入工作区并 `git add`；结果仍含标记、过大、含 NUL、文件已不在冲突中都拒绝；`Journal` 开时先把原工作区文件钉成恢复点并记下写后的树）、`undoResolve`（Recovery 找回 = 重新打开冲突并放回原文件；暂停的合并必须还在、文件不能被改过，否则 `ErrStaleState`；Git 在合并提交后仍保留 resolve-undo 数据，所以单靠 `update-index --unresolve` 不够）（`conflict_test.go`，每条变异检查过）。
- `command_log.go`：命令日志内核（2026-11-12 决定第 (11) 条）：`CommandLog`（内存环形记录，读 400 / 写 600 各一环，刷新洪流挤不掉写）、`CommandEntry`（argv、读写类、耗时、退出码与失败类别、`Line()` 给可复制的一行并把控制字符写成 `$'…'`）、`Entries(root, withReads)` 新的在前、`Clear(root)`；URL 里的凭据与超长参数在记录时就被遮；`Runner.Commands` 打开记录（`run` / `runStdinEnv` 外包一层），关闭时零痕迹；stdin 与 commit 信息永远不进来；engine 的 `policyRunner` 写同一个日志。
- `command_log_test.go`：读写都记且新的在前、失败带类别与退出码、读写各自成环、按仓库根过滤与清除、URL 凭据与超长参数被遮、stdin 不进日志、Line 引号与控制字符、nil 日志安全；每条做过变异检查。
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
