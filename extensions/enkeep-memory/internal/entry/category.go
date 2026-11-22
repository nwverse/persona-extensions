package entry

import (
	"slices"
	"strings"
)

// Categories (unified memory design v2 §3.2/§3.3b, domain spaces §3.6, user 11-22 05:44: "敏感空间也可以收集……
// 但是信息使用的时候确实要按照场景去做分级"): everything is collected and tagged; what an exit may carry is decided by
// the categories and the scene, not by leaving content out. A credential is not a category: it is never kept.
//
// An entry with no category is general. The enumeration is fixed in code (the scene rules depend on it).
const (
	CatHealth        = "health"
	CatFinance       = "finance"
	CatRelationships = "relationships" // family, partner, friends, colleagues, and evaluations of a person
	CatEmotion       = "emotion"
	CatLegalID       = "legal_id" // identity papers and numbers, visas, contracts, lawsuits
)

// CategoryNames lists the categories in display order.
var CategoryNames = []string{CatHealth, CatFinance, CatRelationships, CatEmotion, CatLegalID}

// Who tagged a category.
const (
	ByModel        = "model"         // the extraction's label, or the plugin's keyword rules
	BySpaceDefault = "space_default" // the source space's default categories
	ByUser         = "user"
)

// Category is one category of an entry and who put it there.
type Category struct {
	Name string `json:"name"`
	By   string `json:"by"`
}

// CategoryOf maps a label (the daily prompt's sensitive field, the K13 names, a seed's category) to a category, ""
// when it is none. K13's "family" is part of relationships.
func CategoryOf(label string) string {
	l := strings.ToLower(strings.TrimSpace(label))
	switch l {
	case SensFamily:
		return CatRelationships
	case CatHealth, CatFinance, CatRelationships, CatEmotion, CatLegalID:
		return l
	}
	return ""
}

// Cats are the entry's categories: Categories, and the legacy single Sensitive label (entries before 0.2.0-beta.40).
func (e *Entry) Cats() []string {
	var out []string
	for _, c := range e.Categories {
		if n := CategoryOf(c.Name); n != "" && !slices.Contains(out, n) && !slices.Contains(e.CatsRemoved, n) {
			out = append(out, n)
		}
	}
	if n := CategoryOf(e.Sensitive); n != "" && !slices.Contains(out, n) && !slices.Contains(e.CatsRemoved, n) {
		out = append(out, n)
	}
	return out
}

// IsSensitive says whether the entry carries any category (not general).
func (e *Entry) IsSensitive() bool { return len(e.Cats()) > 0 }

// AddCategory adds a category (anyone may add); one the user removed is not added back but by the user.
func (e *Entry) AddCategory(name, by string) bool {
	n := CategoryOf(name)
	if n == "" || slices.Contains(e.Cats(), n) || by != ByUser && slices.Contains(e.CatsRemoved, n) {
		return false
	}
	e.CatsRemoved = slices.DeleteFunc(e.CatsRemoved, func(r string) bool { return r == n })
	e.Categories = append(e.Categories, Category{Name: n, By: by})
	return true
}

// RemoveCategory takes a category off; only the user may (agents and the plugin only add).
func (e *Entry) RemoveCategory(name, by string) bool {
	n := CategoryOf(name)
	if n == "" || by != ByUser || !slices.Contains(e.Cats(), n) {
		return false
	}
	e.Categories = slices.DeleteFunc(e.Categories, func(c Category) bool { return CategoryOf(c.Name) == n })
	if CategoryOf(e.Sensitive) == n {
		e.Sensitive = ""
	}
	e.CatsRemoved = append(e.CatsRemoved, n)
	return true
}

// MergeCategories adds every category of from (with who tagged it) to e: what one sighting carries, the entry
// carries.
func (e *Entry) MergeCategories(from *Entry) {
	if from == nil {
		return
	}
	for _, c := range from.Categories {
		e.AddCategory(c.Name, c.By)
	}
	if n := CategoryOf(from.Sensitive); n != "" {
		e.AddCategory(n, ByModel)
	}
	if e.OriginSpace == "" {
		e.OriginSpace = from.OriginSpace
	}
}

// SpaceDefaults are the default categories of a space, by slug. The hub does not tell an extension a space's
// default categories yet; the health space's is fixed by its slug (domain spaces §3.6.4 keeps the slug).
func SpaceDefaults(space string) []string {
	if strings.TrimSpace(space) == "health" {
		return []string{CatHealth}
	}
	return nil
}

// TagSpace sets the entry's source space, when it has none, and adds the space's default categories.
func (e *Entry) TagSpace(space string) {
	space = strings.TrimSpace(space)
	if space == "" {
		return
	}
	if e.OriginSpace == "" {
		e.OriginSpace = space
	}
	for _, c := range SpaceDefaults(space) {
		e.AddCategory(c, BySpaceDefault)
	}
}

// Resident says whether the entry may reach the resident exits (the assistant's instruction block, the agents'
// rules, the profile and project files): the strict side of scene 2 (unified memory design v2 §3.3b, §8). A general
// entry may; one with categories only when the user widened every one of them (K22.<category> on), or when the user
// wrote it about themself (about me). An entry from a domain space reaches them only as a way of working with the
// AI or as what the user is like (interaction, persona): the space's own content stays in the space and in look-ups.
func (e *Entry) Resident(sw map[string]SwitchSetting) bool {
	if e.OriginSpace != "" && e.Facet != Interaction && e.Facet != Persona {
		return false
	}
	if e.Origin == SelfReport {
		return true
	}
	for _, c := range e.Cats() {
		if !On(sw, K22Resident+"."+c) {
			return false
		}
	}
	return true
}

// Named says whether every category of the entry is among names (a look-up asks for a category by name before it is
// given content in it).
func (e *Entry) Named(names []string) bool {
	for _, c := range e.Cats() {
		if !slices.ContainsFunc(names, func(n string) bool { return CategoryOf(n) == c }) {
			return false
		}
	}
	return true
}
