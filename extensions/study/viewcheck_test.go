package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/nwverse/persona/sdk/go/ext"
	"github.com/nwverse/persona/sdk/go/wire"
	fsrs "github.com/open-spaced-repetition/go-fsrs/v4"
)

// Every page the extension draws, in every state it can be in, must pass wire.CheckView (what the apps accept),
// survive a JSON round trip through wire.CheckViewJSON, and keep the rules the schema cannot say on its own.

var allModes = []string{modeHome, modeReview, modeDrafts, modeTopics, modeNotion, modeObsidian, modeNLM, modeCards, modeCard, modeWrite, modeLogseq, modeWeak, modeTutor}

// checkPage holds one view to everything an app checks, and to this extension's own promises.
func checkPage(t *testing.T, label string, v *wire.View) {
	t.Helper()
	if v == nil {
		t.Fatalf("%s: no view", label)
	}
	if err := wire.CheckView(v); err != nil {
		t.Fatalf("%s: CheckView: %v", label, err)
	}
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("%s: %v", label, err)
	}
	if err := wire.CheckViewJSON(raw); err != nil {
		t.Fatalf("%s: CheckViewJSON: %v", label, err)
	}
	keys := map[string]bool{}
	for i, s := range v.Sections {
		for j, c := range s.Items {
			if c.Key != "" {
				if keys[c.Key] {
					t.Fatalf("%s: section %d item %d: key %q twice", label, i, j, c.Key)
				}
				keys[c.Key] = true
			}
			if c.Type == wire.ComponentSegmented {
				if len(c.Choices) < 2 || len(c.Choices) > 5 {
					t.Fatalf("%s: segmented with %d choices", label, len(c.Choices))
				}
				for _, ch := range c.Choices {
					if n := utf8.RuneCountInString(ch.Title); n == 0 || n > 12 {
						t.Fatalf("%s: segmented choice %q", label, ch.Title)
					}
				}
			}
		}
	}
	if len(v.State) > wire.StateMost {
		t.Fatalf("%s: state of %d bytes", label, len(v.State))
	}
}

func viewOf(t *testing.T, a *app, st pageState) *wire.View {
	t.Helper()
	v, err := a.view(t.Context(), st, "")
	if err != nil {
		t.Fatalf("%+v: %v", st, err)
	}
	checkPage(t, fmt.Sprintf("%+v", st), v)
	return v
}

func actOn(t *testing.T, a *app, action string, st pageState, values map[string]string) (*wire.ActionResult, error) {
	t.Helper()
	res, err := a.action(t.Context(), request(action, st, values))
	if err == nil {
		checkPage(t, action, res.View)
	}
	return res, err
}

func stateOf(t *testing.T, v *wire.View) pageState {
	t.Helper()
	var st pageState
	if err := (&ext.UIRequest{UIParams: wire.UIParams{State: v.State}}).DecodeState(&st); err != nil {
		t.Fatal(err)
	}
	return st
}

