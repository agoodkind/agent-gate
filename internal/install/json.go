package installer

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

const jsonCommandKey = "command"

var errInvalidHookJSON = errors.New("invalid JSON")

func prepareJSONHooks(
	templatesDir string,
	tool string,
	binPath string,
	targetPath string,
	cursor bool,
) ([]byte, error) {
	renderedHooks, err := renderJSONHooks(templatesDir, tool, binPath)
	if err != nil {
		return nil, err
	}

	configJSON := []byte(defaultGenericJSONHooks)
	if cursor {
		configJSON = []byte(defaultCursorConfig)
	}

	if existingJSON, readErr := os.ReadFile(targetPath); readErr == nil {
		configJSON = existingJSON
	} else if !errors.Is(readErr, os.ErrNotExist) {
		return nil, logInstallError(
			"read JSON hook config failed",
			fmt.Errorf("read %s: %w", targetPath, readErr),
			slog.String("path", targetPath),
		)
	}

	var target map[string]json.RawMessage
	if err := json.Unmarshal(configJSON, &target); err != nil {
		return nil, logInstallError(
			"parse JSON hook config failed",
			fmt.Errorf("parse %s: %w", targetPath, err),
			slog.String("path", targetPath),
		)
	}

	if target == nil {
		target = make(map[string]json.RawMessage)
	}

	if cursor {
		if _, ok := target["version"]; !ok {
			target["version"] = json.RawMessage("1")
		}
	}

	mergedHooks, err := mergeJSONHooks(target["hooks"], renderedHooks)
	if err != nil {
		return nil, logInstallError(
			"merge JSON hooks failed",
			fmt.Errorf("merge %s: %w", targetPath, err),
			slog.String("path", targetPath),
		)
	}

	target["hooks"] = mergedHooks

	return marshalJSONHookConfig(targetPath, target)
}

func prepareReplacementJSONHooks(
	templatesDir string,
	tool string,
	binPath string,
	targetPath string,
) ([]byte, error) {
	renderedHooks, err := renderJSONHooks(templatesDir, tool, binPath)
	if err != nil {
		return nil, err
	}

	if _, readErr := os.ReadFile(targetPath); readErr != nil &&
		!errors.Is(readErr, os.ErrNotExist) {
		return nil, logInstallError(
			"read JSON hook config failed",
			fmt.Errorf("read %s: %w", targetPath, readErr),
			slog.String("path", targetPath),
		)
	}

	target := map[string]json.RawMessage{"hooks": renderedHooks}

	return marshalJSONHookConfig(targetPath, target)
}

func marshalJSONHookConfig(targetPath string, target map[string]json.RawMessage) ([]byte, error) {
	output, err := json.MarshalIndent(target, "", "  ")
	if err != nil {
		return nil, logInstallError(
			"render JSON hook config failed",
			fmt.Errorf("render %s: %w", targetPath, err),
			slog.String("path", targetPath),
		)
	}

	output = append(output, '\n')

	return output, nil
}

