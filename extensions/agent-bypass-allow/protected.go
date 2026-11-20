package main

import (
	"os"
	"path/filepath"
	"strings"
)

func (g *guard) protectedTargets(targets []string) bool {
	home, _ := os.UserHomeDir()
	home = resolve(home, true)
	for _, target := range targets {
		if strings.HasPrefix(target, "-") {
			continue
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
			follow := strings.HasSuffix(p, "/") || strings.HasSuffix(p, "/.") || p == "." || p == ".."
			if i := strings.IndexAny(p, "*?["); i >= 0 {
				p = p[:strings.LastIndex(p[:i], "/")+1]
				follow = true
			}
			if p == "" {
				continue
			}
			abs := []string{p}
			if !filepath.IsAbs(p) {
				abs = nil
				if g.sv != nil {
					for _, cwd := range g.sv.cwds {
						abs = append(abs, cwd+"/"+p)
					}
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

func evaluatedZshExpansion(cmd string) bool {
	single, double := false, false
	for i := 0; i < len(cmd); i++ {
		switch {
		case cmd[i] == '#' && !single && !double && (i == 0 || strings.ContainsRune(" \t\r\n;&|(<>", rune(cmd[i-1]))):
			for i+1 < len(cmd) && cmd[i+1] != '\n' {
				i++
			}
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
