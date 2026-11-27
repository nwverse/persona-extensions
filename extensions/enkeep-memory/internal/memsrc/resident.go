package memsrc

import (
	"slices"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/entry"
	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/export"
	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/norm"
	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/recall"
	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/store"
)

// The standing set (unified design §2.1): the few entries the hub puts in every main-space turn, chosen in three
// buckets within the configured room (default 1400 bytes, 4096 on hubs with an independent standing budget):
//
//   - Bucket 0 (Hot Slot): dynamic 0%–15% (at most HotSlotMaxBytes and HotSlotMax entries) for user-pinned entries
//     and Fast-Path temporary hard contracts (TTL 7 days, retired from the hot slot once L1 absorbs them). Unused
//     hot-slot bytes return to Buckets 1 and 2.
//   - Bucket 1 (Contracts): 80% of the remaining room for L1 collaboration contracts (①②). Pass 1 applies gentle
//     anti-monopoly polarity caps (check <= 45%, act <= 35%, communicate <= 30%, route <= 30%) and non-generic group
//     cap (GroupShare 20%); pass 2 refills leftover bucket room by unified Score so high-leverage cross-scene contracts
//     are never displaced by low-scoring narrow rules.
//   - Bucket 2 (Principles): 20% of the remaining room for L1 cognitive principles (③④⑥⑤), chosen strictly by unified
//     leverage Score with non-generic group cap (GroupShare 20%) rather than forcing low-action dimensions into standing.
//
// Bidirectional L1 spillover: when one L1 main bucket exhausts its qualified L1 candidates with bytes left over (for
// example, few L1 principles), the leftover budget flows to the other L1 main bucket, whose polarity/dimension/group
// caps scale proportionally with its expanded effective budget. L0 fragments never fill leftover L1 room; L0 fallback
// applies only to a main bucket that has zero qualified L1 candidates (cold start).
const (
	ResidentBytes = 1400
	ResidentMost  = 12

	// HotSlotShare, HotSlotMaxBytes, HotSlotMax, HotSlotTTLDays bound Bucket 0 (Hot Slot).
	HotSlotShare    = 0.15
	HotSlotMaxBytes = 600
	HotSlotMax      = 3
	HotSlotTTLDays  = 7

	// ContractShare is Bucket 1's nominal share of the room left after Bucket 0 (80%; Bucket 2 gets 20%).
	ContractShare = 0.80
	// PolarityShare is the default cap for an unspecified contract polarity; specific polarities use polarityShareOf
	// (check <= 45%, act <= 35%, communicate <= 30%, route <= 30%) in pass 1, with pass 2 refilling leftover room
	// by unified Score so high-leverage cross-scene rules are never displaced by low-scoring narrow rules.
	PolarityShare = 0.35
	// GroupShare caps any single non-generic occasion/type class ("<facet>:<type>") within a bucket's effective room.
	GroupShare = 0.20

	// Per-line maximum lengths in bytes by bucket (unified design §2.1): wide enough to keep concrete bans + exceptions intact.
	HotTextMost       = 280
	ContractTextMost  = 300
	PrincipleTextMost = 280
	residentTextMost  = 280

	// sameRule is the bigram Jaccard threshold above which two standing lines are treated as duplicates.
	sameRule = 0.45
	// rankHot is the first hot-slot rank (995..990); rankStand is the first Bucket 1/2 interleaved rank (989 down).
	rankHot   = 995
	rankStand = 989
	// residentOverhead is what the hub adds to each line ("- " and a line end).
	residentOverhead = 3
)

// Bucket names for three-bucket standing selection.
const (
	BucketHot       = "hot"
	BucketContract  = "contract"
	BucketPrinciple = "principle"
)

