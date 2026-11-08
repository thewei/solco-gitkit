// search.go owns the Files sidebar's search (2026-10-30): the root's file list
// for name search and a bounded full-text search. Both are bounded local
// reads — ripgrep runs the content search when it is installed, Git's own
// grep otherwise; Git also lists the files, so ignore semantics stay Git's
// (the same rule as the tree). No second searcher is written here: this file
// only builds argument lists, bounds the work and parses what comes back.

package filesview

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	// SearchDeadline bounds one search or file listing.
	SearchDeadline = 15 * time.Second
	// MaxIndexedFiles caps the listed files of one root.
	MaxIndexedFiles = 100000
	// MaxContentMatches caps the matches one content search returns.
	MaxContentMatches = 500
	// MaxMatchesPerFile caps the matches kept from one file.
	MaxMatchesPerFile = 20
	// MaxLineChars caps the characters of a matching line that are shown.
	MaxLineChars = 300
	// MinContentQuery is the shortest pattern a content search runs for.
	MinContentQuery = 2
	maxResultLine   = 4 << 20 // one rg JSON event may carry a long line
)

// ErrNoContentEngine reports that neither ripgrep nor a Git repository is
// available to search the root's content.
var ErrNoContentEngine = errors.New("Content search needs ripgrep installed, or a Git repository")

// FileIndex is a root's files as slash-separated relative paths.
type FileIndex struct {
	Paths     []string
	Truncated bool // more than MaxIndexedFiles existed
}

// ListFiles lists the files under root: the ones Git tracks plus the ones it
// does not ignore, minus the tree's fixed heavy names; includeIgnored adds
// what the rules hide. A root outside any repository is listed by ripgrep
// when it is installed.
func ListFiles(ctx context.Context, root string, includeIgnored bool) (FileIndex, error) {
	ctx, cancel := context.WithTimeout(ctx, SearchDeadline)
	defer cancel()
	idx, err := gitFiles(ctx, root, includeIgnored)
	if err != nil {
		rg := ripgrepPath()
		if rg == "" {
			return FileIndex{}, ErrNoContentEngine
		}
		idx, err = rgFiles(ctx, rg, root, includeIgnored)
	}
	if err != nil {
		return FileIndex{}, err
	}
	if !includeIgnored {
		kept := idx.Paths[:0]
		for _, p := range idx.Paths {
			if !hasHeavySegment(p) {
				kept = append(kept, p)
			}
		}
		idx.Paths = kept
	}
	sort.Strings(idx.Paths)
	return idx, nil
}

// hasHeavySegment reports whether a path runs through one of the fixed
// heavy/generated names the tree hides.
func hasHeavySegment(rel string) bool {
	for _, seg := range strings.Split(rel, "/") {
		if _, ok := defaultIgnoredNames[seg]; ok {
			return true
		}
	}
	return false
}

func gitFiles(ctx context.Context, root string, includeIgnored bool) (FileIndex, error) {
	var idx FileIndex
	seen := map[string]bool{}
	lists := [][]string{{"ls-files", "-z", "--cached", "--others", "--exclude-standard"}}
	if includeIgnored {
		lists = append(lists, []string{"ls-files", "-z", "--others", "--ignored", "--exclude-standard"})
	}
	for _, args := range lists {
		out, err := exec.CommandContext(ctx, "git", append([]string{"-C", root}, args...)...).Output()
		if err != nil {
			return FileIndex{}, err
		}
		for _, entry := range bytes.Split(out, []byte{0}) {
			p := filepath.ToSlash(string(entry))
			if p == "" || seen[p] {
				continue
			}
			if len(idx.Paths) >= MaxIndexedFiles {
				idx.Truncated = true
				return idx, nil
			}
			seen[p] = true
			idx.Paths = append(idx.Paths, p)
		}
	}
	return idx, nil
}

func rgFiles(ctx context.Context, rg, root string, includeIgnored bool) (FileIndex, error) {
	args := []string{"--no-config", "--files", "--null", "--hidden", "--glob", "!.git"}
	if includeIgnored {
		args = append(args, "--no-ignore")
	}
	cmd := exec.CommandContext(ctx, rg, append(args, "--", ".")...)
	cmd.Dir = root
	out, err := cmd.Output()
	var exit *exec.ExitError
	if err != nil && !(errors.As(err, &exit) && len(out) > 0) { // exit 2 can come with a partial list
		return FileIndex{}, err
	}
	var idx FileIndex
	for _, entry := range bytes.Split(out, []byte{0}) {
		p := strings.TrimPrefix(filepath.ToSlash(string(entry)), "./")
		if p == "" {
			continue
		}
		if len(idx.Paths) >= MaxIndexedFiles {
			idx.Truncated = true
			break
		}
		idx.Paths = append(idx.Paths, p)
	}
	return idx, nil
}

