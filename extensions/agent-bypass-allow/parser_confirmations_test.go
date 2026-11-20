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
