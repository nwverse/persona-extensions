package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"unicode"
	"unicode/utf8"
)

const (
	// writeMaxRunes bounds one write-back: it lands where others read it (the to-do's thread, other extensions, later
	// Agents' hand-over context), so the bot writes a summary and leaves the details in its Orca chat.
	writeMaxRunes = 1500
	// writesPerTodo bounds how many write-backs one to-do takes.
	writesPerTodo = 10
)

// safeID is what an id from Persona or Orca must look like before it goes into a path, a socket message or a call.
var safeID = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,200}$`)

// clip cuts s to at most n runes, marking the cut.
func clip(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n]) + "…"
}

// cleanText drops control characters other than newline, carriage return and tab: the hub refuses them on to-do
// writes (terminal_control_refused), and they have no place in what a bot reads either.
func cleanText(s string) string {
	s = strings.ToValidUTF8(s, "")
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == '\t' {
			return r
		}
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
}

// shortHash is the first n hex digits of the SHA-256 of the parts joined by NUL.
func shortHash(n int, parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	h := hex.EncodeToString(sum[:])
	if n > len(h) {
		n = len(h)
	}
	return h[:n]
}

// writeJSONFile writes v as JSON to path atomically (a temporary file renamed over it), readable only by this user.
func writeJSONFile(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// readJSONFile reads path into v; a missing file leaves v as it is. A file that is not valid JSON is kept aside
// (.bad) and v left as it is, so a damaged state never stops the program.
func readJSONFile(path string, v any) error {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, v); err != nil {
		_ = os.Rename(path, path+".bad")
		return err
	}
	return nil
}

// ownedByMe: the file belongs to this process's user.
func ownedByMe(fi os.FileInfo) bool {
	st, ok := fi.Sys().(*syscall.Stat_t)
	return ok && int(st.Uid) == os.Getuid()
}
