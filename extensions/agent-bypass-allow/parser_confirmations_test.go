package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestParserSafetyConfirmations(t *testing.T) {
	b := newBox(t)
	e := newEnv(t)
	for _, c := range []struct {
		name, command string
		allow         bool
	}{
		{"reported quoted indirection", `k=PATH; v="${(P)k}"; printf '%s\n' "$v"`, true},
		{"unquoted indirection with command-like name", `k=rm; v=${(P)rm}; printf '%s\n' "$v"`, true},
		{"quoted script indirection", `zsh -c 'v=${(P)rm}; printf "%s\n" "$v"'`, true},
		{"zsh key expansion", `v=${(k)rm}; printf '%s\n' "$v"`, true},
		{"process substitution", `diff <(printf a) <(printf b)`, true},
		{"scratch glob after cd", `cd {project}/sub && rm -f ./*`, true},
		{"indirection and separate scratch removal", `v="${(P)k}"; rm -rf {tmp}/cache`, true},
		{"indirection and separate protected removal", `v="${(P)k}"; rm -rf /`, false},
		{"indirect removal target", `rm -rf "${(P)k}"`, false},
		{"resolved indirect scratch target", `k=S; S={tmp}/cache; rm -rf "${(P)k}"`, true},
		{"resolved unquoted indirect scratch target", `k=S; S={tmp}/cache; rm -rf ${(P)k}`, true},
		{"resolved indirect protected target", `k=S; S=/; rm -rf "${(P)k}"`, false},
		{"partially known selector list", `S={tmp}/cache; for k in S UNKNOWN; do rm -rf "${(P)k}"; done`, false},
		{"tainted selector", `k=S; S={tmp}/cache; read k; rm -rf "${(P)k}"`, false},
		{"tainted target", `k=S; S={tmp}/cache; read S; rm -rf "${(P)k}"`, false},
		{"selector before assignment", `S={tmp}/cache; rm -rf "${(P)k}"; k=S`, false},
		{"computed selector", `k=$(printf S); S={tmp}/cache; rm -rf "${(P)k}"`, false},
		{"dynamic executable", `"${(P)k}" -rf /`, false},
		{"dynamic executable after wrapper", `env "$cmd" -rf /`, false},
		{"dynamic executable after timeout", `cmd=rm; timeout 10 "$cmd" -rf --no-preserve-root /`, false},
		{"dynamic executable after sudo options", `cmd=rm; sudo -u root "$cmd" -rf --no-preserve-root /`, false},
		{"dynamic executable in find exec", `cmd=rm; find . -maxdepth 0 -exec "$cmd" -rf --no-preserve-root / \;`, false},
		{"sudo option is not an executable", `sudo -u "$USER" printf hello`, true},
		{"timeout duration is not an executable", `timeout "$SECONDS" printf hello`, true},
		{"dynamic builtin target", `k=S; S={tmp}/cache; typeset "$k=/"; rm -rf "${(P)k}"`, false},
		{"dynamic printf target", `k=S; S={tmp}/cache; printf -v "$k" /; rm -rf "${(P)k}"`, false},
		{"comment does not hide evaluated expansion", "A='$'; B='(rm -rf /)'; k=$A$B; # '\nv=${(e)k}", false},
		{"quoted heredoc is literal", "cat <<'EOF'\n${(e)k}\nEOF", true},
		{"unquoted heredoc evaluates", "cat <<EOF\n${(e)k}\nEOF", false},
		{"evaluated expansion", `v="${(e)k}"`, false},
		{"literal evaluated expansion", `printf '%s\n' '${(e)k}'`, true},
		{"substitution inside flags", `v=${(P)$(rm -rf /)}`, false},
		{"forced push", `v="${(P)k}"; git push --force`, false},
		{"permission change", `v="${(P)k}"; chmod 777 file`, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := request(e, "bypassPermissions", "Bash", b.cmd(c.command))
			r["cwd"] = b.project
			got := e.hook(t, r, time.Now())
			if (strings.TrimSpace(got) == allowOutput) != c.allow {
				t.Fatalf("reply = %q, want allow %v", got, c.allow)
			}
		})
	}
}

