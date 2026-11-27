package curate

import (
	"testing"

	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/entry"
	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/kbsrc"
	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/norm"
	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/relay"
	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/schema"
	srcs "github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/sources"
	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/store"
	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/triage"
	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/verify"
)

func TestPrecleanUnitsStripsBoilerplateAndMultiSessionTemplates(t *testing.T) {
	tplLine := "请严格按照占位派活模板执行，完成后汇报结果和验证命令。"
	units := []Unit{
		{
			Ref: "agents/m1/s1", Entity: srcs.KindAgents, Day: "2026-11-20",
			Lines: []norm.Line{
				{N: 1, Speaker: srcs.SpeakerPersona, Body: "<persona-context>背景</persona-context>\nKeep hub credentials private."},
				{N: 2, Speaker: srcs.SpeakerPersona, Body: tplLine},
				{N: 3, Speaker: norm.Me, Body: "这次先只改占位模块的超时参数"},
			},
		},
		{
			Ref: "agents/m1/s2", Entity: srcs.KindAgents, Day: "2026-11-20",
			Lines: []norm.Line{
				{N: 1, Speaker: srcs.SpeakerPersona, Body: tplLine},
			},
		},
		{
			Ref: "agents/m1/s3", Entity: srcs.KindAgents, Day: "2026-11-20",
			Lines: []norm.Line{
				{N: 1, Speaker: norm.Me, Body: tplLine},
				{N: 2, Speaker: norm.Me, Body: relay.RelayedMark + "别动线上配置，先跑单测"},
			},
		},
		{
			Ref: "chats/persona-c1", Entity: "chats", Day: "2026-11-20",
			Lines: []norm.Line{
				{N: 1, Speaker: norm.Me, Body: "主对话里的占位要求"},
			},
		},
	}

	got, stats := PrecleanUnits(units, nil)
	if stats.BoilerplateLines != 1 {
		t.Errorf("BoilerplateLines = %d, want 1", stats.BoilerplateLines)
	}
	if stats.TemplateLines != 3 {
		t.Errorf("TemplateLines = %d, want 3", stats.TemplateLines)
	}
	if stats.S3UnitsDropped != 1 {
		t.Errorf("S3UnitsDropped = %d, want 1 (s2 became empty)", stats.S3UnitsDropped)
	}
	if len(got) != 3 {
		t.Fatalf("remaining units = %d, want 3", len(got))
	}
	if len(got[0].Lines) != 1 || got[0].Lines[0].Body != "这次先只改占位模块的超时参数" {
		t.Errorf("s1 lines = %+v", got[0].Lines)
	}
	if len(got[1].Lines) != 1 || got[1].Lines[0].Body != relay.RelayedMark+"别动线上配置，先跑单测" {
		t.Errorf("s3 lines = %+v", got[1].Lines)
	}
}

func TestS3ScopingAllowsToolsAndWhoSelfInteractionJudgementOnly(t *testing.T) {
	c := &Curator{Cfg: Config{Language: "zh-Hans"}}
	u := &Unit{
		Ref:    "agents/m1/s1",
		Entity: srcs.KindAgents,
		Meta:   kbsrc.Meta{Kind: "agent", Name: "agent-s1"},
		Day:    "2026-11-20",
		Lines: []norm.Line{
			{N: 1, Speaker: norm.Me, Body: "在占位项目里别自动合并，先给我看结果"},
			{N: 2, Speaker: srcs.SpeakerAgent, Body: "工具 host-a 跑构建需要先设置环境变量"},
		},
		Who: map[int]string{1: entry.WhoSelf},
	}
	w := &verify.Window{Chat: u.Ref, Day: u.Day, Kind: "agent", All: map[int]norm.Line{1: u.Lines[0], 2: u.Lines[1]}, Shown: map[int]bool{1: true, 2: true}}

	// Workflow, Persona, People are disallowed from S3.
	for _, facet := range []string{string(entry.Workflow), string(entry.Persona), string(entry.People)} {
		r := c.checkObservation(w, u, nil, triage.TierOwn, schema.Observation{
			Facet: facet, Text: "用户在占位项目里习惯先看结果", Domain: entry.Work, Confidence: "high",
			Choice: "先看结果", Evidence: []schema.Evidence{{N: 1, Quote: "先给我看结果"}},
			Impact: schema.ObsImpact{Moment: "dispatch", Action: "先汇报再合并", Object: "占位项目"},
		})
		if r.rej != obsFacetNotAllowed {
			t.Errorf("S3 facet %s: rej = %q, want %q", facet, r.rej, obsFacetNotAllowed)
		}
	}

	// Tools from agent report is allowed.
	rTool := c.checkObservation(w, u, nil, triage.TierOwn, schema.Observation{
		Facet: string(entry.Tools), Object: "host-a", Text: "在 host-a 上跑构建需要先设置环境变量",
		Domain: entry.Work, Confidence: "high", Evidence: []schema.Evidence{{N: 2, Quote: "工具 host-a 跑构建需要先设置环境变量"}},
		Impact: schema.ObsImpact{Moment: "use_object", Action: "构建前设置环境变量", Object: "host-a"},
	})
	if rTool.rej != "" {
		t.Errorf("S3 tools rejected: %q", rTool.rej)
	}

	// Interaction from WhoSelf is allowed.
	rInt := c.checkObservation(w, u, nil, triage.TierOwn, schema.Observation{
		Facet: string(entry.Interaction), Text: "在占位项目里不要自动合并，先给用户看结果",
		Situation: "占位项目改动完成", Choice: "先给用户看结果再合并", Domain: entry.Work, Confidence: "high",
		Evidence: []schema.Evidence{{N: 1, Quote: "在占位项目里别自动合并，先给我看结果"}},
		Impact:   schema.ObsImpact{Moment: "dispatch", Action: "合并前先汇报结果", Object: "占位项目"},
	})
	if rInt.rej != "" {
		t.Errorf("S3 WhoSelf interaction rejected: %q", rInt.rej)
	}

	// Interaction from agent line (not WhoSelf) is rejected.
	rAgentInt := c.checkObservation(w, u, nil, triage.TierOwn, schema.Observation{
		Facet: string(entry.Interaction), Text: "在 host-a 上构建前设置环境变量",
		Situation: "构建", Choice: "先设置环境变量", Domain: entry.Work, Confidence: "high",
		Evidence: []schema.Evidence{{N: 2, Quote: "工具 host-a 跑构建需要先设置环境变量"}},
		Impact:   schema.ObsImpact{Moment: "dispatch", Action: "先设置环境变量", Object: "host-a"},
	})
	if rAgentInt.rej == "" {
		t.Errorf("S3 non-WhoSelf interaction should be rejected")
	}

	_ = store.State{}
}