func mergeJSONHooks(
	existingJSON json.RawMessage,
	managedJSON json.RawMessage,
) (json.RawMessage, error) {
	existingHooks := make(map[string][]json.RawMessage)
	if len(existingJSON) > 0 && string(existingJSON) != "null" {
		if err := json.Unmarshal(existingJSON, &existingHooks); err != nil {
			return nil, logInstallError(
				"parse existing JSON hooks failed",
				fmt.Errorf("parse existing hooks: %w", err),
			)
		}
	}

	managedHooks := make(map[string][]json.RawMessage)
	if err := json.Unmarshal(managedJSON, &managedHooks); err != nil {
		return nil, logInstallError(
			"parse managed JSON hooks failed",
			fmt.Errorf("parse managed hooks: %w", err),
		)
	}

	for eventName, groups := range existingHooks {
		if len(groups) == 0 {
			continue
		}

		preservedGroups := make([]json.RawMessage, 0, len(groups))
		for _, group := range groups {
			preservedGroup, preserve, err := removeAgentGateCommands(group)
			if err != nil {
				return nil, logInstallError(
					"clean JSON hook event failed",
					fmt.Errorf("clean event %s: %w", eventName, err),
					slog.String("event", eventName),
				)
			}

			if preserve {
				preservedGroups = append(preservedGroups, preservedGroup)
			}
		}

		if len(preservedGroups) == 0 {
			delete(existingHooks, eventName)

			continue
		}

		existingHooks[eventName] = preservedGroups
	}

	for eventName, managedGroups := range managedHooks {
		existingHooks[eventName] = append(existingHooks[eventName], managedGroups...)
	}

	output, err := json.Marshal(existingHooks)
	if err != nil {
		return nil, logInstallError(
			"marshal merged JSON hooks failed",
			fmt.Errorf("marshal merged hooks: %w", err),
		)
	}

	return json.RawMessage(output), nil
}

func removeAgentGateCommands(group json.RawMessage) (json.RawMessage, bool, error) {
	var groupObject map[string]json.RawMessage
	if err := json.Unmarshal(group, &groupObject); err != nil {
		return nil, false, logInstallError(
			"parse JSON hook group failed",
			fmt.Errorf("parse hook group: %w", err),
		)
	}

	if isAgentGateCommandObject(groupObject) {
		return nil, false, nil
	}

	nestedHooksJSON, hasNestedHooks := groupObject["hooks"]
	if !hasNestedHooks {
		return group, true, nil
	}

	var nestedHooks []json.RawMessage
	if err := json.Unmarshal(nestedHooksJSON, &nestedHooks); err != nil {
		return nil, false, logInstallError(
			"parse JSON command group failed",
			fmt.Errorf("parse command group: %w", err),
		)
	}

	if len(nestedHooks) == 0 {
		return group, true, nil
	}

	preservedHooks := make([]json.RawMessage, 0, len(nestedHooks))
	for _, hook := range nestedHooks {
		var hookObject map[string]json.RawMessage
		if err := json.Unmarshal(hook, &hookObject); err != nil {
			preservedHooks = append(preservedHooks, hook)

			continue
		}

		if !isAgentGateCommandObject(hookObject) {
			preservedHooks = append(preservedHooks, hook)
		}
	}

	if len(preservedHooks) == 0 {
		return nil, false, nil
	}

	preservedHooksJSON, err := json.Marshal(preservedHooks)
	if err != nil {
		return nil, false, logInstallError(
			"marshal JSON command group failed",
			fmt.Errorf("marshal command group: %w", err),
		)
	}

	groupObject["hooks"] = preservedHooksJSON
	preservedGroup, err := json.Marshal(groupObject)
	if err != nil {
		return nil, false, logInstallError(
			"marshal JSON hook group failed",
			fmt.Errorf("marshal hook group: %w", err),
		)
	}

	return json.RawMessage(preservedGroup), true, nil
}

func isAgentGateCommandObject(commandObject map[string]json.RawMessage) bool {
	commandJSON, ok := commandObject[jsonCommandKey]
	if !ok {
		return false
	}

	var command string
	if err := json.Unmarshal(commandJSON, &command); err != nil {
		return false
	}

	trimmedCommand := strings.TrimSpace(command)
	if trimmedCommand == agentGateBinaryName {
		return true
	}

	if markerIndex := strings.LastIndex(trimmedCommand, " managed-hook "); markerIndex > 0 {
		executable, err := parseShellCommandArgument(
			strings.TrimSpace(trimmedCommand[:markerIndex]),
		)

		return err == nil && filepath.Base(executable) == agentGateBinaryName
	}

	executable := firstCommandToken(trimmedCommand)

	return filepath.Base(executable) == agentGateBinaryName
}

