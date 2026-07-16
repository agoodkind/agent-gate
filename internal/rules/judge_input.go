package rules

import "strings"

// buildJudgeInput assembles the context panels an LLM judge sees for one tool
// call: the chat working directory, the tool-call cwd (when it differs), the tool
// name, the verbatim tool input, a structural parse of a shell command, and the
// recent conversation tail. transcriptTail is fetched once per command by the
// caller and passed in (may be empty when the fetch failed / fail-open). It does
// no I/O and no policy judgment; it just renders labeled, token-frugal text.
//
// The panels lead with the situation (chat working dir, then the conversation)
// so a later task can place the stable rule-intents prefix ahead of this
// per-call variable panel, then present the thing being judged (tool, verbatim
// call, structural parse). It never labels anything safe or dangerous; it only
// renders directory, call, structure, and conversation so a downstream general
// judge can reason.
func buildJudgeInput(fields FieldSet, transcriptTail string) string {
	var builder strings.Builder

	renderWorkingDirectories(&builder, fields)
	renderConversationPanel(&builder, transcriptTail)
	renderToolPanel(&builder, fields)

	return strings.TrimSpace(builder.String())
}

// renderWorkingDirectories writes the chat working directory (the project the
// conversation is about) and, only when it differs, the effective tool-call
// directory the command runs in after any cd. Surfacing the difference matters
// because a command running in a different directory than the project is
// decision-relevant.
func renderWorkingDirectories(builder *strings.Builder, fields FieldSet) {
	chatCwd := strings.TrimSpace(fields.CWD)
	if chatCwd != "" {
		builder.WriteString("chat working directory: " + chatCwd + "\n")
	}
	effectiveCwd := strings.TrimSpace(fields.effectiveCWD())
	if effectiveCwd != "" && effectiveCwd != chatCwd {
		builder.WriteString("tool-call directory: " + effectiveCwd + "\n")
	}
}

// renderConversationPanel writes the caller-provided transcript tail verbatim
// under a clear label. The tail is already token-bounded by the caller, so this
// includes it as-is and drops the panel entirely when empty.
func renderConversationPanel(builder *strings.Builder, transcriptTail string) {
	tail := strings.TrimSpace(transcriptTail)
	if tail == "" {
		return
	}
	builder.WriteString("\nrecent conversation:\n" + tail + "\n")
}

// renderToolPanel writes the tool name, the verbatim tool input, and a
// structural parse. A shell command is parsed with renderCommandAST; a write
// tool surfaces its target file path and a bounded content snippet instead,
// since there is no shell to parse.
func renderToolPanel(builder *strings.Builder, fields FieldSet) {
	toolName := strings.TrimSpace(fields.ToolName)
	if toolName != "" {
		builder.WriteString("\ntool: " + toolName + "\n")
	}

	command := strings.TrimSpace(fields.CommandValue())
	if command != "" {
		renderShellCall(builder, fields, command)
		return
	}

	filePath := strings.TrimSpace(fields.FilePathValue())
	if filePath != "" {
		renderWriteCall(builder, fields, filePath)
	}
}

// renderShellCall writes the verbatim command and its structural AST parse. The
// AST starts from the tool-call directory before the command's own cd chain, so
// renderCommandAST can resolve any in-command cd itself. Home is passed empty to
// keep the render pure; renderCommandAST tolerates an empty home.
func renderShellCall(builder *strings.Builder, fields FieldSet, command string) {
	builder.WriteString("\ntool call:\n" + command + "\n")
	startCwd := fields.BaseCWD()
	builder.WriteString("\nstructure:\n" + renderCommandAST(command, startCwd, "") + "\n")
}

// renderWriteCall writes the target file path as the verbatim salient field plus
// a bounded snippet of the new content, then surfaces the write target
// structurally so the judge sees the effect uniformly with the shell-write case.
func renderWriteCall(builder *strings.Builder, fields FieldSet, filePath string) {
	builder.WriteString("\ntool call:\nwrites: " + filePath + "\n")
	snippet := judgeWriteContentSnippet(fields)
	if snippet != "" {
		builder.WriteString("content (truncated): " + snippet + "\n")
	}
	builder.WriteString("\nstructure:\nwrites: " + filePath + "\n")
}

// judgeWriteContentSnippet returns a bounded, single-line preview of the new
// content a write tool applies, preferring the full-content field, then an
// edit's replacement string, then a multi-edit's joined replacements. It never
// dumps a full body: renderSnippet collapses whitespace and truncates.
func judgeWriteContentSnippet(fields FieldSet) string {
	candidates := []string{
		fields.ToolInputContent,
		fields.ToolInputNewString,
		strings.Join(fields.EditsNewString, "\n"),
	}
	for _, candidate := range candidates {
		candidate = strings.TrimSpace(candidate)
		if candidate != "" {
			return renderSnippet(candidate)
		}
	}
	return ""
}
