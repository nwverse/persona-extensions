package main

// The collaboration evaluation (design l0-leverage §11, 0.2.0-beta.52): with the memory, does the assistant avoid a
// collaboration incident that really happened (the user corrected it, swore at it, or had to say a thing again), and
// does it ask less, do less wrong, make the user repeat less? Understanding and the assistant's rules are judged
// together, by the outcome.
//
// Incidents are drawn by this program, never written by hand:
//
//   - the assistant's feedback memories (a directory of Markdown files with `type: feedback`, each written right after
//     an incident, with its day and the user's words), dated in [cutoff, end];
//   - the assistant's session transcripts (Claude Code JSONL): a user line in [cutoff, end] that reads as a
//     correction (a fixed list of cue words), with the request before it and what the assistant did in between.
//
// A model turns each candidate into a situation (what was asked and how things stood before the assistant's move,
// without the right move in it), the mistake and what the user wanted, or drops it as no incident. An incident a
// model without memory already handles is dropped (that model is the answering one; its answers are kept apart so the
// no-memory arm is asked afresh). Each arm's memory (hardeval's arms: none, scatter, overview, cogpipe, l0, resident,
// built from what was seen before the cutoff) then answers every situation, and another model judges each answer
// blind to the arm against the incident. Nothing here goes to a hub; the data stays in -out.

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/entry"
	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/memsrc"
	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/model"
	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/norm"
	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/overview"
	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/store"
)

// collabCue is what a user's correction reads like (the candidates; a model decides).
var collabCue = regexp.MustCompile(`不是|别再|不要再|又|说过|说了|重复|第[二三四五六]遍|为什么还|怎么还|他妈|错了|搞错|我说的是|早就|不用问|别问|别来问|直接做|直接往下|直接干|废话|脱裤子|搞混|弄混|搞错人|认错|张冠李戴|不是他|不是她|谁负责|他负责|她负责|哪个项目|不是这个项目|不是那个`)

var collabDay = regexp.MustCompile(`\b(1[0-2]|0?[1-9])-([0-3][0-9])\b`)

const (
	collabBatch     = 6
	collabMost      = 60 // incidents kept at most
	collabCandMost  = 160
	collabCtxMost   = 1800
	collabStepModel = "hardeval-collab"
)

type collabCand struct {
	ID     string `json:"id"`
	Source string `json:"source"` // memory | session
	Day    string `json:"day"`
	Text   string `json:"text"`
}

// collabIncident is one incident as the model wrote it from a candidate.
type collabIncident struct {
	ID        string `json:"id"`
	Source    string `json:"source"`
	Day       string `json:"day"`
	Kind      string `json:"kind"` // corrected | scolded | repeated
	Situation string `json:"situation"`
	Mistake   string `json:"mistake"`
	Want      string `json:"want"`
	// Entity: avoiding it needed who someone is, who is responsible for what, or which project or thing was meant
	// (design l0-leverage §12): the incidents the entity relations are for, counted apart.
	Entity bool `json:"entity,omitempty"`
	// Recall is what the arm's recall brought in for this incident (collabRecall), shown to the answer only.
	Recall string `json:"-"`
}

type collabSet struct {
	Cutoff     string           `json:"cutoff"`
	End        string           `json:"end"`
	Candidates int              `json:"candidates"`
	NoIncident int              `json:"no_incident"`
	Duplicates int              `json:"duplicates"`
	Avoided    int              `json:"avoided_without_memory"`
	Incidents  []collabIncident `json:"incidents"`
}

const collabExtractPrompt = `你在从用户和 AI 助手的真实记录里整理「协作事故」：用户纠正过助手、骂过助手、或者不得不把一件事重复交代。每条材料是一份助手事后写的反馈记忆，或者一段会话（用户的请求、助手的动作、用户随后的话）。

对每条材料判断它是不是一次协作事故（incident）。是的话写出：
- kind：corrected（纠正）、scolded（骂了）、repeated（重复交代）之一；
- situation：事故发生前的情境，以助手的视角写：用户要什么、事情进行到哪一步、助手手上有什么。只写事实，不能透露正确做法，也不能写出助手后来做错了什么或用户的反应。80–200 字。人名、项目名、系统名照材料原样写（评测只在本机，不进仓库），不写邮箱、账号、密钥。
- mistake：助手当时怎么做错了，一句话。
- want：用户真正要的是什么样的做法，一句话，写成能推广到同类情境的做法。
- entity：要避开这次事故，是否必须知道实体关系：某人是谁、谁负责什么、谁和谁什么关系、说的是哪个项目或哪样东西（搞混人或项目、不知道谁负责、找错人）。是为 true，只靠做事习惯就能避开的为 false。

不是协作事故的（只是普通追问、闲聊、用户改主意、技术报错）incident 为 false，其余字段留空。输出 JSON。`

