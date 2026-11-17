package page

import (
	"context"
	"fmt"

	"github.com/nwverse/persona-extensions/extensions/github/internal/github"
	"github.com/nwverse/persona/sdk/go/ext"
	"github.com/nwverse/persona/sdk/go/wire"
)

// CardID is the Overview card's ui contribution (persona-extension.json): an overview_card that opens the page.
const CardID = "github-today"

// cardRows is how many of the most pressing PRs the card lists under its counts.
const cardRows = 3

// RenderCard draws the card on Overview (nwverse/persona-extensions#626): how many PRs wait for the user's review,
// their open PRs with how many failed CI, and what is assigned to them or mentions them; under that the most pressing
// few, review requests first, then their PRs whose CI failed, each opening on the page as the page's rows do. It
// reads the page's own lists through their cache (github.Connector.List), so a card drawn soon after the page or
// another card asks GitHub nothing, and within the page's budget, as the page is drawn. Like every Overview card it
// only shows: a tap anywhere else opens the page.
func (p *Page) RenderCard(ctx context.Context, r *ext.UIRequest) (*wire.View, error) {
	p.keepHub(r.Hub)
	req := *r
	return p.within(ctx, fmt.Sprintf("card\x00%d", r.UIVersion), CardID, func(bctx context.Context) *wire.View { return p.card(bctx, &req) },
		func() *wire.View {
			v := cardLine("loading", "正在从 GitHub 读取，读完会自动显示", "")
			v.RefreshAfter = 30 // apps and hubs without ui.invalidate draw it again by themselves
			return v
		}), nil
}

// card is the card as GitHub's lists have it now.
func (p *Page) card(ctx context.Context, r *ext.UIRequest) *wire.View {
	if _, off := p.offline(ctx); off {
		return cardLine("signin", "还没连上 GitHub，在 设置 › 连接器 › GitHub 登录", wire.ToneCaution)
	}
	var lists [3][]github.Entry
	for i, tab := range []string{github.TabReview, github.TabMine, github.TabInvolved} {
		entries, at, err := p.c.List(ctx, tab, false)
		if err != nil && at.IsZero() { // nothing read before either
			return cardLine("error", "读不到 GitHub："+clip(github.Scrub(err.Error()), 160), wire.ToneCaution)
		}
		lists[i] = entries
	}
	review, mine, involved := lists[0], lists[1], lists[2]
	if len(review)+len(mine)+len(involved) == 0 {
		return cardLine("none", "GitHub 上没有等你处理的", "")
	}
	var failed []github.Entry
	for _, e := range mine {
		if ciFailed(e) {
			failed = append(failed, e)
		}
	}
	yours := wire.Stat{Label: "我提的 PR", Value: many(len(mine))}
	if len(failed) > 0 {
		yours.Detail, yours.Tone = fmt.Sprintf("%d 个 CI 失败", len(failed)), wire.ToneDanger
	}
	v := &wire.View{Title: "GitHub", RefreshAfter: refreshAfter, Sections: []wire.Section{{Items: []wire.Component{{
		Type: wire.ComponentStatGrid, Key: "counts", Stats: []wire.Stat{
			{Label: "待我 review", Value: many(len(review))},
			yours,
			{Label: "指派、提到我的", Value: many(len(involved))},
		}}}}}}
	var rows []wire.Component
	seen := map[string]bool{}
	add := func(e github.Entry, why string) {
		if len(rows) == cardRows || seen[e.Ref()] {
			return
		}
		seen[e.Ref()] = true
		e.Why = why
		rows = append(rows, p.cardRow(r, e))
	}
	for _, e := range review {
		add(e, "请你 review")
	}
	for _, e := range failed {
		add(e, "")
	}
	if len(rows) > 0 {
		v.Sections = append(v.Sections, wire.Section{Items: rows})
	}
	return v
}

// cardRow is an entry as the page's row, which opens the PR on the page (pushed onto it from the card, so the page is
// named); an app before UI version 3 has no push, and its tap on the row opens the page as a tap on the card does.
func (p *Page) cardRow(r *ext.UIRequest, e github.Entry) wire.Component {
	row := p.row(r, state{}, e)
	row.Action = nil
	if r.UIVersion >= 3 && e.Number > 0 && e.Repo != "" {
		row.Action = ext.Open("open:"+e.Ref(), ID, state{Repo: e.Repo, Number: e.Number}, e.Ref())
	}
	return row
}

// ciFailed: the PR's CI rollup failed (the page's 「CI 失败」).
func ciFailed(e github.Entry) bool { return e.CI == "FAILURE" || e.CI == "ERROR" }

// cardLine is the card as one line of text: signed out, failing, nothing to do, still reading.
func cardLine(key, text, tone string) *wire.View {
	return &wire.View{Title: "GitHub", RefreshAfter: refreshAfter, Sections: []wire.Section{{Items: []wire.Component{
		{Type: wire.ComponentText, Key: key, Text: text, Tone: tone}}}}}
}
