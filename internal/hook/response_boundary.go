package hook

// ResponseDecision identifies the provider-neutral outcome that a hook
// renderer must encode for the invoking agent host.
type ResponseDecision string

// ResponseDecision variants.
const (
	// ResponseDecisionAllow lets the provider continue the hook event.
	ResponseDecisionAllow ResponseDecision = "allow"
	// ResponseDecisionBlock asks the provider to block the hook event.
	ResponseDecisionBlock ResponseDecision = "block"
)

// FailOpenReason identifies why agent-gate emitted an allow response without
// a successful daemon evaluation.
type FailOpenReason string

// FailOpenReason variants. The string values are stable audit/test labels.
const (
	// FailOpenReasonStdinRead means the hook process could not read stdin.
	FailOpenReasonStdinRead FailOpenReason = "stdin_read_failed"
	// FailOpenReasonDaemonUnavailable means the daemon transport was unavailable.
	FailOpenReasonDaemonUnavailable FailOpenReason = "daemon_unavailable"
	// FailOpenReasonRPCFailed means the daemon returned a transport/RPC error.
	FailOpenReasonRPCFailed FailOpenReason = "rpc_failed"
	// FailOpenReasonPanic means the hook entrypoint recovered a panic.
	FailOpenReasonPanic FailOpenReason = "panic_recovered"
)

// ResponseRequest is the provider-neutral declaration used at the hook
// response boundary. Provider renderers own the concrete stdout, stderr, and
// exit-code shape for their system.
type ResponseRequest struct {
	System         HookSystem
	EventName      string
	Decision       ResponseDecision
	DiagnosticText string
	EventID        string
	FailOpenReason FailOpenReason
}

// Response is the concrete process response returned by a provider
// renderer.
type Response struct {
	Stdout   []byte
	Stderr   []byte
	ExitCode int
}

// RenderResponse turns a provider-neutral response declaration into the
// bytes and exit code expected by the invoking provider.
func RenderResponse(request ResponseRequest) Response {
	if request.Decision == ResponseDecisionBlock {
		request.DiagnosticText = diagnosticWithEventID(request.DiagnosticText, request.EventID)
	}
	switch request.System {
	case SystemCursor:
		return renderCursorResponse(request)
	case SystemCodex:
		return renderCodexResponse(request)
	case SystemGemini:
		return renderGeminiResponse(request)
	case SystemClaude, SystemVSCode, SystemCopilot:
		return renderClaudeResponse(request)
	case SystemUnknown:
		return renderUnknownResponse(request)
	default:
		return renderUnknownResponse(request)
	}
}

func diagnosticWithEventID(text, eventID string) string {
	if eventID == "" {
		return text
	}
	if text == "" {
		return "agent-gate event_id: " + eventID
	}
	return text + "\n\nagent-gate event_id: " + eventID
}

// FailOpenResponse emits a non-blocking response for hook transport,
// availability, or internal failures. Policy decisions from the daemon should
// not use this path.
func FailOpenResponse(system HookSystem, eventName string, diagnosticText string, reason FailOpenReason) Response {
	return RenderResponse(ResponseRequest{
		System:         system,
		EventName:      eventName,
		Decision:       ResponseDecisionAllow,
		DiagnosticText: diagnosticText,
		EventID:        "",
		FailOpenReason: reason,
	})
}

// Unknown providers get an empty stdout success because no provider-specific
// response schema is known at this boundary; command-hook systems commonly
// treat exit 0 with no output as allow, and emitting Claude JSON would be a
// provider-specific guess.
func renderUnknownResponse(request ResponseRequest) Response {
	if request.Decision == ResponseDecisionBlock {
		return Response{
			Stdout:   nil,
			Stderr:   []byte(request.DiagnosticText + "\n"),
			ExitCode: 2,
		}
	}
	return Response{Stdout: nil, Stderr: nil, ExitCode: 0}
}