// MatchFiles ranks the paths that match every word of the query, best first.
// A word matches as a substring of the path or, failing that, as a
// subsequence of it; a hit in the file's own name beats one in a folder, and
// a shorter path beats a longer one. total is the count before limit.
func MatchFiles(paths []string, query string, limit int) (hits []string, total int) {
	words := strings.Fields(strings.ToLower(query))
	if len(words) == 0 {
		return nil, 0
	}
	type scored struct {
		path  string
		score int
	}
	var found []scored
	for _, p := range paths {
		lower := strings.ToLower(p)
		base := lower[strings.LastIndexByte(lower, '/')+1:]
		score, ok := 0, true
		for _, w := range words {
			s := wordScore(w, lower, base)
			if s == 0 {
				ok = false
				break
			}
			score += s
		}
		if ok {
			found = append(found, scored{p, score*1000 - len(p)})
		}
	}
	sort.SliceStable(found, func(i, j int) bool {
		if found[i].score != found[j].score {
			return found[i].score > found[j].score
		}
		return found[i].path < found[j].path
	})
	total = len(found)
	if limit > 0 && len(found) > limit {
		found = found[:limit]
	}
	for _, f := range found {
		hits = append(hits, f.path)
	}
	return hits, total
}

// wordScore is 0 for no match; the name beats the folders, a prefix of the
// name beats the middle of it, a substring beats a scattered subsequence.
func wordScore(word, path, base string) int {
	switch {
	case strings.HasPrefix(base, word):
		return 60
	case strings.Contains(base, word):
		return 50
	case strings.Contains(path, word):
		return 30
	case isSubsequence(word, base):
		return 20
	case isSubsequence(word, path):
		return 10
	}
	return 0
}

func isSubsequence(word, s string) bool {
	i := 0
	for _, r := range s {
		if i < len(word) {
			w, size := utf8.DecodeRuneInString(word[i:])
			if r == w {
				i += size
			}
		}
	}
	return i == len(word)
}

// ContentQuery is one full-text search over a root.
type ContentQuery struct {
	Pattern        string
	CaseSensitive  bool
	WholeWord      bool
	Regex          bool
	IncludeIgnored bool
}

// ContentMatch is one matching line. Text is the line without its leading
// whitespace, cut to MaxLineChars; Start and End are the first match's byte
// range in Text (-1 when it cannot be located); Col is that match's byte
// offset in the whole line, for opening the file on it.
type ContentMatch struct {
	Line       int // 1-based
	Text       string
	Start, End int
	Col        int
}

// ContentFile is the matches of one file.
type ContentFile struct {
	Path    string // slash-separated, relative to the root
	Matches []ContentMatch
	More    bool // more lines matched than MaxMatchesPerFile
}

// ContentResult is a finished search.
type ContentResult struct {
	Files     []ContentFile
	Matches   int
	Truncated bool   // MaxContentMatches reached
	Engine    string // "ripgrep" or "git grep"
}

// SearchContent runs a full-text search under root. A pattern shorter than
// MinContentQuery yields no result.
func SearchContent(ctx context.Context, root string, q ContentQuery) (ContentResult, error) {
	if utf8.RuneCountInString(strings.TrimSpace(q.Pattern)) < MinContentQuery {
		return ContentResult{}, nil
	}
	ctx, cancel := context.WithTimeout(ctx, SearchDeadline)
	defer cancel()
	hl := highlighter(q)
	if rg := ripgrepPath(); rg != "" {
		return rgSearch(ctx, rg, root, q, hl)
	}
	return gitGrepSearch(ctx, root, q, hl)
}

