package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/nwverse/persona-extensions/extensions/household/internal/ui"
)

// TestCardOpensThePage keeps the overview card's tap on the nav page: the card's empty state sends the user there.
func TestCardOpensThePage(t *testing.T) {
	var m struct {
		Contributes struct {
			UI []struct{ ID, Placement, Opens string }
		}
	}
	if err := json.Unmarshal(authorManifest, &m); err != nil {
		t.Fatal(err)
	}
	for _, u := range m.Contributes.UI {
		if u.Placement == "overview_card" && u.Opens == ui.PageUI {
			return
		}
	}
	t.Fatalf("no overview card that opens %q: %+v", ui.PageUI, m.Contributes.UI)
}

// TestReminderSettingsSpeakPlainly keeps the settings free of format codes and hub wording, with an example time and
// the clock they follow.
func TestReminderSettingsSpeakPlainly(t *testing.T) {
	var m struct {
		Configuration struct {
			Properties map[string]struct{ Title, Description string }
		}
	}
	if err := json.Unmarshal(authorManifest, &m); err != nil {
		t.Fatal(err)
	}
	for key, p := range m.Configuration.Properties {
		for _, jargon := range []string{"HH:MM", "hub", "时区", "角标"} {
			if strings.Contains(p.Title+p.Description, jargon) {
				t.Errorf("%s says %q: %s", key, jargon, p.Description)
			}
		}
	}
	at := m.Configuration.Properties["remind_time"].Description
	for _, w := range []string{"例如 08:30", "运行 Persona 的那台电脑的时间"} {
		if !strings.Contains(at, w) {
			t.Errorf("remind_time does not say %q: %s", w, at)
		}
	}
	if d := m.Configuration.Properties["remind"].Description; !strings.Contains(d, "「管理待办」") {
		t.Errorf("remind does not name the to-do permission: %s", d)
	}
}
