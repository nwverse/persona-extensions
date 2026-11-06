package x

import (
	"time"

	"github.com/nwverse/persona-extensions/extensions/x/internal/connector"
)

// Overview is what the page's header shows: from memory, nothing read from X.
type Overview struct {
	SignedIn   bool
	Status     connector.Status
	BudgetUsed int
	Budget     int
	LastSync   time.Time
	FeedSource string
	Syncing    bool
}

// Overview is the connector as the page shows it.
func (c *Connector) Overview() Overview {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.budgetLeftLocked()
	return Overview{SignedIn: c.keys.complete(), Status: c.statusLocked(), BudgetUsed: c.state.BudgetUsed, Budget: c.cfg.budget(),
		LastSync: c.lastSyncLocked(), FeedSource: c.cfg.source(), Syncing: c.syncing}
}

// Location is the user's time zone.
func (c *Connector) Location() *time.Location { return c.loc }
