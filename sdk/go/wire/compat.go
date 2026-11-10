package wire

import "maps"

// Canonical schemas remain unchanged. This detached validation projection admits
// only the two pre-cutover built-in icon aliases, at the exact manifest fields.
// It never rewrites signed package bytes or caller-owned data.
func compatibilityValue(def string, value any) any {
	if def == "BuiltinLook" {
		if s, ok := value.(string); ok {
			return canonicalBuiltinLook(s)
		}
		return value
	}
	object, ok := value.(map[string]any)
	if !ok {
		return value
	}
	if def == "LookItem" {
		return compatibleLookItem(object)
	}
	if def != "Manifest" {
		return value
	}
	contributions, ok := object["contributes"].(map[string]any)
	if !ok {
		return value
	}
	looks, ok := contributions["looks"].([]any)
	if !ok {
		return value
	}
	items := append([]any(nil), looks...)
	for i, item := range items {
		if object, ok := item.(map[string]any); ok {
			items[i] = compatibleLookItem(object)
		}
	}
	nested := maps.Clone(contributions)
	nested["looks"] = items
	out := maps.Clone(object)
	out["contributes"] = nested
	return out
}
func compatibleLookItem(item map[string]any) map[string]any {
	old, ok := item["app_icon_fallback"].(string)
	if !ok {
		return item
	}
	current := canonicalBuiltinLook(old)
	if old == current {
		return item
	}
	out := maps.Clone(item)
	out["app_icon_fallback"] = current
	return out
}
func canonicalBuiltinLook(value string) string {
	// Both retired legacy fallbacks render as the current default; accepting
	// their bytes does not reintroduce retired canonical enum values or drawings.
	switch value {
	case "jarvis", "jarvis-circuit":
		return "persona"
	}
	return value
}

// Only the scheme alias is admitted; authority, path and capability checks stay
// in CheckAssistantOpen, identical for either spelling.
func assistantURLScheme(s string) bool { return s == "persona" || s == "jarvis" }
