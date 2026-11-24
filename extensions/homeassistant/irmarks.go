package main

import (
	"context"
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/nwverse/persona/sdk/go/wire"
)

// IR marks (#774): an IR remote's switch shows what was sent last, and it is wrong when the device was switched off
// some other way (its own remote, a code it never received). 标记为已关 in the tile's menu sets it off without
// sending anything, so a TV whose power code toggles is not turned on to correct the page. A mark is when it was made,
// by the remote's 开机 button; the switch is on only when 开机 was pressed after both 关机 and the mark. Marks are kept
// in the extension's data directory (ir-marks.json), only in memory when it has none.

const irMarksFile = "ir-marks.json"

type irMarks struct {
	mu   sync.Mutex
	file string
	at   map[string]time.Time
}

func (m *irMarks) load(dir string, logf func(string, ...any)) {
	if dir == "" {
		return
	}
	file := filepath.Join(dir, irMarksFile)
	at, _ := readState[map[string]time.Time](file, logf)
	m.mu.Lock()
	m.file, m.at = file, at
	m.mu.Unlock()
}

func (m *irMarks) get(eid string) time.Time {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.at[eid]
}

// mark keeps eid's mark at now, and saves the marks when there is a file.
func (m *irMarks) mark(eid string, now time.Time) error {
	m.mu.Lock()
	if m.at == nil {
		m.at = map[string]time.Time{}
	}
	m.at[eid] = now
	file, at := m.file, maps.Clone(m.at)
	m.mu.Unlock()
	if file == "" {
		return nil
	}
	b, _ := json.MarshalIndent(at, "", "  ")
	tmp := file + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, file)
}

// irMarkAction marks the remote whose 开机 is eid off (markRemoteOff): irmark:<entity>, or irmark with it in the values.
func irMarkAction(eid string) *wire.Action {
	if id := "irmark:" + eid; len(id) <= idMost {
		return &wire.Action{ID: id}
	}
	return &wire.Action{ID: "irmark", Values: map[string]string{"entity": eid}}
}

// markRemoteOff marks a remote off without pressing anything: only a remote the page draws a switch for.
func (a *app) markRemoteOff(ctx context.Context, eid string) (string, error) {
	if !entityRE.MatchString(eid) {
		return "", errUnknownOp
	}
	if _, err := a.switchRemote(ctx, eid); err != nil {
		return "", err
	}
	if err := a.marks.mark(eid, time.Now()); err != nil {
		return "", err
	}
	return "已标记为关，没有发送信号", nil
}
