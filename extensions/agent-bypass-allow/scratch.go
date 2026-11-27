package main

import (
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
)

// Scratch: a deletion or overwrite whose every target is in a temporary or work directory goes through, as nobody
// keeps what is there. The scratch roots are the temporary directories, the session's working directory (when it is
// a project, not the home directory, a parent of it, a hidden directory in it or a system directory), the usual
// scratch directories in the home directory, and Claude Code's worktrees (…/.claude/worktrees/<name>). A target
// counts when it is strictly under a root after its symlinks are resolved, never a root itself, never in a .git
// directory.
//
// A target may use variables the same command set: S=/tmp/x; rm -rf $S/y, and for v in a b; do rm -rf $S/$v; done.
// The rules err toward asking. A variable is read only when every place the command sets it is a plain assignment
// (or a for list) at the top level, on its own between ; or a newline, with a value that has no quote, space, glob
// or expansion in it; when the name appears anywhere else (read S, export S, S+=x, a function, an if); and when no
// command substitution, shell or IFS change is in the command. $HOME and $TMPDIR are the hook's own unless the
// command sets them. A relative target is read against the session's working directory and every directory the
// command cds to; it counts only when it is scratch against all of them. A program run through anything but a plain
// wrapper (sudo, nice, timeout, do and the like), such as xargs, find -exec, ssh or eval, has no scratch targets.

// elsewhere run a command string on another computer or in a container, whose paths are not this computer's.
var elsewhere = map[string]bool{"ssh": true, "autossh": true, "docker": true, "podman": true, "kubectl": true, "lxc": true,
	"nsenter": true, "chroot": true, "machinectl": true, "vagrant": true, "multipass": true, "distrobox": true, "toolbox": true}

// tmpRoots are the temporary directories.
var tmpRoots = []string{"/tmp", "/private/tmp", "/var/tmp", "/var/folders", "/private/var/folders"}

// homeScratch are the scratch directories in the home directory.
var homeScratch = []string{"ag-work", "wt", "tmp", "scratch"}

// systemDirs are never a working directory to delete in.
var systemDirs = []string{"/etc", "/usr", "/bin", "/sbin", "/lib", "/lib32", "/lib64", "/libx32", "/boot", "/dev", "/proc",
	"/sys", "/run", "/var", "/opt", "/root", "/snap", "/srv", "/nix", "/System", "/Library", "/Applications", "/private",
	"/cores", "/Volumes", "/mnt", "/media"}

// scratchWrappers run the program after them in this shell's directory with these arguments, so its targets are
// what they look like. Any other wrapper (xargs, find -exec, ssh, eval, env -C, chroot…) takes them away.
var scratchWrappers = map[string]bool{"sudo": true, "doas": true, "nice": true, "ionice": true, "nohup": true,
	"time": true, "timeout": true, "exec": true, "command": true, "builtin": true, "stdbuf": true, "setsid": true,
	"unbuffer": true, "busybox": true, "env": true,
	"do": true, "then": true, "else": true, "elif": true, "if": true, "while": true, "until": true, "!": true}

// roots is where scratch targets may be: resolved directories a target must be strictly under.
type roots struct {
	dirs []string
	home string // resolved home directory ("" when unknown)
}

// scratchRoots reads the roots for a session in cwd ("" for none).
func scratchRoots(cwd string) *roots {
	return scratchRootsWithWorkspaces(cwd, nil)
}

func scratchRootsWithWorkspaces(cwd string, workspaces []string) *roots {
	r := &roots{}
	homeRaw, _ := os.UserHomeDir()
	if filepath.IsAbs(homeRaw) {
		r.home = resolve(filepath.Clean(homeRaw), true)
	}
	var cand []string
	cand = append(cand, tmpRoots...)
	if t := os.Getenv("TMPDIR"); filepath.IsAbs(t) {
		cand = append(cand, filepath.Clean(t))
	}
	if r.home != "" {
		for _, d := range homeScratch {
			cand = append(cand, filepath.Join(r.home, d))
		}
	}
	for _, w := range append([]string{cwd}, workspaces...) {
		if filepath.IsAbs(w) {
			if c := resolve(filepath.Clean(w), true); r.workDir(c) {
				cand = append(cand, c)
			}
		}
	}
	for _, c := range cand {
		c = resolve(c, true)
		if c == "/" || (r.home != "" && within(r.home, c)) {
			continue // never the home directory or a parent of it
		}
		r.dirs = append(r.dirs, c)
	}
	return r
}

