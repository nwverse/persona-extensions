package main

import (
	"path"
	"regexp"
	"strings"
)

// The floor: Bash commands that stay with a person even in bypassPermissions mode, because they delete or overwrite
// data, force a push, or change permissions (local or online). When Claude Code asks about such a command, the hook
// stays silent and the person answers. It reads the command loosely and errs toward asking: every part of a list,
// pipeline or substitution counts, and a quoted argument is read again as a command (bash -c '…', psql -c "…"),
// except the text of a commit message, title or body (-m, --message, --title, --body).
//
// What it cannot see: a command hidden in a variable, a script file, base64 or another program's input.

// alwaysPerson are programs that delete, overwrite or change permissions whatever their arguments.
var alwaysPerson = map[string]string{
	"rm": "deletion", "rmdir": "deletion", "unlink": "deletion", "shred": "deletion", "srm": "deletion",
	"trash": "deletion", "trash-put": "deletion", "userdel": "deletion", "groupdel": "deletion",
	"dd": "overwrite", "truncate": "overwrite", "wipefs": "overwrite", "fdisk": "overwrite", "parted": "overwrite",
	"mv":    "overwrite",
	"chmod": "permission change", "chown": "permission change", "chgrp": "permission change",
	"chattr": "permission change", "setfacl": "permission change", "usermod": "permission change",
	"passwd": "permission change", "chpasswd": "permission change", "visudo": "permission change",
	"gpasswd": "permission change", "setcap": "permission change",
}

// sqlDestructive is SQL that deletes data or changes who may use it.
var sqlDestructive = regexp.MustCompile(`(?i)\b(drop\s+(table|database|schema|index|view|user|role)|truncate\b|delete\s+from|grant\b|revoke\b|alter\s+(user|role))`)

// onlinePermission is a path in a GitHub API call that is about who may do what.
var onlinePermission = regexp.MustCompile(`(?i)(collaborators|permissions|protection|/teams|/members|memberships|invitations|rulesets|/keys|secrets|/hooks|environments)`)

// redirect is output sent to a file with > or >| (not >>, 2>, >&, or a /dev/ file), which overwrites it.
var redirect = regexp.MustCompile(`(?:^|[^<>&0-9|])>\|?\s*([^\s>&|;()]+)`)

// messageFlags take free text (a commit message, a pull request's title or body), which is not read as a command.
var messageFlags = map[string]bool{"-m": true, "--message": true, "--title": true, "--body": true, "-t": true, "-b": true}

// personOnly says why cmd must stay with a person ("" when it need not).
func personOnly(cmd string) string { return personOnlyDepth(cmd, 0) }

func personOnlyDepth(cmd string, depth int) string {
	for _, m := range redirect.FindAllStringSubmatch(cmd, -1) {
		if !strings.HasPrefix(m[1], "/dev/") {
			return "overwrite: > " + m[1]
		}
	}
	for _, words := range segments(cmd) {
		kept := withoutMessages(words)
		if sqlDestructive.MatchString(strings.Join(kept, " ")) {
			return "deletion or permission change (SQL)"
		}
		if why := segmentPersonOnly(kept); why != "" {
			return why
		}
		if depth < 3 {
			for _, w := range kept {
				if strings.ContainsAny(w, " \t\n;&|()`") {
					if why := personOnlyDepth(w, depth+1); why != "" {
						return why
					}
				}
			}
		}
	}
	return ""
}

// withoutMessages drops the free text after a message flag (-m 'text', --body=text).
func withoutMessages(words []string) []string {
	var out []string
	for i := 0; i < len(words); i++ {
		w := words[i]
		if messageFlags[w] {
			out = append(out, w)
			i++
			continue
		}
		if k, _, ok := strings.Cut(w, "="); ok && messageFlags[k] && strings.HasPrefix(k, "--") {
			out = append(out, k)
			continue
		}
		out = append(out, w)
	}
	return out
}

