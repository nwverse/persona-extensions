package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"
)

type record struct {
	ID string    `json:"id"`
	At time.Time `json:"at"`
	ML int       `json:"ml"`
}
type pending struct {
	At       time.Time `json:"at"`
	Notice   notice    `json:"notice"`
	Attempts int       `json:"attempts"`
	RetryAt  time.Time `json:"retry_at"`
}
type state struct {
	Zone         string               `json:"zone,omitempty"`
	Records      []record             `json:"records"`
	Sequence     uint64               `json:"sequence"`
	LastReminder time.Time            `json:"last_reminder,omitzero"`
	Snooze       time.Time            `json:"snooze,omitzero"`
	Pending      *pending             `json:"pending,omitempty"`
	Reason       string               `json:"reason,omitempty"`
	Seen         map[string]time.Time `json:"seen,omitempty"`
}

func (s state) clone() state {
	s.Records = slices.Clone(s.Records)
	seen := map[string]time.Time{}
	for k, v := range s.Seen {
		seen[k] = v
	}
	s.Seen = seen
	if s.Pending != nil {
		p := *s.Pending
		s.Pending = &p
	}
	return s
}

func (s *state) prune(now time.Time) {
	l := zone(s.Zone)
	cut := at(now.In(l), 0).AddDate(0, 0, -399)
	s.Records = slices.DeleteFunc(s.Records, func(r record) bool { return r.At.Before(cut) })
	for k, t := range s.Seen {
		if !t.After(now.Add(-24 * time.Hour)) {
			delete(s.Seen, k)
		}
	}
}

func load(path string, now time.Time) (state, error) {
	var s state
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	err = json.Unmarshal(b, &s)
	if err == nil {
		for _, r := range s.Records {
			if r.ID == "" || r.At.IsZero() || r.ML < 1 || r.ML > 3000 {
				err = errors.New("invalid record")
				break
			}
		}
		if p := s.Pending; p != nil && (p.At.IsZero() || p.RetryAt.IsZero() || p.Attempts < 1 || p.Attempts > 3 || p.Notice.UI != "water" || p.Notice.ID != "water-reminder") {
			err = errors.New("invalid pending reminder")
		}
	}
	if err != nil {
		if err := os.Chmod(path, 0600); err != nil {
			return state{}, err
		}
		if err := os.Rename(path, fmt.Sprintf("%s.corrupt-%d", path, now.UnixNano())); err != nil {
			return state{}, err
		}
		return state{}, nil
	}
	s.prune(now)
	return s, nil
}

// save replaces a private file atomically; a failed write leaves the prior state intact.
func save(path string, s state) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".water-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(b); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(f.Name(), path)
}
