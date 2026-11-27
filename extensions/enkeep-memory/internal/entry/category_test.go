package entry

import (
	"slices"
	"testing"
)

// Categories: the legacy single label reads as a category; anyone adds, only the user removes, and a category the
// user removed is not added back but by the user; a space tags its default; the resident exits take a category only
// with K22 on, and a domain space's content only as interaction or persona. Synthetic entries only.
func TestCategories(t *testing.T) {
	e := &Entry{Facet: Persona, Sensitive: SensFamily}
	if !slices.Equal(e.Cats(), []string{CatRelationships}) || !e.IsSensitive() {
		t.Fatalf("legacy label: %v", e.Cats())
	}
	if !e.AddCategory(CatLegalID, ByModel) || e.AddCategory("legal_id", ByModel) || e.AddCategory("gossip", ByModel) {
		t.Fatal("add")
	}
	if e.RemoveCategory(CatRelationships, ByModel) || !e.RemoveCategory(CatRelationships, ByUser) || e.Sensitive != "" {
		t.Fatalf("remove: %+v", e)
	}
	if e.AddCategory(SensFamily, ByModel) || !e.AddCategory(SensFamily, ByUser) {
		t.Fatal("a category the user removed came back by the model, or not by the user")
	}
	if !slices.Equal(e.Cats(), []string{CatLegalID, CatRelationships}) {
		t.Fatalf("cats: %v", e.Cats())
	}
	sw := map[string]SwitchSetting{K22Resident + "." + CatLegalID: {Value: "on", By: "user"}}
	if e.Resident(sw) {
		t.Fatal("relationships still off")
	}
	sw[K22Resident+"."+CatRelationships] = SwitchSetting{Value: "on", By: "user"}
	if !e.Resident(sw) {
		t.Fatal("both on")
	}

	w := &Entry{Facet: Workflow}
	w.TagSpace("health")
	if w.OriginSpace != "health" || !slices.Equal(w.Cats(), []string{CatHealth}) || w.Categories[0].By != BySpaceDefault {
		t.Fatalf("space: %+v", w)
	}
	if w.Resident(map[string]SwitchSetting{K22Resident + "." + CatHealth: {Value: "on", By: "user"}}) {
		t.Fatal("a space's workflow reached the resident exits")
	}
	p := &Entry{Facet: Interaction}
	p.TagSpace("reading")
	if !p.Resident(nil) || p.IsSensitive() {
		t.Fatal("a space with no default category: a way of working with the AI goes out")
	}
	self := &Entry{Facet: Persona, Origin: SelfReport}
	self.AddCategory(CatHealth, ByModel)
	if !self.Resident(nil) {
		t.Fatal("what the user wrote about themself goes out")
	}
	k := &Entry{Facet: Persona}
	k.MergeCategories(w)
	if !slices.Equal(k.Cats(), []string{CatHealth}) || k.OriginSpace != "health" {
		t.Fatalf("merge: %+v", k)
	}
}