// segments splits cmd into the words of each simple command, as a shell would group them: quotes keep a word
// together (and are removed), a backslash escapes the next character, and lists, pipes, subshells, groups and command
// substitutions separate.
func segments(cmd string) [][]string {
	var out [][]string
	var words []string
	var cur strings.Builder
	inWord, single, double := false, false, false
	endWord := func() {
		if inWord {
			words = append(words, cur.String())
			cur.Reset()
			inWord = false
		}
	}
	endSegment := func() {
		endWord()
		if len(words) > 0 {
			out = append(out, words)
			words = nil
		}
	}
	rs := []rune(cmd)
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		switch {
		case single:
			if r == '\'' {
				single = false
			} else {
				cur.WriteRune(r)
			}
		case r == '\\' && i+1 < len(rs):
			i++
			if rs[i] != '\n' {
				cur.WriteRune(rs[i])
				inWord = true
			}
		case double:
			if r == '"' {
				double = false
			} else {
				cur.WriteRune(r)
			}
		case r == '\'':
			single, inWord = true, true
		case r == '"':
			double, inWord = true, true
		case strings.ContainsRune(";&|\n()`{}", r):
			endSegment()
		case r == ' ' || r == '\t' || r == '\r':
			endWord()
		case r == '$' && i+1 < len(rs) && rs[i+1] == '(':
			endSegment()
		default:
			cur.WriteRune(r)
			inWord = true
		}
	}
	endSegment()
	return out
}

// isProgram says whether word w names the program name: the bare name, or a path to it in a bin directory.
func isProgram(w, name string) bool {
	return w == name || (strings.Contains(w, "bin/") && path.Base(w) == name)
}

func segmentPersonOnly(words []string) string {
	for i, w := range words {
		name := w
		if strings.Contains(w, "bin/") {
			name = path.Base(w)
		}
		if why, ok := alwaysPerson[name]; ok && isProgram(w, name) {
			return why + ": " + name
		}
		if strings.HasPrefix(name, "mkfs") {
			return "overwrite: " + name
		}
		rest := words[i+1:]
		var why string
		switch name {
		case "git":
			why = gitPersonOnly(rest)
		case "gh":
			why = ghPersonOnly(rest)
		case "find":
			if hasAny(rest, "-delete", "-exec", "-execdir", "-ok") {
				why = "deletion: find -delete"
			}
		case "rsync":
			if hasPrefix(rest, "--delete") || has(rest, "--remove-source-files") {
				why = "deletion: rsync --delete"
			}
		case "cp":
			if has(rest, "--force") || has(rest, "--remove-destination") || hasShort(rest, 'f') {
				why = "overwrite: cp -f"
			}
		case "tee":
			if !has(rest, "--append") && !hasShort(rest, 'a') && len(rest) > 0 {
				why = "overwrite: tee"
			}
		case "docker", "podman":
			if hasAny(rest, "rm", "rmi", "prune") || (has(rest, "down") && (has(rest, "--volumes") || hasShort(rest, 'v'))) {
				why = "deletion: " + name
			}
		case "kubectl", "helm":
			if hasAny(rest, "delete", "uninstall", "drain") {
				why = "deletion: " + name
			} else if hasAny(rest, "create", "apply", "edit", "patch", "replace", "set") &&
				hasAny(rest, "role", "rolebinding", "clusterrole", "clusterrolebinding", "serviceaccount", "sa") {
				why = "permission change: " + name
			}
		case "terraform", "tofu", "pulumi":
			if has(rest, "destroy") || has(rest, "-destroy") {
				why = "deletion: " + name + " destroy"
			}
		case "aws":
			switch {
			case has(rest, "iam") || hasPrefixContaining(rest, "put-", "policy") || hasPrefixContaining(rest, "put-", "acl"):
				why = "permission change: aws"
			case hasPrefix(rest, "delete-") || hasPrefix(rest, "terminate-") || (has(rest, "s3") && hasAny(rest, "rm", "rb")):
				why = "deletion: aws"
			}
		case "gcloud", "gsutil":
			for _, r := range rest {
				if strings.Contains(r, "iam-policy") || r == "iam" || r == "acl" {
					why = "permission change: " + name
				}
			}
			if why == "" && hasAny(rest, "delete", "rm", "rb") {
				why = "deletion: " + name
			}
		case "az":
			if has(rest, "role") {
				why = "permission change: az role"
			} else if has(rest, "delete") {
				why = "deletion: az"
			}
		}
		if why != "" {
			return why
		}
	}
	return ""
}

