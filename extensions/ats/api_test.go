package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/sdk/go/ext"
	"github.com/nwverse/persona/sdk/go/exttest"
	"github.com/nwverse/persona/sdk/go/wire"
)

// newTestApp is the hub program over a test queue, with one allowed repository directory and no dispatcher.
func newTestApp(t *testing.T) *atsApp {
	t.Helper()
	a := newATSApp(newTestQueue(t, DefaultQueueConfig()))
	a.cfg.Machines, a.cfg.Repos = []string{testMachine}, []string{"/srv/code"}
	a.nowFunc = func() time.Time { return testStart }
	return a
}

type apiAnswer struct {
	Status int
	Code   string         `json:"code"`
	Error  string         `json:"error"`
	Body   map[string]any `json:"-"`
}

func call(t *testing.T, a *atsApp, method, path, body string, header ...string) apiAnswer {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	rec := httptest.NewRecorder()
	a.handler().ServeHTTP(rec, req)
	ans := apiAnswer{Status: rec.Code}
	if rec.Body.Len() > 0 && strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") {
		if err := json.Unmarshal(rec.Body.Bytes(), &ans.Body); err != nil {
			t.Fatalf("%s %s answered %q", method, path, rec.Body.String())
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &ans)
	}
	return ans
}

func taskOf(t *testing.T, ans apiAnswer) map[string]any {
	t.Helper()
	task, ok := ans.Body["task"].(map[string]any)
	if !ok {
		t.Fatalf("the answer has no task: %d %v", ans.Status, ans.Body)
	}
	return task
}

const validTask = `{"todo_id":"todo_1","title":"Fix the login page","prompt":"Fix it","repo":"/srv/code/app","acceptance":"go test ./..."}`

func TestPostTaskQueuesAReadOnlyTaskForTheTodo(t *testing.T) {
	a := newTestApp(t)
	ans := call(t, a, "POST", "/tasks", validTask, "X-Persona-Role", "extension")
	if ans.Status != http.StatusCreated || ans.Body["created"] != true {
		t.Fatalf("answer: %d %v", ans.Status, ans.Body)
	}
	task := taskOf(t, ans)
	if task["state"] != StateQueued || task["permission_profile"] != PermissionReadonly || task["todo_id"] != "todo_1" ||
		task["priority"] != PriorityP2 || task["repo"] != "/srv/code/app" || task["acceptance"] != "go test ./..." || task["agent_type"] != AgentClaude {
		t.Fatalf("task: %v", task)
	}
	stored, err := a.queue.GetTask(task["id"].(string))
	if err != nil || stored.Source != AssistantSource || stored.Labels[PersonaTodoLabel] != "todo_1" || stored.AgentType != AgentClaude || !stored.SubmittedAt.Equal(testStart) {
		t.Fatalf("stored: %+v %v", stored, err)
	}

	// The same idempotency key: the same task, not a second one.
	keyed := `{"todo_id":"todo_1","prompt":"Again","repo":"~/work/site","permission_profile":"default","priority":"p0","difficulty":"hard","idempotency_key":"k1"}`
	first := call(t, a, "POST", "/tasks", keyed)
	second := call(t, a, "POST", "/tasks", keyed)
	if first.Status != http.StatusCreated || second.Status != http.StatusOK || second.Body["created"] != false ||
		taskOf(t, first)["id"] != taskOf(t, second)["id"] {
		t.Fatalf("replay: %d then %d %v", first.Status, second.Status, second.Body)
	}
	if w := taskOf(t, first); w["permission_profile"] != PermissionDefault || w["priority"] != PriorityP0 || w["model_tier"] != TierStrong {
		t.Fatalf("a writing task: %v", w)
	}
}

func TestPostTaskCanChooseAnEngine(t *testing.T) {
	a := newTestApp(t)
	body := `{"todo_id":"todo_1","prompt":"Fix it","repo":"/srv/code/app","agent_type":"codex","idempotency_key":"engine-choice"}`
	first := call(t, a, "POST", "/tasks", body)
	if first.Status != http.StatusCreated || taskOf(t, first)["agent_type"] != AgentCodex {
		t.Fatalf("explicit engine: %d %v", first.Status, first.Body)
	}
	stored, err := a.queue.GetTask(taskOf(t, first)["id"].(string))
	if err != nil || stored.AgentType != AgentCodex {
		t.Fatalf("stored engine: %+v %v", stored, err)
	}

	// An idempotent replay keeps the first task's engine, even if the retry asks for another one.
	replay := call(t, a, "POST", "/tasks", strings.Replace(body, `"agent_type":"codex"`, `"agent_type":"claude"`, 1))
	if replay.Status != http.StatusOK || replay.Body["created"] != false || taskOf(t, replay)["agent_type"] != AgentCodex ||
		taskOf(t, replay)["id"] != taskOf(t, first)["id"] {
		t.Fatalf("engine-changing replay: %d %v", replay.Status, replay.Body)
	}
}

