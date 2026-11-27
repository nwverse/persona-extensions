package main

import (
	"cmp"
	"context"
	"encoding/json"
	"slices"
	"sync"
	"time"

	"github.com/nwverse/persona/sdk/go/wire"
)

const (
	playtimeRecapName = "steam_playtime_recap"
	defaultRecapTop   = 5
	maxRecapTop       = 20

	problemRecapStore problem = "playtime_recap_history_unavailable"
)

type recapPeriod string

const (
	recapMonth   recapPeriod = "month"
	recapQuarter recapPeriod = "quarter"
	recapYear    recapPeriod = "year"
	recapAll     recapPeriod = "all"
)

var recapPeriods = [...]recapPeriod{recapMonth, recapQuarter, recapYear, recapAll}

var playtimeRecapTool = wire.ConnectorTool{
	Name:        playtimeRecapName,
	Description: "Review Steam playtime and backlog progress for the current UTC month, quarter, year, or all locally observed history. With no comparable snapshot, shows the library, last two weeks, and local annotations.",
	InputSchema: json.RawMessage(`{"type":"object","properties":{"period":{"type":"string","enum":["month","quarter","year","all"],"default":"month"},"top_n":{"type":"integer","minimum":1,"maximum":20,"default":5}}}`),
}

func monthStart(at time.Time) time.Time {
	y, m, _ := at.UTC().Date()
	return time.Date(y, m, 1, 0, 0, 0, 0, time.UTC)
}

func (p recapPeriod) start(at time.Time) time.Time {
	start := monthStart(at)
	switch p {
	case recapQuarter:
		return start.AddDate(0, -((int(start.Month()) - 1) % 3), 0)
	case recapYear:
		return time.Date(start.Year(), time.January, 1, 0, 0, 0, 0, time.UTC)
	case recapAll:
		return time.Time{}
	default:
		return start
	}
}

type recapGame struct {
	AppID   uint32 `json:"appid"`
	Name    string `json:"name"`
	Minutes uint32 `json:"minutes"`
}

type recapRecent struct {
	Playtime  uint64      `json:"playtime_2weeks"`
	TopGames  []recapGame `json:"top_games"`
	Truncated bool        `json:"truncated"`
}

type recapProgress struct {
	CountChanges map[string]int `json:"count_changes"`
	MovedInto    map[string]int `json:"moved_into"`
	AtStart      int            `json:"backlog_at_start"`
	Consumed     int            `json:"backlog_consumed"`
	Rate         *float64       `json:"backlog_consumption_rate"`
}

type recapDelta struct {
	From            time.Time      `json:"from"`
	To              time.Time      `json:"to"`
	PlaytimeMinutes uint64         `json:"playtime_minutes"`
	NewGames        int            `json:"new_games"`
	TopGames        []recapGame    `json:"top_games"`
	Progress        *recapProgress `json:"progress,omitempty"`
}

type playtimeRecap struct {
	Period      recapPeriod           `json:"period"`
	Available   bool                  `json:"available"`
	Library     *PlaytimeSnapshot     `json:"library,omitempty"`
	Truncated   bool                  `json:"library_truncated,omitempty"`
	Recent      *recapRecent          `json:"recent,omitempty"`
	Annotations map[BacklogStatus]int `json:"annotation_counts,omitempty"`
	Delta       *recapDelta           `json:"delta,omitempty"`
	Hint        string                `json:"hint,omitempty"`
	Warnings    []toolWarning         `json:"warnings,omitempty"`
}

func (r *playtimeRecap) warn(t *text, dataset string, issue problem, stale bool) {
	if issue != "" {
		r.Warnings = append(r.Warnings, toolWarning{dataset, issue, t.message(issue, stale), stale})
	}
}

func (a *app) callPlaytimeRecap(ctx context.Context, raw json.RawMessage) (playtimeRecap, error) {
	var args struct {
		Period *recapPeriod `json:"period"`
		Top    *int         `json:"top_n"`
	}
	t := textFor(a.locale)
	if !decodeArgs(raw, &args) || (args.Period != nil && !slices.Contains(recapPeriods[:], *args.Period)) ||
		(args.Top != nil && (*args.Top < 1 || *args.Top > maxRecapTop)) {
		return playtimeRecap{}, invalid(t.recap.invalidArgs)
	}
	period, top := recapMonth, defaultRecapTop
	if args.Period != nil {
		period = *args.Period
	}
	if args.Top != nil {
		top = *args.Top
	}
	d, ctx, cancel := a.readContext(ctx)
	defer cancel()
	result := a.readPlaytimeRecap(ctx, d, t, period, top, false)
	if issue := d.sessionIssue(); issue != "" {
		result = playtimeRecap{Period: period}
		result.warn(t, "playtime_recap", issue, false)
	}
	return result, nil
}