func firstCommandToken(command string) string {
	if command == "" {
		return ""
	}

	if command[0] != '\'' && command[0] != '"' {
		fields := strings.Fields(command)
		if len(fields) == 0 {
			return ""
		}

		return fields[0]
	}

	quote := command[0]
	for i := 1; i < len(command); i++ {
		if command[i] == quote {
			return command[1:i]
		}
	}

	return command
}

func renderJSONHooks(templatesDir string, tool string, binPath string) (json.RawMessage, error) {
	content, err := readHookTemplate(templatesDir, tool, "json")
	if err != nil {
		return nil, err
	}

	if !json.Valid(content) {
		return nil, logInstallError(
			"parse JSON hook template failed",
			fmt.Errorf("parse %s hook template: %w", tool, errInvalidHookJSON),
			slog.String("tool", tool),
		)
	}

	renderedHooks, err := marshalJSONCommandPlaceholders(json.RawMessage(content), binPath)
	if err != nil {
		return nil, logInstallError(
			"parse JSON hook template failed",
			fmt.Errorf("parse %s hook template: %w", tool, err),
			slog.String("tool", tool),
		)
	}

	output, err := json.MarshalIndent(renderedHooks, "", "  ")
	if err != nil {
		return nil, logInstallError(
			"render JSON hook template failed",
			fmt.Errorf("render %s hook template: %w", tool, err),
			slog.String("tool", tool),
		)
	}

	return json.RawMessage(output), nil
}

func marshalJSONCommandPlaceholders(
	value json.RawMessage,
	binPath string,
) (json.RawMessage, error) {
	trimmedValue := strings.TrimSpace(string(value))
	if trimmedValue == "" {
		return value, nil
	}

	switch trimmedValue[0] {
	case '{':
		objectValue := make(map[string]json.RawMessage)
		if err := json.Unmarshal(value, &objectValue); err != nil {
			return nil, logInstallError(
				"unmarshal JSON hook object failed",
				fmt.Errorf("unmarshal object: %w", err),
			)
		}

		for key, childValue := range objectValue {
			if key == jsonCommandKey {
				replacedValue, replaced := replaceJSONCommand(childValue, binPath)
				if replaced {
					objectValue[key] = replacedValue

					continue
				}
			}

			replacedChild, err := marshalJSONCommandPlaceholders(childValue, binPath)
			if err != nil {
				return nil, err
			}

			objectValue[key] = replacedChild
		}

		output, err := json.Marshal(objectValue)
		if err != nil {
			return nil, logInstallError(
				"marshal JSON hook object failed",
				fmt.Errorf("marshal object: %w", err),
			)
		}

		return json.RawMessage(output), nil
	case '[':
		var arrayValue []json.RawMessage
		if err := json.Unmarshal(value, &arrayValue); err != nil {
			return nil, logInstallError(
				"unmarshal JSON hook array failed",
				fmt.Errorf("unmarshal array: %w", err),
			)
		}

		for childIndex, childValue := range arrayValue {
			replacedChild, err := marshalJSONCommandPlaceholders(childValue, binPath)
			if err != nil {
				return nil, err
			}

			arrayValue[childIndex] = replacedChild
		}

		output, err := json.Marshal(arrayValue)
		if err != nil {
			return nil, logInstallError(
				"marshal JSON hook array failed",
				fmt.Errorf("marshal array: %w", err),
			)
		}

		return json.RawMessage(output), nil
	default:
		return value, nil
	}
}

func replaceJSONCommand(value json.RawMessage, binPath string) (json.RawMessage, bool) {
	var command string
	if err := json.Unmarshal(value, &command); err != nil {
		return value, false
	}

	replacedCommand := strings.ReplaceAll(
		command,
		agentGatePlaceholder,
		shellCommandArgument(binPath),
	)
	output, err := json.Marshal(replacedCommand)
	if err != nil {
		return value, false
	}

	return json.RawMessage(output), true
}
