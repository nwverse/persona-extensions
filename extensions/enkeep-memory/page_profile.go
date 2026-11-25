package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/profile"
	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/store"
	"github.com/nwverse/persona/sdk/go/ext"
	"github.com/nwverse/persona/sdk/go/wire"
)

// profileSnapshot returns the profile summary for the page without taking the store's lock.
func (a *app) profileSnapshot(st *store.State) *profile.Summary {
	if snap := a.c.Live.Snapshot(); snap != nil && snap.Profile != nil {
		return snap.Profile
	}
	lang := a.c.Cfg.Language
	if lang == "" {
		lang = a.c.Lang
	}
	return profile.BuildWith(st, a.c.ProfileInput, time.Now(), lang)
}

// profileCard is the entry row on the Start tab opening the user profile summary subpage.
func (a *app) profileCard(t tr, st *store.State) wire.Section {
	sum := a.profileSnapshot(st)
	sub := t.s("还没有攒够稳定条目：日常对话或答完「三个问题」后会自动生成，无需导入外部画像。",
		"Not enough settled entries yet: builds from your own chats or first-run answers, no external seed needed.")
	badge := &wire.Badge{Text: t.f("已覆盖 %d/6 面", "%d/6 facets", sum.CoveredCount), Tone: wire.ToneNeutral}
	if sum.CoveredCount > 0 {
		badge.Tone = wire.TonePositive
		if sum.Overview != "" {
			sub = short(sum.Overview, 64)
		} else {
			sub = t.f("基于 %d 条稳定条目汇总，点开查看「我是怎样的人」总览", "Summarized from %d settled entries; open for the full overview", sum.SourceCount)
		}
	}
	return wire.Section{
		Title: t.s("用户侧写汇总", "Profile summary"),
		Footer: t.s("把六个方面的稳定条目汇总成「我是怎样的人」总览，同时通过记忆出口供助手按场景召回。",
			"Summarizes settled entries across all six facets into a readable overview and exposes it to the assistant by scene."),
		Items: []wire.Component{
			row(t.s("我是怎样的人（侧写总览）", "Who I am (profile summary)"), sub, badge,
				ext.Open("open:profile", "", page{Tab: tabStart, Open: "profile"}, t.s("用户侧写", "Profile summary"))),
		},
	}
}