// Candidate is one entry that may stand: a hot-slot entry (Pinned or Fast-Path), an L1 contract/principle, or a
// cold-start L0 fallback entry.
type Candidate struct {
	ID    string
	Text  string
	Kind  string // entry.KindRules or entry.KindUnderstanding
	Score int
	// Days (independent quotes/evidence count) and Last (newest evidence day) break ties in score.
	Days int
	Last string
	// Bucket is BucketHot, BucketContract, or BucketPrinciple (inferred from Kind when empty).
	Bucket string
	// L1 marks an L1 overview contract or principle (false for hot-slot and cold-start L0 entries).
	L1 bool
	// Pinned marks a user-pinned entry (ranks first inside the hot slot).
	Pinned bool
	// Hard marks a hard contract (ranks ahead of non-hard contracts in Bucket 1).
	Hard bool
	// Polarity is a contract's action direction (act/check/communicate/route).
	Polarity string
	// Facet is the source facet ("interaction", "tools", "judgement", ...), used for the ② tools floor in Bucket 1.
	Facet string
	// Dimension is a principle's cognitive dimension (decision/thinking/reaction/attention/communication).
	Dimension string
	// Group is the non-generic class GroupShare caps ("<facet>:<type>"); "" is uncapped.
	Group string
}

// polarityShareOf returns the pass-1 anti-monopoly polarity cap within Bucket 1 (unified design §2.1):
// verification/boundary rules get the widest headroom (45%), autonomous-push rules 35%, communicate/route 30%.
func polarityShareOf(pol string) float64 {
	switch pol {
	case "check":
		return 0.45
	case "act":
		return 0.35
	case "communicate":
		return 0.30
	case "route":
		return 0.30
	default:
		return PolarityShare
	}
}

// StandingPrefix returns the lightweight semantic tier tag prepended to L1 standing lines (unified design §2.1).
// Contracts carry no pressure tag (avoiding 【硬】-wall over-braking of autonomous rules); principles carry 【原则】.
func StandingPrefix(bucket string, hard bool, polarity string) string {
	if bucket == BucketPrinciple {
		return "【原则】"
	}
	return ""
}

func (c Candidate) bucket() string {
	if c.Bucket != "" {
		return c.Bucket
	}
	if c.Kind == entry.KindRules {
		return BucketContract
	}
	return BucketPrinciple
}

// Resident picks the standing set from cands in the default ResidentBytes room.
func Resident(cands []Candidate) []Candidate {
	return ResidentWithin(cands, ResidentBytes, ResidentMost)
}

