package main

import "strings"

func findDefaultStart(args []string) bool {
	for len(args) > 0 {
		a := args[0]
		switch {
		case a == "-H" || a == "-L" || a == "-P" || strings.HasPrefix(a, "-O"):
			args = args[1:]
		case a == "-D" && len(args) > 1:
			args = args[2:]
		default:
			return strings.HasPrefix(a, "-") || a == "(" || a == "!"
		}
	}
	return true
}

func dynamicExecutable(words []string) bool {
	for len(words) > 0 {
		i := firstProgram(words)
		if i < 0 {
			return false
		}
		words = words[i:]
		if strings.ContainsAny(words[0], "$`") {
			return true
		}
		name := progName(words[0])
		args := words[1:]
		switch name {
		case "sudo", "doas":
			words = sudoProgram(args)
		case "timeout":
			words = afterOptions(args, "ks", "--kill-after", "--signal")
			if len(words) > 0 {
				words = words[1:]
			}
		case "env":
			words = afterOptions(args, "uC", "--unset", "--chdir")
		case "find":
			for i, arg := range args {
				if hasAny([]string{arg}, "-exec", "-execdir", "-ok", "-okdir") && dynamicExecutable(args[i+1:]) {
					return true
				}
			}
			return false
		case "xargs":
			words = afterOptions(args, "EILnPsd", "--eof", "--replace", "--max-lines", "--max-args", "--max-procs", "--max-chars", "--delimiter")
		case "command", "builtin", "time", "nohup", "setsid":
			words = afterOptions(args, "")
		case "exec":
			words = afterOptions(args, "a")
		case "nice":
			words = afterOptions(args, "n", "--adjustment")
		case "ionice":
			words = afterOptions(args, "cnp", "--class", "--classdata", "--pid")
		case "stdbuf":
			words = afterOptions(args, "ioe", "--input", "--output", "--error")
		default:
			if wrappers[name] || shells[name] || (runners[name] && has(args, "run")) {
				for _, arg := range args {
					if strings.ContainsAny(arg, "$`") && (!shells[name] || strings.HasPrefix(arg, "$") || strings.HasPrefix(arg, "`")) {
						return true
					}
				}
			}
			return false
		}
	}
	return false
}

func afterOptions(args []string, short string, long ...string) []string {
	for len(args) > 0 && strings.HasPrefix(args[0], "-") && args[0] != "-" {
		a := args[0]
		args = args[1:]
		if a == "--" {
			break
		}
		if strings.HasPrefix(a, "--") {
			if !strings.Contains(a, "=") && has(long, a) && len(args) > 0 {
				args = args[1:]
			}
			continue
		}
		for j := 1; j < len(a); j++ {
			if strings.ContainsRune(short, rune(a[j])) {
				if j+1 == len(a) && len(args) > 0 {
					args = args[1:]
				}
				break
			}
		}
	}
	return args
}

func sudoProgram(args []string) []string {
	for len(args) > 0 {
		a := args[0]
		if name, _, assignment := strings.Cut(a, "="); assignment && identifier.MatchString(name) {
			args = args[1:]
			continue
		}
		if !strings.HasPrefix(a, "-") || a == "-" {
			return args
		}
		args = args[1:]
		if a == "--" {
			return args
		}
		if strings.HasPrefix(a, "--") {
			if !strings.Contains(a, "=") && sudoTakesValue(a) && len(args) > 0 {
				args = args[1:]
			}
			continue
		}
		for j := 1; j < len(a); j++ {
			if strings.ContainsRune("CDghpRrTtUuac", rune(a[j])) {
				if j+1 == len(a) && len(args) > 0 {
					args = args[1:]
				}
				break
			}
		}
	}
	return nil
}