func TestPostTaskRefusesWhatItMustNotQueue(t *testing.T) {
	a := newTestApp(t)
	long := strings.Repeat("x", maxPromptBytes+1)
	for name, tc := range map[string]struct {
		body   string
		status int
		code   string
	}{
		"no to-do":                {`{"prompt":"x","repo":"/srv/code/app"}`, 400, "invalid_todo_id"},
		"a to-do id with a slash": {`{"todo_id":"a/b","prompt":"x","repo":"/srv/code/app"}`, 400, "invalid_todo_id"},
		"no prompt":               {`{"todo_id":"todo_1","prompt":" ","repo":"/srv/code/app"}`, 400, "invalid_prompt"},
		"a prompt too long":       {`{"todo_id":"todo_1","prompt":"` + long + `","repo":"/srv/code/app"}`, 400, "invalid_prompt"},
		"no repository":           {`{"todo_id":"todo_1","prompt":"x"}`, 400, "invalid_repo"},
		"a relative repository":   {`{"todo_id":"todo_1","prompt":"x","repo":"code/app"}`, 400, "invalid_repo"},
		"a repository with ..":    {`{"todo_id":"todo_1","prompt":"x","repo":"/srv/code/../../etc"}`, 400, "invalid_repo"},
		"a repository elsewhere":  {`{"todo_id":"todo_1","prompt":"x","repo":"/srv/other/app"}`, 403, "repo_not_allowed"},
		"a look-alike directory":  {`{"todo_id":"todo_1","prompt":"x","repo":"/srv/code2/app"}`, 403, "repo_not_allowed"},
		"a ref that is a flag":    {`{"todo_id":"todo_1","prompt":"x","repo":"/srv/code/app","ref":"--force"}`, 400, "invalid_ref"},
		"a ref with a space":      {`{"todo_id":"todo_1","prompt":"x","repo":"/srv/code/app","ref":"main; rm"}`, 400, "invalid_ref"},
		"an unknown qos":          {`{"todo_id":"todo_1","prompt":"x","repo":"/srv/code/app","qos":"instant"}`, 400, "invalid_qos"},
		"another to-do by label":  {`{"todo_id":"todo_1","prompt":"x","repo":"/srv/code/app","labels":{"persona_todo":"todo_2"}}`, 400, "invalid_labels"},
		"another to-do by tag":    {`{"todo_id":"todo_1","prompt":"x","repo":"/srv/code/app","tags":["persona_todo=todo_2"]}`, 400, "invalid_tags"},
		"an invalid engine":       {`{"todo_id":"todo_1","prompt":"x","repo":"/srv/code/app","agent_type":"bad engine"}`, 400, "invalid"},
		"an unknown profile":      {`{"todo_id":"todo_1","prompt":"x","repo":"/srv/code/app","permission_profile":"bypassPermissions"}`, 400, "invalid"},
		"an unknown priority":     {`{"todo_id":"todo_1","prompt":"x","repo":"/srv/code/app","priority":"p9"}`, 400, "invalid"},
		"an unknown field":        {`{"todo_id":"todo_1","prompt":"x","repo":"/srv/code/app","worker":"anything"}`, 400, "invalid_body"},
		"a field the queue owns":  {`{"todo_id":"todo_1","prompt":"x","repo":"/srv/code/app","source":"owner"}`, 400, "invalid_body"},
		"not JSON":                {`todo_1`, 400, "invalid_body"},
	} {
		t.Run(name, func(t *testing.T) {
			if ans := call(t, a, "POST", "/tasks", tc.body); ans.Status != tc.status || ans.Code != tc.code || ans.Error == "" {
				t.Fatalf("answer: %d %s %s", ans.Status, ans.Code, ans.Error)
			}
		})
	}
	if tasks, _ := a.queue.ListTasks(TaskFilter{}); len(tasks) != 0 {
		t.Fatalf("%d refused requests were queued", len(tasks))
	}

	// A caller that may only read: an answer-only token, a share.
	for _, role := range []string{"answer", "share", "viewer"} {
		if ans := call(t, a, "POST", "/tasks", validTask, "X-Persona-Role", role); ans.Status != http.StatusForbidden || ans.Code != "forbidden" {
			t.Fatalf("role %s: %d %s", role, ans.Status, ans.Code)
		}
	}

	// With no directory allowed, nothing can be queued.
	a.cfg.Repos = nil
	if ans := call(t, a, "POST", "/tasks", validTask); ans.Status != http.StatusForbidden || ans.Code != "repos_not_configured" {
		t.Fatalf("with no allowlist: %d %s", ans.Status, ans.Code)
	}
}

func TestCheckRepoLeavesWhatOnlyTheComputerCanTellToIt(t *testing.T) {
	// Written from the home directory on one side and as an absolute path on the other: the hub cannot compare
	// them, the program on the computer does (TestPrepareRefusesWhatTheAllowlistDoesNotCover).
	if got, err := checkRepo("~/code/app", []string{"/home/me/code"}); err != nil || got != "~/code/app" {
		t.Fatalf("home against absolute: %q %v", got, err)
	}
	if got, err := checkRepo("/srv/code/app/", []string{" /srv/code/ "}); err != nil || got != "/srv/code/app" {
		t.Fatalf("a trailing slash: %q %v", got, err)
	}
	if got, err := checkRepo("/srv/code", []string{"/srv/code"}); err != nil || got != "/srv/code" {
		t.Fatalf("the allowed directory itself: %q %v", got, err)
	}
	var ae *apiError
	if _, err := checkRepo("~/other/app", []string{"~/work"}); !errors.As(err, &ae) || ae.Code != "repo_not_allowed" {
		t.Fatalf("another directory under home: %v", err)
	}
}

func TestPostTaskAnswersTheQueuesLimits(t *testing.T) {
	a := newTestApp(t)
	for i := range 5 {
		if ans := call(t, a, "POST", "/tasks", validTask); ans.Status != http.StatusCreated {
			t.Fatalf("task %d: %d %v", i, ans.Status, ans.Body)
		}
	}
	if ans := call(t, a, "POST", "/tasks", validTask); ans.Status != http.StatusTooManyRequests || ans.Code != "too_many_for_todo" {
		t.Fatalf("the sixth task of a to-do: %d %s", ans.Status, ans.Code)
	}

	a = newTestApp(t)
	cfg := DefaultQueueConfig()
	cfg.MaxQueued = 1
	a.queue.UpdateConfig(cfg)
	call(t, a, "POST", "/tasks", validTask)
	other := strings.Replace(validTask, "todo_1", "todo_2", 1)
	if ans := call(t, a, "POST", "/tasks", other); ans.Status != http.StatusTooManyRequests || ans.Code != "too_many_queued" {
		t.Fatalf("over the queue cap: %d %s", ans.Status, ans.Code)
	}
}