// workDir says whether a session's working directory is a project one may delete in.
func (r *roots) workDir(c string) bool {
	if c == "/" || strings.Count(c, "/") < 2 {
		return false
	}
	if r.home != "" {
		if within(r.home, c) || filepath.Dir(c) == filepath.Dir(r.home) {
			return false // the home directory, a parent of it, or another person's
		}
		if rel, ok := strings.CutPrefix(c, r.home+"/"); ok {
			for _, part := range strings.Split(rel, "/") {
				if strings.HasPrefix(part, ".") {
					return false // ~/.ssh, ~/.config/…: worktrees under .claude count on their own
				}
			}
		}
	}
	if p := filepath.Dir(c); p == "/home" || p == "/Users" {
		return false
	}
	for _, s := range systemDirs {
		if c == s || strings.HasPrefix(c, s+"/") {
			return false
		}
	}
	return true
}

// within says whether d is a or under it.
func within(d, a string) bool { return d == a || strings.HasPrefix(d, strings.TrimSuffix(a, "/")+"/") }

// worktree is a path in Claude Code's worktrees: …/.claude/worktrees/<name>[/…].
var worktree = regexp.MustCompile(`/\.claude/worktrees/[^/]+`)

// holds says whether the resolved path p is scratch. .git counts whatever its letter case: .GIT is .git on macOS's
// case-insensitive file systems (on a case-sensitive one that only asks more).
func (r *roots) holds(p string) bool {
	for _, part := range strings.Split(p, "/") {
		if strings.EqualFold(part, ".git") {
			return false
		}
	}
	if worktree.MatchString(p) {
		return true
	}
	for _, d := range r.dirs {
		if p != d && within(p, d) {
			return true
		}
	}
	return false
}

// resolve is p with its symlinks resolved: the whole path when follow is set, otherwise its parent only (a rm of a
// link removes the link). The part that does not exist yet is kept as it is.
func resolve(p string, follow bool) string {
	if !follow {
		if p == "/" {
			return p
		}
		return filepath.Join(resolve(filepath.Dir(p), true), filepath.Base(p))
	}
	rest := ""
	for d := p; ; d = filepath.Dir(d) {
		if real, err := filepath.EvalSymlinks(d); err == nil {
			return filepath.Join(real, rest)
		}
		if filepath.Dir(d) == d {
			return p
		}
		rest = filepath.Join(filepath.Base(d), rest)
	}
}

// physical is the absolute path p cleaned the way the kernel reads it: a .. goes to the parent of what the path
// before it leads to, symlinks resolved, where filepath.Clean would only drop the name before it. ok is false when a
// .. comes after a link that leads nowhere yet: where it goes depends on what the command makes first.
func physical(p string) (out string, ok bool) {
	out = "/"
	for _, part := range strings.Split(p, "/") {
		switch part {
		case "", ".":
		case "..":
			if real, err := filepath.EvalSymlinks(out); err == nil {
				out = real
			} else if dangles(out) {
				return "", false
			}
			out = filepath.Dir(out)
		default:
			out = filepath.Join(out, part)
		}
	}
	return out, true
}

// dangles says whether the deepest part of p that exists is a link that leads nowhere (or loops): what is behind it
// depends on what is made there later, so it cannot be read.
func dangles(p string) bool {
	for d := p; ; d = filepath.Dir(d) {
		if _, err := os.Lstat(d); err == nil {
			_, err := filepath.EvalSymlinks(d)
			return err != nil
		}
		if filepath.Dir(d) == d {
			return false
		}
	}
}