func (a *app) readPlaytimeRecap(ctx context.Context, d *steamData, t *text, period recapPeriod, top int, refresh bool) playtimeRecap {
	result := playtimeRecap{Period: period}
	var account reading[player]
	if d.session == nil {
		account = d.account(ctx, profileTTL, refresh)
		result.warn(t, "profile", account.Issue, account.Stale)
	}
	id, issue := d.readID(account)
	if issue != "" {
		if issue != account.Issue {
			result.warn(t, "library", issue, false)
		}
		return result
	}
	var owned, recent reading[gameList]
	var reads sync.WaitGroup
	reads.Go(func() {
		owned = d.ownedGames(ctx, account, refresh)
		// A still-fresh library cache can cross midnight. Get a new observation for the new day.
		if !refresh && owned.Data != nil && owned.Issue == "" && !utcDay(owned.At).Equal(utcDay(d.library.now())) {
			owned = d.ownedGames(ctx, account, true)
		}
	})
	reads.Go(func() { recent = d.recentGames(ctx, account, refresh) })
	reads.Wait()
	result.warn(t, "library", owned.Issue, owned.Stale)
	result.warn(t, "recent_games", recent.Issue, recent.Stale)
	if r := recent.Data; r != nil {
		result.Available = true
		result.Recent = &recapRecent{Playtime: r.Playtime2Weeks, TopGames: make([]recapGame, 0, top), Truncated: r.Truncated}
		for _, g := range r.Games {
			if g.Playtime2Weeks == 0 || len(result.Recent.TopGames) == top {
				break // Steam's dedicated recent list is sorted by recent minutes.
			}
			result.Recent.TopGames = append(result.Recent.TopGames, recapGame{g.AppID, t.gameName(g.AppID, g.Name), g.Playtime2Weeks})
		}
	}
	if owned.Data == nil {
		return result
	}
	library := snapshotPlaytime(owned.Data, owned.At)
	result.Library, result.Available, result.Truncated = &library, true, owned.Data.Truncated
	marks, backlogIssue := a.backlogEntries(d, account)
	result.warn(t, "backlog", backlogIssue, false)
	current := recapSnapshot{At: owned.At, Minutes: make(map[uint32]uint32, len(owned.Data.Games))}
	if backlogIssue == "" {
		current.Statuses = make(map[uint32]BacklogStatus)
		result.Annotations = make(map[BacklogStatus]int, len(backlogStatuses))
		for _, status := range backlogStatuses {
			result.Annotations[status] = 0
		}
	}
	for _, g := range owned.Data.Games {
		current.Minutes[g.AppID] = g.PlaytimeForever
		if status := marks[g.AppID].Status; slices.Contains(backlogStatuses[:], status) {
			current.Statuses[g.AppID] = status
			result.Annotations[status]++
		}
	}
	result.Hint = t.recap.noBaseline
	if result.Truncated || owned.Issue != "" || account.Stale || !validSnapshotTime(current.At) || ctx.Err() != nil {
		return result
	}
	var history []recapSnapshot
	var historyErr error
	d.recordSnapshot(func() { history, historyErr = d.stores.playtime.observeRecap(id, current) })
	if historyErr != nil {
		result.warn(t, "playtime_history", problemRecapStore, false)
	}
	start, today := period.start(current.At), utcDay(current.At)
	for _, prior := range history {
		if prior.Minutes != nil && !prior.At.Before(start) && prior.At.Before(today) {
			delta := compareRecap(prior, current, owned.Data.Games, t, top)
			result.Delta, result.Hint = &delta, ""
			break
		}
	}
	return result
}

func compareRecap(prior, current recapSnapshot, games []game, t *text, top int) recapDelta {
	out := recapDelta{From: prior.At, To: current.At, TopGames: make([]recapGame, 0)}
	for _, g := range games {
		before, existed := prior.Minutes[g.AppID]
		if !existed {
			out.NewGames++
		}
		if g.PlaytimeForever > before {
			minutes := g.PlaytimeForever - before
			out.PlaytimeMinutes += uint64(minutes)
			out.TopGames = append(out.TopGames, recapGame{g.AppID, t.gameName(g.AppID, g.Name), minutes})
		}
	}
	slices.SortFunc(out.TopGames, func(a, b recapGame) int {
		if byTime := cmp.Compare(b.Minutes, a.Minutes); byTime != 0 {
			return byTime
		}
		return cmp.Compare(a.AppID, b.AppID)
	})
	out.TopGames = out.TopGames[:min(top, len(out.TopGames))]
	if prior.Statuses != nil && current.Statuses != nil {
		out.Progress = compareRecapProgress(prior, current)
	}
	return out
}

const recapBacklog = "backlog"

var recapBuckets = [...]string{recapBacklog, string(BacklogPlaying), string(BacklogCompleted), string(BacklogDropped)}

func (s recapSnapshot) bucket(id uint32) string {
	switch status := s.Statuses[id]; status {
	case BacklogPlaying, BacklogCompleted, BacklogDropped:
		return string(status)
	case BacklogQueued, BacklogPaused, BacklogUnplayed:
		return recapBacklog
	}
	if s.Minutes[id] == 0 {
		return recapBacklog
	}
	return ""
}

func compareRecapProgress(prior, current recapSnapshot) *recapProgress {
	out := &recapProgress{CountChanges: make(map[string]int), MovedInto: make(map[string]int)}
	for _, key := range recapBuckets {
		out.CountChanges[key], out.MovedInto[key] = 0, 0
	}
	for id, minutes := range prior.Minutes {
		bucket := prior.bucket(id)
		if bucket != "" {
			out.CountChanges[bucket]--
		}
		if bucket == recapBacklog {
			out.AtStart++
			if after, stillOwned := current.Minutes[id]; stillOwned {
				next := current.bucket(id)
				if next == string(BacklogPlaying) || next == string(BacklogCompleted) || after > minutes {
					out.Consumed++
				}
			}
		}
	}
	for id := range current.Minutes {
		if bucket := current.bucket(id); bucket != "" {
			out.CountChanges[bucket]++
			if _, existed := prior.Minutes[id]; !existed || prior.bucket(id) != bucket {
				out.MovedInto[bucket]++
			}
		}
	}
	if out.AtStart > 0 {
		rate := float64(out.Consumed) / float64(out.AtStart)
		out.Rate = &rate
	}
	return out
}
