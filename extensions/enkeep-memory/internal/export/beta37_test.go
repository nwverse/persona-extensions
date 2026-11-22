package export

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/entry"
	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/store"
)

// Synthetic projects only (占位项目, project-x): the knowledge base's id is the slug the hub looks a file up by.
var kbProjects = []entry.KBProject{
	{ID: "占位项目-整理插件", Name: "占位项目·整理插件", Aliases: []string{"整理插件"}},
	{ID: "project-x", Name: "Project X"},
	{ID: "dup-a", Name: "dup-a", Aliases: []string{"同名"}},
	{ID: "dup-b", Name: "dup-b", Aliases: []string{"同名"}},
}

func compileWith(t *testing.T, st *store.State, dir string, projects []entry.KBProject) *Report {
	t.Helper()
	rep, err := Compile(context.Background(), st, Options{Dir: dir, Model: &echo{}, Lang: "zh-Hans", Loc: time.UTC,
		Now: func() time.Time { return now }, Projects: projects})
	if err != nil {
		t.Fatal(err)
	}
	return rep
}

func scoped(e *entry.Entry, project string) *entry.Entry {
	e.Scopes = []entry.Scope{{Kind: entry.ScopeProject, Name: project}}
	return e
}

// Exit D: an entry's project name (or alias) is matched to the knowledge base's project, and the file is named by
// the project's id; a name the knowledge base does not have gets no file; a shared alias leads nowhere. What holds in
// a known project leaves exit A for the project's file; ① goes to the project too, under the general part.
func TestProjectFilesByKnowledgeBase(t *testing.T) {
	dir := t.TempDir()
	st := &store.State{Entries: map[string]*entry.Entry{}}
	add := func(e *entry.Entry) { st.Entries[e.ID] = e }
	add(ent("i-0000000001", entry.Interaction, entry.Class, "汇报时先给结论再给细节（占位通用）"))
	add(scoped(ent("i-0000000002", entry.Interaction, entry.Class, "这个项目里改动前先在群里说一声（占位项目规矩）"), "整理插件"))
	add(scoped(ent("w-0000000001", entry.Workflow, entry.Class, "发版前先跑一遍离线回放（占位项目做法）"), "占位项目·整理插件"))
	add(scoped(ent("j-0000000001", entry.Judgement, entry.Class, "遇到占位岔口时选先上线再优化，因为要看真实负载"), "Project X"))
	add(scoped(ent("w-0000000002", entry.Workflow, entry.Class, "未知项目里的占位做法"), "知识库里没有的项目"))
	add(scoped(ent("w-0000000003", entry.Workflow, entry.Class, "别名重名的占位做法"), "同名"))
	compileWith(t, st, dir, kbProjects)

	cn := read(t, dir, "agent/projects/占位项目-整理插件.md")
	for _, want := range []string{"# 项目背景：占位项目·整理插件", "## 和用户打交道（通用）", "汇报时先给结论", "改动前先在群里说一声", "发版前先跑一遍离线回放"} {
		if !strings.Contains(cn, want) {
			t.Errorf("project file lacks %q:\n%s", want, cn)
		}
	}
	if len(cn) > ProjectMax {
		t.Errorf("project file %d bytes", len(cn))
	}
	if !strings.Contains(read(t, dir, "agent/projects/project-x.md"), "先上线再优化") {
		t.Error("project-x file lacks its judgement")
	}
	files, _ := filepath.Glob(filepath.Join(dir, ProjectsDir, "*.md"))
	if len(files) != 2 { // dup-a and dup-b: nothing of their own, no file with the general part alone
		t.Errorf("project files %v: want the two known projects only", files)
	}
	a := read(t, dir, InstructionsPath)
	if !strings.Contains(a, "汇报时先给结论") {
		t.Errorf("the general rule left exit A:\n%s", a)
	}
	for _, not := range []string{"改动前先在群里说一声", "发版前先跑一遍离线回放", "先上线再优化"} {
		if strings.Contains(a, not) {
			t.Errorf("exit A carries the project rule %q", not)
		}
	}
	for _, still := range []string{"未知项目里的占位做法", "别名重名的占位做法"} {
		if !strings.Contains(a, still) {
			t.Errorf("an entry of no known project left exit A: %q", still)
		}
	}
	if !strings.Contains(a, "最近条目最多的项目：") {
		t.Errorf("names index missing:\n%s", a)
	}

	// Without the project list (the knowledge base not read): files by kb.Slug's rule, exit A as before.
	dir2 := t.TempDir()
	st2, _ := st.Clone(now)
	compileWith(t, st2, dir2, nil)
	if read(t, dir2, "agent/projects/整理插件.md") == "" || read(t, dir2, "agent/projects/知识库里没有的项目.md") == "" {
		files, _ := filepath.Glob(filepath.Join(dir2, ProjectsDir, "*.md"))
		t.Errorf("without the list, files by kb.Slug: %v", files)
	}
	if !strings.Contains(read(t, dir2, InstructionsPath), "改动前先在群里说一声") {
		t.Error("without the list a project rule left exit A")
	}
}

// Old ways-of-working skills of an earlier version are removed by the first compile of this one.
func TestUpgradeRemovesWaysOfWorkingSkills(t *testing.T) {
	dir := t.TempDir()
	old := filepath.Join(dir, SkillsDir, "enkeep-ways-of-working-release", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(old), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(old, []byte("---\nname: x\n---\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	man := fmt.Sprintf(`{"compiled":"x","exits":{},"files":{%q:{"exit":"B","hash":"h","bytes":17,"written":"2026-01-01T00:00:00Z","entries":["w-1"]}}}`,
		SkillsDir+"/enkeep-ways-of-working-release/SKILL.md")
	if err := os.WriteFile(filepath.Join(dir, ManifestPath), []byte(man), 0o600); err != nil {
		t.Fatal(err)
	}
	compileWith(t, fixture(), dir, nil)
	if _, err := os.Stat(filepath.Dir(old)); !os.IsNotExist(err) {
		t.Fatalf("old skill directory still there: %v", err)
	}
}

// Shares: a section's unused share goes to the next sections with lines left; the block stays within 6 KB; an
// action judgement with its fork, choice and reason goes in, one without does not.
func TestSharesAndForkReady(t *testing.T) {
	dir := t.TempDir()
	st := &store.State{Entries: map[string]*entry.Entry{}}
	for i := 0; i < 120; i++ {
		id := fmt.Sprintf("w-%010d", i)
		st.Entries[id] = ent(id, entry.Workflow, entry.Class, fmt.Sprintf("第 %d 条占位做事方式，写得长一些以便占满预算再多写几个字", i))
	}
	j := ent("j-0000000001", entry.Judgement, entry.Action, "遇到占位岔口甲时选方案甲，不选方案乙，因为占位理由")
	j.Situation, j.Choice, j.Rejected = "占位岔口甲", "方案甲", "方案乙"
	st.Entries[j.ID] = j
	st.Entries["j-0000000002"] = ent("j-0000000002", entry.Judgement, entry.Action, "一次性的占位拍板")
	rep := compileWith(t, st, dir, nil)
	a := read(t, dir, InstructionsPath)
	if len(a) > InstructionsMax {
		t.Fatalf("%d bytes", len(a))
	}
	if rep.Sections["workflow"] <= InstructionsMax*SectionShare["workflow"]/100 {
		t.Errorf("④ did not get the shares left unused: %+v", rep.Sections)
	}
	if !strings.Contains(a, "选方案甲") || strings.Contains(a, "一次性的占位拍板") {
		t.Errorf("fork-ready gate:\n%s", a)
	}
}