// shellVars is what one command string sets that the scratch check can read.
type shellVars struct {
	vars        map[string][]string // the values each readable variable can have
	from        map[string]int      // the first segment that sets it
	tainted     map[string]bool     // names the command sets or touches some other way
	off         bool                // no variable is read
	indirectOff bool
	links       bool     // the command makes links or mounts: what a path leads to may change before the deletion runs
	cwds        []string // directories relative scratch targets are read against; none: relative targets are not scratch
	protCwds    []string // candidate working directories for protected-target checks (retained across bare cd and local subshells)
	protUnknown bool     // true if a cd moved to an unknown directory (cd -, popd, dynamic cd)
}

var identifier = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// literal says whether a value is used as it is: no quote, space, glob, expansion or escape.
func literal(v string) bool {
	return v != "" && !strings.ContainsAny(v, " \t\n\r'\"`$\\*?[]{}()<>|&;!~") && !strings.ContainsRune(v, '\uE003')
}

// readVars reads cmd's segments (scanned from raw, its heredoc bodies taken out): the variables they set and the
// directories they cd to. relative says whether the command runs in the session's working directory.
func readVars(raw string, segs []segment, tail, cwd string, relative bool) *shellVars {
	return readVarsWithProtected(raw, segs, tail, cwd, relative, nil, false)
}

func readVarsWithProtected(raw string, segs []segment, tail, cwd string, relative bool, inheritedProt []string, inheritedUnknown bool) *shellVars {
	sv := &shellVars{
		vars:        map[string][]string{},
		from:        map[string]int{},
		tainted:     map[string]bool{},
		protUnknown: inheritedUnknown,
	}
	if relative && cwd != "" {
		clean := filepath.Clean(cwd)
		sv.cwds = []string{clean}
		sv.protCwds = []string{clean}
	} else if len(inheritedProt) > 0 {
		sv.protCwds = append([]string(nil), inheritedProt...)
	}
	if strings.Contains(raw, "$(") || strings.Contains(raw, "`") || strings.Contains(raw, "\\$") || quotedDollar(raw) {
		sv.off = true
	}
	tainted := sv.tainted
	depth := 0
	set := func(name string, vals []string, k int) {
		if _, ok := sv.from[name]; !ok {
			sv.from[name] = k
		}
		sv.vars[name] = append(sv.vars[name], vals...)
	}
	for k, s := range segs {
		for _, r := range s.before {
			switch r {
			case '(', '{':
				depth++
			case ')', '}':
				depth--
			}
		}
		words := s.words
		first := words[0]
		switch first {
		case "fi", "done", "esac":
			depth--
		case "case", "select":
			sv.off = true
		}
		if depth < 0 {
			sv.off = true
		}
		alone := depth == 0 && plainStart(s.before) && plainEnd(after(segs, k, tail))
		used := make([]bool, len(words))
		switch {
		case alone && allAssignments(words):
			for i, w := range words {
				name, val, _ := strings.Cut(w, "=")
				if literal(val) {
					set(name, []string{val}, k)
				} else {
					tainted[name] = true
				}
				used[i] = true
			}
		case first == "for" && len(words) >= 4 && words[2] == "in" && identifier.MatchString(words[1]):
			ok := depth == 0 && plainStart(s.before)
			for _, w := range words[3:] {
				ok = ok && literal(w)
			}
			if ok {
				set(words[1], words[3:], k)
				for i := range words {
					used[i] = true
				}
			} else {
				tainted[words[1]] = true
			}
		}
		if shellish(words) {
			sv.off = true
		}
		for _, w := range words {
			switch progName(w) {
			case "read", "export", "declare", "typeset", "readonly", "local", "unset", "getopts", "mapfile", "readarray", "vared", "let", "source", ".":
				sv.indirectOff = true
			case "printf":
				if hasShort(words, 'v') {
					sv.indirectOff = true
				}
			}
		}
		if strings.Contains(raw, "((") {
			sv.indirectOff = true
		}
		for i, w := range words {
			if i == firstProgram(words) || (i > 0 && wrappers[progName(words[i-1])]) {
				switch progName(w) {
				case "ln", "link", "mount", "bindfs":
					sv.links = true
				}
			}
		}
		for i, w := range words {
			if used[i] {
				continue
			}
			taint(w, tainted)
		}
		switch first {
		case "if", "while", "until", "for", "case", "select":
			depth++
		}
		if prog := cdWord(words); prog >= 0 {
			switch path.Base(words[prog]) {
			case "cd", "chdir", "pushd", "popd":
				sv.cd(words[prog+1:], k)
			case "eval":
				sv.cwds = nil // its command string may cd anywhere
				sv.protUnknown = true
			}
		}
	}
	if tainted["IFS"] || sv.vars["IFS"] != nil {
		sv.off = true
	}
	for name := range tainted {
		delete(sv.vars, name)
	}
	if sv.off {
		sv.vars = map[string][]string{}
	}
	return sv
}

