package ui

import (
	"strconv"

	"github.com/nwverse/persona/sdk/go/wire"

	"github.com/nwverse/persona-extensions/extensions/household/internal/rules"
)

// cardItems is the most components the overview card has: one stat grid and the rows after it.
const cardItems = 12

// cardRowsTitle heads the card's rows. They include the items about to expire, which 需要处理 does not count, so the
// rows are not titled 需要处理.
const cardRowsTitle = "最要紧的"

// card is the overview card: three totals, then the most urgent records, each opening its form. A card has no actions of
// its own, so its rows carry none, and the empty state says what to do on the page the card opens (manifest "opens").
func (c *ctx) card() *wire.View {
	if c.empty() {
		return &wire.View{Sections: []wire.Section{{Items: []wire.Component{{
			Type: wire.ComponentEmpty, Title: welcomeTitle,
			Detail: "帮你记家里有什么、该买什么、哪些家务到了该做的时候。点开这里添加第一件物品，或者直接跟助手说，例如「记一下冰箱里有两盒牛奶」。",
		}}}}}
	}
	sum := rules.Summarize(c.home, c.today)
	v := &wire.View{Sections: []wire.Section{{Items: []wire.Component{statGrid([]wire.Stat{
		alertsStat(sum),
		{Label: "家务到期", Value: strconv.Itoa(sum.ChoresDue), Tone: cautionIf(sum.ChoresDue)},
		{Label: "即将过期", Value: strconv.Itoa(sum.Expiring), Tone: cautionIf(sum.Expiring)},
	})}}}}
	if rows := c.cardRows(cardItems - 1); len(rows) > 0 {
		v.Sections = append(v.Sections, wire.Section{Title: cardRowsTitle, Items: rows})
	}
	return v
}

// cardRows are up to limit rows, most urgent first: the findings that call for a purchase, the chores due, then the
// items about to expire.
func (c *ctx) cardRows(limit int) []wire.Component {
	findings := rules.Findings(c.home, c.today)
	var rows []wire.Component
	for _, f := range findings {
		if f.Kind.Buyable() {
			rows = append(rows, c.findingRow(f))
		}
	}
	for _, d := range rules.ChoresDue(c.home, c.today) {
		rows = append(rows, c.choreRow(d))
	}
	for _, f := range findings {
		if f.Kind == rules.Expiring {
			rows = append(rows, c.expiringRow(f))
		}
	}
	for i := range rows {
		rows[i].Actions = nil
	}
	return slicesOf(rows, limit)
}
