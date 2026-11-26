package main

import (
	"strings"
	"testing"

	"github.com/nwverse/persona/sdk/go/ext"
	"github.com/nwverse/persona/sdk/go/wire"
)

func TestDetailCombinedBoundsAndCreateLocale(t *testing.T) {
	t.Run("short detail control", func(t *testing.T) {
		v := detailView(&ext.UIRequest{UIParams: wire.UIParams{Locale: "en", UIVersion: 6}}, pageState{Mode: "detail", EntityID: entityID}, sampleEntity())
		if err := wire.CheckView(v); err != nil {
			t.Fatalf("control detailView failed: %v", err)
		}
	})

	t.Run("detailView with multiline description and notes stays within wire.ItemsMost", func(t *testing.T) {
		item := sampleEntity()
		item.Description = strings.Repeat("description line\n", 180)
		item.Notes = strings.Repeat("maintenance note\n", 180)
		v := detailView(&ext.UIRequest{UIParams: wire.UIParams{Locale: "zh-Hans", UIVersion: 6}}, pageState{Mode: "detail", EntityID: entityID}, item)
		if err := wire.CheckView(v); err != nil {
			t.Fatalf("detailView with multiline description and notes failed CheckView: %v", err)
		}
	})

	t.Run("inventory page localizes create section for zh-Hans and ja", func(t *testing.T) {
		f := newFakeHomebox(t)
		a := f.app()
		for _, locale := range []string{"zh-Hans", "ja"} {
			v, err := a.page(t.Context(), &ext.UIRequest{UIParams: wire.UIParams{Locale: locale, UIVersion: 6}}, pageState{})
			if err != nil {
				t.Fatalf("%s page failed: %v", locale, err)
			}
			if err := wire.CheckView(v); err != nil {
				t.Fatalf("%s CheckView failed: %v", locale, err)
			}
			want := textsFor(locale).create
			if got := v.Sections[1].Title; got != want {
				t.Fatalf("%s create section title = %q, want %q", locale, got, want)
			}
			if push := v.Sections[1].Items[0].Action.Push; push == nil || push.Title != want {
				t.Fatalf("%s create push title = %+v, want %q", locale, push, want)
			}
		}
	})
}