// quotedDollar says whether a $ is inside single quotes in s, where it is not an expansion.
func quotedDollar(s string) bool {
	single, double := false, false
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case single:
			if c == '$' {
				return true
			}
			single = c != '\''
		case c == '\\':
			i++
		case c == '"':
			double = !double
		case c == '\'' && !double:
			single = true
		}
	}
	return false
}

// plainStart says whether a segment after these separators always runs, in this shell: at the start, or after ; or a
// newline (not after &&, ||, a pipe or &).
func plainStart(before string) bool {
	return before == "" || strings.HasSuffix(before, ";") || strings.HasSuffix(before, "\n")
}

// plainEnd says whether a segment followed by these separators runs in this shell: not in a pipeline or behind &.
func plainEnd(after string) bool {
	switch {
	case after == "", strings.HasPrefix(after, ";"), strings.HasPrefix(after, "\n"), strings.HasPrefix(after, "&&"),
		strings.HasPrefix(after, "||"):
		return true
	}
	return false
}

// after is the separators after segment k.
func after(segs []segment, k int, tail string) string {
	if k+1 < len(segs) {
		return segs[k+1].before
	}
	return tail
}

func allAssignments(words []string) bool {
	for _, w := range words {
		name, _, ok := strings.Cut(w, "=")
		if !ok || !identifier.MatchString(name) {
			return false
		}
	}
	return true
}

// shellish says whether words run a shell, whose own variables the outer ones are not.
func shellish(words []string) bool {
	for i, w := range words {
		b := progName(w)
		if shells[b] || len(globMatches(b)) > 0 {
			return true
		}
		if b == "env" {
			for _, a := range words[i+1:] {
				if strings.HasPrefix(a, "-S") || strings.HasPrefix(a, "--split-string") {
					return true
				}
			}
		}
	}
	return false
}

// taint marks the names a word could set: NAME (read NAME, export NAME), NAME=…, NAME+=…, NAME[…], -vNAME.
func taint(w string, tainted map[string]bool) {
	if identifier.MatchString(w) {
		tainted[w] = true
		return
	}
	if i := strings.IndexAny(w, "=+["); i > 0 && identifier.MatchString(w[:i]) {
		tainted[w[:i]] = true
	}
	if len(w) > 2 && w[0] == '-' && identifier.MatchString(w[2:]) {
		tainted[w[2:]] = true
	}
}

// firstProgram is the index of a segment's program word (after assignments and plain keywords), -1 when none.
func firstProgram(words []string) int {
	for i, w := range words {
		if strings.Contains(w, "=") && !strings.HasPrefix(w, "-") {
			continue
		}
		switch w {
		case "do", "then", "else", "elif", "if", "while", "until", "!", "{":
			continue
		}
		return i
	}
	return -1
}