func TestViewsEmpty(t *testing.T) {
	// No cards, no computer, no Notion, no model: every page still draws, mostly as an empty state.
	a := testApp(t, &fakeHub{}, defaults())
	for _, m := range allModes {
		viewOf(t, a, pageState{Mode: m})
	}
	viewOf(t, a, pageState{Mode: modeCard, Card: "missing"})
	viewOf(t, a, pageState{Mode: modeTutor, Exercise: "expired", Hints: 3})
	viewOf(t, a, pageState{Mode: modeWeak, Topic: "nothing", Status: statusWeak})
	viewOf(t, a, pageState{Mode: modeReview, Done: 3})
	if _, err := actOn(t, a, "tutor_start", pageState{Mode: modeTutor}, nil); err == nil {
		t.Fatal("practice without cards")
	}
	// No hub at all.
	b, err := openApp("", defaults(), "", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range allModes {
		v, err := b.view(t.Context(), pageState{Mode: m}, "")
		if err != nil {
			t.Fatal(m, err)
		}
		checkPage(t, "no hub "+m, v)
	}
	for _, fam := range []string{wire.WidgetFamilySmall, wire.WidgetFamilyAccessoryInline, wire.WidgetFamilyAccessoryCircular, wire.WidgetFamilyAccessoryRectangular} {
		w, err := b.renderWidget(t.Context(), &ext.UIRequest{UIParams: wire.UIParams{Contribution: widgetID, Placement: wire.PlacementWidget, Size: fam, UIVersion: 4}})
		if err != nil {
			t.Fatal(err)
		}
		checkPage(t, "widget "+fam, w)
	}
}

func TestViewsLongAndOddFields(t *testing.T) {
	long := strings.Repeat("长", 5000)
	h := &fakeHub{
		topics: []wire.KBEntry{{ID: "t1", Name: "", OneLine: long}, {ID: "t2", Name: long}},
		node:   &nodeWorker{cfg: nodeConfig{Vault: t.TempDir(), Logseq: t.TempDir()}, look: func() string { return "" }},
	}
	a := testApp(t, h, config{Machines: []string{"mac"}})
	src := Source{Kind: "unknown-kind", Ref: long, Title: strings.Repeat("题", 300)}
	a.addDirect([]cardText{
		// An Obsidian embed and an http link are outside the Markdown subset: the answer falls back to text.
		{Front: strings.Repeat("问", maxFront+50), Back: strings.Repeat("答", maxBack-40) + " ![[diagram.png]] [x](http://example.com)", Topic: strings.Repeat("知", 200)},
		{Front: "Markdown answer", Back: "**bold** and [a link](https://example.com)"},
		{Front: "Reference link", Back: "[a][1]\n\n[1]: https://example.com"},
	}, src)
	a.addDrafts([]cardText{{Front: long, Back: long, Topic: long}}, src)
	for _, c := range a.st.Cards {
		if utf8.RuneCountInString(c.Topic) > maxTopic || utf8.RuneCountInString(c.Front) > maxFront {
			t.Fatalf("not clipped: %d %d", utf8.RuneCountInString(c.Topic), utf8.RuneCountInString(c.Front))
		}
	}
	for _, m := range allModes {
		viewOf(t, a, pageState{Mode: m})
	}
	for _, c := range a.st.sortedCards() {
		viewOf(t, a, pageState{Mode: modeCard, Card: c.ID})
		viewOf(t, a, pageState{Mode: modeReview, Card: c.ID, Shown: true})
		viewOf(t, a, pageState{Mode: modeWeak, Topic: topicOf(c)})
	}
	if got := answerBlock("![img](https://example.com/x.png)"); got.Type != wire.ComponentText {
		t.Fatal("an image answer stayed Markdown")
	}
	if got := answerBlock("**fine**"); got.Type != wire.ComponentMarkdown {
		t.Fatal("plain Markdown became text")
	}
	if got := answerBlock(""); got.Text == "" {
		t.Fatal("an empty answer")
	}
	// A failing source with a very long error message draws as an error page.
	h.kbErr = errors.New(long)
	viewOf(t, a, pageState{Mode: modeTopics})
	// A long notebook title and query in the state.
	viewOf(t, a, pageState{Mode: modeNLM, Notebook: "nb", NBTitle: strings.Repeat("本", 300), Query: strings.Repeat("q", 200)})
	// Empty strings never reach a required field.
	for _, c := range []wire.Component{text("", ""), button("", "x", nil), row("", "", "x", nil), empty("", "")} {
		checkPage(t, "helper "+c.Type, &wire.View{Sections: []wire.Section{{Items: []wire.Component{c}}}})
	}
}

func TestViewsLargeCollections(t *testing.T) {
	var notebooks []string
	for i := 0; i < 400; i++ {
		notebooks = append(notebooks, fmt.Sprintf(`{"id":"nb_%d","title":"Notebook %d"}`, i, i))
	}
	run := func(_ context.Context, _ string, args ...string) ([]byte, error) {
		switch cmd := strings.Join(args, " "); {
		case strings.HasPrefix(cmd, "--quiet auth check"):
			return []byte(`{}`), nil
		case cmd == "--quiet list --json":
			return []byte(`{"notebooks":[` + strings.Join(notebooks, ",") + `]}`), nil
		}
		return nil, errors.New("unexpected")
	}
	h := &fakeHub{node: &nodeWorker{cfg: nodeConfig{}, look: func() string { return "/bin/notebooklm" }, run: run}}
	for i := 0; i < 400; i++ {
		h.topics = append(h.topics, wire.KBEntry{ID: fmt.Sprintf("t%d", i), Name: fmt.Sprintf("Topic %d", i)})
	}
	a := testApp(t, h, config{Machines: []string{"mac"}, NewPerDay: 200})
	var cards []cardText
	for i := 0; i < 450; i++ {
		cards = append(cards, cardText{Front: fmt.Sprintf("Question %d", i), Back: "Answer", Topic: fmt.Sprintf("Topic %d", i%150)})
	}
	if n, err := a.addDirect(cards, Source{Kind: srcManual}); err != nil || n != 450 {
		t.Fatal(n, err)
	}
	var drafts []cardText
	for i := 0; i < maxDrafts+50; i++ {
		drafts = append(drafts, cardText{Front: fmt.Sprintf("Draft %d", i), Back: "B"})
	}
	if n, _ := a.addDrafts(drafts, Source{Kind: srcManual}); n != maxDrafts {
		t.Fatal(n)
	}
	// Some reviews spread the topics over every status.
	for i, c := range a.st.sortedCards() {
		if i%3 == 0 {
			continue
		}
		r := fsrs.Good
		if i%3 == 1 {
			r = fsrs.Again
		}
		if _, err := a.grade(c.ID, r); err != nil {
			t.Fatal(err)
		}
	}
	for _, m := range allModes {
		viewOf(t, a, pageState{Mode: m})
	}
	for _, s := range []string{"", statusWeak, statusShaky, statusSolid, statusNew} {
		viewOf(t, a, pageState{Mode: modeWeak, Status: s})
		viewOf(t, a, pageState{Mode: modeWeak, Status: s, Topic: "Topic 7"})
	}
	// Filtering through the page's own actions.
	st := pageState{Mode: modeWeak}
	for _, f := range []string{"all", statusWeak, statusShaky, statusSolid, statusNew} {
		res, err := actOn(t, a, "weak_filter", st, map[string]string{"status": f})
		if err != nil {
			t.Fatal(f, err)
		}
		if got := stateOf(t, res.View); (f == "all" && got.Status != "") || (f != "all" && got.Status != f) {
			t.Fatalf("%s: %+v", f, got)
		}
	}
	if _, err := actOn(t, a, "weak_filter", st, map[string]string{"status": "bogus"}); err == nil {
		t.Fatal("a bad filter was accepted")
	}
	res, err := actOn(t, a, "weak_topic", st, map[string]string{"topic": "Topic 3"})
	if err != nil || stateOf(t, res.View).Topic != "Topic 3" || res.View.Title != "Topic 3" {
		t.Fatal(err)
	}
	// One big notebook list.
	viewOf(t, a, pageState{Mode: modeNLM})
	// The home page and the widget with this much.
	for _, fam := range []string{wire.WidgetFamilySmall, wire.WidgetFamilyAccessoryInline, wire.WidgetFamilyAccessoryCircular, wire.WidgetFamilyAccessoryRectangular} {
		w, err := a.renderWidget(t.Context(), &ext.UIRequest{UIParams: wire.UIParams{Contribution: widgetID, Placement: wire.PlacementWidget, Size: fam, UIVersion: 4}})
		if err != nil {
			t.Fatal(err)
		}
		checkPage(t, "widget "+fam, w)
	}
}

func TestViewsTutorStates(t *testing.T) {
	exercises := map[string]string{
		kindChoice:   choiceExercise,
		kindCloze:    `{"question":"The ____ makes ATP.","answer":"mitochondrion","accepted":["mitochondria"],"hints":["Think energy."],"explanation":"Respiration."}`,
		kindConcept:  `{"question":"How is a mitochondrion different from a chloroplast?","answer":"One releases energy, the other captures it.","hints":["a","b","c"]}`,
		kindScenario: `{"question":"A cell's mitochondria stop working. What happens?","answer":"ATP runs out.","hints":["a","b"],"explanation":"No respiration."}`,
	}
	kind := kindChoice
	verdict := `{"verdict":"partial","score":60,"cause":"transfer","diagnosis":"` + strings.Repeat("诊", 400) + `","feedback":"` + strings.Repeat("馈", 900) + `"}`
	h := &fakeHub{model: func(p wire.ModelCompleteParams) (wire.ModelCompleteResult, error) {
		switch p.Label {
		case "study.exercise":
			return wire.ModelCompleteResult{JSON: json.RawMessage(exercises[kind])}, nil
		case "study.judge":
			return wire.ModelCompleteResult{JSON: json.RawMessage(verdict)}, nil
		}
		return wire.ModelCompleteResult{JSON: json.RawMessage(`{"cards":[{"front":"Reinforce","back":"Card"}]}`)}, nil
	}}
	a := testApp(t, h, defaults())
	a.addDirect([]cardText{{Front: "Powerhouse of the cell?", Back: "Mitochondrion", Topic: "Cells"}}, Source{Kind: srcManual})
	id := a.st.sortedCards()[0].ID
	if _, err := a.grade(id, fsrs.Easy); err != nil {
		t.Fatal(err)
	}
	setNow(a, t0.Add(90*24*time.Hour))

	// From the home page into the tutor, through each kind.
	res, err := actOn(t, a, "go", pageState{}, map[string]string{"mode": modeTutor, "topic": "Cells"})
	if err != nil {
		t.Fatal(err)
	}
	st := stateOf(t, res.View)
	if st.Mode != modeTutor || st.Topic != "Cells" || st.Kind != kindConcept {
		t.Fatalf("%+v", st)
	}
	for _, k := range exerciseKinds {
		kind = k
		res, err = actOn(t, a, "tutor_kind", st, map[string]string{"kind": k})
		if err != nil {
			t.Fatal(err)
		}
		st = stateOf(t, res.View)
		res, err = actOn(t, a, "tutor_start", st, nil)
		if err != nil {
			t.Fatal(err)
		}
		st = stateOf(t, res.View)
		e, ok := a.exercise(st.Exercise)
		if !ok || e.Kind != k || e.Plain {
			t.Fatalf("%s: %+v", k, e)
		}
		// Ask for every hint, and one more than there are.
		for i := 0; i <= len(e.Hints); i++ {
			res, err = actOn(t, a, "tutor_hint", st, nil)
			if err != nil {
				t.Fatal(err)
			}
			st = stateOf(t, res.View)
		}
		if st.Hints != len(e.Hints) {
			t.Fatal(st.Hints, len(e.Hints))
		}
		answer := "B"
		if k != kindChoice {
			answer = "my answer"
		}
		if _, err := actOn(t, a, "tutor_answer", st, map[string]string{"answer": "  "}); err == nil {
			t.Fatal("an empty answer was submitted")
		}
		res, err = actOn(t, a, "tutor_answer", st, map[string]string{"answer": answer})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(fmt.Sprint(res.View.Sections), "参考答案") {
			t.Fatal("no result page")
		}
	}
	// The last answer was partial: reinforcement from the result page goes to the drafts.
	res, err = actOn(t, a, "reinforce_exercise", st, nil)
	if err != nil || stateOf(t, res.View).Mode != modeDrafts || len(a.st.Drafts) == 0 {
		t.Fatal(err)
	}
	if _, err := actOn(t, a, "go_topic", st, map[string]string{"topic": "Cells"}); err != nil {
		t.Fatal(err)
	}
	if _, err := actOn(t, a, "reinforce_topic", pageState{Mode: modeWeak, Topic: "Cells"}, nil); err != nil {
		t.Fatal(err)
	}
	// A card page offers practice for that card and changes its topic.
	cs := pageState{Mode: modeCard, Card: id}
	viewOf(t, a, cs)
	res, err = actOn(t, a, "go", cs, map[string]string{"mode": modeTutor, "card": id})
	if err != nil || stateOf(t, res.View).Card != id {
		t.Fatal(err)
	}
	if _, err := actOn(t, a, "set_topic", cs, map[string]string{"topic": "  Organelles \n"}); err != nil || a.st.Cards[id].Topic != "Organelles" {
		t.Fatal(err)
	}
	if _, err := actOn(t, a, "set_topic", pageState{Mode: modeCard, Card: "gone"}, map[string]string{"topic": "x"}); err == nil {
		t.Fatal("changed a missing card")
	}
	// Without a model: plain exercises and rough judgement still draw.
	h.mu.Lock()
	h.model = nil
	h.mu.Unlock()
	res, err = actOn(t, a, "tutor_start", pageState{Mode: modeTutor, Kind: kindChoice}, nil)
	if err != nil || res.Toast == "" {
		t.Fatal(err)
	}
	st = stateOf(t, res.View)
	if _, err := actOn(t, a, "tutor_answer", st, map[string]string{"answer": "不知道"}); err != nil {
		t.Fatal(err)
	}
	if _, err := actOn(t, a, "tutor_kind", st, map[string]string{"kind": "essay"}); err == nil {
		t.Fatal("an unknown kind was accepted")
	}
}

func TestPageStateBounds(t *testing.T) {
	a := testApp(t, &fakeHub{}, defaults())
	for _, st := range []pageState{
		{Status: "bogus"}, {Kind: "essay"}, {Hints: -1}, {Hints: maxHints + 1}, {Done: -1},
		{Topic: strings.Repeat("知", maxTopic+1)}, {Exercise: strings.Repeat("x", 65)}, {Card: strings.Repeat("c", 65)},
	} {
		if _, err := a.render(t.Context(), request("", st, nil)); err == nil {
			t.Fatalf("%+v was accepted", st)
		}
	}
	if _, err := a.action(t.Context(), request("go", pageState{}, map[string]string{"mode": "nowhere"})); err == nil {
		t.Fatal("an unknown page")
	}
	if _, err := a.action(t.Context(), request("nothing", pageState{}, nil)); err == nil {
		t.Fatal("an unknown action")
	}
}

func TestViewsConcurrent(t *testing.T) {
	// Pages render while tools, reviews and practice change the state; run with -race.
	h := &fakeHub{model: modelBy(map[string]string{"study.exercise": choiceExercise})}
	a := testApp(t, h, defaults())
	var cards []cardText
	for i := 0; i < 30; i++ {
		cards = append(cards, cardText{Front: fmt.Sprintf("Q%d", i), Back: "A", Topic: fmt.Sprintf("T%d", i%4)})
	}
	a.addDirect(cards, Source{Kind: srcManual})
	ids := []string{}
	for _, c := range a.st.sortedCards() {
		ids = append(ids, c.ID)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 64)
	for g := 0; g < 4; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 10; i++ {
				for _, m := range []string{modeHome, modeWeak, modeCards, modeTutor, modeReview} {
					v, err := a.view(context.Background(), pageState{Mode: m}, "")
					if err == nil {
						err = wire.CheckView(v)
					}
					if err != nil {
						errs <- err
						return
					}
				}
				if _, err := a.grade(ids[(g*10+i)%len(ids)], fsrs.Good); err != nil {
					errs <- err
					return
				}
				if _, err := a.practice(context.Background(), "", fmt.Sprintf("T%d", g), kindChoice); err != nil {
					errs <- err
					return
				}
				if _, err := a.callTool(context.Background(), "study_weak_points", json.RawMessage(`{}`)); err != nil {
					errs <- err
					return
				}
			}
		}(g)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	if len(a.st.Exercises) > maxExercises {
		t.Fatal(len(a.st.Exercises))
	}
}
