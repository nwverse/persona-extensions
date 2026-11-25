package main

import (
	"os"
	"path/filepath"
	"strings"
)

func (g *guard) protectedTargets(targets []string) bool {
	return g.protectedTargetsWithContext(nil, targets, false)
}

func (g *guard) protectedTargetsWithContext(before, targets []string, followAll bool) bool {
	home, _ := os.UserHomeDir()
	home = resolve(home, true)
	extraCwds, extraUnknown := wrapperCwdChanges(before, home)
	dashdash := false
	for _, target := range targets {
		if !dashdash {
			if target == "--" {
				dashdash = true
				continue
			}
			if strings.HasPrefix(target, "-") {
				continue
			}
		}
		paths := []string{target}
		if g.sv != nil {
			if expanded, ok := g.sv.expand(target, g.seg); ok {
				paths = expanded
			}
		}
		for _, p := range paths {
			if p == "" || strings.ContainsAny(p, "$`") {
				continue
			}
			follow := followAll || strings.HasSuffix(p, "/") || strings.HasSuffix(p, "/.") || p == "." || p == ".."
			if i := strings.IndexAny(p, "*?["); i >= 0 {
				p = p[:strings.LastIndex(p[:i], "/")+1]
				if p == "" {
					p = "."
				}
				follow = true
			}
			abs := []string{p}
			if !filepath.IsAbs(p) {
				if extraUnknown || (g.sv != nil && g.sv.protUnknown) {
					cleanRel := filepath.Clean(p)
					if cleanRel == "." || cleanRel == ".." || strings.HasPrefix(cleanRel, "../") {
						return true
					}
				}
				abs = nil
				if g.sv != nil {
					for _, cwd := range g.sv.protCwds {
						abs = append(abs, cwd+"/"+p)
					}
				} else if g.cwd != "" {
					abs = append(abs, g.cwd+"/"+p)
				}
				for _, cwd := range extraCwds {
					abs = append(abs, cwd+"/"+p)
				}
			}
			for _, a := range abs {
				if real, ok := physical(a); ok {
					real = resolve(real, follow)
					if real == "/" || (filepath.IsAbs(home) && within(home, real)) {
						return true
					}
				}
			}
		}
	}
	return false
}

// wrapperCwdChanges extracts candidate working directories introduced by wrapper flags
// (env -C/--chdir, sudo/doas -D/--chdir/-i/--login/-R/--chroot) in before.
func wrapperCwdChanges(before []string, home string) (cwds []string, unknown bool) {
	addDir := func(d string) {
		d = strings.TrimSpace(d)
		if d == "" {
			unknown = true
			return
		}
		if d == "~" || strings.HasPrefix(d, "~/") {
			if home != "" {
				d = home + strings.TrimPrefix(d, "~")
			} else {
				unknown = true
				return
			}
		}
		if filepath.IsAbs(d) {
			cwds = append(cwds, d)
		} else {
			unknown = true
		}
	}
	for i := 0; i < len(before); i++ {
		w := before[i]
		b := progName(w)
		switch b {
		case "env":
			for j := i + 1; j < len(before); j++ {
				a := before[j]
				if a == "--" || !strings.HasPrefix(a, "-") || a == "-" {
					break
				}
				switch {
				case longOpt(a, "--chdir"):
					if _, val, ok := strings.Cut(a, "="); ok {
						addDir(val)
					} else if j+1 < len(before) {
						j++
						addDir(before[j])
					} else {
						unknown = true
					}
				case strings.HasPrefix(a, "--"):
					if !strings.Contains(a, "=") && longOpt(a, "--unset", "--split-string", "--argv0") && j+1 < len(before) {
						j++
					}
				default:
					for k := 1; k < len(a); k++ {
						switch a[k] {
						case 'C':
							if k+1 < len(a) {
								addDir(a[k+1:])
							} else if j+1 < len(before) {
								j++
								addDir(before[j])
							} else {
								unknown = true
							}
							k = len(a)
						case 'u', 'S', 'a':
							if k+1 == len(a) && j+1 < len(before) {
								j++
							}
							k = len(a)
						}
					}
				}
			}
		case "sudo", "doas":
			for j := i + 1; j < len(before); j++ {
				a := before[j]
				if name, _, ok := strings.Cut(a, "="); ok && identifier.MatchString(name) {
					continue
				}
				if a == "--" || !strings.HasPrefix(a, "-") || a == "-" {
					break
				}
				switch {
				case longOpt(a, "--chdir"):
					if _, val, ok := strings.Cut(a, "="); ok {
						addDir(val)
					} else if j+1 < len(before) {
						j++
						addDir(before[j])
					} else {
						unknown = true
					}
				case longOpt(a, "--login"):
					if home != "" {
						cwds = append(cwds, home)
					}
					unknown = true
				case longOpt(a, "--chroot"):
					cwds = append(cwds, "/")
					unknown = true
				case strings.HasPrefix(a, "--"):
					if !strings.Contains(a, "=") && sudoTakesValue(a) && j+1 < len(before) {
						j++
					}
				default:
					for k := 1; k < len(a); k++ {
						switch a[k] {
						case 'D':
							if k+1 < len(a) {
								addDir(a[k+1:])
							} else if j+1 < len(before) {
								j++
								addDir(before[j])
							} else {
								unknown = true
							}
							k = len(a)
						case 'i':
							if home != "" {
								cwds = append(cwds, home)
							}
							unknown = true
						case 'R':
							cwds = append(cwds, "/")
							unknown = true
							if k+1 == len(a) && j+1 < len(before) {
								j++
							}
							k = len(a)
						case 'C', 'g', 'h', 'p', 'r', 'T', 't', 'U', 'u', 'a', 'c':
							if k+1 == len(a) && j+1 < len(before) {
								j++
							}
							k = len(a)
						}
					}
				}
			}
		}
	}
	return cwds, unknown
}

func evaluatedZshExpansion(cmd string) bool {
	single, double, ansiSingle := false, false, false
	for i := 0; i < len(cmd); i++ {
		switch {
		case ansiSingle:
			if cmd[i] == '\\' {
				i++
			} else if cmd[i] == '\'' {
				ansiSingle = false
			}
		case cmd[i] == '#' && !single && !double && (i == 0 || strings.ContainsRune(" \t\r\n;&|(<>", rune(cmd[i-1]))):
			for i+1 < len(cmd) && cmd[i+1] != '\n' {
				i++
			}
		case cmd[i] == '$' && !single && !double && i+1 < len(cmd) && cmd[i+1] == '\'':
			ansiSingle = true
			i++
		case cmd[i] == '\\' && !single:
			i++
		case cmd[i] == '\'' && !double:
			single = !single
		case cmd[i] == '"' && !single:
			double = !double
		case !single && strings.HasPrefix(cmd[i:], "${("):
			end := strings.IndexByte(cmd[i+3:], ')')
			if end >= 0 && strings.ContainsRune(cmd[i+3:i+3+end], 'e') {
				return true
			}
		}
	}
	return false
}