// cdPrefixes run a cd after them in this shell: builtin, command, time, exec, and zsh's noglob, nocorrect and -.
// zsh's command runs /usr/bin/cd instead, and exec replaces the shell; reading a cd there only asks more.
var cdPrefixes = map[string]bool{"builtin": true, "command": true, "time": true, "exec": true, "noglob": true,
	"nocorrect": true, "-": true}

// cdWord is the index of the word that may be a segment's cd (cd, chdir, pushd, popd) or eval: its program after
// cdPrefixes, their options (command -p, time -p, builtin --) and the assignments and ! after them (time FOO=1 cd,
// time ! cd, zsh's nocorrect FOO=1 cd); -1 when none.
func cdWord(words []string) int {
	i := firstProgram(words)
	for i >= 0 && cdPrefixes[words[i]] {
		for i++; i < len(words) && strings.HasPrefix(words[i], "-"); i++ {
		}
		if j := firstProgram(words[i:]); j >= 0 {
			i += j
		} else {
			i = -1
		}
	}
	return i
}

func isCdFlags(a string) bool {
	if len(a) < 2 || a[0] != '-' || a == "--" {
		return false
	}
	for i := 1; i < len(a); i++ {
		switch a[i] {
		case 'P', 'L', 'e', '@':
		default:
			return false
		}
	}
	return true
}

// cd adds the directories a cd to args may go to; one it cannot read leaves relative targets unread.
func (sv *shellVars) cd(args []string, k int) {
	for len(args) > 0 && isCdFlags(args[0]) {
		args = args[1:]
	}
	if len(args) > 0 && args[0] == "--" {
		args = args[1:]
	}
	if len(args) == 0 {
		// Bare cd changes to $HOME: keep scratch failing closed, and record $HOME for protected-target checks.
		sv.cwds = nil
		if home, err := os.UserHomeDir(); err == nil && filepath.IsAbs(home) {
			sv.protCwds = append(sv.protCwds, filepath.Clean(home))
			if real, ok := physical(home); ok && real != filepath.Clean(home) {
				sv.protCwds = append(sv.protCwds, real)
			}
		} else {
			sv.protUnknown = true
		}
		if sv.tainted["HOME"] || sv.sets("HOME") {
			sv.protUnknown = true
		}
		return
	}
	if len(args) != 1 || args[0] == "-" || strings.HasPrefix(args[0], "+") || strings.HasPrefix(args[0], "-") {
		sv.cwds = nil // cd -, popd, pushd +N: somewhere unknown
		sv.protUnknown = true
		return
	}
	dirs, ok := sv.expand(args[0], k)
	if !ok {
		sv.cwds = nil
		sv.protUnknown = true
		return
	}
	var next, protNext []string
	// cd reads a .. logically, cd -P (or zsh's chase_links) physically: both count
	addTo := func(dst *[]string, d string) bool {
		*dst = append(*dst, filepath.Clean(d))
		real, ok := physical(d)
		if ok && real != filepath.Clean(d) {
			*dst = append(*dst, real)
		}
		return ok
	}
	scratchFailed := false
	for _, d := range dirs {
		if filepath.IsAbs(d) {
			if !addTo(&protNext, d) {
				sv.protUnknown = true
			}
			if !addTo(&next, d) {
				scratchFailed = true
			}
			continue
		}
		// Shells search CDPATH (zsh: also its tied cdpath array) unless the first component is . or ... We do
		// not resolve that search: a command may set CDPATH dynamically, so relative destructive targets must
		// remain subject to confirmation.
		first := strings.SplitN(d, string(filepath.Separator), 2)[0]
		if first != "." && first != ".." && (os.Getenv("CDPATH") != "" || sv.sets("CDPATH") || sv.sets("cdpath")) {
			scratchFailed = true
			sv.protUnknown = true
			continue
		}
		for _, c := range sv.protCwds {
			if !addTo(&protNext, c+"/"+d) {
				sv.protUnknown = true
			}
		}
		if len(sv.cwds) == 0 {
			scratchFailed = true
			continue
		}
		for _, c := range sv.cwds {
			if !addTo(&next, c+"/"+d) {
				scratchFailed = true
			}
		}
	}
	sv.protCwds = append(sv.protCwds, protNext...)
	if scratchFailed || len(sv.cwds) == 0 {
		sv.cwds = nil
		return // relative targets stay unread for scratch
	}
	sv.cwds = append(sv.cwds, next...)
}

