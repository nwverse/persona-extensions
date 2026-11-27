package curate

import (
	"slices"
	"testing"
)

// The settings renamed in 0.2.0 (the hub's install preview took admit_all_below_tokens and private_keywords for
// secrets) still count when a hub kept a configuration saved under the old names; privacy keywords from both names
// are kept.
func TestConfigOldNames(t *testing.T) {
	c, err := ParseConfig([]byte(`{"admit_all_below_tokens": 5000, "private_keywords": ["体检", "工资"], "privacy_keywords": ["工资", "病历"]}`))
	if err != nil {
		t.Fatal(err)
	}
	if c.AdmitAllBelowTokens != 5000 {
		t.Errorf("admit_all_below from the old name: %d", c.AdmitAllBelowTokens)
	}
	for _, k := range []string{"体检", "工资", "病历"} {
		if !slices.Contains(c.PrivateKeywords, k) {
			t.Errorf("privacy keyword %q lost: %v", k, c.PrivateKeywords)
		}
	}
	if len(c.PrivateKeywords) != 3 {
		t.Errorf("duplicates: %v", c.PrivateKeywords)
	}
	// A value set under the new name wins over the old one.
	c, _ = ParseConfig([]byte(`{"admit_all_below_tokens": 5000, "admit_all_below": 800}`))
	if c.AdmitAllBelowTokens != 800 {
		t.Errorf("the new name: %d", c.AdmitAllBelowTokens)
	}
	// Neither: the default.
	c, _ = ParseConfig([]byte(`{}`))
	if c.AdmitAllBelowTokens != Defaults().AdmitAllBelowTokens || len(c.PrivateKeywords) != 0 {
		t.Errorf("defaults: %+v", c)
	}
}

// The model route: on by default (no primary model until one is set; Sonnet for long calls, Haiku for short ones); "purpose"
// turns it off, back to every call on the purpose's model.
func TestConfigRoute(t *testing.T) {
	rt := Defaults().Route()
	if rt == nil || len(rt.Primary) != 0 || rt.Long != "claude-sonnet-5-5" ||
		rt.Short != "claude-haiku-5-5" || rt.LongAbove != 16000 {
		t.Fatalf("default route %+v", rt)
	}
	c, err := ParseConfig([]byte(`{"model_route": "purpose"}`))
	if err != nil || c.Route() != nil {
		t.Fatalf("purpose: %+v %v", c.Route(), err)
	}
	c, err = ParseConfig([]byte(`{"model_primary": ["m1", "m2"], "model_long_above": 8000}`))
	if err != nil || !slices.Equal(c.Route().Primary, []string{"m1", "m2"}) || c.Route().LongAbove != 8000 {
		t.Fatalf("set: %+v %v", c.Route(), err)
	}
	c, err = ParseConfig([]byte(`{"model_primary": [], "model_long": "", "model_short": ""}`))
	if err != nil || c.Route() != nil {
		t.Fatalf("no models: %+v %v", c.Route(), err)
	}
}