// profilePage renders the mobile-friendly user profile summary subpage (Open: "profile").
func (a *app) profilePage(t tr, st *store.State, sum *profile.Summary) *wire.View {
	if sum == nil {
		sum = profile.BuildWith(st, a.c.ProfileInput, time.Now(), a.c.Lang)
	}
	v := &wire.View{Title: t.s("用户侧写", "Profile summary")}

	// Header meta & refresh button.
	updated := t.s("尚未生成", "not generated yet")
	if len(sum.GeneratedAt) >= 16 {
		updated = strings.Replace(sum.GeneratedAt[:16], "T", " ", 1) + " UTC"
	}
	modeLabel := t.s("条目直出", "entry summary")
	if sum.Mode == profile.ModeModel {
		modeLabel = t.s("每日整理归纳", "daily synthesis")
	}
	headItems := []wire.Component{
		secondary(t.f("覆盖 %d/6 个方面 · 来源 %d 条稳定条目 · %s · 更新于 %s",
			"%d/6 facets covered · %d settled entries · %s · updated %s",
			sum.CoveredCount, sum.SourceCount, modeLabel, updated)),
		button(t.s("立即刷新汇总", "Refresh summary"), "profile:refresh", ""),
	}
	v.Sections = append(v.Sections, wire.Section{Items: headItems})

	// Blank new user empty state.
	if sum.CoveredCount == 0 && len(sum.Sensitive) == 0 {
		v.Sections = append(v.Sections, wire.Section{
			Title: t.s("我是怎样的人", "Who I am"),
			Items: []wire.Component{
				empty(t.s("还没有足够的稳定条目来生成侧写汇总", "Not enough settled entries for a profile summary yet")),
				secondary(t.s("空白新用户无需导入外部画像：随着日常对话、Agent 协作或在「起步」里回答三个问题，六个方面的侧写会自动长出来。",
					"No external profile seed is needed: as you chat, work with agents, or answer the three first-run questions, all six facets fill in by themselves.")),
			},
		})
		return v
	}

	// Overview + Recent section.
	var ovItems []wire.Component
	if sum.Overview != "" {
		ovItems = append(ovItems, text(sum.Overview))
	}
	if len(sum.Recent) > 0 {
		ovItems = append(ovItems, secondary(t.s("近况：", "Recently: ")+strings.Join(sum.Recent, t.s("；", "; "))))
	}
	if len(ovItems) > 0 {
		v.Sections = append(v.Sections, wire.Section{
			Title: t.s("我是怎样的人（总览）", "Who I am (overview)"),
			Footer: t.s("通用侧写仅包含非敏感稳定条目，已通过记忆出口同步给助手按需召回。",
				"The general overview includes only non-sensitive settled entries and is exposed to the assistant via memory.entries."),
			Items: ovItems,
		})
	}

	// Six facets in order, each in its own mobile-readable vertical section.
	for _, sec := range sum.Sections {
		groupTag := t.s("用平台更丝滑", "Smoother platform use")
		if sec.Group == "understanding" {
			groupTag = t.s("理解更准", "Deeper understanding")
		}
		secTitle := fmt.Sprintf("%s · %s", t.facetLabel(sec.Facet), groupTag)
		var items []wire.Component
		if len(sec.Points) == 0 {
			items = append(items, secondary(t.s("这一面还没有稳定条目，空着不用泛泛默认画像填。",
				"No settled entries in this facet yet; left empty instead of using generic defaults.")))
		} else {
			if sec.Summary != "" {
				items = append(items, text(sec.Summary))
			}
			for _, pt := range sec.Points {
				sub := ""
				if pt.Last != "" {
					sub = t.s("最近 ", "last ") + pt.Last
				}
				items = append(items, row(pt.Text, sub, nil, ext.Open("open:"+pt.ID, "", page{Tab: tabFacets, Facet: string(sec.Facet), Open: "e:" + pt.ID}, short(pt.Text, 20))))
			}
		}
		items = append(items, row(
			t.f("查看「%s」全部条目（%d）", "See all \"%s\" entries (%d)", t.facetLabel(sec.Facet), sec.SourceCount),
			"",
			nil,
			ext.Open("open:facet:"+string(sec.Facet), "", page{Tab: tabFacets, Facet: string(sec.Facet)}, t.facetLabel(sec.Facet)),
		))
		v.Sections = append(v.Sections, wire.Section{Title: secTitle, Items: items})
	}

	// Sensitive sections, isolated by category with explicit scene notice.
	if len(sum.Sensitive) > 0 {
		var sensItems []wire.Component
		for _, sens := range sum.Sensitive {
			label := t.categoryLabels([]string{sens.Category})
			sub := sens.Summary
			if sens.OriginSpace != "" {
				sub = fmt.Sprintf("[%s] %s", sens.OriginSpace, sub)
			}
			sensItems = append(sensItems, row(
				label,
				sub,
				&wire.Badge{Text: t.f("%d 条", "%d", sens.SourceCount), Tone: wire.ToneCaution},
				nil,
			))
		}
		v.Sections = append(v.Sections, wire.Section{
			Title: t.s("受控敏感侧写（按场景分级）", "Sensitive profile sections (scene-gated)"),
			Footer: t.s("以上分类与通用侧写严格隔离，保留完整分类与来源空间标签；在锁屏、外发、派活与第三方场景中不出。",
				"Kept strictly separate from the general summary with full category and origin_space tags; withheld on lockscreen, outbound, dispatch, and third-party scenes."),
			Items: sensItems,
		})
	}

	return v
}