// ResidentWithin picks the standing set from cands in total bytes, at most most entries (most <= 0 means no count cap
// beyond HotSlotMax for Bucket 0 and total bytes for Buckets 1 and 2).
func ResidentWithin(cands []Candidate, total, most int) []Candidate {
	if total <= 0 {
		total = ResidentBytes
	}
	var hotPool, cL1, cL0, pL1, pL0 []Candidate
	for _, c := range cands {
		if strings.TrimSpace(c.Text) == "" {
			continue
		}
		switch c.bucket() {
		case BucketHot:
			c.Bucket = BucketHot
			hotPool = append(hotPool, c)
		case BucketContract:
			c.Bucket = BucketContract
			if c.L1 {
				cL1 = append(cL1, c)
			} else {
				cL0 = append(cL0, c)
			}
		default:
			c.Bucket = BucketPrinciple
			if c.L1 {
				pL1 = append(pL1, c)
			} else {
				pL0 = append(pL0, c)
			}
		}
	}

	// Each main bucket uses L1 exclusively once any qualified L1 candidate exists; only a bucket with 0 L1 candidates
	// falls back to cold-start L0 candidates.
	cIsL1 := len(cL1) > 0
	contractPool := cL1
	if !cIsL1 {
		contractPool = cL0
	}
	pIsL1 := len(pL1) > 0
	principlePool := pL1
	if !pIsL1 {
		principlePool = pL0
	}

	sortHot(hotPool)
	sortContracts(contractPool)
	sortPrinciples(principlePool)

	taken := map[string]bool{}
	var allChosen []Candidate
	alike := func(c Candidate) bool {
		for _, x := range allChosen {
			if norm.Jaccard(x.Text, c.Text) >= sameRule {
				return true
			}
		}
		return false
	}
	atMost := func() bool { return most > 0 && len(allChosen) >= most }

	// Step 1: Bucket 0 (Hot Slot): min(15% of total, HotSlotMaxBytes), at most HotSlotMax entries.
	hotCap := min(int(float64(total)*HotSlotShare), HotSlotMaxBytes)
	var hotChosen []Candidate
	hotUsed := 0
	for _, c := range hotPool {
		if len(hotChosen) >= HotSlotMax || atMost() {
			break
		}
		if taken[c.ID] || alike(c) {
			continue
		}
		size := len(c.Text) + residentOverhead
		if hotUsed+size > hotCap || hotUsed+size > total {
			continue
		}
		hotUsed += size
		taken[c.ID] = true
		hotChosen = append(hotChosen, c)
		allChosen = append(allChosen, c)
	}

	rem := total - hotUsed
	cBase := int(float64(rem) * ContractShare)
	pBase := rem - cBase

	var pChosen, cChosen []Candidate
	pUsed, cUsed := 0, 0
	pGroups := map[string]int{}
	cPols, cGroups := map[string]int{}, map[string]int{}

	fillPrinciples := func(budget int, relaxGroup bool) {
		grpCap := int(float64(budget) * GroupShare)
		for _, c := range principlePool {
			if atMost() {
				return
			}
			if taken[c.ID] || alike(c) {
				continue
			}
			size := len(c.Text) + residentOverhead
			if pUsed+size > budget || hotUsed+pUsed+cUsed+size > total {
				continue
			}
			if !relaxGroup && c.Group != "" && grpCap > 0 && pGroups[c.Group]+size > grpCap {
				continue
			}
			pUsed += size
			if c.Group != "" {
				pGroups[c.Group] += size
			}
			taken[c.ID] = true
			pChosen = append(pChosen, c)
			allChosen = append(allChosen, c)
		}
	}

	tryAddContract := func(c Candidate, budget int, relaxPol bool) bool {
		if taken[c.ID] || alike(c) {
			return false
		}
		size := len(c.Text) + residentOverhead
		if cUsed+size > budget || hotUsed+pUsed+cUsed+size > total {
			return false
		}
		if !relaxPol && c.Polarity != "" {
			polCap := int(float64(budget) * polarityShareOf(c.Polarity))
			if polCap > 0 && cPols[c.Polarity]+size > polCap {
				return false
			}
		}
		grpCap := int(float64(budget) * GroupShare)
		if !relaxPol && c.Group != "" && grpCap > 0 && cGroups[c.Group]+size > grpCap {
			return false
		}
		cUsed += size
		if c.Polarity != "" {
			cPols[c.Polarity] += size
		}
		if c.Group != "" {
			cGroups[c.Group] += size
		}
		taken[c.ID] = true
		cChosen = append(cChosen, c)
		allChosen = append(allChosen, c)
		return true
	}

	fillContracts := func(budget int) {
		// Pass 1: enforce anti-monopoly polarity and non-generic group caps.
		for _, c := range contractPool {
			if atMost() {
				return
			}
			tryAddContract(c, budget, false)
		}
		// Pass 2: refill remaining contract budget strictly by unified Score so high-leverage cross-scene rules are
		// never left out while lower-scoring narrow rules sit unused or crowd the budget.
		for _, c := range contractPool {
			if atMost() {
				return
			}
			tryAddContract(c, budget, true)
		}
	}

	// Step 2: Fill Bucket 2 (Principles, 20%) strictly by unified leverage Score, then fill Bucket 1 (Contracts, 80% +
	// any unused principle room).
	fillPrinciples(pBase, false)
	cEff := cBase
	if pLeft := pBase - pUsed; pLeft > 0 && cIsL1 {
		cEff += pLeft
	}
	fillContracts(cEff)

	// Step 3: If contracts still leave room unused and principles are L1, spill back into principles.
	// Never spill leftover L1 room into L0 fragments.
	if remLeft := rem - pUsed - cUsed; remLeft > 0 && pIsL1 {
		pEff := pUsed + remLeft
		fillPrinciples(pEff, false)
		if pUsed < pEff {
			fillPrinciples(pEff, true)
		}
	}

	// Step 4: Output order — Hot Slot first (ranks 995..990), then Bucket 1 (Contracts, ordered naturally by unified
	// leverage Score without artificially walling all check rules ahead of high-score act rules) and Bucket 2
	// (Principles) interleaved from rank 989 downward.
	out := make([]Candidate, 0, len(hotChosen)+len(cChosen)+len(pChosen))
	out = append(out, hotChosen...)
	for i := 0; i < len(cChosen) || i < len(pChosen); i++ {
		if i < len(cChosen) {
			out = append(out, cChosen[i])
		}
		if i < len(pChosen) {
			out = append(out, pChosen[i])
		}
	}
	return out
}

