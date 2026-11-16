package main

import (
	"context"
	"fmt"
	"time"

	"github.com/nwverse/persona/sdk/go/ext"
	"github.com/nwverse/persona/sdk/go/wire"
)

// logDrink is shared by page buttons, notification buttons and shortcuts.
// The caller holds app.mu and persists the change through update.
func logDrink(s *state, now time.Time, ml int) {
	s.Sequence++
	s.Records = append(s.Records, record{ID: fmt.Sprintf("d%d", s.Sequence), At: now, ML: ml})
	s.Snooze = time.Time{}
	s.Pending = nil
}

func (a *app) shortcut(_ context.Context, r *ext.ShortcutRequest) (*wire.ShortcutResult, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := a.now()
	if err := a.remember(&ext.UIRequest{UIParams: wire.UIParams{TZ: r.TZ}}, now); err != nil {
		return nil, err
	}
	ml := a.cfg.Cup
	if v, exists := r.Values["amount"]; exists {
		var ok bool
		ml, ok = amount(v)
		if !ok {
			return &wire.ShortcutResult{Text: "请填 1 到 3000 之间的毫升数"}, nil
		}
	}
	if err := a.update(now, func(s *state) { logDrink(s, now, ml) }); err != nil {
		return nil, err
	}
	n := total(a.s, now, a.s.location())
	text := fmt.Sprintf("已记 %d 毫升，今天 %d / %d", ml, n, a.cfg.Goal)
	if n >= a.cfg.Goal {
		text += "，今天的目标达成了"
	}
	view, err := a.widget(wire.WidgetFamilySmall, now)
	if err != nil {
		return nil, err
	}
	return &wire.ShortcutResult{Text: text, View: view}, nil
}
