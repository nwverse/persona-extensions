package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// ledger is the extension's own record in its data directory: today's allowance used, the orders it placed, and each
// write's result by the hub's request_id (so a repeated actions.do answers the first result and sends nothing).
type ledger struct {
	mu   sync.Mutex
	path string
	st   ledgerState
}

type ledgerState struct {
	Day      string                 `json:"day"`
	UsedUSDT float64                `json:"used_usdt"`
	Orders   int                    `json:"orders"`
	Results  map[string]writeResult `json:"results"`
	Placed   []placedOrder          `json:"placed"`
	Notified map[string]string      `json:"notified,omitempty"`  // order key → last status told
	Snap     map[string]float64     `json:"snapshots,omitempty"` // day → total USDT
}

type writeResult struct {
	Text       string    `json:"text"`
	MessageRef string    `json:"message_ref,omitempty"`
	At         time.Time `json:"at"`
}

// placedOrder is an order the extension placed, followed until it is done.
type placedOrder struct {
	Market    string    `json:"market"`
	Symbol    string    `json:"symbol"`
	OrderID   int64     `json:"order_id"`
	ClientID  string    `json:"client_id"`
	Side      string    `json:"side"`
	Type      string    `json:"type"`
	Quantity  string    `json:"quantity,omitempty"`
	Price     string    `json:"price,omitempty"`
	Notional  float64   `json:"notional"`
	Reducing  bool      `json:"reducing,omitempty"` // reduce-only / closing order (never counts toward USDT allowance)
	Counts    bool      `json:"counts,omitempty"`   // currently counted against the USDT allowance
	Status    string    `json:"status"`
	Executed  string    `json:"executed,omitempty"`
	FillPrice string    `json:"fill_price,omitempty"` // paper fills only
	At        time.Time `json:"at"`
	RequestID string    `json:"request_id"`
}

// dayIn is the guard day the order was placed on.
func (o placedOrder) dayIn(a *app) string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.cfg.dayKey(o.At)
}

func (o placedOrder) key() string { return o.Market + ":" + o.Symbol + ":" + itoa(o.OrderID) }

// open says whether the order may still change.
func (o placedOrder) open() bool {
	switch o.Status {
	case "NEW", "PARTIALLY_FILLED", "PENDING_NEW", "PENDING_SUBMIT", "UNKNOWN", "":
		return true
	}
	return false
}

func openLedger(dir string) (*ledger, error) {
	return openNamedLedger(dir, "ledger.json")
}

func openNamedLedger(dir, name string) (*ledger, error) {
	l := &ledger{st: emptyState()}
	if dir == "" {
		return l, nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	l.path = filepath.Join(dir, name)
	b, err := os.ReadFile(l.path)
	if errors.Is(err, os.ErrNotExist) {
		return l, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, &l.st); err != nil {
		return nil, errors.New(name + " is damaged; move it away to start over")
	}
	normalizeLedgerState(&l.st)
	return l, nil
}

func emptyState() ledgerState {
	return ledgerState{Results: map[string]writeResult{}, Notified: map[string]string{}, Snap: map[string]float64{}}
}

func normalizeLedgerState(s *ledgerState) {
	if s.Results == nil {
		s.Results = map[string]writeResult{}
	}
	if s.Notified == nil {
		s.Notified = map[string]string{}
	}
	if s.Snap == nil {
		s.Snap = map[string]float64{}
	}
}

// roll starts a new day's allowance (caller holds mu).
// Orders still open from earlier days (or of unknown outcome) carry their amount into the new day, so a day's limit
// cannot be stretched by leaving big limit orders open overnight.
func (l *ledger) roll(day string) {
	if l.st.Day != day {
		carry := 0.0
		for _, o := range l.st.Placed {
			if o.Counts && o.open() {
				carry += o.Notional
			}
		}
		l.st.Day, l.st.UsedUSDT, l.st.Orders = day, carry, 0
	}
}

// usage is today's allowance used.
func (l *ledger) usage(day string) (used float64, orders int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.roll(day)
	return l.st.UsedUSDT, l.st.Orders
}

// result is the first answer of a write by request id.
func (l *ledger) result(requestID string) (writeResult, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	r, ok := l.st.Results[requestID]
	return r, ok
}

// placedByRequest finds a recorded order by the hub's request id.
func (l *ledger) placedByRequest(requestID string) (placedOrder, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, o := range l.st.Placed {
		if o.RequestID == requestID {
			return o, true
		}
	}
	return placedOrder{}, false
}

// update changes the state under the lock and saves it; fn returning an error saves nothing.
func (l *ledger) update(fn func(st *ledgerState) error) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	before, err := json.Marshal(l.st)
	if err != nil {
		return err
	}
	err = fn(&l.st)
	if err == nil {
		l.prune(time.Now())
		err = l.save()
	}
	if err != nil {
		l.st = ledgerState{}
		_ = json.Unmarshal(before, &l.st)
		normalizeLedgerState(&l.st)
	}
	return err
}

// prune keeps 30 days of results, the last 300 orders and 120 days of snapshots.
func (l *ledger) prune(now time.Time) {
	for k, r := range l.st.Results {
		if now.Sub(r.At) > 30*24*time.Hour {
			delete(l.st.Results, k)
		}
	}
	if n := len(l.st.Placed); n > 300 {
		l.st.Placed = append([]placedOrder(nil), l.st.Placed[n-300:]...)
	}
	if len(l.st.Snap) > 120 {
		days := make([]string, 0, len(l.st.Snap))
		for d := range l.st.Snap {
			days = append(days, d)
		}
		sort.Strings(days)
		for _, d := range days[:len(days)-120] {
			delete(l.st.Snap, d)
		}
	}
}

func (l *ledger) save() error {
	if l.path == "" {
		return nil
	}
	b, err := json.MarshalIndent(l.st, "", " ")
	if err != nil {
		return err
	}
	tmp := l.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, l.path)
}

// snapshot reads a copy of the state.
func (l *ledger) snapshot() ledgerState {
	l.mu.Lock()
	defer l.mu.Unlock()
	b, _ := json.Marshal(l.st)
	var out ledgerState
	_ = json.Unmarshal(b, &out)
	return out
}

func itoa(n int64) string {
	b, _ := json.Marshal(n)
	return string(b)
}
