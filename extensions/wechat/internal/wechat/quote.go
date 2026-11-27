package wechat

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	quoteFileName     = "quote-index.json"
	quoteFileVersion  = 1
	quoteIDMost       = 256
	quoteFileReadMost = 32 << 20
)

var defaultQuotePolicy = quotePolicy{
	textTTL: 30 * 24 * time.Hour, mediaTTL: 7 * 24 * time.Hour,
	maxEntries: 2048, maxBytes: 4 << 20,
}

// quotePolicy bounds the local index. maxBytes counts encoded entries, including their JSON field names.
type quotePolicy struct {
	textTTL    time.Duration
	mediaTTL   time.Duration
	maxEntries int
	maxBytes   int
}

// quoteMedia is a summary, never a CDN address, decryption key or downloaded payload.
type quoteMedia struct {
	Kind string `json:"kind"`
	Name string `json:"name,omitempty"`
	Size int64  `json:"size,omitempty"`
}

type quoteEntry struct {
	ID       string       `json:"id"`
	Text     string       `json:"text,omitempty"`
	Media    []quoteMedia `json:"media,omitempty"`
	StoredAt int64        `json:"stored_at"`
}

type quoteDisk struct {
	Version int          `json:"version"`
	Entries []quoteEntry `json:"entries"`
}

// quoteStore is safe for concurrent receivers and senders in the extension's single polling process.
// Disk failures only affect persistence; chat continues.
type quoteStore struct {
	mu      sync.Mutex
	path    string
	now     func() time.Time
	policy  quotePolicy
	entries map[string]quoteEntry
	bytes   int
}

func openQuoteStore(dir string, now func() time.Time) *quoteStore {
	return openQuoteStoreWithPolicy(dir, now, defaultQuotePolicy)
}

func openQuoteStoreWithPolicy(dir string, now func() time.Time, policy quotePolicy) *quoteStore {
	if now == nil {
		now = time.Now
	}
	s := &quoteStore{now: now, policy: policy, entries: map[string]quoteEntry{}}
	if dir == "" {
		return s
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		log.Printf("wechat: preparing the quote cache: %v", err)
		return s
	}
	if err := os.Chmod(dir, 0700); err != nil {
		log.Printf("wechat: protecting the quote cache directory: %v", err)
	}
	s.path = filepath.Join(dir, quoteFileName)
	data, err := readQuoteFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return s
	}
	if err != nil {
		log.Printf("wechat: reading the quote cache, starting empty: %v", err)
		return s
	}
	var disk quoteDisk
	if err := json.Unmarshal(data, &disk); err != nil || disk.Version != quoteFileVersion {
		log.Printf("wechat: the quote cache is invalid, starting empty")
		return s
	}
	for _, entry := range disk.Entries {
		if !validQuoteEntry(entry) {
			continue
		}
		entry.ID = strings.TrimSpace(entry.ID)
		entry.Text = strings.TrimSpace(entry.Text)
		entry.Media = cleanQuoteMedia(entry.Media)
		s.entries[entry.ID] = entry
	}
	s.recomputeBytesLocked()
	if s.pruneLocked() {
		s.saveLocked()
	}
	return s
}

func readQuoteFile(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, quoteFileReadMost+1))
	if err != nil {
		return nil, err
	}
	if len(data) > quoteFileReadMost {
		return nil, errors.New("quote cache file is too large")
	}
	return data, nil
}

func validQuoteEntry(entry quoteEntry) bool {
	id := strings.TrimSpace(entry.ID)
	return id != "" && len(id) <= quoteIDMost && entry.StoredAt > 0 && len(entry.Media) <= 20 &&
		(entry.Text != "" || len(entry.Media) > 0)
}

func cleanQuoteMedia(media []quoteMedia) []quoteMedia {
	if len(media) == 0 {
		return nil
	}
	out := make([]quoteMedia, 0, len(media))
	for _, md := range media {
		md.Kind = strings.TrimSpace(md.Kind)
		md.Name = strings.TrimSpace(md.Name)
		if md.Size < 0 {
			md.Size = 0
		}
		if md.Kind != "" {
			out = append(out, md)
		}
	}
	return out
}

func (s *quoteStore) put(id, text string, media []quoteMedia) {
	id, text = strings.TrimSpace(id), strings.TrimSpace(text)
	media = cleanQuoteMedia(media)
	if id == "" || len(id) > quoteIDMost || text == "" && len(media) == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	changed := s.pruneLocked()
	entry := quoteEntry{ID: id, Text: text, Media: media, StoredAt: s.now().UnixMilli()}
	size := quoteEntrySize(entry)
	if size > s.policy.maxBytes {
		if changed {
			s.saveLocked()
		}
		return
	}
	if old, ok := s.entries[id]; ok {
		s.bytes -= quoteEntrySize(old)
	}
	s.entries[id] = entry
	s.bytes += size
	s.pruneLocked()
	s.saveLocked()
}

