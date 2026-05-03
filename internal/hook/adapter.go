package hook

func NormalizePayload(system HookSystem, raw RawPayload) RawPayload {
	switch system {
	case SystemVSCode:
		return NormalizeVSCodePayload(raw)
	case SystemCopilot:
		return NormalizeCopilotPayload(raw)
	default:
		return raw
	}
}