// expandMax is how many paths one word may expand to.
const expandMax = 64

// dollar matches $NAME and ${NAME}.
var dollar = regexp.MustCompile(`\$(?:\{([A-Za-z_][A-Za-z0-9_]*)\}|([A-Za-z_][A-Za-z0-9_]*)|\{\(P\)([A-Za-z_][A-Za-z0-9_]*)\})`)

// expand is the paths word w (in segment k) can be: ~ and the readable variables expanded. ok is false when w has an
// expansion the check cannot read.
func (sv *shellVars) expand(w string, k int) (out []string, ok bool) {
	if w == "~" || strings.HasPrefix(w, "~/") {
		home, err := os.UserHomeDir()
		if err != nil || !filepath.IsAbs(home) || sv.sets("HOME") {
			return nil, false
		}
		w = home + strings.TrimPrefix(w, "~")
	} else if strings.HasPrefix(w, "~") {
		return nil, false
	}
	out = []string{""}
	rest := w
	for rest != "" {
		loc := dollar.FindStringSubmatchIndex(rest)
		if loc == nil {
			if strings.Contains(rest, "$") {
				return nil, false
			}
			for i := range out {
				out[i] += rest
			}
			break
		}
		lit := rest[:loc[0]]
		if strings.Contains(lit, "$") {
			return nil, false
		}
		name := ""
		if loc[2] >= 0 {
			name = rest[loc[2]:loc[3]]
		} else if loc[4] >= 0 {
			name = rest[loc[4]:loc[5]]
		}
		var vals []string
		if loc[6] >= 0 {
			selectors, known := sv.value(rest[loc[6]:loc[7]], k)
			if !known || sv.off || sv.indirectOff {
				return nil, false
			}
			for _, selector := range selectors {
				if !identifier.MatchString(selector) {
					return nil, false
				}
				values, known := sv.value(selector, k)
				if !known {
					return nil, false
				}
				vals = append(vals, values...)
				if len(vals) > expandMax {
					return nil, false
				}
			}
		} else {
			var known bool
			vals, known = sv.value(name, k)
			if !known {
				return nil, false
			}
		}
		var next []string
		for _, o := range out {
			for _, v := range vals {
				next = append(next, o+lit+v)
				if len(next) > expandMax {
					return nil, false
				}
			}
		}
		out, rest = next, rest[loc[1]:]
	}
	return out, true
}

// sets says whether the command sets or touches name.
func (sv *shellVars) sets(name string) bool {
	_, ok := sv.from[name]
	return ok || sv.tainted[name]
}

// value is what $name can be in segment k.
func (sv *shellVars) value(name string, k int) ([]string, bool) {
	if vals, ok := sv.vars[name]; ok && !sv.off && sv.from[name] < k {
		return vals, true
	}
	if name == "HOME" || name == "TMPDIR" {
		if v := os.Getenv(name); filepath.IsAbs(v) && literal(v) && !sv.sets(name) {
			return []string{v}, true
		}
	}
	return nil, false
}