// highlighter locates a pattern's first match in a line; nil when the pattern
// does not compile (ripgrep and Go differ in corners — highlighting is only
// presentation, the engine's matches stand).
func highlighter(q ContentQuery) *regexp.Regexp {
	pat := q.Pattern
	if !q.Regex {
		pat = regexp.QuoteMeta(pat)
	}
	if q.WholeWord {
		pat = `\b(?:` + pat + `)\b`
	}
	if !q.CaseSensitive {
		pat = "(?i)" + pat
	}
	re, err := regexp.Compile(pat)
	if err != nil {
		return nil
	}
	return re
}

// newMatch trims and bounds a matched line for display.
func newMatch(line int, text string, hl *regexp.Regexp) ContentMatch {
	text = strings.TrimRight(text, "\r\n")
	m := ContentMatch{Line: line, Start: -1, End: -1}
	if hl != nil {
		if loc := hl.FindStringIndex(text); loc != nil {
			m.Col = loc[0]
			m.Start, m.End = loc[0], loc[1]
		}
	}
	indent := len(text) - len(strings.TrimLeftFunc(text, unicode.IsSpace))
	text = text[indent:]
	if m.Start >= 0 {
		m.Start, m.End = m.Start-indent, m.End-indent
		if m.Start < 0 { // the match began inside the indent
			m.Start = 0
		}
	}
	if utf8.RuneCountInString(text) > MaxLineChars {
		cut := 0
		for n := 0; n < MaxLineChars; n++ {
			_, size := utf8.DecodeRuneInString(text[cut:])
			cut += size
		}
		text = text[:cut] + "…"
		if m.End > cut {
			m.Start, m.End = -1, -1
		}
	}
	m.Text = text
	return m
}

// collector gathers matches under the per-file and total caps.
type collector struct {
	res   ContentResult
	index map[string]int
}

func newCollector(engine string) *collector {
	return &collector{res: ContentResult{Engine: engine}, index: map[string]int{}}
}

// add records one match; false once the total cap is reached.
func (c *collector) add(path string, m ContentMatch) bool {
	if c.res.Matches >= MaxContentMatches {
		c.res.Truncated = true
		return false
	}
	i, ok := c.index[path]
	if !ok {
		i = len(c.res.Files)
		c.index[path] = i
		c.res.Files = append(c.res.Files, ContentFile{Path: path})
	}
	f := &c.res.Files[i]
	if len(f.Matches) >= MaxMatchesPerFile {
		f.More = true
		return true
	}
	f.Matches = append(f.Matches, m)
	c.res.Matches++
	return true
}

func rgSearch(ctx context.Context, rg, root string, q ContentQuery, hl *regexp.Regexp) (ContentResult, error) {
	args := []string{"--no-config", "--json", "--hidden", "--glob", "!.git", "--max-filesize", "2M",
		"--max-count", strconv.Itoa(MaxMatchesPerFile + 1)}
	if q.CaseSensitive {
		args = append(args, "--case-sensitive")
	} else {
		args = append(args, "--ignore-case")
	}
	if q.WholeWord {
		args = append(args, "--word-regexp")
	}
	if !q.Regex {
		args = append(args, "--fixed-strings")
	}
	if q.IncludeIgnored {
		args = append(args, "--no-ignore")
	} else {
		for name := range defaultIgnoredNames {
			args = append(args, "--glob", "!"+name)
		}
	}
	args = append(args, "--regexp", q.Pattern, "--", ".")

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := exec.CommandContext(ctx, rg, args...)
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "RIPGREP_CONFIG_PATH=")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return ContentResult{}, err
	}
	var stderr bytes.Buffer
	cmd.Stderr = &limitedBuffer{buf: &stderr, limit: 16 << 10}
	if err := cmd.Start(); err != nil {
		return ContentResult{}, err
	}

	col := newCollector("ripgrep")
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 64<<10), maxResultLine)
	for scanner.Scan() {
		var ev struct {
			Type string `json:"type"`
			Data struct {
				Path  struct{ Text string } `json:"path"`
				Lines struct{ Text string } `json:"lines"`
				Line  int                   `json:"line_number"`
			} `json:"data"`
		}
		if json.Unmarshal(scanner.Bytes(), &ev) != nil || ev.Type != "match" || ev.Data.Path.Text == "" {
			continue
		}
		path := strings.TrimPrefix(filepath.ToSlash(ev.Data.Path.Text), "./")
		if !col.add(path, newMatch(ev.Data.Line, ev.Data.Lines.Text, hl)) {
			cancel()
			break
		}
	}
	truncated := scanner.Err() != nil
	if truncated {
		cancel()
	}
	waitErr := cmd.Wait()
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		truncated = true // the deadline cut a slow search short
	}
	col.res.Truncated = col.res.Truncated || truncated
	if col.res.Matches == 0 && waitErr != nil && !col.res.Truncated && ctx.Err() == nil {
		var exit *exec.ExitError
		if errors.As(waitErr, &exit) && exit.ExitCode() == 1 {
			return col.res, nil // no matches
		}
		return ContentResult{}, errors.New(firstLine(stderr.String(), waitErr.Error()))
	}
	return col.res, nil
}

