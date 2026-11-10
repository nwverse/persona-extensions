package ext

func extensionEnv(name string) string {
	return first("PERSONA_EXTENSION_"+name, "PERSONA_PLUGIN_"+name, "JARVIS_EXTENSION_"+name, "JARVIS_PLUGIN_"+name)
}

func hubEnv(name string) string { return first("PERSONA_HUB_"+name, "JARVIS_HUB_"+name) }