// targetsOf is what a deleting or overwriting program changes, from its arguments: nil when the check cannot tell.
func targetsOf(name string, args []string) []string {
	var operands, values []string
	valueFlags := map[string]bool{}
	switch name {
	case "shred":
		valueFlags = map[string]bool{"-n": true, "-s": true}
	case "truncate":
		valueFlags = map[string]bool{"-s": true, "-r": true}
	case "mv", "cp", "ln":
		valueFlags = map[string]bool{"-t": true, "--target-directory": true, "-S": true, "--suffix": true}
	case "rmdir":
		if hasShort(args, 'p') || hasLongOpt(args, "--parents") {
			return nil // it removes the parents too
		}
	case "find":
		return findTargets(args)
	case "rsync":
		// only the destination is deleted in; options may follow it, so it must be the last two words
		if len(args) < 2 || has(args, "--remove-source-files") || strings.HasPrefix(args[len(args)-1], "-") ||
			strings.HasPrefix(args[len(args)-2], "-") {
			return nil
		}
		dst := args[len(args)-1]
		if i := strings.Index(dst, ":"); i >= 0 && !strings.Contains(dst[:i], "/") {
			return nil // a remote one
		}
		return []string{dst}
	case "rm", "unlink", "rimraf", "trash", "trash-put", "tee":
	default:
		return nil // dd, sed -i and the rest: not read
	}
	dashdash := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case dashdash || a == "-" || !strings.HasPrefix(a, "-"):
			operands = append(operands, a)
		case a == "--":
			dashdash = true
		case valueFlags[a]:
			if i+1 >= len(args) {
				return nil
			}
			i++
			values = append(values, a, args[i])
		case (name == "mv" || name == "cp" || name == "ln") && longOpt(a, "--target-directory"):
			if _, val, ok := strings.Cut(a, "="); ok {
				values = append(values, "-t", val)
			} else if i+1 < len(args) {
				i++
				values = append(values, "-t", args[i])
			} else {
				return nil
			}
		case (name == "mv" || name == "cp" || name == "ln") && longOpt(a, "--suffix"):
			if !strings.Contains(a, "=") {
				if i+1 >= len(args) {
					return nil
				}
				i++
			}
		case !strings.HasPrefix(a, "--") && len(a) > 2 && (name == "mv" || name == "cp" || name == "ln"):
			for j := 1; j < len(a); j++ {
				switch a[j] {
				case 't':
					if j+1 < len(a) {
						values = append(values, "-t", a[j+1:])
					} else if i+1 < len(args) {
						i++
						values = append(values, "-t", args[i])
					} else {
						return nil
					}
					j = len(a)
				case 'S':
					if j+1 < len(a) {
						j = len(a)
					} else if i+1 < len(args) {
						i++
						j = len(a)
					} else {
						return nil
					}
				}
			}
		case strings.HasPrefix(a, "--") && strings.Contains(a, "=") && !strings.HasPrefix(a, "--suffix=") &&
			!strings.HasPrefix(a, "--size=") && !strings.HasPrefix(a, "--reference=") && !strings.HasPrefix(a, "--iterations="):
			return nil // an option whose value may be a path the check does not know
		}
	}
	var targetDirs []string
	for k := 0; k+1 < len(values); k += 2 {
		if values[k] == "-t" || values[k] == "--target-directory" {
			if values[k+1] == "" {
				return nil
			}
			// -t/--target-directory always traverses DIR as a directory (never replaces a symlink itself);
			// a trailing "/" makes scratchPath follow symlinks and check dangling links at DIR.
			dir := values[k+1]
			if !strings.HasSuffix(dir, "/") {
				dir += "/"
			}
			targetDirs = append(targetDirs, dir)
		}
	}
	switch name {
	case "cp", "ln":
		// only the destination changes
		if len(targetDirs) > 0 {
			return targetDirs
		}
		if len(operands) == 1 && name == "ln" {
			return []string{path.Base(operands[0])}
		}
		if len(operands) < 2 {
			return nil
		}
		return operands[len(operands)-1:]
	case "mv":
		if len(targetDirs) > 0 {
			operands = append(operands, targetDirs...)
		}
		if len(operands) < 2 {
			return nil
		}
	case "tee":
		if len(operands) == 0 {
			return nil
		}
	}
	for _, o := range operands {
		if o == "-" {
			return nil
		}
	}
	return operands
}

// findOutput are the find actions that create or truncate the file named after them.
var findOutput = map[string]bool{"-fprint": true, "-fprint0": true, "-fprintf": true, "-fls": true}

