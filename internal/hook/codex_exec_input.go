package hook

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	treesitter "github.com/tree-sitter/go-tree-sitter"
	grammars "goodkind.io/gksyntax/treesitter"
)

type codexExecInput struct {
	source  []byte
	root    *treesitter.Node
	command string
	workdir string
	matches int
	unknown bool
}

type codexObjectEntryKind string

const (
	codexObjectSpread    codexObjectEntryKind = "spread_element"
	codexObjectMethod    codexObjectEntryKind = "method_definition"
	codexObjectShorthand codexObjectEntryKind = "shorthand_property_identifier"
	codexObjectPair      codexObjectEntryKind = "pair"
)

func codexExecWorkdirFromInput(input, command string) (string, bool, error) {
	language, found := grammars.GrammarForLanguage("javascript")
	if !found {
		err := errors.New("JavaScript grammar is unavailable")
		slog.Error("load Codex exec JavaScript grammar failed", "err", err)
		return "", true, err
	}
	parser := treesitter.NewParser()
	defer parser.Close()
	if err := parser.SetLanguage(language); err != nil {
		slog.Error("set Codex exec JavaScript parser language failed", "err", err)
		return "", true, fmt.Errorf("set JavaScript parser language: %w", err)
	}
	source := []byte(input)
	tree := parser.Parse(source, nil)
	if tree == nil {
		err := errors.New("JavaScript parser returned no tree")
		slog.Error("parse Codex exec JavaScript input returned no tree", "err", err)
		return "", true, err
	}
	defer tree.Close()
	root := tree.RootNode()
	if root.HasError() {
		return "", true, nil
	}
	scan := codexExecInput{source: source, root: root, command: command, workdir: "", matches: 0, unknown: false}
	scan.walk(root)
	if scan.unknown || scan.matches > 1 {
		return "", true, nil
	}
	return scan.workdir, scan.matches == 1, nil
}

func (scan *codexExecInput) walk(node *treesitter.Node) {
	if node.Kind() == "call_expression" {
		scan.inspectCall(node)
	}
	for index := range node.NamedChildCount() {
		scan.walk(node.NamedChild(index))
	}
}

func (scan *codexExecInput) inspectCall(node *treesitter.Node) {
	function := node.ChildByFieldName("function")
	if function == nil || function.Kind() != "member_expression" {
		return
	}
	object := function.ChildByFieldName("object")
	property := function.ChildByFieldName("property")
	if object == nil || property == nil || object.Utf8Text(scan.source) != "tools" || property.Utf8Text(scan.source) != "exec_command" {
		return
	}
	arguments := node.ChildByFieldName("arguments")
	if arguments == nil || arguments.NamedChildCount() != 1 {
		scan.unknown = true
		return
	}
	input := arguments.NamedChild(0)
	commandNode, unambiguous := scan.objectField(input, "cmd")
	command, resolved := scan.stringValue(commandNode)
	if !unambiguous {
		scan.unknown = true
		return
	}
	if !resolved {
		if scan.commandCouldMatch(commandNode) {
			scan.unknown = true
		}
		return
	}
	if command != scan.command {
		return
	}
	scan.matches++
	workdirNode, unambiguous := scan.objectField(input, "workdir")
	if unambiguous {
		scan.workdir, _ = scan.stringValue(workdirNode)
	}
}

func (scan *codexExecInput) commandCouldMatch(node *treesitter.Node) bool {
	if node == nil || node.Kind() != "binary_expression" {
		return true
	}
	operator := node.ChildByFieldName("operator")
	if operator == nil || operator.Utf8Text(scan.source) != "+" {
		return true
	}
	// Literal string operands constrain concatenation even when the other
	// operand cannot be resolved. Other expressions remain ambiguous.
	if prefix, resolved := scan.stringValue(node.ChildByFieldName("left")); resolved && !strings.HasPrefix(scan.command, prefix) {
		return false
	}
	if suffix, resolved := scan.stringValue(node.ChildByFieldName("right")); resolved && !strings.HasSuffix(scan.command, suffix) {
		return false
	}
	return true
}

func (scan *codexExecInput) objectField(object *treesitter.Node, name string) (*treesitter.Node, bool) {
	if object == nil || object.Kind() != "object" {
		return nil, false
	}
	var result *treesitter.Node
	for index := range object.NamedChildCount() {
		child := object.NamedChild(index)
		switch codexObjectEntryKind(child.Kind()) {
		case codexObjectSpread, codexObjectMethod:
			return nil, false
		case codexObjectShorthand:
			if child.Utf8Text(scan.source) == name {
				if result != nil {
					return nil, false
				}
				result = child
			}
		case codexObjectPair:
			key := child.ChildByFieldName("key")
			keyName := key.Utf8Text(scan.source)
			if key.Kind() == "string" {
				keyName, _ = codexJSString(key, scan.source)
			}
			if key.Kind() == "computed_property_name" {
				return nil, false
			}
			if keyName == name {
				if result != nil {
					return nil, false
				}
				result = child.ChildByFieldName("value")
			}
		}
	}
	return result, true
}

func (scan *codexExecInput) stringValue(node *treesitter.Node) (string, bool) {
	if node == nil {
		return "", false
	}
	if node.Kind() != "identifier" && node.Kind() != "shorthand_property_identifier" {
		return codexJSString(node, scan.source)
	}
	// Resolve only preceding constants in the top-level scope. Nested scopes
	// can shadow a constant, and mutable declarations do not establish its value.
	for parent := node.Parent(); parent != nil; parent = parent.Parent() {
		if parent.Kind() == "statement_block" || strings.Contains(parent.Kind(), "function") {
			return "", false
		}
	}
	name := node.Utf8Text(scan.source)
	for index := range scan.root.NamedChildCount() {
		declaration := scan.root.NamedChild(index)
		if declaration.StartByte() >= node.StartByte() || declaration.Kind() != "lexical_declaration" || declaration.Child(0).Kind() != "const" {
			continue
		}
		for childIndex := range declaration.NamedChildCount() {
			binding := declaration.NamedChild(childIndex)
			identifier := binding.ChildByFieldName("name")
			if identifier != nil && identifier.Kind() == "identifier" && identifier.Utf8Text(scan.source) == name {
				return codexJSString(binding.ChildByFieldName("value"), scan.source)
			}
		}
	}
	return "", false
}

func codexJSString(node *treesitter.Node, source []byte) (string, bool) {
	if node == nil || node.Kind() != "string" {
		return "", false
	}
	text := node.Utf8Text(source)
	if strings.HasPrefix(text, "\"") {
		var value string
		if err := json.Unmarshal([]byte(text), &value); err != nil {
			return "", false
		}
		return value, true
	}
	if len(text) >= 2 && !strings.ContainsRune(text, '\\') {
		return text[1 : len(text)-1], true
	}
	return "", false
}
