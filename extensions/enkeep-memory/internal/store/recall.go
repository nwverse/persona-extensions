package store

import (
	"encoding/json"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"sync"
)

// RecallLog keeps enkeep_recall's counts per local day in recall.json beside the state: how often it was asked, how
// often it found nothing, and the hits per facet and status. Never the query or the answer. The last RecallDays days
// are kept. It has its own lock: counting a call never waits for the store's, which a run holds for its whole pass.
type RecallLog struct {
	path string
	mu   sync.Mutex
	days map[string]*RecallDay
}

// RecallFile is the counts' file name in the store's directory.
const RecallFile = "recall.json"

// OpenRecall reads dir's counts. old is the state's counts of 0.2.0-beta.37 (State.Recall): when the file does not
// exist yet they become its content, written at once; the caller then drops them from the state.
func OpenRecall(dir string, old map[string]*RecallDay) *RecallLog {
	l := &RecallLog{path: filepath.Join(dir, RecallFile), days: map[string]*RecallDay{}}
	raw, err := os.ReadFile(l.path)
	switch {
	case err == nil:
		_ = json.Unmarshal(raw, &l.days) // a damaged file only loses counts
		if l.days == nil {
			l.days = map[string]*RecallDay{}
		}
	case errors.Is(err, os.ErrNotExist) && len(old) > 0:
		maps.Copy(l.days, old)
		_ = l.save()
	}
	return l
}

// Count adds one call on day with its hits' facets and statuses (empty when nothing was found) and writes the file.
func (l *RecallLog) Count(day string, facets, statuses []string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	d := l.days[day]
	if d == nil {
		d = &RecallDay{}
		l.days[day] = d
		days := slices.Sorted(maps.Keys(l.days))
		for len(days) > RecallDays {
			delete(l.days, days[0])
			days = days[1:]
		}
	}
	d.Calls++
	if len(facets) == 0 {
		d.Empty++
	}
	for _, f := range facets {
		if d.Facets == nil {
			d.Facets = map[string]int{}
		}
		d.Facets[f]++
	}
	for _, s := range statuses {
		if d.Statuses == nil {
			d.Statuses = map[string]int{}
		}
		d.Statuses[s]++
	}
	return l.save()
}

// Days is a copy of the last n days' counts, oldest first, with their days.
func (l *RecallLog) Days(n int) ([]string, []RecallDay) {
	l.mu.Lock()
	defer l.mu.Unlock()
	keys := make([]string, 0, len(l.days))
	for k := range l.days {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if len(keys) > n {
		keys = keys[len(keys)-n:]
	}
	out := make([]RecallDay, len(keys))
	for i, k := range keys {
		out[i] = *l.days[k]
	}
	return keys, out
}

// save writes the file; the caller holds l.mu (or l is not shared yet).
func (l *RecallLog) save() error {
	b, err := json.Marshal(l.days)
	if err != nil {
		return err
	}
	return writeAtomic(l.path, b)
}