const collabExtractSchema = `{"type":"object","additionalProperties":false,"required":["items"],"properties":{"items":{"type":"array","items":{"type":"object","additionalProperties":false,"required":["id","incident","kind","situation","mistake","want","entity"],"properties":{"id":{"type":"string"},"incident":{"type":"boolean"},"entity":{"type":"boolean"},"kind":{"type":"string","enum":["","corrected","scolded","repeated"]},"situation":{"type":"string"},"mistake":{"type":"string"},"want":{"type":"string"}}}}}}`

const collabAnswerPrompt = `你是这位用户的主 AI 助手：能和用户对话、能派 Agent 干活、能执行命令和发授权卡。下面给你对这位用户的记忆（可能为空）和若干个互相独立的情境。对每个情境，写出你此刻实际会怎么做：如果要回复用户，先写那句回复；再写你要执行的动作。具体、简短，每个不超过 150 字。只依据记忆和情境本身。输出 JSON。`

const collabAnswerSchema = `{"type":"object","additionalProperties":false,"required":["answers"],"properties":{"answers":{"type":"array","items":{"type":"object","additionalProperties":false,"required":["id","response"],"properties":{"id":{"type":"string"},"response":{"type":"string"}}}}}}`

const collabJudgePrompt = `你在评判 AI 助手在一个真实协作情境里的应对。每条给你：情境、当时真实发生的事故（助手怎么做错了）、用户真正要的做法、以及一份待评的应对（你不知道它来自哪个版本）。判断：
- avoided：这份应对是否避开了这次事故，并且做法符合用户要的（true/false）；
- needless_ask：是否去问了用户本不需要问的事、或请用户确认他已经同意过的事；
- wrong_action：是否做了用户不要的事；
- repeat：照这份应对，用户是否还得把这件事再说一遍。
只按应对文字判断，不因为写得长或客气加分。输出 JSON。`

const collabJudgeSchema = `{"type":"object","additionalProperties":false,"required":["verdicts"],"properties":{"verdicts":{"type":"array","items":{"type":"object","additionalProperties":false,"required":["id","avoided","needless_ask","wrong_action","repeat"],"properties":{"id":{"type":"string"},"avoided":{"type":"boolean"},"needless_ask":{"type":"boolean"},"wrong_action":{"type":"boolean"},"repeat":{"type":"boolean"}}}}}}`

// collabMemoryCands are the feedback memories dated in [cutoff, end].
func collabMemoryCands(dir, cutoff, end string) ([]collabCand, error) {
	files, err := filepath.Glob(filepath.Join(dir, "*.md"))
	if err != nil {
		return nil, err
	}
	var out []collabCand
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			return nil, err
		}
		s := string(raw)
		if !strings.Contains(s, "type: feedback") {
			continue
		}
		body := s
		if parts := strings.SplitN(s, "\n---", 2); strings.HasPrefix(s, "---") && len(parts) == 2 {
			body = parts[1]
		}
		m := collabDay.FindStringSubmatch(body)
		if m == nil {
			continue
		}
		mo, _ := strconv.Atoi(m[1])
		dd, _ := strconv.Atoi(m[2])
		day := fmt.Sprintf("%s-%02d-%02d", cutoff[:4], mo, dd)
		if day < cutoff || day > end {
			continue
		}
		out = append(out, collabCand{ID: "m-" + heHash(filepath.Base(f))[:10], Source: "memory", Day: day, Text: heClip(body, 2400)})
	}
	return out, nil
}

// collabLine is one line of a transcript: a user's own text, or the assistant's text and calls.
type collabLine struct {
	user bool
	day  string
	text string
}