func sortHot(cs []Candidate) {
	sort.SliceStable(cs, func(i, j int) bool {
		a, b := cs[i], cs[j]
		if a.Pinned != b.Pinned {
			return a.Pinned
		}
		if a.Last != b.Last {
			return a.Last > b.Last
		}
		if a.Score != b.Score {
			return a.Score > b.Score
		}
		return a.ID < b.ID
	})
}

func sortContracts(cs []Candidate) {
	sort.SliceStable(cs, func(i, j int) bool {
		a, b := cs[i], cs[j]
		if a.Score != b.Score {
			return a.Score > b.Score
		}
		if a.Hard != b.Hard {
			return a.Hard
		}
		aCheck, bCheck := a.Polarity == "check", b.Polarity == "check"
		if aCheck != bCheck {
			return aCheck
		}
		if a.Last != b.Last {
			return a.Last > b.Last
		}
		if a.Days != b.Days {
			return a.Days > b.Days
		}
		return a.ID < b.ID
	})
}

func sortPrinciples(cs []Candidate) {
	sort.SliceStable(cs, func(i, j int) bool {
		a, b := cs[i], cs[j]
		if a.Score != b.Score {
			return a.Score > b.Score
		}
		if a.Last != b.Last {
			return a.Last > b.Last
		}
		if a.Days != b.Days {
			return a.Days > b.Days
		}
		return a.ID < b.ID
	})
}

// FormatResidentLine formats a standing line as body+suffix within maxBytes, always clipping body first and keeping
// suffix (the exception or poles boundary) whole. Only when suffix itself leaves fewer than 30 bytes for body is the
// suffix dropped to protect readability.
func FormatResidentLine(body, suffix string, maxBytes int) string {
	body = strings.TrimSpace(body)
	suffix = strings.TrimSpace(suffix)
	if maxBytes <= 0 {
		maxBytes = residentTextMost
	}
	if len(body)+len(suffix) <= maxBytes {
		return body + suffix
	}
	const minBodyBytes = 30
	if suffix != "" && maxBytes-len(suffix) < minBodyBytes {
		suffix = ""
	}
	return clipBytes(body, maxBytes-len(suffix)) + suffix
}

// ResidentText is the standing line for an L0 entry (hot slot or cold-start fallback): for understanding, its principle
// first then the choice it shows; for a rule, what the assistant does. At most HotTextMost bytes, keeping the exception
// whole.
func ResidentText(e *entry.Entry) string {
	line := strings.TrimSpace(recall.Head(e))
	if p := e.Principle(); p != "" && entry.KindOf(e) == entry.KindUnderstanding && e.User == nil {
		ex := strings.TrimSpace(e.Choice)
		if s := strings.TrimSpace(e.Situation); s != "" && ex != "" {
			ex = s + "：" + ex
		}
		if ex == "" {
			ex = line
		}
		line = "原则：" + strings.TrimRight(p, "。.") + "（如 " + strings.TrimRight(ex, "。.") + "）"
	}
	return FormatResidentLine(line, recall.Exception(e), HotTextMost)
}