// gitPersonOnly looks at what follows git: its subcommand (after -C, -c and other global flags) and that one's
// arguments.
func gitPersonOnly(args []string) string {
	i := 0
	for i < len(args) && strings.HasPrefix(args[i], "-") {
		if args[i] == "-C" || args[i] == "-c" || args[i] == "--git-dir" || args[i] == "--work-tree" {
			i++
		}
		i++
	}
	if i >= len(args) {
		return ""
	}
	sub, rest := args[i], args[i+1:]
	switch sub {
	case "push":
		for _, a := range rest {
			if strings.HasPrefix(a, "--force") || a == "--mirror" || a == "--delete" || a == "--prune" ||
				strings.HasPrefix(a, "+") || (strings.HasPrefix(a, ":") && len(a) > 1) || shortFlag(a, 'f') || shortFlag(a, 'd') {
				return "force push: git push " + a
			}
		}
	case "clean", "rm", "filter-branch", "filter-repo":
		return "deletion: git " + sub
	case "restore":
		return "overwrite: git restore"
	case "reset":
		if has(rest, "--hard") || has(rest, "--merge") || has(rest, "--keep") {
			return "overwrite: git reset --hard"
		}
	case "checkout", "switch":
		if has(rest, "--") || has(rest, ".") || has(rest, "--force") || has(rest, "--discard-changes") || hasShort(rest, 'f') {
			return "overwrite: git " + sub
		}
	case "branch", "tag":
		if has(rest, "--delete") || has(rest, "--force") || hasShort(rest, 'd') || hasShort(rest, 'D') || hasShort(rest, 'f') || hasShort(rest, 'M') {
			return "deletion or overwrite: git " + sub
		}
	case "stash":
		if hasAny(rest, "drop", "clear") {
			return "deletion: git stash drop"
		}
	case "worktree":
		if hasAny(rest, "remove", "prune") {
			return "deletion: git worktree remove"
		}
	case "update-ref":
		if has(rest, "-d") {
			return "deletion: git update-ref -d"
		}
	case "reflog":
		if hasAny(rest, "expire", "delete") {
			return "deletion: git reflog"
		}
	}
	return ""
}

// ghPersonOnly looks at what follows gh.
func ghPersonOnly(args []string) string {
	if has(args, "delete") || has(args, "--delete-branch") || hasPrefix(args, "--delete") {
		return "deletion: gh"
	}
	if len(args) == 0 {
		return ""
	}
	switch args[0] {
	case "secret", "variable", "ssh-key", "gpg-key":
		return "permission change: gh " + args[0]
	case "repo":
		if len(args) > 1 && (args[1] == "deploy-key" || args[1] == "archive" || (args[1] == "edit" && hasPrefix(args, "--visibility"))) {
			return "permission change: gh repo " + args[1]
		}
	case "api":
		method := "GET"
		for i, a := range args {
			switch {
			case (a == "-X" || a == "--method") && i+1 < len(args):
				method = strings.ToUpper(args[i+1])
			case strings.HasPrefix(a, "-X") && len(a) > 2:
				method = strings.ToUpper(a[2:])
			case strings.HasPrefix(a, "--method="):
				method = strings.ToUpper(strings.TrimPrefix(a, "--method="))
			case a == "-f" || a == "-F" || a == "--field" || a == "--raw-field" || a == "--input" ||
				strings.HasPrefix(a, "--field=") || strings.HasPrefix(a, "--raw-field=") || strings.HasPrefix(a, "--input="):
				if method == "GET" {
					method = "POST"
				}
			}
		}
		if method == "DELETE" {
			return "deletion: gh api DELETE"
		}
		if has(args, "graphql") {
			for _, a := range args {
				if strings.Contains(strings.ToLower(a), "mutation") {
					return "online change: gh api graphql mutation"
				}
			}
		}
		if method != "GET" && method != "HEAD" {
			for _, a := range args {
				if v := strings.ToLower(a); strings.HasPrefix(v, "visibility=") || strings.HasPrefix(v, "private=") {
					return "permission change: gh api " + a
				}
			}
		}
		if method != "GET" && method != "HEAD" {
			for _, a := range args[1:] {
				if !strings.HasPrefix(a, "-") && onlinePermission.MatchString(a) {
					return "permission change: gh api " + method
				}
			}
		}
	}
	return ""
}

func has(words []string, w string) bool { return hasAny(words, w) }

func hasAny(words []string, ws ...string) bool {
	for _, x := range words {
		for _, w := range ws {
			if x == w {
				return true
			}
		}
	}
	return false
}

// hasPrefixContaining says whether a word starts with p and has c in it (put-bucket-policy).
func hasPrefixContaining(words []string, p, c string) bool {
	for _, x := range words {
		if strings.HasPrefix(x, p) && strings.Contains(x, c) {
			return true
		}
	}
	return false
}

func hasPrefix(words []string, p string) bool {
	for _, x := range words {
		if strings.HasPrefix(x, p) {
			return true
		}
	}
	return false
}

// shortFlag says whether a is a cluster of one-letter flags (-f, -fu) that has c.
func shortFlag(a string, c byte) bool {
	if len(a) < 2 || a[0] != '-' || a[1] == '-' {
		return false
	}
	return strings.IndexByte(a[1:], c) >= 0
}

func hasShort(words []string, c byte) bool {
	for _, x := range words {
		if shortFlag(x, c) {
			return true
		}
	}
	return false
}
