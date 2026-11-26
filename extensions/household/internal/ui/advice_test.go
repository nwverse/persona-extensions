package ui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/nwverse/persona/sdk/go/wire"

	"github.com/nwverse/persona-extensions/extensions/household/internal/rules"
)

func TestAdviceBlockAgreesWithTheNumbers(t *testing.T) {
	st := newStore(t, true)
	a, _ := newApp(st)
	v := render(t, a, state{})
	checkView(t, "overview with advice", v, false)
	sec, ok := sectionTitled(v, "今天的建议")
	if !ok {
		t.Fatal("no 今天的建议")
	}
	if v.Sections[1].Title != "今天的建议" {
		t.Fatalf("section 1 is %q, want the advice right under the numbers", v.Sections[1].Title)
	}
	if len(sec.Items) > 4 {
		t.Fatalf("%d lines of advice", len(sec.Items))
	}
	sum := rules.Summarize(home(t, st), testToday)
	stats := overviewGrid(t, v).Stats
	want := []struct{ prefix, tab, stat string }{
		{fmt.Sprintf("先吃 %d 样", sum.Expiring), "tab:stock", stats[3].Value},
		{"可以做：", "tab:meal", ""},
		{fmt.Sprintf("该买 %d 样", sum.Expired+sum.Out+sum.Low+sum.Runout), "tab:buy", ""},
		{fmt.Sprintf("家务 %d 件", sum.ChoresDue), "tab:chore", stats[2].Value},
	}
	for i, w := range want {
		row := sec.Items[i]
		if !strings.HasPrefix(row.Title, w.prefix) || row.Subtitle == "" || row.Action == nil || row.Action.ID != w.tab {
			t.Fatalf("advice line %d %+v, want %q opening %s", i, row, w.prefix, w.tab)
		}
		if w.stat != "" && !strings.Contains(row.Title, " "+w.stat+" ") {
			t.Fatalf("line %q does not carry the number %s shown above", row.Title, w.stat)
		}
	}
	// 该买 is the 补货 part of 需要处理.
	if !strings.Contains(stats[0].Detail, fmt.Sprintf("补货 %d", sum.Expired+sum.Out+sum.Low+sum.Runout)) {
		t.Fatalf("需要处理 detail %q", stats[0].Detail)
	}
	// The cook line is the first meal idea of 今天吃什么.
	meals, _ := sectionTitled(v, "今天吃什么")
	if sec.Items[1].Title != "可以做："+meals.Items[0].Title {
		t.Fatalf("cook %q, first meal idea %q", sec.Items[1].Title, meals.Items[0].Title)
	}
	// A line opens its tab.
	res := act(t, a, state{}, nil, sec.Items[2].Action.ID)
	if viewState(t, res.View).T != buyTab {
		t.Fatalf("tap on 该买 went to %+v", viewState(t, res.View))
	}
}

func TestAdviceBlockWhenNothingIsDue(t *testing.T) {
	st := newStore(t, false)
	a, _ := newApp(st)
	act(t, a, state{F: "new:item"}, map[string]string{"title": "盐"}, "save")
	if entityCount(t, st) != 1 {
		t.Fatal("the item was not saved")
	}
	ov := render(t, a, state{})
	sec, ok := sectionTitled(ov, "今天的建议")
	if !ok || len(sec.Items) != 1 || sec.Items[0].Type != wire.ComponentText || sec.Items[0].Text != "今天没有要特别注意的" {
		t.Fatalf("advice on a quiet household %+v", sec)
	}
}