// clipBytes cuts s to at most n bytes at a UTF-8 rune boundary, reserving space for "…" (3 bytes) when clipped.
func clipBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	const ellipsis = "…"
	if n <= len(ellipsis) {
		return ""
	}
	s = s[:n-len(ellipsis)]
	for len(s) > 0 && !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s + ellipsis
}

// CuratedCandidate converts e into a standing Candidate (used by cold-start fallback and offline evaluation tools), or
// returns false when e may not stand (not usable, no side, sensitive/space-tagged, template-only, or covered).
func CuratedCandidate(st *store.State, e *entry.Entry, tpl map[string]bool) (Candidate, bool) {
	k := entry.KindOf(e)
	if k == "" && e.Pinned { // what the user pinned stands whatever it is
		if k = entry.KindOfFacet(string(e.Facet)); k == "" {
			k = entry.KindUnderstanding
		}
	}
	if k == "" || !export.Usable(st, e) || !Pool(st, e) || (!e.Pinned && entry.TemplateOnly(e, tpl)) ||
		(!e.Pinned && entry.CoveredBy(e) != "" && coverLive(st, e)) {
		return Candidate{}, false
	}
	if cats, space, _ := Tags(st, e); len(cats) > 0 || space != "" {
		return Candidate{}, false
	}
	days := map[string]bool{}
	for _, ev := range e.Evidence {
		days[ev.Day] = true
	}
	bucket := BucketPrinciple
	if k == entry.KindRules {
		bucket = BucketContract
	}
	if e.Pinned {
		bucket = BucketHot
	}
	dim := ""
	if e.Leverage != nil {
		dim = e.Leverage.Dimension
	}
	grp := GroupOf(st, e)
	if genericTypes[strings.ToLower(strings.TrimSpace(e.Type))] {
		grp = ""
	}
	return Candidate{
		ID:        e.ID,
		Text:      ResidentText(e),
		Kind:      k,
		Bucket:    bucket,
		Pinned:    e.Pinned,
		Hard:      entry.Hard(e),
		Score:     entry.Score(e),
		Days:      len(days),
		Last:      e.Last,
		Dimension: dim,
		Group:     grp,
	}, true
}

// HotSlotCandidate returns e as a Bucket 0 (Hot Slot) candidate when it is either user-pinned or an active Fast-Path
// temporary hard contract within HotSlotTTLDays, excluding sensitive/space-tagged or template-only entries.
func HotSlotCandidate(st *store.State, e *entry.Entry, tpl map[string]bool, now time.Time) (Candidate, bool) {
	if e == nil || !e.Live() || !Pool(st, e) {
		return Candidate{}, false
	}
	if cats, space, _ := Tags(st, e); len(cats) > 0 || space != "" {
		return Candidate{}, false
	}
	if e.Pinned {
		k := entry.KindOf(e)
		if k == "" {
			if k = entry.KindOfFacet(string(e.Facet)); k == "" {
				k = entry.KindUnderstanding
			}
		}
		return Candidate{
			ID:     e.ID,
			Text:   ResidentText(e),
			Kind:   k,
			Bucket: BucketHot,
			Pinned: true,
			Score:  entry.Score(e),
			Last:   e.Last,
		}, true
	}
	if !entry.IsFastHot(e, now, HotSlotTTLDays) {
		return Candidate{}, false
	}
	if entry.TemplateOnly(e, tpl) || (entry.CoveredBy(e) != "" && coverLive(st, e)) {
		return Candidate{}, false
	}
	return Candidate{
		ID:     e.ID,
		Text:   ResidentText(e),
		Kind:   entry.KindRules,
		Bucket: BucketHot,
		Hard:   true,
		Score:  entry.Score(e),
		Last:   entry.FastHotDay(e),
	}, true
}