// findOutputs is the first of find's -fprint, -fprint0, -fprintf and -fls in its arguments, or "".
func findOutputs(args []string) string {
	for _, a := range args {
		if findOutput[a] {
			return a
		}
	}
	return ""
}

// findTargets is what find changes: its starting points when it has -delete, and each file it writes its list to.
func findTargets(args []string) []string {
	var out []string
	if has(args, "-delete") {
		if out = findStarts(args); out == nil {
			return nil
		}
	}
	for i, a := range args {
		if findOutput[a] {
			if i+1 >= len(args) {
				return nil
			}
			out = append(out, args[i+1])
		}
	}
	return out
}

// findStarts are find's starting points (find -H/-L/-follow follows links out of them: not read).
func findStarts(args []string) []string {
	out, followLinks := parseFindStarts(args)
	if followLinks {
		return nil
	}
	for i, o := range out {
		if o == "." || o == "./" {
			out[i] = "./x" // find . -delete deletes what is in the directory, not the directory
		}
	}
	return out
}

// scratchTargets says whether every target is scratch: a program run after the words before, with the shell's
// variables and directories of the command being read.
func (g *guard) scratchTargets(before, targets []string) bool {
	if g.roots == nil || g.sv == nil || len(targets) == 0 {
		return false
	}
	for i, w := range before {
		b := progName(w)
		if (wrappers[w] || wrappers[b]) && !scratchWrappers[b] {
			return false // xargs, find -exec, ssh, eval…: the targets are not what they look like
		}
		if (b == "sudo" || b == "doas") && sudoOption(before[i+1:], "iDR", "--login", "--chdir", "--chroot") {
			// sudo -i: a login shell, which starts in the target user's home; sudo -D/etc, -bD /etc, --chd /etc:
			// another directory; sudo -R: another root, where the command starts at /
			return false
		}
		if runners[b] {
			return false // uv run --directory x, poetry -C x run: another directory
		}
		if w == "-D" || w == "-C" || strings.HasPrefix(w, "--chdir") || strings.HasPrefix(w, "-S") || strings.HasPrefix(w, "--split-string") {
			return false // sudo -D, env -C: another directory; env -S: a command line
		}
	}
	for _, t := range targets {
		paths, ok := g.sv.expand(t, g.seg)
		if !ok {
			return false
		}
		for _, p := range paths {
			if !g.scratchPath(p) {
				return false
			}
		}
	}
	return true
}

// scratchPath says whether one expanded target is scratch.
func (g *guard) scratchPath(p string) bool {
	if p == "" {
		return false
	}
	follow := strings.HasSuffix(p, "/") || strings.HasSuffix(p, "/.") || p == "." || p == ".."
	parts := strings.Split(p, "/")
	for k, part := range parts {
		if !strings.ContainsAny(part, "*?[") {
			continue
		}
		// a glob only in the last name, matching neither . nor .., and not followed into a directory
		if k != len(parts)-1 || follow {
			return false
		}
		// the shell matches a leading . only with one written out (and . and .. only then): .* can be ..
		pattern := strings.ReplaceAll(part, "[!", "[^")
		if _, err := path.Match(pattern, "x"); err != nil || strings.HasPrefix(part, ".") {
			return false
		}
	}
	if g.sv.links {
		return false
	}
	var abs []string
	if filepath.IsAbs(p) {
		abs = []string{p}
	} else {
		if len(g.sv.cwds) == 0 {
			return false
		}
		for _, c := range g.sv.cwds {
			abs = append(abs, c+"/"+p)
		}
	}
	for _, a := range abs {
		real, ok := physical(a)
		if !ok {
			return false
		}
		// a link on the way that leads nowhere yet: the command may make where it leads before the deletion runs
		through := real
		if !follow {
			through = filepath.Dir(real)
		}
		if dangles(through) {
			return false
		}
		if !g.roots.holds(resolve(real, follow)) {
			return false
		}
	}
	return true
}
