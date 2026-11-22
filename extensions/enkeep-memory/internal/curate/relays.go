package curate

import (
	"context"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/entry"
	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/hubapi"
	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/norm"
	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/relay"
	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/sources"
	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/speaker"
)

// Speakers in three values (design v2 §4.2, 0.2.0-beta.38): a task prompt or a relay Persona typed into an agent
// session is not the user's, but the user's own words it relays are. The sources write each such claim as a line of
// its own (sources.SpeakerRelayCand); here, once the day's units are all read, a claim is kept as the user's words
// when what the user really said bears it out: one of the day's own lines of the user (speaker norm.Me, not itself
// relayed), or a by=user entry of the hub's ledger. A kept claim becomes the user's line with relay.RelayedMark; the
// rest is dropped: the relay's text stays in the session as Persona's line, context only, never guessed to be the
// user's.

// relayMinRunes: a claim this short is verified only by a whole line of the user's saying exactly it; a longer one
// may be part of a line.
const relayMinRunes = 4

// verifyRelays settles every relayed claim of the day's units.
func (c *Curator) verifyRelays(ctx context.Context, units []Unit) {
	var own []string
	claims := 0
	for _, u := range units {
		for _, l := range u.Lines {
			switch {
			case l.Speaker == sources.SpeakerRelayCand:
				claims++
			case l.Speaker == norm.Me && relay.Three(l.Speaker, l.Body, norm.Me) == relay.WhoUser:
				if q := norm.Q(l.Body); q != "" {
					own = append(own, q)
				}
			}
		}
	}
	if claims == 0 {
		return
	}
	for _, q := range c.ledgerQuotes(ctx) {
		if q = norm.Q(q); q != "" {
			own = append(own, q)
		}
	}
	for i := range units {
		u := &units[i]
		kept := u.Lines[:0]
		for _, l := range u.Lines {
			if l.Speaker != sources.SpeakerRelayCand {
				kept = append(kept, l)
				continue
			}
			if !relayVerified(l.Body, own) {
				continue
			}
			l.Speaker, l.Body = norm.Me, relay.RelayedMark+l.Body
			kept = append(kept, l)
			if u.Who == nil {
				u.Who, u.WhoWhy = map[int]string{}, map[int]string{}
			}
			u.Who[l.N], u.WhoWhy[l.N] = entry.WhoSelf, speaker.WhyRelayed
		}
		u.Lines = kept
	}
}

// relayVerified: the claim is one of the user's own lines, or (when long enough to tell) part of one.
func relayVerified(claim string, own []string) bool {
	q := norm.Q(claim)
	if q == "" {
		return false
	}
	short := utf8.RuneCountInString(q) < relayMinRunes
	for _, o := range own {
		if o == q || !short && strings.Contains(o, q) {
			return true
		}
	}
	return false
}

// ledgerQuotes are the quotes of the hub's ledger entries by the user (design v2 §5.1, appendix D I1): read when the
// hub has the ledger, skipped without a word when it does not (an older hub, no route, no scope).
func (c *Curator) ledgerQuotes(ctx context.Context) []string {
	lr, ok := c.Hub.(hubapi.LedgerReader)
	if !ok || lr == nil {
		return nil
	}
	whoInit.Lock()
	if c.ledger == nil {
		c.ledger = &ledgerCache{}
	}
	lc := c.ledger
	whoInit.Unlock()
	lc.mu.Lock()
	defer lc.mu.Unlock()
	if !lc.at.IsZero() && time.Since(lc.at) < ledgerFresh {
		return lc.quotes
	}
	lc.at, lc.quotes = time.Now(), nil
	es, err := lr.Ledger(ctx, hubapi.LedgerQuery{By: "user"})
	if err != nil {
		return nil
	}
	for _, e := range es {
		if e.By == "user" && e.Quote != "" && !e.Superseded() {
			lc.quotes = append(lc.quotes, e.Quote)
		}
	}
	return lc.quotes
}

type ledgerCache struct {
	mu     sync.Mutex
	at     time.Time
	quotes []string
}

// ledgerFresh is how long one read of the ledger serves the day's units (a pass reads many days' units).
const ledgerFresh = 10 * time.Minute
