package main

import (
	"path"
	"regexp"
	"strings"
)

// The floor: Bash commands that stay with a person even in bypassPermissions mode, because they delete or overwrite
// data, force a push, or change permissions (local or online). The hook leaves any prompt for such a command to
// Claude Code, which then asks as usual. It reads the command loosely and errs toward asking: quotes are dropped,
// every word of every part of a pipeline or list is looked at, so `echo rm` or `grep chmod` count too.
//
// What it cannot see: a command hidden in a variable, a script file, base64 or another program's input.

// alwaysPerson are programs that delete, overwrite or change permissions whatever their arguments.
var alwaysPerson = map[string]string{
	"rm": "deletion", "rmdir": "deletion", "unlink": "deletion", "shred": "deletion", "srm": "deletion",
	"trash": "deletion", "trash-put": "deletion", "userdel": "deletion", "groupdel": "deletion",
	"dd": "overwrite", "truncate": "overwrite", "wipefs": "overwrite", "fdisk": "overwrite", "parted": "overwrite",
	"chmod": "permission change", "chown": "permission change", "chgrp": "permission change",
	"chattr": "permission change", "setfacl": "permission change", "usermod": "permission change",
	"passwd": "permission change", "chpasswd": "permission change", "visudo": "permission change",
	"gpasswd": "permission change", "setcap": "permission change",
}

// sqlDestructive is SQL that deletes data, anywhere in the command.
var sqlDestructive = regexp.MustCompile(`(?i)\b(drop\s+(table|database|schema|index|view)|truncate\s+table|delete\s+from)\b`)

// onlinePermission is a path in a GitHub API call that is about who may do what.
var onlinePermission = regexp.MustCompile(`(?i)(collaborators|permissions|protection|/teams|/members|memberships|invitations|rulesets|/keys|secrets|/hooks|environments)`)

// personOnly says why cmd must stay with a person ("" when it need not).
func personOnly(cmd string) string {
	if sqlDestructive.MatchString(cmd) {
		return "deletion (SQL)"
	}
	for _, words := range segments(cmd) {
		if why := segmentPersonOnly(words); why != "" {
			return why
		}
	}
	return ""
}

// segments splits cmd into the words of each simple command, loosely: quotes and line continuations are dropped,
// and lists, pipes, subshells and command substitutions all separate.
func segments(cmd string) [][]string {
	cmd = strings.ReplaceAll(cmd, "\\\n", " ")
	cmd = strings.NewReplacer(`'`, "", `"`, "", `\`, "").Replace(cmd)
	parts := strings.FieldsFunc(cmd, func(r rune) bool {
		return strings.ContainsRune(";&|\n()`{}", r)
	})
	var out [][]string
	for _, p := range parts {
		p = strings.ReplaceAll(p, "$", " ")
		if w := strings.Fields(p); len(w) > 0 {
			out = append(out, w)
		}
	}
	return out
}

func segmentPersonOnly(words []string) string {
	for i, w := range words {
		name := path.Base(w)
		if why, ok := alwaysPerson[name]; ok {
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
			if has(rest, "-delete") {
				why = "deletion: find -delete"
			}
		case "rsync":
			if hasPrefix(rest, "--delete") || has(rest, "--remove-source-files") {
				why = "deletion: rsync --delete"
			}
		case "docker", "podman":
			if len(rest) > 0 && (has(rest, "rm") || has(rest, "rmi") || has(rest, "prune")) {
				why = "deletion: " + name
			}
		case "kubectl", "helm":
			if has(rest, "delete") || has(rest, "uninstall") {
				why = "deletion: " + name
			} else if hasAny(rest, "role", "rolebinding", "clusterrole", "clusterrolebinding", "serviceaccount") {
				why = "permission change: " + name
			}
		case "aws":
			if len(rest) > 0 && rest[0] == "iam" {
				why = "permission change: aws iam"
			}
		case "gcloud", "gsutil":
			for _, r := range rest {
				if strings.Contains(r, "iam-policy") || r == "iam" || r == "acl" {
					why = "permission change: " + name
				}
			}
		case "az":
			if len(rest) > 0 && rest[0] == "role" {
				why = "permission change: az role"
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
			return "deletion: git " + sub + " -d"
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