func TestAgyParserSafetyConfirmations(t *testing.T) {
	e := newAgyTestEnv(t, `{"toolPermission":"always-proceed"}`)
	for _, override := range []bool{false, true} {
		writeJSON(t, filepath.Join(e.data, optionsFile), `{"allow_destructive":`+strconv.FormatBool(override)+`}`)
		for _, c := range []struct {
			command string
			allow   bool
		}{
			{`k=PATH; v="${(P)k}"; printf '%s\n' "$v"`, true},
			{`k=S; S=` + e.project + `/cache; rm -rf "${(P)k}"`, true},
			{`rm -rf /`, false},
			{`rm -rf ~`, false},
			{`"${(P)k}" -rf /`, false},
			{`v="${(e)k}"`, false},
		} {
			r := e.preToolReq("run_command", map[string]any{"CommandLine": c.command})
			got := e.hook(t, r, time.Now(), nil)
			if (strings.TrimSpace(got) == agyAllowOutput) != c.allow {
				t.Errorf("override=%v, %q: reply=%q, want allow=%v", override, c.command, got, c.allow)
			}
		}
	}
}

func TestProtectedDeletionTargetsEvenWithOverride(t *testing.T) {
	b := newBox(t)
	if err := os.Symlink(b.home, filepath.Join(b.tmp, "home-link")); err != nil {
		t.Fatal(err)
	}
	for _, override := range []bool{false, true} {
		for _, command := range []string{
			`rm -rf /`, `rm -rf //`, `rm -rf /./`, `rm -rf /*`,
			`rm -rf ~`, `rm -rf ~/`, `rm -rf "$HOME"`, `rm -rf "${HOME}"`,
			`rm -rf {home}`, `rm -rf {home}/*`, `rm -rf {home}/..`,
			`rm -rf {tmp}/home-link/`, `rm -rf {tmp}/home-link/*`,
			`sudo rm -rf /`, `bash -c 'rm -rf /'`, `ssh host 'rm -rf /'`,
			`rmdir /`, `rimraf {home}`, `find {home} -delete`, `cd {home}; find . -delete`,
			`cd {home}; find -delete`, `cd /; find -delete`, `find -L / -delete`,
			`k=S; S=/; rm -rf "${(P)k}"`, `k=S; S={home}; rm -rf "${(P)k}"`,
		} {
			g := newGuard(b.project, override)
			if why := g.personOnly(b.cmd(command)); why == "" {
				t.Errorf("override=%v: %q was allowed", override, command)
			}
		}
	}
	g := newGuard(b.project, true)
	for _, command := range []string{`rm -rf {home}/notes`, `rm -rf {tmp}/cache`, `rm -f {tmp}/home-link`} {
		if why := g.personOnly(b.cmd(command)); why != "" {
			t.Errorf("ordinary explicit override target %q: %s", command, why)
		}
	}
}

