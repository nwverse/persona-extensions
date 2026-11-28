package generic

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/nwverse/persona-extensions/extensions/cloud-hosts/internal/core"
)

// stateFileName is the state file in Config.DataDir.
const stateFileName = "custom-hosts.json"

// aliveEvery is how stale the recorded last successful probe may get before a new one is written to the file.
const aliveEvery = time.Hour

// hostState is what the extension itself knows of a host beyond its configuration: dates, and the request id of
// the last renewal applied; no address or URL.
type hostState struct {
	Expires     time.Time `json:"expires,omitzero"`
	Renews      time.Time `json:"renews,omitzero"`
	LastAlive   time.Time `json:"last_alive,omitzero"`
	LastRenewal string    `json:"last_renewal_request,omitempty"`
}

type stateFile struct {
	Version int                  `json:"version"`
	Hosts   map[string]hostState `json:"hosts"`
}

// store keeps the hosts' state in memory and, with a directory, in the state file.
type store struct {
	mu    sync.Mutex
	path  string // "" = memory only
	hosts map[string]hostState
	saved map[string]time.Time // LastAlive as last written
	due   bool                 // a LastAlive to keep is an hour newer than the written one: flush writes the file
}

// openStore reads dir's state file. A missing file is an empty state; so is one that cannot be read as JSON (the
// configured dates still stand, and the next renewal writes it anew).
func openStore(dir string) *store {
	s := &store{hosts: map[string]hostState{}, saved: map[string]time.Time{}}
	if dir == "" {
		return s
	}
	s.path = filepath.Join(dir, stateFileName)
	raw, err := os.ReadFile(s.path)
	if err != nil {
		return s
	}
	var f stateFile
	if json.Unmarshal(raw, &f) != nil {
		return s
	}
	for name, st := range f.Hosts {
		if nameRE.MatchString(name) {
			if len(st.LastRenewal) > 128 {
				st.LastRenewal = ""
			}
			s.hosts[name] = st
			s.saved[name] = st.LastAlive
		}
	}
	return s
}

func (s *store) get(name string) hostState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hosts[name]
}

// alive records a successful probe in memory. When keep and the written one is an hour old, the next flush writes
// the file: a listing of many hosts writes it once, after its probes, not once per host.
func (s *store) alive(name string, at time.Time, keep bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.hosts[name]
	if !at.After(st.LastAlive) {
		return
	}
	st.LastAlive = at
	s.hosts[name] = st
	if keep && s.path != "" && at.Sub(s.saved[name]) >= aliveEvery {
		s.due = true
	}
}

// flush writes the file when a successful probe is due to be kept (see alive).
func (s *store) flush() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.due || s.path == "" {
		return
	}
	if s.writeLocked() == nil {
		s.markSavedLocked()
	}
}

// markSavedLocked notes every host's LastAlive as written.
func (s *store) markSavedLocked() {
	s.due = false
	for name, st := range s.hosts {
		s.saved[name] = st.LastAlive
	}
}

// update changes name's state with fn and writes the file. The memory keeps the change even when the file could
// not be written; the error says so.
func (s *store) update(name string, fn func(*hostState)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.hosts[name]
	fn(&st)
	s.hosts[name] = st
	if s.path == "" {
		return nil
	}
	if err := s.writeLocked(); err != nil {
		return err
	}
	s.markSavedLocked()
	return nil
}

// writeLocked writes the file atomically (a temporary file, then a rename), readable by the user only.
func (s *store) writeLocked() error {
	fail := core.Failed("the custom hosts' state file could not be written")
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fail
	}
	raw, err := json.MarshalIndent(stateFile{Version: 1, Hosts: s.hosts}, "", "  ")
	if err != nil {
		return fail
	}
	tmp, err := os.CreateTemp(dir, ".custom-hosts-*.tmp")
	if err != nil {
		return fail
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fail
	}
	if _, err := tmp.Write(append(raw, '\n')); err != nil {
		tmp.Close()
		return fail
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fail
	}
	if err := tmp.Close(); err != nil {
		return fail
	}
	if err := os.Rename(name, s.path); err != nil {
		return fail
	}
	return nil
}
