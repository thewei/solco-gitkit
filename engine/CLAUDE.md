# engine/
> L2 | 父级: ../CLAUDE.md

solco-gitkit 里的嵌套 Go 模块（`github.com/thewei/solco-gitkit/engine`，独立 `go.mod`；2026-11-12「Solco Git on LazyGit's command layer」决定）：用 LazyGit 的命令层（`pkg/commands`，只当库用，不碰 gocui/控制器）实现 `gitworkbench.Runner` 的一部分能力，并在其上重新施加 gitworkbench 的运行策略。根模块仍只用标准库；本模块是家族里唯一 import lazygit 的地方，且没有任何 lazygit 类型泄出——结果一律是 gitworkbench 的模型。

成员清单
- `engine.go`：`Engine`（装到 `runner.Backend`）与按仓库根缓存的 `session`；`newSession` 用 `NewGitCommandAux` 把命令层钉在目录上（上游构造器会 chdir，多仓库同开不行），runner 换成 `policyRunner`；不读用户的 lazygit 配置（`NewDummyAppConfig`）。
- `policy.go`：`policyRunner`——整体替换 LazyGit 的 runner：读 5 s / 写 30 s（按子命令判读写，拿不准按写）、输出上限、`GIT_TERMINAL_PROMPT=0`/无分页/`LC_ALL=C`、失败一律 `*gitworkbench.Error`（含超时、取消、输出过大）；忽略 LazyGit 的流式/pty/凭据策略（要凭据的命令立刻失败）；不做单仓库串行（那在 Runner 一侧，后端不得再锁）。
- `branches.go`：`ListBranches`（BranchLoader；按名排序、不含游离 HEAD 与未出生分支、upstream 写成 remote/branch、head 取 7 位）。
- `lists.go`：`ListTags`、`ListWorktrees`（对拍通过的读取组；`lists_test.go` 还含 remotes 为何不迁的证据测试）。
- `history.go`：`RewriteHistory`——批量历史改写（reword / squash / fixup / drop / move，一段相邻提交）走 LazyGit 的 `Rebase` 命令组：载入 HEAD 的最近 1000 个提交，定位选中的连续区间，拒绝合并提交与过远的提交，其余交给 `RebaseCommands`；停下的变基由 `Runner.RewriteHistory` 负责中止（冲突报 `ErrConflict`）。
- `daemon.go`：`RunDaemon`——LazyGit 的变基命令把正在运行的可执行文件设成 git 的编辑器（环境里带 `LAZYGIT_DAEMON_*`），宿主在 main / TestMain 的第一行调它：被 git 拉起时改写 todo 后退出，否则什么都不做。
- `patch.go`：自定义补丁——`PatchSource`（读一个提交里某文件的 diff，标出可选的 `+`/`-` 行，行号即 LazyGit `PatchBuilder` 的行索引）与 `RewriteWithPatch`（从提交移除 / 拆成新提交 / 移到另一个提交 / 撤回到工作区；按整文件或行区间）。`PatchBuilder` 是有状态单例，每次操作前后 `Reset`；补丁临时文件写进每个 session 私有的临时目录（`tempConfig`，LazyGit 的默认 dummy 配置会把它们落在进程当前目录），操作后清空，`Engine.Close` 删目录。`patch_test.go`：五种动作的真实仓库结果、冲突即中止、各种拒绝、无残留（含变异检查：去掉清理或临时目录配置会失败）。
- `amend_pick.go`：`AmendTo`（暂存的改动并入更早的提交：LazyGit 先做 fixup 提交再由守护进程移动，所以同样依赖 `RunDaemon`）与 `CherryPickCommits`（按序摘取一串提交；LazyGit 要新→旧，适配层反转并用只带 hash 的占位提交）。`amend_pick_test.go`：并入旧/头/最老提交且最终树 = 原树 + 暂存、冲突后不留 fixup 提交且改动仍在暂存区、各种拒绝；摘取与 argv 路径对拍、冲突留在序列里等待继续/中止（变异检查：去掉 `reset --soft` 回收、去掉反转都会失败）。
- `writes.go`：写入组（`WriteBackend`）——分支切换/创建/改名/删除、stash 推入/弹出/应用/丢弃、轻量标签创建/删除，一一对应 LazyGit 的 Branch / Stash / Tag 命令。`writes_test.go`：每个操作在两个相同仓库上分别走 argv 与引擎，要求结果类别与仓库状态（引用+树、HEAD、工作区、stash）一致，含失败路径（冲突阻止切换、重名、未合并删除、空 stash、弹出冲突、不存在的条目）。**不迁、留在 argv 的写入**：提交（Solco 的提交按路径部分提交、消息走 stdin、处理改名，LazyGit 的提交命令没有这些形态）、带注释的标签（消息走 stdin）、空消息的 stash（LazyGit 的 `-m ""` 会得到 `On main: ` 而不是 Git 的 `WIP on …`）、未出生 HEAD 上建分支（`checkout -b` 没有起点）。注意：同一操作的 `Error.Op` 名随路径不同（`switch` / `checkout`），类别一致，没有调用方依赖它。
- `history_test.go` / `main_test.go`：对拍——同一批操作在引擎路径和 argv 的 `InteractiveRebase` 上得到相同的消息与树；冲突即中止且仓库逐字节如初；用户的编辑器环境变量不得拉起编辑器；各种拒绝（不相邻、未知提交、合并、脏工作区、无后端）不改仓库；`TestMain` 调 `RunDaemon`（git 把测试二进制当编辑器）。
- `engine_test.go`：对拍——同一批真实仓库（未出生 HEAD、多分支、跟踪远端、upstream 已删、游离 HEAD）上 argv 路径与引擎路径结果必须一致（未出生分支的差异就是它抓到的）；策略合同——环境变量压过父进程、超时杀进程、输出上限、失败带类别与操作类、子命令解析；每条做过变异检查。

读取组的结论（2026-11-12 逐项对拍；不要重做）：**已迁**——`ListBranches`、`ListTags`、`ListWorktrees`（`lists.go`；worktrees 要经主工作树的 session 取，否则 LazyGit 会把当前工作树提到最前、丢掉 Git 的列表顺序，且处于变基中的工作树要还原成 detached，两处都有对拍与变异检查；代价是每个工作树多一次 `rev-parse --absolute-git-dir`）。**不迁**（`TestRemotesDoNotReachParity` / `TestUnmigratedGroupsStayOnArgv` 把理由钉成测试，且防止误迁）：remotes——LazyGit 读的是本仓库配置里的原始 URL，不应用 `url.*.insteadOf`（argv 的 `remote get-url` 会），也看不到只在全局配置里定义的 remote；stashes——`StashEntry` 没有基底提交，要逐条补 `rev-parse`；submodules——LazyGit 只解析 `.gitmodules`，没有 `submodule status` 的状态；files(status)——gitworkbench 的变更快照（numstat 先行、大 diff 延迟、未跟踪界限）远比 `GetStatusFiles` 丰富。LazyGit 真正多出来的价值在写入与历史改写（`history.go` 已接；自定义补丁构建器、cherry-pick 序列等是新增能力，不是迁移）。

迁移规则：一组能力只在「同一套真实仓库测试在两条路径上都过」之后才切到这里；LazyGit 没有的（Push Multiple、Autostash、`ListTree`、`FetchedAt`、blame）留在 `gitworkbench`。LazyGit 的 `pkg/commands` 没有 semver 承诺：版本钉死在 `go.mod`，升级是一次带全量对拍的显式动作。

[PROTOCOL]: 变更时更新此头部，然后检查 ../CLAUDE.md