// UnabsorbedRuleHotCandidate promotes a high-certainty rule (UserOwned, Hard, or cross-month Abstract with Count >= 3)
// first seen strictly after the latest L1 build day (l1LatestDay, YYYY-MM-DD) into Bucket 0 (Hot Slot) within
// HotSlotTTLDays of now, closing the 1–6 day gap between new rules and weekly L1 aggregation.
func UnabsorbedRuleHotCandidate(st *store.State, e *entry.Entry, tpl map[string]bool, l1LatestDay string, now time.Time) (Candidate, bool) {
	if e == nil || l1LatestDay == "" || !e.Live() || !Pool(st, e) {
		return Candidate{}, false
	}
	if e.First == "" || e.First <= l1LatestDay {
		return Candidate{}, false
	}
	if firstTime, err := time.Parse("2006-01-02", e.First); err != nil || now.Sub(firstTime) > time.Duration(HotSlotTTLDays)*24*time.Hour {
		return Candidate{}, false
	}
	if !export.Usable(st, e) || entry.TemplateOnly(e, tpl) || (entry.CoveredBy(e) != "" && coverLive(st, e)) {
		return Candidate{}, false
	}
	if cats, space, _ := Tags(st, e); len(cats) > 0 || space != "" {
		return Candidate{}, false
	}
	isUserOwned := e.Origin == entry.Handwritten || e.Origin == entry.SelfReport || e.Pinned || e.User != nil
	highAbstract := e.Grain == entry.Abstract && e.Count >= 3 && (e.Facet == entry.Interaction || e.Facet == entry.Tools)
	if !isUserOwned && !entry.Hard(e) && !highAbstract {
		return Candidate{}, false
	}
	k := entry.KindOf(e)
	if k == "" {
		k = entry.KindOfFacet(string(e.Facet))
	}
	if k != entry.KindRules {
		return Candidate{}, false
	}
	return Candidate{
		ID:     e.ID,
		Text:   ResidentText(e),
		Kind:   k,
		Bucket: BucketHot,
		Hard:   entry.Hard(e),
		Score:  entry.Score(e),
		Last:   e.Last,
	}, true
}

// coverLive: the rule that covers e is still there to stand in its place (when it went, e stands again).
func coverLive(st *store.State, e *entry.Entry) bool {
	x := st.Entries[entry.CoveredBy(e)]
	return x != nil && x.Live()
}

// GroupOf is the class an entry's line counts towards in the standing set: its facet and its type as the type table
// names it now (a renamed type under its new name); "" without a type.
func GroupOf(st *store.State, e *entry.Entry) string {
	name := strings.TrimSpace(e.Type)
	if name == "" {
		return ""
	}
	for i := 0; i < 8 && st != nil; i++ {
		j := slices.IndexFunc(st.Types[e.Facet], func(t entry.TypeDef) bool { return norm.Q(t.Name) == norm.Q(name) })
		if j < 0 || st.Types[e.Facet][j].Renamed == "" {
			break
		}
		name = st.Types[e.Facet][j].Renamed
	}
	return string(e.Facet) + ":" + norm.Q(name)
}

// Quote is a line of the user's own words behind e, for recall on demand ("原话：「…」（day）"); "" when it rests on
// no line of the user's.
func Quote(e *entry.Entry) string {
	for i := len(e.Evidence) - 1; i >= 0; i-- {
		ev := e.Evidence[i]
		if ev.Who != entry.WhoSelf || ev.Reported || strings.TrimSpace(ev.Quote) == "" {
			continue
		}
		q := clip(strings.Join(strings.Fields(ev.Quote), " "), quoteMost)
		return "原话：「" + q + "」（" + ev.Day + "）"
	}
	return ""
}

// quoteMost is the longest quote a recalled line carries, in characters.
const quoteMost = 80
