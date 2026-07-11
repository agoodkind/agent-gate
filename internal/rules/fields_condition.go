package rules

import "goodkind.io/agent-gate/internal/config"

// FirstStringForCondition resolves selectors with the declarations owned by
// condition. Other selectors retain their context-free behavior.
func (fields FieldSet) FirstStringForCondition(selectors []config.FieldSelectorSpec, condition *config.Condition) (string, string) {
	for _, selector := range selectors {
		value := fields.StringForCondition(selector.Selector, condition)
		if value != "" {
			return selector.Path, value
		}
	}
	return "", ""
}

// StringForCondition returns a selector value using declarations carried by
// condition when the field requires rule context.
func (fields FieldSet) StringForCondition(selector config.FieldSelector, condition *config.Condition) string {
	if selector == config.FieldCmdWriteTargets && condition != nil {
		return fields.CmdWriteTargetsWithSpecs(condition.WriteSpecs)
	}
	return fields.String(selector)
}

type conditionFieldAccessor struct {
	fields    *FieldSet
	condition *config.Condition
}

func (accessor conditionFieldAccessor) String(selector config.FieldSelector) string {
	return accessor.fields.StringForCondition(selector, accessor.condition)
}

func (accessor conditionFieldAccessor) FilePathValue() string {
	return accessor.fields.FilePathValue()
}