func TestReadingTasksAndEvidence(t *testing.T) {
	a := newTestApp(t)
	q := a.queue
	zero := 0
	done := mustEnqueue(t, q, EnqueueInput{Title: "done", Prompt: "Do it well", AcceptanceCommand: "make check", Labels: map[string]string{PersonaTodoLabel: "todo_1"}})
	_, att, err := q.LeaseTask(done.ID, "dispatcher", testMachine, testStart)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := q.CompleteAttempt(CompleteAttemptInput{TaskID: done.ID, FenceToken: att.FenceToken, WorkerID: "dispatcher",
		Outcome: StateSucceeded, Verdict: VerdictPass, ExitCode: &zero, Message: "checked",
		Artifacts: []Artifact{{Kind: "branch", Path: "ats/example-1"}}, Now: testStart.Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}
	unverified := mustEnqueue(t, q, EnqueueInput{Title: "unverified", Labels: map[string]string{PersonaTodoLabel: "todo_1"}, Now: testStart.Add(time.Second)})
	_, att, _ = q.LeaseTask(unverified.ID, "dispatcher", testMachine, testStart)
	if _, err := q.CompleteAttempt(CompleteAttemptInput{TaskID: unverified.ID, FenceToken: att.FenceToken, WorkerID: "dispatcher",
		Outcome: StateSucceeded, Verdict: VerdictUnverified, Now: testStart.Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}
	waiting := mustEnqueue(t, q, EnqueueInput{Title: "waiting", Priority: PriorityP0, Labels: map[string]string{PersonaTodoLabel: "todo_2"}, Now: testStart.Add(2 * time.Second)})
	_, att, _ = q.LeaseTask(waiting.ID, "dispatcher", testMachine, testStart)
	if _, err := q.UpdateExec(waiting.ID, att.FenceToken, testStart, func(e *Execution) {
		e.Phase, e.MachineID, e.SessionID = PhaseWorking, testMachine, "session-1"
	}); err != nil {
		t.Fatal(err)
	}

	titles := func(ans apiAnswer) []string {
		var out []string
		for _, v := range ans.Body["tasks"].([]any) {
			task := v.(map[string]any)
			if _, has := task["prompt"]; has {
				t.Error("the list carries prompts")
			}
			out = append(out, task["title"].(string))
		}
		return out
	}
	all := call(t, a, "GET", "/tasks", "", "X-Persona-Role", "answer")
	if all.Status != 200 || !slices.Equal(titles(all), []string{"waiting", "unverified", "done"}) {
		t.Fatalf("all tasks: %d %v", all.Status, titles(all))
	}
	sched := all.Body["scheduler"].(map[string]any)
	if sched["admission"] != AdmissionFull || sched["computers"] != float64(1) {
		t.Fatalf("scheduler: %v", sched)
	}
	if got := titles(call(t, a, "GET", "/tasks?todo_id=todo_1", "")); !slices.Equal(got, []string{"unverified", "done"}) {
		t.Fatalf("by to-do: %v", got)
	}
	if got := titles(call(t, a, "GET", "/tasks?state=running,queued", "")); !slices.Equal(got, []string{"waiting"}) {
		t.Fatalf("by state: %v", got)
	}
	if got := titles(call(t, a, "GET", "/tasks?priority=p0&limit=1", "")); !slices.Equal(got, []string{"waiting"}) {
		t.Fatalf("by priority: %v", got)
	}
	for path, code := range map[string]string{"/tasks?state=done": "invalid_state", "/tasks?limit=0": "invalid_limit", "/tasks?limit=many": "invalid_limit"} {
		if ans := call(t, a, "GET", path, ""); ans.Status != 400 || ans.Code != code {
			t.Fatalf("%s: %d %s", path, ans.Status, ans.Code)
		}
	}

	one := call(t, a, "GET", "/tasks/"+waiting.ID, "")
	if task := taskOf(t, one); task["phase"] != PhaseWorking || task["session_id"] != "session-1" || task["runs_on"] != testMachine || task["prompt"] != "Do the thing" {
		t.Fatalf("one task: %v", task)
	}
	if n := len(one.Body["attempts"].([]any)); n != 1 {
		t.Fatalf("attempts: %d", n)
	}

	ev := call(t, a, "GET", "/tasks/"+done.ID+"/evidence", "")
	if ev.Status != 200 || ev.Body["verified"] != true || ev.Body["acceptance"] != "make check" || ev.Body["todo_id"] != "todo_1" {
		t.Fatalf("evidence: %v", ev.Body)
	}
	report := ev.Body["last_report"].(map[string]any)
	if report["exit_code"] != float64(0) || !strings.Contains(report["message"].(string), "checked") {
		t.Fatalf("report: %v", report)
	}
	if ev := call(t, a, "GET", "/tasks/"+unverified.ID+"/evidence", ""); ev.Body["verified"] != false || ev.Body["verdict"] != VerdictUnverified {
		t.Fatalf("a task with no acceptance is shown as verified: %v", ev.Body)
	}

	for _, path := range []string{"/tasks/task_none", "/tasks/task_none/evidence"} {
		if ans := call(t, a, "GET", path, ""); ans.Status != http.StatusNotFound || ans.Code != "not_found" {
			t.Fatalf("%s: %d %s", path, ans.Status, ans.Code)
		}
	}
	// Nothing else is served: no delete, no change, no other path.
	for _, req := range [][2]string{{"DELETE", "/tasks/" + done.ID}, {"PUT", "/tasks/" + done.ID}, {"POST", "/tasks/" + done.ID + "/retry"}, {"GET", "/state"}} {
		if ans := call(t, a, req[0], req[1], ""); ans.Status != http.StatusNotFound && ans.Status != http.StatusMethodNotAllowed {
			t.Fatalf("%s %s: %d", req[0], req[1], ans.Status)
		}
	}

	if ans := call(t, newATSApp(nil), "GET", "/tasks", ""); ans.Status != http.StatusServiceUnavailable || ans.Code != "not_ready" {
		t.Fatalf("before the queue is there: %d %s", ans.Status, ans.Code)
	}
}

func TestGateAsksBeforeQueueingAndRefusesTheRest(t *testing.T) {
	raw, err := os.ReadFile("assistant/gate.json")
	if err != nil {
		t.Fatal(err)
	}
	var gate struct {
		Command string   `json:"command"`
		Script  string   `json:"script"`
		Env     []string `json:"env"`
		BaseEnv string   `json:"base_env"`
		Secret  []string `json:"secret"`
		Rules   []struct{ Method, Path, Action string }
	}
	if err := json.Unmarshal(raw, &gate); err != nil {
		t.Fatal(err)
	}
	if gate.Command != "ats" || gate.BaseEnv != "ATS_BASE" || !slices.Equal(gate.Secret, []string{"ATS_TOKEN"}) {
		t.Fatalf("gate: %+v", gate)
	}
	if info, err := os.Stat("skills/" + gate.Script); err != nil || info.Mode()&0o111 == 0 {
		t.Fatalf("the gate's script is not an executable file of the skill: %v", err)
	}
	last := gate.Rules[len(gate.Rules)-1]
	if last.Method != "*" || last.Path != "**" || last.Action != "deny" {
		t.Fatalf("the last rule does not refuse everything else: %+v", last)
	}
	for _, r := range gate.Rules[:len(gate.Rules)-1] {
		switch {
		case r.Method == "GET" && r.Action == "allow" && strings.HasPrefix(r.Path, "/tasks"):
		case r.Method == "POST" && r.Path == "/tasks" && r.Action == "confirm":
		default:
			t.Errorf("a rule that lets more through than reads and a confirmed POST /tasks: %+v", r)
		}
	}
	if !slices.ContainsFunc(gate.Rules, func(r struct{ Method, Path, Action string }) bool { return r.Method == "POST" && r.Action == "confirm" }) {
		t.Fatal("queueing a task does not ask the user")
	}

	// The credentials the gate asks the program for name exactly the gate's variables.
	a := newTestApp(t)
	if _, err := a.assistant(t.Context(), "credentials", nil); err == nil {
		t.Fatal("credentials were given before the hub gave a token")
	}
	a.hubURL, a.token = "http://127.0.0.1:1", "test-token"
	out, err := a.assistant(t.Context(), "credentials", nil)
	if err != nil {
		t.Fatal(err)
	}
	env := out.(map[string]any)["env"].(map[string]string)
	if env["ATS_BASE"] != "http://127.0.0.1:1/api/v1/extensions/ats" || env["ATS_TOKEN"] != "test-token" || len(env) != len(gate.Env) {
		t.Fatalf("credentials: %v", env)
	}
	if _, err := a.assistant(t.Context(), "anything", nil); err == nil {
		t.Fatal("an unknown assistant method was answered")
	}
}

func TestManifest(t *testing.T) {
	if err := wire.ValidateJSON("Manifest", manifest); err != nil {
		t.Fatalf("the packaged manifest is not valid: %v", err)
	}
	var m struct {
		Version string `json:"version"`
		Runtime struct {
			HTTP bool `json:"http"`
		} `json:"runtime"`
		Node struct {
			Command  map[string]string `json:"command"`
			Args     []string          `json:"args"`
			Machines string            `json:"machines"`
		} `json:"node"`
		Contributes struct {
			McpServers []any `json:"mcp_servers"`
			Assistant  []struct {
				Skills []string `json:"skills"`
				Gate   string   `json:"gate"`
			} `json:"assistant"`
		} `json:"contributes"`
		Permissions struct {
			Hub           []string `json:"hub"`
			UsageGateways bool     `json:"usage_gateways"`
		} `json:"permissions"`
		Configuration struct {
			Properties map[string]struct {
				Default any `json:"default"`
				Items   struct {
					Format string   `json:"format"`
					XOS    []string `json:"x-os"`
				} `json:"items"`
			} `json:"properties"`
		} `json:"configuration"`
	}
	if err := json.Unmarshal(authorManifest, &m); err != nil {
		t.Fatal(err)
	}
	if !m.Runtime.HTTP || len(m.Contributes.McpServers) != 0 {
		t.Fatalf("the assistant's way in is the gated HTTP API alone: http %v, %d MCP servers", m.Runtime.HTTP, len(m.Contributes.McpServers))
	}
	if len(m.Contributes.Assistant) != 1 || m.Contributes.Assistant[0].Gate != "assistant/gate.json" {
		t.Fatalf("assistant: %+v", m.Contributes.Assistant)
	}
	for _, dir := range m.Contributes.Assistant[0].Skills {
		if _, err := os.Stat(dir + "/SKILL.md"); err != nil {
			t.Errorf("skill %s: %v", dir, err)
		}
	}
	want := []string{"todos.write", "agents.read", "agents.control", "machines.read", "extensions.call:ats"}
	if !slices.Equal(m.Permissions.Hub, want) || !m.Permissions.UsageGateways {
		t.Fatalf("permissions: %+v", m.Permissions)
	}
	if !slices.Equal(m.Node.Args, []string{"node"}) || m.Node.Machines != "machines" {
		t.Fatalf("node: %+v", m.Node)
	}
	// The computers offered in the settings are the ones the program is built for.
	machines := m.Configuration.Properties["machines"].Items
	var built []string
	for platform := range m.Node.Command {
		if os, _, _ := strings.Cut(platform, "-"); !slices.Contains(built, os) {
			built = append(built, os)
		}
	}
	slices.Sort(built)
	slices.Sort(machines.XOS)
	if machines.Format != "machine" || !slices.Equal(machines.XOS, built) {
		t.Fatalf("machines offers %v, the program is built for %v", machines.XOS, built)
	}
	// The defaults the settings show are the ones the program uses when nothing is set.
	def := defaultExtConfig()
	for key, want := range map[string]any{
		"write_permission_mode": def.WritePermissionMode, "parallel_per_machine": float64(def.ParallelPerMachine),
		"task_timeout_minutes": float64(def.TaskTimeoutMinutes), "acceptance_timeout_minutes": float64(def.AcceptTimeoutMinutes),
		"max_queued": float64(def.MaxQueued), "max_retries": float64(def.MaxRetries), "reserve_headroom_pct": float64(def.ReserveHeadroomPct),
	} {
		if got := m.Configuration.Properties[key].Default; got != want {
			t.Errorf("%s: the manifest's default is %v, the program's %v", key, got, want)
		}
	}
}

func TestConformance(t *testing.T) {
	a := newTestApp(t)
	// The dispatcher is made here and not run: the conformance check has no hub API behind it.
	a.api, a.nodes = newFakeHub(), &localNodes{fail: map[string]error{}}
	a.disp = a.newDispatcher()
	mustEnqueue(t, a.queue, EnqueueInput{Title: "Queued", Labels: map[string]string{PersonaTodoLabel: "todo_1"}})
	var m wire.Manifest
	if err := json.Unmarshal(manifest, &m); err != nil {
		t.Fatal(err)
	}
	exttest.Check(t, manifest, exttest.Serve(t, a.program(m.ID, m.Version)))
}

func TestQueuePageShowsEveryStateAndItsButtonsWork(t *testing.T) {
	r := newRig(t)
	a := newATSApp(r.q)
	a.cfg, a.disp = r.cfg, r.d
	a.nowFunc = func() time.Time { return r.now }

	working := r.enqueue(EnqueueInput{Title: "Working task", Priority: PriorityP0})
	r.tick()
	queued := r.enqueue(EnqueueInput{Title: "Queued task"})
	finished := r.enqueue(EnqueueInput{Title: "Finished task"})
	if _, err := r.q.CancelTask(finished.ID, r.now); err != nil {
		t.Fatal(err)
	}

	render := func() string {
		t.Helper()
		view, err := a.render(t.Context(), &ext.UIRequest{Contribution: queueUI, TZ: "Europe/Berlin"})
		if err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(view)
		if err != nil {
			t.Fatal(err)
		}
		if err := exttest.CheckView(raw, false); err != nil {
			t.Fatalf("the page is not a valid view: %v", err)
		}
		return string(raw)
	}
	page := render()
	for _, want := range []string{"Working task", "Queued task", "Finished task", "ATS 在做", "排着队", "cancel:" + working.ID, "cancel:" + queued.ID, "retry:" + finished.ID, "Studio"} {
		if !strings.Contains(page, want) {
			t.Errorf("the page does not have %q", want)
		}
	}
	if strings.Contains(page, "开始用之前") {
		t.Error("the page asks for settings that are there")
	}

	// Cancel goes through the dispatcher: the session is interrupted, the worktree removed.
	res, err := a.action(context.Background(), &ext.UIRequest{Contribution: queueUI, Action: "cancel:" + working.ID})
	if err != nil || !strings.Contains(res.Toast, "已取消") || len(r.hub.interrupts) != 1 {
		t.Fatalf("cancel: %+v %v, interrupts %v", res, err, r.hub.interrupts)
	}
	if got := r.task(working.ID); got.State != StateCanceled {
		t.Fatalf("after cancel: %s", got.State)
	}
	res, err = a.action(context.Background(), &ext.UIRequest{Contribution: queueUI, Action: "retry:" + finished.ID})
	if err != nil || !strings.Contains(res.Toast, "已经重新交给它做") || r.task(finished.ID).State != StateQueued {
		t.Fatalf("retry: %+v %v", res, err)
	}
	if res, err := a.action(context.Background(), &ext.UIRequest{Contribution: queueUI, Action: "retry:task_none"}); err != nil || res.Toast != "没能再试一次：找不到这个任务了，可能已经被清掉。刷新一下看看。" {
		t.Fatalf("retry of an unknown task: %+v %v", res, err)
	}
	if _, err := a.render(t.Context(), &ext.UIRequest{Contribution: "other"}); err == nil {
		t.Fatal("an unknown page was rendered")
	}

	// With nothing set up, the page says what is missing.
	bare := newATSApp(newTestQueue(t, DefaultQueueConfig()))
	view, err := bare.render(t.Context(), &ext.UIRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if raw, _ := json.Marshal(view); !strings.Contains(string(raw), "第 1 步：选一台干活的电脑") {
		t.Error("the page does not say that computers and repositories are not set")
	}
}

func TestAcceptanceResultArrivesAsANodeEventOrByAsking(t *testing.T) {
	// As the event the program on the computer sends.
	r := newRig(t)
	a := newATSApp(r.q)
	a.cfg, a.disp = r.cfg, r.d
	r.worker.notify = func(event string, params any) {
		raw, _ := json.Marshal(map[string]any{"event": event, "machine_id": testMachine, "params": params})
		if _, err := a.nodeEvent(context.Background(), "event", raw); err != nil {
			t.Error(err)
		}
	}
	r.nodes.fail["ats.accept_status"] = errors.New("not asked in this test")
	task := r.enqueue(EnqueueInput{AcceptanceCommand: "echo by event"})
	r.tick()
	r.hub.turnEnds("session-1", "u1", "done")
	r.tick()
	r.tick()
	r.waitAccepted(task.ID, 1)
	for i := 0; i < 20 && r.task(task.ID).State != StateSucceeded; i++ {
		time.Sleep(10 * time.Millisecond) // the event is sent just after the result is stored
		r.tick()
	}
	if got := r.task(task.ID); got.State != StateSucceeded || got.Verdict != VerdictPass {
		t.Fatalf("by event: %s %s", got.State, got.Verdict)
	}

	// The event is lost (the hub program was restarting): the dispatcher asks.
	r = newRig(t)
	r.worker.notify = nil
	task = r.enqueue(EnqueueInput{AcceptanceCommand: "echo by asking"})
	r.tick()
	r.hub.turnEnds("session-1", "u1", "done")
	r.tick()
	r.tick()
	r.waitAccepted(task.ID, 1)
	for i := 0; i < 20 && r.task(task.ID).State != StateSucceeded; i++ {
		r.tick()
	}
	got := r.task(task.ID)
	if got.State != StateSucceeded || got.Verdict != VerdictPass || !strings.Contains(got.Reports[len(got.Reports)-1].Message, "by asking") {
		t.Fatalf("by asking: %s %s", got.State, got.Verdict)
	}
}

// GET /tasks/{id} is read to see how a task is going, so it does not hand back the whole brief or the queue's
// bookkeeping unless asked: a long prompt is cut to its beginning, with the length of the whole.
func TestGetTaskCutsALongPromptUnlessAskedForAll(t *testing.T) {
	a := newTestApp(t)
	long := strings.Repeat("修复登录页。", 100) // 600 runes, 1800 bytes
	short := mustEnqueue(t, a.queue, EnqueueInput{Title: "short", Prompt: "  Do the thing\n", Repo: "/srv/code/app", Now: testStart})
	task := mustEnqueue(t, a.queue, EnqueueInput{Title: "long", Prompt: long, Repo: "/srv/code/app", Now: testStart})
	if _, _, err := a.queue.LeaseTask(task.ID, "dispatcher", testMachine, testStart); err != nil {
		t.Fatal(err)
	}

	got := taskOf(t, call(t, a, "GET", "/tasks/"+short.ID, ""))
	if _, has := got["prompt_bytes"]; got["prompt"] != "Do the thing" || has {
		t.Fatalf("a short prompt: %v", got)
	}

	brief := call(t, a, "GET", "/tasks/"+task.ID, "")
	got = taskOf(t, brief)
	head, _ := got["prompt"].(string)
	if want := string([]rune(long)[:promptHeadRunes]) + "…"; head != want || got["prompt_bytes"] != float64(len(long)) {
		t.Fatalf("a long prompt: %d bytes of prompt, prompt_bytes %v", len(head), got["prompt_bytes"])
	}
	att := brief.Body["attempts"].([]any)[0].(map[string]any)
	for _, k := range []string{"attempt_no", "state", "machine", "started_at"} {
		if _, has := att[k]; !has {
			t.Errorf("the attempt lost %s: %v", k, att)
		}
	}
	for _, k := range []string{"task_id", "attempt_id", "fence_token", "worker_id", "leased_at", "lease_expires_at"} {
		if _, has := att[k]; has {
			t.Errorf("the attempt carries %s: %v", k, att)
		}
	}

	full := call(t, a, "GET", "/tasks/"+task.ID+"?view=full", "")
	got = taskOf(t, full)
	if _, has := got["prompt_bytes"]; got["prompt"] != long || has {
		t.Fatalf("view=full: %d bytes of prompt, prompt_bytes %v", len(got["prompt"].(string)), got["prompt_bytes"])
	}
	att = full.Body["attempts"].([]any)[0].(map[string]any)
	for _, k := range []string{"attempt_id", "fence_token", "worker_id", "lease_expires_at"} {
		if _, has := att[k]; !has {
			t.Errorf("view=full: the attempt lost %s: %v", k, att)
		}
	}

	if ans := call(t, a, "GET", "/tasks/"+task.ID+"?view=brief", ""); ans.Status != 400 || ans.Code != "invalid_view" {
		t.Fatalf("an unknown view: %d %s", ans.Status, ans.Code)
	}
}

// The evidence is read to learn whether a task is really done: the conclusion comes first in the answer, the
// last report keeps the end of its output, and attempt=N has the whole report of one attempt.
func TestEvidencePutsTheConclusionFirstAndCutsTheOutput(t *testing.T) {
	a := newTestApp(t)
	q := a.queue
	one := 1
	output := "验收命令退出码 1\n" + strings.Repeat("ok  \tpkg\t0.1s\n", 200) + "--- FAIL: TestLogin\nFAIL"
	task := mustEnqueue(t, q, EnqueueInput{Title: "t", Prompt: "p", Repo: "/srv/code/app", AcceptanceCommand: "make check", Now: testStart})
	for i := range 2 {
		_, att, err := q.LeaseTask(task.ID, "dispatcher", testMachine, testStart.Add(time.Duration(i)*time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := q.CompleteAttempt(CompleteAttemptInput{TaskID: task.ID, FenceToken: att.FenceToken, WorkerID: "dispatcher", Outcome: StateFailed,
			Verdict: VerdictFail, ErrorClass: "task_failed", ExitCode: &one, Message: fmt.Sprintf("%s (attempt %d)", output, att.AttemptNo), Summary: "tried", Now: testStart}); err != nil {
			t.Fatal(err)
		}
	}

	raw := tokRaw(t, a, "GET", "/tasks/"+task.ID+"/evidence", "")
	if !strings.HasPrefix(raw, `{"verified":false,"state":"`) || strings.Index(raw, `"verdict"`) > strings.Index(raw, `"last_report"`) {
		t.Fatalf("the conclusion does not come first: %.200s", raw)
	}
	ev := call(t, a, "GET", "/tasks/"+task.ID+"/evidence", "")
	last := ev.Body["last_report"].(map[string]any)
	msg, _ := last["message"].(string)
	if n := len([]rune(msg)); n > evidenceTailRunes+2 || !strings.HasPrefix(msg, "验收命令退出码 1\n…") || !strings.HasSuffix(msg, "--- FAIL: TestLogin\nFAIL (attempt 2)") {
		t.Fatalf("the output: %d runes: %q", n, msg)
	}
	if last["message_bytes"] != float64(len(output)+len(" (attempt 2)")) || last["exit_code"] != float64(1) || last["summary"] != "tried" || last["attempt_no"] != float64(2) {
		t.Fatalf("the last report: %v", last)
	}
	for _, k := range []string{"task_id", "worker_id", "report_id", "fence_token"} {
		if _, has := last[k]; has {
			t.Errorf("the last report carries %s", k)
		}
	}

	whole := call(t, a, "GET", "/tasks/"+task.ID+"/evidence?attempt=1", "")
	rep, _ := whole.Body["report"].(map[string]any)
	if _, has := whole.Body["last_report"]; has || rep["message"] != output+" (attempt 1)" || rep["attempt_no"] != float64(1) || whole.Body["verified"] != false {
		t.Fatalf("attempt=1: %v", whole.Body)
	}
	for path, want := range map[string]string{"?attempt=3": "no_report", "?attempt=0": "invalid_attempt", "?attempt=last": "invalid_attempt"} {
		if ans := call(t, a, "GET", "/tasks/"+task.ID+"/evidence"+path, ""); ans.Code != want || ans.Status == 200 {
			t.Errorf("%s: %d %s", path, ans.Status, ans.Code)
		}
	}
}

// With no acceptance command the report's message is the agent's whole reply; the evidence shows its summary.
func TestEvidenceLeavesOutAReplyItsSummaryStandsFor(t *testing.T) {
	a := newTestApp(t)
	q := a.queue
	reply := strings.Repeat("I looked at the code. ", 100) + "\nSummary: nothing to fix"
	task := mustEnqueue(t, q, EnqueueInput{Title: "t", Prompt: "p", Repo: "/srv/code/app", Now: testStart})
	_, att, err := q.LeaseTask(task.ID, "dispatcher", testMachine, testStart)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := q.CompleteAttempt(CompleteAttemptInput{TaskID: task.ID, FenceToken: att.FenceToken, WorkerID: "dispatcher", Outcome: StateSucceeded,
		Verdict: VerdictUnverified, Message: reply, Summary: agentSummary(reply), Now: testStart}); err != nil {
		t.Fatal(err)
	}
	ev := call(t, a, "GET", "/tasks/"+task.ID+"/evidence", "")
	last := ev.Body["last_report"].(map[string]any)
	if _, has := last["message"]; has || last["summary"] != "nothing to fix" || last["message_bytes"] != float64(len(reply)) || ev.Body["verified"] != false {
		t.Fatalf("the last report: %v", last)
	}
	if rep := call(t, a, "GET", "/tasks/"+task.ID+"/evidence?attempt=1", "").Body["report"].(map[string]any); rep["message"] != reply {
		t.Fatalf("attempt=1: %v", rep)
	}
}

func TestOutputEnd(t *testing.T) {
	for name, tc := range map[string]struct{ in, want string }{
		"short":            {" 验收命令退出码 0\nok\n", "验收命令退出码 0\nok"},
		"long output":      {"验收命令退出码 2\n" + strings.Repeat("x", 50) + "\nend", "验收命令退出码 2\n…" + strings.Repeat("x", 27) + "\nend"},
		"one long line":    {strings.Repeat("y", 50), "…" + strings.Repeat("y", 40)},
		"a long first one": {strings.Repeat("头", 130) + "\ntail", strings.Repeat("头", 10) + "…\ntail"},
	} {
		if got := outputEnd(tc.in, 40); got != tc.want {
			t.Errorf("%s: %q, want %q", name, got, tc.want)
		}
	}
}

// The list is read to see how the tasks of a to-do are going: a task is listed in short, a field at its default
// is left out, and view=full lists every field.
func TestListShowsTasksInShortUnlessAskedForAll(t *testing.T) {
	a := newTestApp(t)
	q := a.queue
	plain := mustEnqueue(t, q, EnqueueInput{Title: "plain", Repo: "/srv/code/app", AcceptanceCommand: "make check",
		Labels: map[string]string{PersonaTodoLabel: "todo_1"}, Now: testStart})
	special := mustEnqueue(t, q, EnqueueInput{Title: "special", Repo: "/srv/code/app", Priority: PriorityP0, PermissionProfile: PermissionDefault,
		AgentType: "codex", Labels: map[string]string{PersonaTodoLabel: "todo_1"}, Now: testStart.Add(time.Second)})
	zero := 0
	for i := range 2 { // fails once, then passes on its second attempt
		_, att, err := q.LeaseTask(special.ID, "dispatcher", testMachine, testStart.Add(time.Duration(i)*time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		in := CompleteAttemptInput{TaskID: special.ID, FenceToken: att.FenceToken, WorkerID: "dispatcher", Outcome: StateFailed, Verdict: VerdictFail,
			ErrorClass: "task_failed", Now: testStart.Add(time.Duration(i) * time.Hour)}
		if i == 1 {
			in.Outcome, in.Verdict, in.ErrorClass, in.ExitCode = StateSucceeded, VerdictPass, "", &zero
		}
		if _, err := q.CompleteAttempt(in); err != nil {
			t.Fatal(err)
		}
	}
	byID := func(path string) map[string]map[string]any {
		out := map[string]map[string]any{}
		for _, v := range call(t, a, "GET", path, "").Body["tasks"].([]any) {
			task := v.(map[string]any)
			out[task["id"].(string)] = task
		}
		return out
	}

	got := byID("/tasks")
	if p := got[plain.ID]; len(p) != 5 || p["title"] != "plain" || p["state"] != StateQueued || p["todo_id"] != "todo_1" || p["submitted_at"] == nil {
		t.Fatalf("a task with every field at its default: %v", p)
	}
	s := got[special.ID]
	if s["priority"] != PriorityP0 || s["permission_profile"] != PermissionDefault || s["agent_type"] != "codex" || s["attempt_no"] != float64(2) ||
		s["verdict"] != VerdictPass || s["finished_at"] == nil {
		t.Fatalf("a task with fields off their default: %v", s)
	}
	for _, k := range []string{"repo", "ref", "acceptance", "difficulty", "model_tier", "retries_left", "session_id", "prompt"} {
		if _, has := s[k]; has {
			t.Errorf("the list carries %s", k)
		}
	}
	if p := byID("/tasks?todo_id=todo_1")[plain.ID]; len(p) != 4 || p["todo_id"] != nil {
		t.Fatalf("a task in its to-do's list: %v", p)
	}

	p := byID("/tasks?view=full")[plain.ID]
	if p["priority"] != PriorityP2 || p["permission_profile"] != PermissionReadonly || p["agent_type"] != AgentClaude || p["verdict"] != VerdictUnverified ||
		p["repo"] != "/srv/code/app" || p["acceptance"] != "make check" || p["todo_id"] != "todo_1" || p["attempt_no"] != float64(0) {
		t.Fatalf("view=full: %v", p)
	}
	if ans := call(t, a, "GET", "/tasks?view=short", ""); ans.Status != 400 || ans.Code != "invalid_view" {
		t.Fatalf("an unknown view: %d %s", ans.Status, ans.Code)
	}
}

// A list longer than the limit says where the next page starts, and the pages together are the whole list, each
// task once.
func TestListGoesOnInPages(t *testing.T) {
	a := newTestApp(t)
	var want []string
	for i := range 7 {
		todo := []string{"todo_1", "todo_2"}[i%2]
		task := mustEnqueue(t, a.queue, EnqueueInput{Title: fmt.Sprint(i), Repo: "/srv/code/app", Labels: map[string]string{PersonaTodoLabel: todo}, Now: testStart})
		want = append([]string{task.ID}, want...) // the same submission time: newest id first
	}
	pages := func(query string) (ids []string, n int) {
		for after := ""; ; n++ {
			ans := call(t, a, "GET", "/tasks?"+query+after, "")
			if ans.Status != 200 || n > 10 {
				t.Fatalf("page %d of %s: %d %v", n, query, ans.Status, ans.Body)
			}
			for _, v := range ans.Body["tasks"].([]any) {
				ids = append(ids, v.(map[string]any)["id"].(string))
			}
			next, more := ans.Body["next"].(string)
			if !more {
				return ids, n + 1
			}
			if !strings.HasSuffix(next, "-"+ids[len(ids)-1]) {
				t.Fatalf("next is %s, the page ends with %s", next, ids[len(ids)-1])
			}
			after = "&after=" + next
		}
	}
	if ids, n := pages("limit=3"); !slices.Equal(ids, want) || n != 3 {
		t.Fatalf("7 tasks by 3: %d pages, %v, want %v", n, ids, want)
	}
	if ids, n := pages("limit=7"); !slices.Equal(ids, want) || n != 1 {
		t.Fatalf("7 tasks by 7: %d pages, %v", n, ids)
	}
	if ids, n := pages("limit=2&todo_id=todo_2"); len(ids) != 3 || n != 2 {
		t.Fatalf("a to-do's 3 tasks by 2: %d pages, %v", n, ids)
	}
	if ids, n := pages("limit=200"); !slices.Equal(ids, want) || n != 1 {
		t.Fatalf("the largest limit: %d pages, %v", n, ids)
	}
	// The page goes on from a place, not from a task: the last task of a page may leave the filtered list (it
	// starts running) before the next page is asked for.
	first := call(t, a, "GET", "/tasks?state=queued&limit=3", "")
	next := first.Body["next"].(string)
	if _, _, err := a.queue.LeaseTask(want[2], "dispatcher", testMachine, testStart); err != nil {
		t.Fatal(err)
	}
	second := call(t, a, "GET", "/tasks?state=queued&limit=3&after="+next, "")
	var ids []string
	for _, v := range second.Body["tasks"].([]any) {
		ids = append(ids, v.(map[string]any)["id"].(string))
	}
	if second.Status != 200 || !slices.Equal(ids, want[3:6]) {
		t.Fatalf("the page after a task that left the list: %d %v, want %v", second.Status, ids, want[3:6])
	}
	// Past the end there is nothing more; what is not a cursor is refused.
	if ans := call(t, a, "GET", "/tasks?after=1-task_0", ""); ans.Status != 200 || len(ans.Body["tasks"].([]any)) != 0 || ans.Body["next"] != nil {
		t.Fatalf("past the end: %d %v", ans.Status, ans.Body)
	}
	for _, path := range []string{"/tasks?after=task_3", "/tasks?after=-task_3", "/tasks?after=12-"} {
		if ans := call(t, a, "GET", path, ""); ans.Status != 400 || ans.Code != "invalid_after" {
			t.Fatalf("%s: %d %s", path, ans.Status, ans.Code)
		}
	}
}
