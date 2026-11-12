package engine

import (
	"os"

	"github.com/jesseduffield/lazygit/pkg/app/daemon"
	"github.com/sirupsen/logrus"
)

/**
 * [INPUT]: 进程环境里的 LAZYGIT_DAEMON_KIND / LAZYGIT_DAEMON_INSTRUCTION（LazyGit 变基命令把本程序设为 git 的编辑器时写入）
 * [OUTPUT]: RunDaemon——被 git 当编辑器拉起时改写 todo 后退出；否则什么都不做
 * [POS]: 历史改写的隐藏入口：宿主程序（和使用 engine 的测试）在 main / TestMain 的第一行调用它
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */

// RunDaemon serves the one case where Git runs this very program as its
// editor: LazyGit's rebase commands set GIT_SEQUENCE_EDITOR to the running
// executable and describe the todo change in the environment. It reports
// whether this process was such a call; when true the work is done and the
// caller must return from main without starting anything else. A failure
// exits the process with status 1 and a line on stderr, which Git turns into
// a failed rebase.
//
// Call it first thing in main (before any lock, window or log file) and in a
// test binary's TestMain: the environment variable is the whole protocol.
func RunDaemon() bool {
	if !daemon.InDaemonMode() {
		return false
	}
	// Only a failed instruction (logged as Fatal, which exits with status 1)
	// is worth a line on stderr, where Git reports it; the info chatter is not.
	log := logrus.New()
	log.SetOutput(os.Stderr)
	log.SetLevel(logrus.FatalLevel)
	daemon.Handle(logrus.NewEntry(log))
	return true
}
