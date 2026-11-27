package main

import "strings"

func isFindLeadingFlags(a string) (ok, follow bool) {
	if len(a) < 2 || a[0] != '-' || a == "--" {
		return false, false
	}
	for i := 1; i < len(a); i++ {
		switch a[i] {
		case 'H', 'L':
			follow = true
		case 'P', 'E', 'X', 'd', 's', 'x':
		default:
			return false, false
		}
	}
	return true, follow
}

// parseFindStarts extracts find's starting paths and whether symlink traversal (-H, -L, -follow) is enabled.
func parseFindStarts(args []string) (paths []string, followLinks bool) {
	all := args
	for len(args) > 0 {
		a := args[0]
		switch {
		case a == "--":
			args = args[1:]
			goto starts
		case a == "-D" && len(args) > 1:
			args = args[2:]
		case strings.HasPrefix(a, "-O"):
			args = args[1:]
		case a == "-f" && len(args) > 1:
			paths = append(paths, args[1])
			args = args[2:]
		case len(a) > 2 && strings.HasPrefix(a, "-f") && (a[2] == '/' || a[2] == '.' || a[2] == '~'):
			paths = append(paths, a[2:])
			args = args[1:]
		default:
			if ok, follow := isFindLeadingFlags(a); ok {
				if follow {
					followLinks = true
				}
				args = args[1:]
				continue
			}
			goto starts
		}
	}
starts:
	for _, a := range args {
		if strings.HasPrefix(a, "-") || a == "(" || a == "!" || a == "," {
			break
		}
		paths = append(paths, a)
	}
	if has(all, "-follow") {
		followLinks = true
	}
	if len(paths) == 0 {
		paths = []string{"."}
	}
	return paths, followLinks
}

func dynamicExecutable(words []string) bool {
	return dynamicExecutableDepth(words, 0)
}

func dynamicExecutableDepth(words []string, depth int) bool {
	if depth > 4 {
		return true
	}
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
			if envSplitStringDynamic(args, depth) {
				return true
			}
			words = envAfterOptions(args)
		case "find":
			for i, arg := range args {
				if hasAny([]string{arg}, "-exec", "-execdir", "-ok", "-okdir") && dynamicExecutableDepth(args[i+1:], depth+1) {
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
					if shells[name] {
						if shellDynamicArg(arg, depth) {
							return true
						}
						continue
					}
					if strings.ContainsAny(arg, "$`") {
						return true
					}
				}
			}
			return false
		}
	}
	return false
}

// shortFlagAttached returns the attached value following flag in a combined short-option word
// such as -c<val>, -ec<val>, or -iS<val>. Every character before flag must be a single-letter
// ASCII option flag so words like "-q$(cmd)oProxyCommand=nc" are never mistaken for -c.
func shortFlagAttached(arg string, flag byte) (string, bool) {
	if len(arg) < 3 || arg[0] != '-' || arg[1] == '-' {
		return "", false
	}
	for i := 1; i < len(arg); i++ {
		c := arg[i]
		if c == flag {
			if i+1 < len(arg) {
				return arg[i+1:], true
			}
			return "", false
		}
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')) {
			return "", false
		}
	}
	return "", false
}

func shellDynamicArg(arg string, depth int) bool {
	if !strings.ContainsAny(arg, "$`") {
		return false
	}
	if strings.HasPrefix(arg, "$") || strings.HasPrefix(arg, "`") {
		return true
	}
	payload := arg
	switch {
	case strings.HasPrefix(arg, "--command="):
		payload = strings.TrimPrefix(arg, "--command=")
	case strings.HasPrefix(arg, "--"):
		return false
	case strings.HasPrefix(arg, "-"):
		if rest, ok := shortFlagAttached(arg, 'c'); ok {
			payload = rest
		} else {
			return false
		}
	}
	trimmed := strings.TrimSpace(payload)
	if strings.HasPrefix(trimmed, "$") || strings.HasPrefix(trimmed, "`") {
		return true
	}
	segs, _ := scan(payload)
	for _, seg := range segs {
		if dynamicExecutableDepth(seg, depth+1) {
			return true
		}
	}
	return false
}

func envAfterOptions(args []string) []string {
	for len(args) > 0 && strings.HasPrefix(args[0], "-") && args[0] != "-" {
		a := args[0]
		args = args[1:]
		if a == "--" {
			break
		}
		if strings.HasPrefix(a, "--") {
			if !strings.Contains(a, "=") && longOpt(a, "--unset", "--chdir", "--split-string", "--argv0") && len(args) > 0 {
				args = args[1:]
			}
			continue
		}
		for j := 1; j < len(a); j++ {
			if strings.ContainsRune("uCSa", rune(a[j])) {
				if j+1 == len(a) && len(args) > 0 {
					args = args[1:]
				}
				break
			}
		}
	}
	return args
}

func envSplitStringDynamic(args []string, depth int) bool {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" || !strings.HasPrefix(a, "-") || a == "-" {
			break
		}
		var val string
		hasVal := false
		switch {
		case longOpt(a, "--split-string"):
			if _, after, ok := strings.Cut(a, "="); ok {
				val, hasVal = after, true
			} else if i+1 < len(args) {
				i++
				val, hasVal = args[i], true
			}
		case strings.HasPrefix(a, "--"):
			if !strings.Contains(a, "=") && longOpt(a, "--unset", "--chdir", "--argv0") && i+1 < len(args) {
				i++
			}
		default:
			for j := 1; j < len(a); j++ {
				switch a[j] {
				case 'u', 'C', 'a':
					if j+1 == len(a) && i+1 < len(args) {
						i++
					}
					j = len(a)
				case 'S':
					if j+1 < len(a) {
						val, hasVal = a[j+1:], true
					} else if i+1 < len(args) {
						i++
						val, hasVal = args[i], true
					}
					j = len(a)
				}
			}
		}
		if hasVal {
			// env -S decodes \_ as a space separator.
			normalized := strings.ReplaceAll(val, `\_`, " ")
			trimmed := strings.TrimSpace(normalized)
			if strings.HasPrefix(trimmed, "$") || strings.HasPrefix(trimmed, "`") {
				return true
			}
			segs, _ := scan(normalized)
			for _, seg := range segs {
				if dynamicExecutableDepth(seg, depth+1) {
					return true
				}
			}
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