func gitGrepSearch(ctx context.Context, root string, q ContentQuery, hl *regexp.Regexp) (ContentResult, error) {
	args := []string{"-C", root, "grep", "-z", "-n", "-I", "--no-color", "--untracked"}
	if !q.CaseSensitive {
		args = append(args, "-i")
	}
	if q.WholeWord {
		args = append(args, "-w")
	}
	if q.Regex {
		args = append(args, "-E")
	} else {
		args = append(args, "-F")
	}
	if q.IncludeIgnored {
		args = append(args, "--no-exclude-standard")
	}
	args = append(args, "-e", q.Pattern, "--", ".")
	if !q.IncludeIgnored {
		for name := range defaultIgnoredNames {
			args = append(args, ":(exclude,glob)**/"+name+"/**")
		}
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return ContentResult{}, err
	}
	var stderr bytes.Buffer
	cmd.Stderr = &limitedBuffer{buf: &stderr, limit: 16 << 10}
	if err := cmd.Start(); err != nil {
		return ContentResult{}, err
	}

	col := newCollector("git grep")
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 64<<10), maxResultLine)
	for scanner.Scan() {
		// path NUL line NUL text
		parts := bytes.SplitN(scanner.Bytes(), []byte{0}, 3)
		if len(parts) != 3 {
			continue
		}
		line, err := strconv.Atoi(string(parts[1]))
		if err != nil {
			continue
		}
		path := strings.TrimPrefix(filepath.ToSlash(string(parts[0])), "./")
		if !col.add(path, newMatch(line, string(parts[2]), hl)) {
			cancel()
			break
		}
	}
	truncated := scanner.Err() != nil
	if truncated {
		cancel()
	}
	waitErr := cmd.Wait()
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		truncated = true
	}
	col.res.Truncated = col.res.Truncated || truncated
	if col.res.Matches == 0 && waitErr != nil && !col.res.Truncated && ctx.Err() == nil {
		var exit *exec.ExitError
		if errors.As(waitErr, &exit) && exit.ExitCode() == 1 && stderr.Len() == 0 {
			return col.res, nil // no matches
		}
		if strings.Contains(stderr.String(), "not a git repository") {
			return ContentResult{}, ErrNoContentEngine
		}
		return ContentResult{}, errors.New(firstLine(stderr.String(), waitErr.Error()))
	}
	return col.res, nil
}

// firstLine is the first non-empty line of an engine's complaint.
func firstLine(text, fallback string) string {
	for _, line := range strings.Split(text, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			return line
		}
	}
	return fallback
}

// ripgrepPath is ResolveRipgrep; tests swap it to exercise the Git fallback.
var ripgrepPath = ResolveRipgrep

// ResolveRipgrep finds ripgrep: PATH first, then the places a Finder-launched
// app (which does not inherit the shell's PATH) will not look on its own.
// Empty when it is not installed.
func ResolveRipgrep() string {
	if rg, err := exec.LookPath("rg"); err == nil {
		return rg
	}
	home, _ := os.UserHomeDir()
	for _, p := range []string{
		"/opt/homebrew/bin/rg", "/usr/local/bin/rg", "/usr/bin/rg",
		filepath.Join(home, ".cargo/bin/rg"), filepath.Join(home, ".local/bin/rg"),
	} {
		if st, err := os.Stat(p); err == nil && !st.IsDir() && st.Mode()&0o111 != 0 {
			return p
		}
	}
	return ""
}

// limitedBuffer keeps the first limit bytes of what is written to it.
type limitedBuffer struct {
	buf   *bytes.Buffer
	limit int
}

func (l *limitedBuffer) Write(p []byte) (int, error) {
	if room := l.limit - l.buf.Len(); room > 0 {
		l.buf.Write(p[:min(len(p), room)])
	}
	return len(p), nil
}