func TestProtectedTargetsBareGlobsCdNestedAndFindFollow(t *testing.T) {
	b := newBox(t)
	if err := os.Symlink(b.home, filepath.Join(b.tmp, "home-link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/", filepath.Join(b.project, "-x")); err != nil {
		t.Fatal(err)
	}
	homeParent := filepath.Dir(b.home)
	homeBase := filepath.Base(b.home)
	for _, c := range []struct {
		name    string
		cwd     string
		command string
	}{
		{"bare star in HOME", b.home, `rm -rf *`},
		{"bare star in root", "/", `rm -rf *`},
		{"bare dotstar in HOME", b.home, `rm -rf .*`},
		{"cd root then bare star", b.project, `cd / && rm -rf *`},
		{"bare cd to HOME then dot", b.project, `cd && rm -rf .`},
		{"bare cd to HOME then star", b.project, `cd && rm -rf *`},
		{"cd -- parent of HOME then remove HOME", b.project, `cd -- ` + homeParent + ` && rm -rf ` + homeBase},
		{"cd -LP parent of HOME then remove HOME", b.project, `cd -LP ` + homeParent + ` && rm -rf ` + homeBase},
		{"cd -- bare then cd .. then remove HOME", b.project, `cd -- && cd .. && rm -rf ` + homeBase},
		{"eval cd root then bare star", b.project, `eval 'cd /' && rm -rf *`},
		{"dashdash operand pointing at root", b.project, `rm -rf -- -x/*`},
		{"env -C root then bare star", b.project, `env -C / rm -rf *`},
		{"env --chdir=root then bare star", b.project, `env --chdir=/ rm -rf *`},
		{"env -C root nested bash bare star", b.project, `env -C / bash -c 'rm -rf *'`},
		{"sudo -D root then bare star", b.project, `sudo -D / rm -rf *`},
		{"sudo env assign before -D root then bare star", b.project, `sudo FOO=1 -D / rm -rf *`},
		{"nested bash dot in HOME", b.home, `bash -c 'rm -rf .'`},
		{"nested bash star in HOME", b.home, `bash -c 'rm -rf *'`},
		{"cd root then nested bash dot", b.project, `cd / && bash -c 'rm -rf .'`},
		{"find -L symlink to HOME", b.project, `find -L {tmp}/home-link -delete`},
		{"find -H symlink to HOME", b.project, `find -H {tmp}/home-link -delete`},
		{"find -Lx symlink to HOME", b.project, `find -Lx {tmp}/home-link -delete`},
		{"find -Hx symlink to HOME", b.project, `find -Hx {tmp}/home-link -delete`},
		{"find -follow symlink to HOME", b.project, `find {tmp}/home-link -follow -delete`},
		{"find -x root delete", b.project, `find -x / -delete`},
		{"find -- root delete", b.project, `find -- / -delete`},
		{"find -f root delete", b.project, `find -f / -delete`},
		{"find -f dash-prefixed symlink dir to root", b.project, `find -f -x/ -delete`},
		{"find -H -f dash-prefixed symlink to root", b.project, `find -H -f -x -delete`},
		{"find -E root delete", b.project, `find -E / -delete`},
		{"find -s root delete", b.project, `find -s / -delete`},
	} {
		t.Run(c.name, func(t *testing.T) {
			for _, override := range []bool{false, true} {
				g := newGuard(c.cwd, override)
				if why := g.personOnly(b.cmd(c.command)); why == "" {
					t.Errorf("override=%v: %q in cwd=%s was allowed", override, c.command, c.cwd)
				}
			}
		})
	}
	// Ordinary relative subdirectory deletion inside nested bash in HOME remains allowed under override.
	if why := newGuard(b.home, true).personOnly(`bash -c 'rm -rf notes'`); why != "" {
		t.Errorf("ordinary nested override target in HOME: %s", why)
	}
	// find <scratch> -name '*.tmp' -delete in cwd=$HOME must NOT treat -name's glob pattern as "." in $HOME.
	for _, override := range []bool{false, true} {
		if why := newGuard(b.home, override).personOnly(b.cmd(`find {tmp}/cache -name '*.tmp' -delete`)); why != "" {
			t.Errorf("override=%v: find scratch -name '*.tmp' -delete in HOME got %q, want allow", override, why)
		}
	}
}

func TestParserAnsiCAndAttachedDynamicGuards(t *testing.T) {
	for _, c := range []struct {
		name    string
		command string
		allow   bool
	}{
		{"ansi-c escaped quote before zsh eval", `a='$'; b='(rm -rf /)'; x="$a$b"; echo $'\'' && echo ${(e)x}`, false},
		{"ansi-c escaped backslash before quote", `echo $'\\' && echo ${(e)x}`, false},
		{"ansi-c literal zsh eval", `echo $'${(e)x}'`, true},
		{"attached sh -c dynamic", `cmd='rm -rf /'; sh -c"$cmd"`, false},
		{"empty-quote prefixed sh -c dynamic", `cmd='rm -rf /'; sh -c "''$cmd"`, false},
		{"empty-quote attached sh -c dynamic", `cmd='rm -rf /'; sh -c"''$cmd"`, false},
		{"attached zsh -c indirection", `k=c; c='rm -rf /'; zsh -c"${(P)k}"`, false},
		{"empty-quote zsh -c indirection", `k=c; c='rm -rf /'; zsh -c"''${(P)k}"`, false},
		{"attached env -S dynamic", `cmd='rm -rf /'; env -S"$cmd"`, false},
		{"separate env -S dynamic", `cmd='rm -rf /'; env -S "$cmd"`, false},
		{"attached env --split-string dynamic", `cmd='rm -rf /'; env --split-string="$cmd"`, false},
		{"abbreviated env --split dynamic", `cmd='rm -rf /'; env --split="$cmd"`, false},
		{"abbreviated env --split static rm", `env --split='rm -rf /'`, false},
		{"separate env --split-string dynamic", `cmd='rm -rf /'; env --split-string "$cmd"`, false},
		{"env -a before -S dynamic", `cmd='rm -rf /'; env -a x -S "$cmd"`, false},
		{"env -S underscore space escape dynamic", `cmd=rm; env -S "FOO=1\_$cmd\_-rf\_/"`, false},
		{"env -S underscore space escape static rm", `env -S 'rm\_-rf\_/'`, false},
		{"env -S non-dynamic command with var arg", `env -S "printf %s $HOME"`, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			why := newGuard("/tmp", false).personOnly(c.command)
			if (why == "") != c.allow {
				t.Errorf("%q: why=%q, want allow=%v", c.command, why, c.allow)
			}
		})
	}
}
