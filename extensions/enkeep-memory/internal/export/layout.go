// Package export compiles the entries into the exits (design §6) and writes them under the extension's data
// directory, where the hub reads them once it has changes H1 (instructions and skills), H4 (agent project
// context) and H5 (inbox context and never_quote). Until then the files are written all the same (cheap, and the
// page shows what would go out) and each exit is marked waiting for its hub change.
//
// This file fixes the layout and the budgets; they are the contract with the hub (§12) and with the page.
package export

// Paths, relative to the data directory. The manifest declares them with source "data" once the hub has H1/H4/H5.
const (
	// Exit A: the per-turn block appended to the assistant's CLAUDE.md / AGENTS.md (H1, kind "instructions").
	InstructionsPath = "assistant/instructions.md"
	// Exit B: skills (H1, skills with source "data"): one directory per skill, each with SKILL.md. Only look-up
	// handbooks stay skills (0.2.0-beta.37): the ways-of-working skills ("enkeep-ways-of-working-<slug>") were loaded
	// no time at all; their entries go to exit A, exit D or enkeep_recall, and an upgrade removes the old directories
	// (write drops every file no longer made).
	SkillsDir  = "assistant/skills"
	ToolsSkill = "enkeep-machines-and-tools" // ② every stable entry, by object
	// Exit D: agent project context (H4, kind "project_context"): one file per knowledge-base project, named by the
	// project's id in the knowledge base, which is the slug the hub looks the file up by (kb.Slug).
	ProjectsDir = "agent/projects"
	// Exit E: inbox triage and drafting (H5, kind "inbox_context"), and the lines a draft must never contain.
	InboxContextPath = "inbox/context.md"
	NeverQuotePath   = "inbox/never_quote.txt"
	// What was compiled, for the page and the readback: exit → hash, bytes, entries, and per-entry ids.
	ManifestPath = "exits.json"
)

// Budgets in bytes (6.3). Exit A sections each keep a floor: no facet crowds out another; an empty section is
// left out, never filled with placeholder text.
const (
	InstructionsMax = 6 * 1024
	SkillsMax       = 256 * 1024
	ProjectMax      = 1536
	InboxMax        = 1024
	NeverQuoteMax   = 64 * 1024
)

// Section shares of exit A, in percent of what the title and the fixed lines leave of InstructionsMax
// (0.2.0-beta.37, docs/exits.md): each section with content first gets its share; what an empty or short section
// leaves goes to the next sections that still have lines, in order. Shares, not byte counts from any one user's
// entries, so a new user's block grows the same way.
var SectionShare = map[string]int{
	"interaction": 22, // ① the rules that hold across projects
	"tools":       8,  // ② the index line of the machines-and-tools skill
	"judgement":   22, // ③ default judgements with their fork
	"workflow":    18, // ④ ways of working that hold across projects
	"world":       12, // ⑤ open commitments, the names index
	"persona":     18, // ⑥ about the user
}

// Section order of exit A. People (⑤′) never go into exit A.
var SectionOrder = []string{"interaction", "tools", "judgement", "workflow", "world", "persona"}

// Hub changes each exit waits for.
var Needs = map[string]string{"A": "H1", "B": "H1", "C": "", "D": "H4", "E": "H5", "F": "H1", "G": ""}