func collabRead(path string) ([]collabLine, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []collabLine
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	for sc.Scan() {
		var j struct {
			Type      string `json:"type"`
			Timestamp string `json:"timestamp"`
			IsMeta    bool   `json:"isMeta"`
			Message   struct {
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal(sc.Bytes(), &j) != nil || j.IsMeta || (j.Type != "user" && j.Type != "assistant") {
			continue
		}
		day := ""
		if len(j.Timestamp) >= 10 {
			day = j.Timestamp[:10] // UTC; close enough for a cutoff a week back
		}
		var text string
		if json.Unmarshal(j.Message.Content, &text) == nil {
			if j.Type == "user" && !strings.HasPrefix(strings.TrimSpace(text), "<") {
				out = append(out, collabLine{user: true, day: day, text: text})
			}
			continue
		}
		var parts []struct {
			Type  string          `json:"type"`
			Text  string          `json:"text"`
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
		}
		if json.Unmarshal(j.Message.Content, &parts) != nil {
			continue
		}
		var b strings.Builder
		for _, p := range parts {
			switch {
			case p.Type == "text" && j.Type == "user" && !strings.HasPrefix(strings.TrimSpace(p.Text), "<"):
				b.WriteString(p.Text)
			case p.Type == "text" && j.Type == "assistant":
				b.WriteString(p.Text + "\n")
			case p.Type == "tool_use":
				b.WriteString("[" + p.Name + " " + heClip(string(p.Input), 160) + "]\n")
			}
		}
		if t := strings.TrimSpace(b.String()); t != "" {
			out = append(out, collabLine{user: j.Type == "user", day: day, text: t})
		}
	}
	return out, sc.Err()
}

// collabSessionCands are the user lines in [cutoff, end] that read as a correction, with their context.
func collabSessionCands(dirs []string, cutoff, end string) ([]collabCand, error) {
	var out []collabCand
	for _, dir := range dirs {
		files, err := filepath.Glob(filepath.Join(dir, "*.jsonl"))
		if err != nil {
			return nil, err
		}
		for _, f := range files {
			lines, err := collabRead(f)
			if err != nil {
				return nil, err
			}
			for i, l := range lines {
				if !l.user || l.day < cutoff || l.day > end || len(l.text) > 1500 || !collabCue.MatchString(l.text) {
					continue
				}
				// The request before: the last user line before the assistant's move; the move: what is between.
				req, move := -1, ""
				var mv []string
				for k := i - 1; k >= 0; k-- {
					if lines[k].user {
						req = k
						break
					}
					mv = append([]string{lines[k].text}, mv...)
				}
				if req < 0 || len(mv) == 0 {
					continue
				}
				move = strings.Join(mv, "\n")
				text := "用户的请求：" + heClip(lines[req].text, 600) + "\n助手的动作：" + heClip(move, collabCtxMost) +
					"\n用户随后说：" + heClip(l.text, 600)
				out = append(out, collabCand{ID: "s-" + heHash(f + fmt.Sprint(i))[:10], Source: "session", Day: l.day, Text: text})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return heHash(out[i].ID) < heHash(out[j].ID) })
	return out, nil
}

// collabCall runs one batched request and decodes its JSON into v.
func collabCall(ctx context.Context, m model.Model, system, schema string, in any, v any) error {
	raw, _ := json.Marshal(in)
	resp, err := m.Complete(ctx, model.Request{Step: collabStepModel, Tier: model.Small, System: system, User: string(raw),
		MaxTokens: 6000, Schema: json.RawMessage(schema)})
	if err != nil {
		return err
	}
	return json.Unmarshal([]byte(resp.Text), v)
}

// collabBatches runs fn over xs in batches, at most the shared limiter's calls at once; the first error.
func collabBatches[T any](xs []T, fn func([]T) error) error {
	var wg sync.WaitGroup
	var mu sync.Mutex
	var first error
	for b := 0; b < len(xs); b += collabBatch {
		batch := xs[b:min(len(xs), b+collabBatch)]
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := fn(batch); err != nil && !errors.Is(err, model.ErrRefused) {
				mu.Lock()
				if first == nil {
					first = err
				}
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	return first
}

type collabVerdict struct {
	ID          string `json:"id"`
	Avoided     bool   `json:"avoided"`
	NeedlessAsk bool   `json:"needless_ask"`
	WrongAction bool   `json:"wrong_action"`
	Repeat      bool   `json:"repeat"`
}

// collabAnswer answers every incident with memory, then judges the answers; the verdicts by incident id.
// attempt > 0 asks again (a repeat): it is in the input, so the answer is not the cached one.
func collabAnswer(ctx context.Context, answer, judge model.Model, memory string, incs []collabIncident, attempt int) (map[string]collabVerdict, map[string]string, error) {
	type q struct {
		ID        string `json:"id"`
		Situation string `json:"situation"`
	}
	resp := map[string]string{}
	var mu sync.Mutex
	err := collabBatches(incs, func(b []collabIncident) error {
		in := struct {
			Attempt    int    `json:"attempt,omitempty"`
			Memory     string `json:"memory"`
			Situations []q    `json:"situations"`
		}{Attempt: attempt, Memory: memory}
		for _, x := range b {
			s := x.Situation
			if x.Recall != "" {
				s += "\n\n按需召回（情境里提到的人、项目带出的记忆）：\n" + x.Recall
			}
			in.Situations = append(in.Situations, q{x.ID, s})
		}
		var out struct {
			Answers []struct{ ID, Response string } `json:"answers"`
		}
		if err := collabCall(ctx, answer, collabAnswerPrompt, collabAnswerSchema, in, &out); err != nil {
			return err
		}
		mu.Lock()
		for _, a := range out.Answers {
			resp[a.ID] = a.Response
		}
		mu.Unlock()
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	type jq struct {
		ID        string `json:"id"`
		Situation string `json:"situation"`
		Mistake   string `json:"mistake"`
		Want      string `json:"want"`
		Response  string `json:"response"`
	}
	verdicts := map[string]collabVerdict{}
	err = collabBatches(incs, func(b []collabIncident) error {
		var in struct {
			Attempt int  `json:"attempt,omitempty"`
			Items   []jq `json:"items"`
		}
		in.Attempt = attempt
		for _, x := range b {
			in.Items = append(in.Items, jq{x.ID, x.Situation, x.Mistake, x.Want, resp[x.ID]})
		}
		var out struct {
			Verdicts []collabVerdict `json:"verdicts"`
		}
		if err := collabCall(ctx, judge, collabJudgePrompt, collabJudgeSchema, in, &out); err != nil {
			return err
		}
		mu.Lock()
		for _, v := range out.Verdicts {
			verdicts[v.ID] = v
		}
		mu.Unlock()
		return nil
	})
	return verdicts, resp, err
}

func collabBuild(args []string) error {
	fs := flag.NewFlagSet("hardeval collab-build", flag.ContinueOnError)
	memDir := fs.String("memory-dir", "", "the assistant's feedback memory directory (read only)")
	sessions := fs.String("sessions", "", "comma-separated directories of session transcripts (*.jsonl, read only)")
	cutoff := fs.String("cutoff", "", "first held-out day, YYYY-MM-DD")
	end := fs.String("end", "", "last held-out day")
	out := fs.String("out", "", "directory for collab.json and the caches (keep it local)")
	cmd := fs.String("model-cmd", "claude", "the model command line")
	mdl := fs.String("model", "claude-sonnet-5-5", "the model that writes the incidents and answers without memory (the answering one)")
	jdg := fs.String("judge-model", "claude-opus-5-5", "the model that judges")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *cutoff == "" || *end == "" || *out == "" || (*memDir == "" && *sessions == "") {
		return errors.New("-cutoff, -end, -out and -memory-dir or -sessions are required")
	}
	if err := os.MkdirAll(*out, 0o700); err != nil {
		return err
	}
	var cands []collabCand
	if *memDir != "" {
		c, err := collabMemoryCands(*memDir, *cutoff, *end)
		if err != nil {
			return err
		}
		cands = append(cands, c...)
	}
	if *sessions != "" {
		c, err := collabSessionCands(strings.Split(*sessions, ","), *cutoff, *end)
		if err != nil {
			return err
		}
		cands = append(cands, c[:min(len(c), collabCandMost)]...)
	}
	ctx := context.Background()
	ext := heCLI(*cmd, *mdl, filepath.Join(*out, "extract-cache"))
	set := collabSet{Cutoff: *cutoff, End: *end, Candidates: len(cands)}
	var mu sync.Mutex
	byID := map[string]collabCand{}
	for _, c := range cands {
		byID[c.ID] = c
	}
	var incs []collabIncident
	err := collabBatches(cands, func(b []collabCand) error {
		type item struct {
			ID   string `json:"id"`
			Text string `json:"text"`
		}
		var in struct {
			Items []item `json:"items"`
		}
		for _, c := range b {
			in.Items = append(in.Items, item{c.ID, c.Text})
		}
		var o struct {
			Items []struct {
				ID, Kind, Situation, Mistake, Want string
				Incident, Entity                   bool
			} `json:"items"`
		}
		if err := collabCall(ctx, ext, collabExtractPrompt, collabExtractSchema, in, &o); err != nil {
			return err
		}
		mu.Lock()
		defer mu.Unlock()
		for _, x := range o.Items {
			c, ok := byID[x.ID]
			if !ok {
				continue
			}
			if !x.Incident || strings.TrimSpace(x.Situation) == "" || strings.TrimSpace(x.Want) == "" {
				set.NoIncident++
				continue
			}
			incs = append(incs, collabIncident{ID: c.ID, Source: c.Source, Day: c.Day, Kind: x.Kind, Situation: x.Situation,
				Mistake: x.Mistake, Want: x.Want, Entity: x.Entity})
		}
		return nil
	})
	if err != nil {
		return err
	}
	// One incident told twice (a memory written after a session's correction): the memory's stays.
	sort.Slice(incs, func(i, j int) bool {
		if incs[i].Source != incs[j].Source {
			return incs[i].Source == "memory"
		}
		return heHash(incs[i].ID) < heHash(incs[j].ID)
	})
	var kept []collabIncident
	for _, x := range incs {
		dup := false
		for _, k := range kept {
			if k.Day == x.Day && norm.Jaccard(k.Want, x.Want) >= 0.35 {
				dup = true
				break
			}
		}
		if dup {
			set.Duplicates++
			continue
		}
		kept = append(kept, x)
	}
	// Drop what the answering model gets right without memory (its answers kept apart from the run's).
	ans := heCLI(*cmd, *mdl, filepath.Join(*out, "filter-cache"))
	judge := heCLI(*cmd, *jdg, filepath.Join(*out, "filter-cache"))
	v, _, err := collabAnswer(ctx, ans, judge, "", kept, 0)
	if err != nil {
		return err
	}
	for _, x := range kept {
		if v[x.ID].Avoided {
			set.Avoided++
			continue
		}
		if len(set.Incidents) < collabMost {
			set.Incidents = append(set.Incidents, x)
		}
	}
	b, _ := json.MarshalIndent(set, "", " ")
	if err := os.WriteFile(filepath.Join(*out, "collab.json"), b, 0o600); err != nil {
		return err
	}
	n := map[string]int{}
	for _, x := range set.Incidents {
		n[x.Source]++
	}
	fmt.Printf("hardeval collab-build: %d candidates, %d not an incident, %d duplicates, %d avoided without memory, %d incidents (memory %d, session %d)\n",
		set.Candidates, set.NoIncident, set.Duplicates, set.Avoided, len(set.Incidents), n["memory"], n["session"])
	return nil
}

type collabResult struct {
	Model   string                                   `json:"model"`
	Judge   string                                   `json:"judge"`
	Cutoff  string                                   `json:"cutoff"` // "per incident": each incident's memory is what was there the day before
	N       int                                      `json:"n"`
	Repeats int                                      `json:"repeats"`
	Score   map[string]map[string]map[string]float64 `json:"score"` // budget → arm → avoided | needless_ask | wrong_action | repeat
	Bytes   map[string]map[string]int                `json:"bytes"` // budget → arm → mean bytes of the memory
	// PerInc: budget → arm → incident → share of the repeats it was avoided in.
	PerInc map[string]map[string]map[string]float64 `json:"per_incident"`
	// Entity are the incidents that need entity relations (collabIncident.Entity), reported apart.
	Entity []string `json:"entity,omitempty"`
}

func collabRun(args []string) error {
	fs := flag.NewFlagSet("hardeval collab-run", flag.ContinueOnError)
	statePath := fs.String("state", "", "a copy of state.json (read only)")
	ovPath := fs.String("overview", "", "patterns built before the cutoff (overview-backtest.json)")
	cogDir := fs.String("cogpipe-dir", "", "the old pipeline's injected texts, one <YYYY-MM-DD>.md a build: each incident gets the last one before its day")
	out := fs.String("out", "", "the directory collab-build wrote")
	cmd := fs.String("model-cmd", "claude", "the model command line")
	mdl := fs.String("model", "claude-sonnet-5-5", "the model that answers")
	jdg := fs.String("judge-model", "claude-opus-5-5", "the model that judges")
	label := fs.String("label", "baseline", "name of this reading (collab-<label>.json)")
	arms := fs.String("arms", strings.Join(heArms, ","), "arms to answer")
	budgets := fs.String("budgets", "1400,4096", "memory budgets in bytes")
	repeats := fs.Int("repeats", 3, "times each arm answers (and is judged), averaged")
	recallBytes := fs.Int("recall", 2048, "bytes of recall per incident for the recall arms (recall: the standing set and what the incident's names bring in; recall-noent: the same without the world and people entries)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	st, err := readState(*statePath)
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(filepath.Join(*out, "collab.json"))
	if err != nil {
		return err
	}
	var set collabSet
	if err := json.Unmarshal(raw, &set); err != nil {
		return err
	}
	ov, _, err := heInputs(*ovPath, "")
	if err != nil {
		return err
	}
	cogs, err := collabCogpipe(*cogDir)
	if err != nil {
		return err
	}
	// Incidents by day: each day's memory is what was first seen before it.
	byDay := map[string][]collabIncident{}
	for _, x := range set.Incidents {
		byDay[x.Day] = append(byDay[x.Day], x)
	}
	var days []string
	for d := range byDay {
		days = append(days, d)
	}
	sort.Strings(days)
	ans := heCLI(*cmd, *mdl, filepath.Join(*out, "cache"))
	judge := heCLI(*cmd, *jdg, filepath.Join(*out, "cache"))
	res := collabResult{Model: *mdl, Judge: *jdg, Cutoff: "per incident", N: len(set.Incidents), Repeats: *repeats,
		Score: map[string]map[string]map[string]float64{}, Bytes: map[string]map[string]int{}, PerInc: map[string]map[string]map[string]float64{}}
	for _, inc := range set.Incidents {
		if inc.Entity {
			res.Entity = append(res.Entity, inc.ID)
		}
	}
	names := collabNames(st)
	samples := map[string]string{}
	responses := map[string]map[string]string{}
	f := heFilter{leak: map[string]bool{}, units: map[string]bool{}} // the day is the leak: an incident's own day and later are left out
	armList := strings.Split(*arms, ",")
	for _, bs := range strings.Split(*budgets, ",") {
		budget := 0
		fmt.Sscan(bs, &budget)
		bk := fmt.Sprint(budget)
		res.Score[bk], res.Bytes[bk], res.PerInc[bk] = map[string]map[string]float64{}, map[string]int{}, map[string]map[string]float64{}
		for _, arm := range armList {
			sc := map[string]float64{}
			per := map[string]float64{}
			bytes := 0
			for _, d := range days {
				mems := heMemories(st, ov, collabCogFor(cogs, d), f, d, budget)
				mem, incs := mems[arm], byDay[d]
				if arm == "recall" || arm == "recall-noent" {
					mem = mems["resident"]
					incs = slices.Clone(incs)
					for i := range incs {
						incs[i].Recall = names.recall(st, f, d, incs[i].Situation, mem, *recallBytes, arm == "recall")
						bytes += len(incs[i].Recall)
						samples[bk+"/"+arm+"/"+incs[i].ID] = incs[i].Recall
					}
				}
				bytes += len(mem) * len(incs)
				samples[bk+"/"+arm+"/"+d] = mem
				for r := 0; r < *repeats; r++ {
					v, rs, err := collabAnswer(context.Background(), ans, judge, mem, incs, r)
					if err != nil {
						return fmt.Errorf("%s/%s/%s: %w", bk, arm, d, err)
					}
					if r == 0 {
						if responses[bk+"/"+arm] == nil {
							responses[bk+"/"+arm] = map[string]string{}
						}
						for k, x := range rs {
							responses[bk+"/"+arm][k] = x
						}
					}
					for _, x := range byDay[d] {
						vv := v[x.ID]
						sc["avoided"] += b2f(vv.Avoided)
						sc["needless_ask"] += b2f(vv.NeedlessAsk)
						sc["wrong_action"] += b2f(vv.WrongAction)
						sc["repeat"] += b2f(vv.Repeat)
						per[x.ID] += b2f(vv.Avoided) / float64(*repeats)
					}
				}
			}
			n := float64(max(1, len(set.Incidents)**repeats))
			for k := range sc {
				sc[k] /= n
			}
			res.Score[bk][arm], res.PerInc[bk][arm] = sc, per
			res.Bytes[bk][arm] = bytes / max(1, len(set.Incidents))
		}
	}
	for name, v := range map[string]any{"collab-" + *label + ".json": res, "collab-memories-" + *label + ".json": samples,
		"collab-responses-" + *label + ".json": responses} {
		b, _ := json.MarshalIndent(v, "", " ")
		if err := os.WriteFile(filepath.Join(*out, name), b, 0o600); err != nil {
			return err
		}
	}
	md := collabTable(res, armList)
	if err := os.WriteFile(filepath.Join(*out, "collab-"+*label+".md"), []byte(md), 0o600); err != nil {
		return err
	}
	fmt.Print(md)
	return nil
}

// collabCogpipe reads a directory of injected texts named by their build day; nil without one.
func collabCogpipe(dir string) (map[string][]string, error) {
	if dir == "" {
		return nil, nil
	}
	files, err := filepath.Glob(filepath.Join(dir, "*.md"))
	if err != nil {
		return nil, err
	}
	out := map[string][]string{}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			return nil, err
		}
		var ls []string
		for _, l := range strings.Split(string(b), "\n") {
			if t := strings.TrimSpace(l); t != "" && !strings.HasPrefix(t, "<!--") { // its build notes are not memory
				ls = append(ls, l)
			}
		}
		out[strings.TrimSuffix(filepath.Base(f), ".md")] = ls
	}
	return out, nil
}

// collabCogFor is the last build before day.
func collabCogFor(cogs map[string][]string, day string) []string {
	best := ""
	for d := range cogs {
		if d < day && d > best {
			best = d
		}
	}
	return cogs[best]
}

func b2f(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

// collabTable: per budget, each arm's share avoided and of needless asks, wrong actions and repeats (over incidents
// and repeats), and per incident against the new standing set (resident): win/tie/loss for it by the share avoided.
func collabTable(r collabResult, arms []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "collaboration evaluation, answer %s, judge %s, memory per incident (the day before), %d incidents × %d repeats\n",
		r.Model, r.Judge, r.N, r.Repeats)
	var bks []string
	for bk := range r.Score {
		bks = append(bks, bk)
	}
	sort.Slice(bks, func(i, j int) bool { return len(bks[i]) < len(bks[j]) || len(bks[i]) == len(bks[j]) && bks[i] < bks[j] })
	for _, bk := range bks {
		fmt.Fprintf(&b, "\n### budget %s bytes\n\n| arm | bytes | avoided | needless asks | wrong actions | user repeats | vs resident (win/tie/loss for resident) | entity incidents avoided (%d) |\n|---|---|---|---|---|---|---|---|\n", bk, len(r.Entity))
		for _, arm := range arms {
			s := r.Score[bk][arm]
			w, t, l := 0, 0, 0
			for id, v := range r.PerInc[bk][arm] {
				rv := r.PerInc[bk]["resident"][id]
				switch {
				case rv == v:
					t++
				case rv > v:
					w++
				default:
					l++
				}
			}
			ent := "-"
			if len(r.Entity) > 0 {
				sum := 0.0
				for _, id := range r.Entity {
					sum += r.PerInc[bk][arm][id]
				}
				ent = fmt.Sprintf("%.0f%%", 100*sum/float64(len(r.Entity)))
			}
			fmt.Fprintf(&b, "| %s | %d | %.0f%% | %.0f%% | %.0f%% | %.0f%% | %d/%d/%d | %s |\n", arm, r.Bytes[bk][arm], 100*s["avoided"],
				100*s["needless_ask"], 100*s["wrong_action"], 100*s["repeat"], w, t, l, ent)
		}
	}
	return b.String()
}

// heInputs reads the overview's backtest patterns and the external injected text, either optional.
func heInputs(ovPath, cogPath string) (*overview.State, []string, error) {
	var ov *overview.State
	if ovPath != "" {
		b, err := os.ReadFile(ovPath)
		if err != nil {
			return nil, nil, err
		}
		ov = &overview.State{}
		if err := json.Unmarshal(b, ov); err != nil {
			return nil, nil, err
		}
	}
	var cog []string
	if cogPath != "" {
		b, err := os.ReadFile(cogPath)
		if err != nil {
			return nil, nil, err
		}
		for _, l := range strings.Split(string(b), "\n") {
			if strings.TrimSpace(l) != "" {
				cog = append(cog, l)
			}
		}
	}
	return ov, cog, nil
}

// collabNameIndex maps the names entries can be recalled by (memsrc.Subjects: a card's person, the object, the
// person or project a rule is for, the scopes) to the entries, for the recall arms.
type collabNameIndex map[string][]*entry.Entry

func collabNames(st *store.State) collabNameIndex {
	idx := collabNameIndex{}
	for _, e := range st.Entries {
		if !memsrc.Pageable(st, e) || len(e.Cats()) > 0 || e.OriginSpace != "" || e.Inferred {
			continue
		}
		for _, n := range memsrc.Subjects(st, e) {
			if r := []rune(n); len(r) < 2 || (len(n) == len(r) && len(r) < 3) || collabGeneric[n] {
				continue
			}
			idx[n] = append(idx[n], e)
		}
	}
	// A name behind very many entries ("Persona", a word for the user) names nothing in particular: the hub's recall
	// would not bring all of them in by it either.
	for n, es := range idx {
		if len(es) > collabNameMost {
			delete(idx, n)
		}
	}
	return idx
}

// collabGeneric are words for the user and the assistant, never a subject to recall by.
var collabGeneric = map[string]bool{"用户": true, "助手": true, "我": true, "the user": true, "user": true, "assistant": true, "AI": true}

// collabIdent is an identifier-like name in a situation (a repository, a service, a tool): long enough to name one.
var collabIdent = regexp.MustCompile(`[A-Za-z][A-Za-z0-9_.]*-[A-Za-z0-9_.-]{3,}|[A-Za-z][A-Za-z0-9_]{7,}`)

// collabNameMost is the most entries one name may bring in before it counts as too common to name anything.
const collabNameMost = 150

// recall is what mentioning the incident's names brings in, as the hub's recall would: the entries whose subjects the
// situation names, first seen before the day, not already in the standing memory, highest rank first, in budget
// bytes. entities false leaves out the world and people entries (the ablation).
func (idx collabNameIndex) recall(st *store.State, f heFilter, day, situation, standing string, budget int, entities bool) string {
	// hits: how specifically the situation's names point at an entry, each name weighing by how few entries it is
	// behind (idf), so "Persona" brings in less than a repository's own name.
	hits := map[string]float64{}
	total := float64(max(1, len(st.Entries)))
	idf := func(df int) float64 { return math.Log(total / float64(1+df)) }
	var cands []*entry.Entry
	seen := map[string]bool{}
	add := func(e *entry.Entry, w float64) {
		if !seen[e.ID] {
			seen[e.ID] = true
			cands = append(cands, e)
		}
		hits[e.ID] += w
	}
	for n, es := range idx {
		if !strings.Contains(situation, n) {
			continue
		}
		w := idf(len(es))
		for _, e := range es {
			if f.entryOut(st, e, day) || (!entities && (e.Facet == entry.World || e.Facet == entry.People)) {
				continue
			}
			add(e, w)
		}
	}
	// A fact that names the thing in its text without carrying it as a subject (an older world line) is found by the
	// name too, as a search would.
	if entities {
		names := map[string]bool{}
		for n := range idx {
			if strings.Contains(situation, n) && len([]rune(n)) >= 4 {
				names[n] = true
			}
		}
		for _, n := range collabIdent.FindAllString(situation, -1) { // a repository, a service, a command named in it
			names[n] = true
		}
		for n := range names {
			var found []*entry.Entry
			for _, e := range st.Entries {
				if (e.Facet == entry.World || e.Facet == entry.People) && strings.Contains(e.Shown(), n) {
					found = append(found, e)
				}
			}
			w := idf(len(found))
			for _, e := range found {
				if !memsrc.Pageable(st, e) || len(e.Cats()) > 0 || e.OriginSpace != "" || e.Inferred || f.entryOut(st, e, day) {
					continue
				}
				add(e, w)
			}
		}
	}
	sort.Slice(cands, func(i, j int) bool {
		a, b := cands[i], cands[j]
		if hits[a.ID] != hits[b.ID] {
			return hits[a.ID] > hits[b.ID]
		}
		if ra, rb := memsrc.Rank(a), memsrc.Rank(b); ra != rb {
			return ra > rb
		}
		if a.Last != b.Last {
			return a.Last > b.Last
		}
		return a.ID < b.ID
	})
	var lines []string
	for _, e := range cands {
		if len(lines) > 64 {
			break
		}
		t := heClip(e.Shown(), 200)
		if strings.Contains(standing, t) {
			continue
		}
		lines = append(lines, "- "+t)
	}
	return heFit(lines, budget)
}
