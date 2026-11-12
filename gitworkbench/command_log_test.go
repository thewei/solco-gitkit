package gitworkbench

import (
	"context"
	"strings"
	"testing"
)

/**
 * [INPUT]: command_log.go、Runner.run / runStdinEnv、真实临时仓库
 * [OUTPUT]: 命令日志的合同：读写都记、失败带类别与退出码、新的在前、读与写各自成环（刷新洪流不挤掉写）、按仓库根过滤、URL 凭据与超长参数被遮、stdin 内容不进日志、Line 可复制、nil 日志无副作用、关闭时不留痕
 * [POS]: 2026-11-12 决定第 (11) 条命令日志的防线；每条都做过变异检查
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */

func TestCommandLogRecordsReadsAndWritesNewestFirst(t *testing.T) {
	root, r := tempRepo(t)
	r.Commands = NewCommandLog()
	ctx := context.Background()

	if _, err := r.Read(ctx, root, "rev-parse", "--git-dir"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Mutate(ctx, root, "config", "user.name", "Somebody"); err != nil {
		t.Fatal(err)
	}

	all := r.Commands.Entries(root, true)
	if len(all) != 2 || all[0].Args[0] != "config" || all[1].Args[0] != "rev-parse" {
		t.Fatalf("entries = %+v, want the config write first and the read second", all)
	}
	if all[0].Class != OpMutate || all[1].Class != OpRead || all[0].Failed() || all[0].Root != root || all[0].Time == 0 {
		t.Errorf("entry fields wrong: %+v", all[0])
	}
	if got := r.Commands.Entries(root, false); len(got) != 1 || got[0].Args[0] != "config" {
		t.Errorf("without reads = %+v, want only the write", got)
	}
	if all[0].Seq <= all[1].Seq {
		t.Errorf("sequence numbers must grow: %d then %d", all[1].Seq, all[0].Seq)
	}
}

func TestCommandLogKeepsAFailureWithItsClassAndExitCode(t *testing.T) {
	root, r := tempRepo(t)
	r.Commands = NewCommandLog()
	if _, err := r.Read(context.Background(), root, "rev-parse", "--verify", "--quiet", "no-such-ref"); err == nil {
		t.Fatal("the lookup must fail")
	}
	got := r.Commands.Entries(root, true)
	if len(got) != 1 || !got[0].Failed() || got[0].ExitCode != 1 || got[0].Failure == "" {
		t.Fatalf("entry = %+v, want a failure with exit code 1 and a class", got)
	}
}

func TestCommandLogReadsDoNotPushOutWrites(t *testing.T) {
	l := NewCommandLog()
	l.Record(CommandEntry{Root: "/r", Args: []string{"commit"}, Class: OpMutate})
	for i := 0; i < maxLoggedReads*3; i++ {
		l.Record(CommandEntry{Root: "/r", Args: []string{"status"}, Class: OpRead})
	}
	if got := l.Entries("/r", false); len(got) != 1 || got[0].Args[0] != "commit" {
		t.Fatalf("writes = %+v, a flood of reads must not evict the commit", got)
	}
	if got := l.Entries("/r", true); len(got) != 1+maxLoggedReads {
		t.Fatalf("with reads: %d entries, want the write plus the newest %d reads", len(got), maxLoggedReads)
	}
	for i := 0; i < maxLoggedWrites+10; i++ {
		l.Record(CommandEntry{Root: "/r", Args: []string{"add"}, Class: OpMutate})
	}
	if got := l.Entries("/r", false); len(got) != maxLoggedWrites {
		t.Fatalf("writes ring holds %d, want %d", len(got), maxLoggedWrites)
	}
}

func TestCommandLogIsPerRepositoryAndClearsPerRepository(t *testing.T) {
	l := NewCommandLog()
	l.Record(CommandEntry{Root: "/a", Args: []string{"add", "x"}, Class: OpMutate})
	l.Record(CommandEntry{Root: "/b", Args: []string{"add", "y"}, Class: OpMutate})
	if got := l.Entries("/a", true); len(got) != 1 || got[0].Args[1] != "x" {
		t.Fatalf("/a = %+v", got)
	}
	l.Clear("/a")
	if len(l.Entries("/a", true)) != 0 || len(l.Entries("/b", true)) != 1 {
		t.Fatal("Clear must forget only its own repository")
	}
}

func TestCommandLogHidesURLCredentialsAndCapsLongArguments(t *testing.T) {
	l := NewCommandLog()
	long := strings.Repeat("x", maxLoggedArg*2)
	l.Record(CommandEntry{Root: "/r", Class: OpMutate,
		Args: []string{"remote", "add", "o", "https://me:hunter2@example.com/r.git", long}})
	got := l.Entries("/r", true)[0].Args
	if strings.Contains(strings.Join(got, " "), "hunter2") || got[3] != "https://***@example.com/r.git" {
		t.Errorf("credentials leaked: %q", got[3])
	}
	if len(got[4]) > maxLoggedArg+len("…") {
		t.Errorf("long argument kept at %d bytes", len(got[4]))
	}
}

func TestCommandLogNeverSeesStdin(t *testing.T) {
	root, r := tempRepo(t)
	r.Commands = NewCommandLog()
	if _, err := r.Stdin(context.Background(), root, "TOP-SECRET-MESSAGE", "hash-object", "--stdin"); err != nil {
		t.Fatal(err)
	}
	got := r.Commands.Entries(root, true)
	if len(got) != 1 || got[0].Args[0] != "hash-object" {
		t.Fatalf("entries = %+v, want the stdin command recorded", got)
	}
	if strings.Contains(got[0].Line(), "SECRET") {
		t.Errorf("stdin leaked into the log: %s", got[0].Line())
	}
}

func TestCommandEntryLineQuotesWhatNeedsIt(t *testing.T) {
	e := CommandEntry{Args: []string{"commit", "-m", "it's a fix", "--", "a b.txt", "plain"}}
	want := `git commit -m 'it'\''s a fix' -- 'a b.txt' plain`
	if got := e.Line(); got != want {
		t.Errorf("Line() = %s, want %s", got, want)
	}
	// a record separator or a newline must not break the line in two
	f := CommandEntry{Args: []string{"log", "--format=%H\x1f%s\n", "it's"}}
	if got, want := f.Line(), `git log $'--format=%H\x1f%s\n' 'it'\''s'`; got != want || strings.Contains(got, "\n") {
		t.Errorf("Line() = %q, want %q", got, want)
	}
}

func TestCommandLogOffLeavesNoTraceAndNilIsSafe(t *testing.T) {
	root, r := tempRepo(t)
	if _, err := r.Read(context.Background(), root, "rev-parse", "--git-dir"); err != nil {
		t.Fatal(err)
	}
	var l *CommandLog
	l.Record(CommandEntry{})
	l.Clear(root)
	if l.Entries(root, true) != nil {
		t.Error("a nil log has no entries")
	}
}
