package gitworkbench

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

/**
 * [INPUT]: Runner.run / runStdinEnv 与 engine 的 policyRunner 在每条 git 子进程结束时报来的一条记录
 * [OUTPUT]: CommandLog（内存环形记录）、CommandEntry、Entries / Clear；Runner.Commands 打开记录
 * [POS]: 2026-11-12 决定第 (11) 条命令日志：应用为用户做过什么 git 命令，一眼可见、可复制；只在内存里，退出即清，不落盘、不进 applog
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */

// Ring sizes. Reads (status, log, rev-parse… on every refresh) and writes
// have their own rings, so a burst of refreshes never pushes out the
// commands that changed something.
const (
	maxLoggedReads  = 400
	maxLoggedWrites = 600
	maxLoggedArg    = 300
)

// CommandEntry is one finished Git command. Args is the argv after the
// executable, as run. The message of a commit and other stdin never appear:
// they travel by pipe, not by argv.
type CommandEntry struct {
	Seq      uint64
	Time     int64 // unix nanoseconds, when the command finished
	Root     string
	Args     []string
	Class    OpClass
	Duration time.Duration
	ExitCode int      // 0 on success
	Failure  ErrClass // empty on success
}

// Failed reports whether the command did not succeed.
func (e CommandEntry) Failed() bool { return e.Failure != "" || e.ExitCode != 0 }

// Created is the finish time.
func (e CommandEntry) Created() time.Time { return time.Unix(0, e.Time) }

// Line is the command as one shell-looking line, `git` first. It is for
// reading and copying; words with spaces are single-quoted.
func (e CommandEntry) Line() string {
	var b strings.Builder
	b.WriteString("git")
	for _, a := range e.Args {
		b.WriteByte(' ')
		b.WriteString(shellWord(a))
	}
	return b.String()
}

func shellWord(a string) string {
	if a != "" && !strings.ContainsAny(a, " \t\n\r'\"\\$`*?[]{}()<>|&;!#~") && !hasControl(a) {
		return a
	}
	if hasControl(a) {
		// $'…' keeps a separator or newline visible and the line one line.
		var b strings.Builder
		b.WriteString("$'")
		for _, r := range a {
			switch {
			case r == '\\' || r == '\'':
				b.WriteByte('\\')
				b.WriteRune(r)
			case r == '\n':
				b.WriteString(`\n`)
			case r == '\t':
				b.WriteString(`\t`)
			case r == '\r':
				b.WriteString(`\r`)
			case r < 0x20 || r == 0x7f:
				fmt.Fprintf(&b, `\x%02x`, r)
			default:
				b.WriteRune(r)
			}
		}
		b.WriteByte('\'')
		return b.String()
	}
	return "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
}

func hasControl(a string) bool {
	return strings.ContainsFunc(a, func(r rune) bool { return r < 0x20 || r == 0x7f })
}

// CommandLog keeps the last commands the Runner (and a Backend) ran. The zero
// value is not usable; make one with NewCommandLog. Safe for concurrent use.
type CommandLog struct {
	mu     sync.Mutex
	seq    uint64
	reads  []CommandEntry
	writes []CommandEntry
}

// NewCommandLog returns an empty log. Install it with `runner.Commands = log`
// (and give the same one to the engine).
func NewCommandLog() *CommandLog { return &CommandLog{} }

// userinfo matches the credentials of a URL (`https://user:token@host`).
var userinfo = regexp.MustCompile(`(://)[^/\s@]+@`)

func redact(arg string) string {
	arg = userinfo.ReplaceAllString(arg, "${1}***@")
	if len(arg) > maxLoggedArg {
		arg = arg[:maxLoggedArg] + "…"
	}
	return arg
}

// Record adds one finished command. A nil log ignores it, so call sites need
// no check. Seq is assigned here; Time defaults to now.
func (l *CommandLog) Record(e CommandEntry) {
	if l == nil {
		return
	}
	args := make([]string, len(e.Args))
	for i, a := range e.Args {
		args[i] = redact(a)
	}
	e.Args = args
	if e.Time == 0 {
		e.Time = time.Now().UnixNano()
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.seq++
	e.Seq = l.seq
	if e.Class == OpRead {
		l.reads = appendCapped(l.reads, e, maxLoggedReads)
	} else {
		l.writes = appendCapped(l.writes, e, maxLoggedWrites)
	}
}

func appendCapped(s []CommandEntry, e CommandEntry, limit int) []CommandEntry {
	if len(s) >= limit {
		s = append(s[:0], s[len(s)-limit+1:]...)
	}
	return append(s, e)
}

// Entries returns the commands run in root, newest first. Reads are left out
// unless withReads.
func (l *CommandLog) Entries(root string, withReads bool) []CommandEntry {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []CommandEntry
	pick := func(src []CommandEntry) {
		for _, e := range src {
			if e.Root == root {
				out = append(out, e)
			}
		}
	}
	pick(l.writes)
	if withReads {
		pick(l.reads)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Seq > out[j].Seq })
	return out
}

// Clear forgets every command of root.
func (l *CommandLog) Clear(root string) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	keep := func(src []CommandEntry) []CommandEntry {
		out := src[:0]
		for _, e := range src {
			if e.Root != root {
				out = append(out, e)
			}
		}
		return out
	}
	l.reads, l.writes = keep(l.reads), keep(l.writes)
}

// logCommand records one finished command of this Runner.
func (r *Runner) logCommand(root string, args []string, class OpClass, start time.Time, err error) {
	if r.Commands == nil {
		return
	}
	e := CommandEntry{Root: root, Args: args, Class: class, Duration: time.Since(start)}
	if err != nil {
		e.Failure, e.ExitCode = ErrOther, -1
		if ge, ok := GitError(err); ok {
			e.Failure, e.ExitCode = ge.Class, ge.ExitCode
		}
	}
	r.Commands.Record(e)
}
