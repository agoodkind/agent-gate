package config

// StdoutJSONEqualsValue returns the decoded scalar used by stdout_json_field.
func (c *Condition) StdoutJSONEqualsValue() TOMLScalarValue { return c.stdoutJSONValue }