func (s *quoteStore) reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries = map[string]quoteEntry{}
	s.bytes = 0
	s.saveLocked()
}

func (s *quoteStore) lookup(id string) string {
	id = strings.TrimSpace(id)
	if id == "" {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pruneLocked() {
		s.saveLocked()
	}
	entry, ok := s.entries[id]
	if !ok {
		return ""
	}
	parts := make([]string, 0, 1+len(entry.Media))
	if entry.Text != "" {
		parts = append(parts, entry.Text)
	}
	for _, md := range entry.Media {
		parts = append(parts, quoteMediaSummary(md))
	}
	return strings.Join(parts, "\n")
}

func quoteMediaSummary(md quoteMedia) string {
	label := "媒体"
	switch md.Kind {
	case "image":
		label = "图片"
	case "file":
		label = "文件"
	case "video":
		label = "视频"
	case "voice":
		label = "语音"
	}
	if md.Name != "" {
		label += ": " + md.Name
	}
	if md.Size > 0 {
		label += " (" + strconv.FormatInt(md.Size, 10) + " bytes)"
	}
	return "[" + label + "]"
}

// pruneLocked expires old content and evicts the oldest entries until both limits hold. It reports whether disk changed.
func (s *quoteStore) pruneLocked() bool {
	now := s.now()
	changed := false
	for id, entry := range s.entries {
		if entry.StoredAt > now.UnixMilli() {
			entry.StoredAt = now.UnixMilli()
			changed = true
		}
		age := now.Sub(time.UnixMilli(entry.StoredAt))
		if entry.Text != "" && age >= s.policy.textTTL {
			entry.Text = ""
			changed = true
		}
		if len(entry.Media) > 0 && age >= s.policy.mediaTTL {
			entry.Media = nil
			changed = true
		}
		if entry.Text == "" && len(entry.Media) == 0 {
			delete(s.entries, id)
			changed = true
			continue
		}
		s.entries[id] = entry
	}
	if changed {
		s.recomputeBytesLocked()
	}
	ordered := make([]quoteEntry, 0, len(s.entries))
	for _, entry := range s.entries {
		ordered = append(ordered, entry)
	}
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].StoredAt == ordered[j].StoredAt {
			return ordered[i].ID < ordered[j].ID
		}
		return ordered[i].StoredAt < ordered[j].StoredAt
	})
	for _, entry := range ordered {
		if len(s.entries) <= s.policy.maxEntries && s.bytes <= s.policy.maxBytes {
			break
		}
		if _, ok := s.entries[entry.ID]; !ok {
			continue
		}
		delete(s.entries, entry.ID)
		s.bytes -= quoteEntrySize(entry)
		changed = true
	}
	return changed
}

func quoteEntrySize(entry quoteEntry) int {
	encoded, _ := json.Marshal(entry)
	return len(encoded) + 1 // the entry and its comma in the enclosing JSON array
}

func (s *quoteStore) recomputeBytesLocked() {
	s.bytes = 0
	for _, entry := range s.entries {
		s.bytes += quoteEntrySize(entry)
	}
}

// saveLocked writes a complete replacement beside the old file, then renames it. Readers see the old or new file.
func (s *quoteStore) saveLocked() {
	if s.path == "" {
		return
	}
	ordered := make([]quoteEntry, 0, len(s.entries))
	for _, entry := range s.entries {
		ordered = append(ordered, entry)
	}
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].StoredAt == ordered[j].StoredAt {
			return ordered[i].ID < ordered[j].ID
		}
		return ordered[i].StoredAt < ordered[j].StoredAt
	})
	data, err := json.Marshal(quoteDisk{Version: quoteFileVersion, Entries: ordered})
	if err != nil {
		log.Printf("wechat: encoding the quote cache: %v", err)
		return
	}
	f, err := os.CreateTemp(filepath.Dir(s.path), ".quote-*")
	if err != nil {
		log.Printf("wechat: creating the quote cache: %v", err)
		return
	}
	name := f.Name()
	defer os.Remove(name)
	if err := f.Chmod(0600); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(name, s.path)
	}
	if err != nil {
		log.Printf("wechat: saving the quote cache: %v", err)
		return
	}
	if d, err := os.Open(filepath.Dir(s.path)); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
}
