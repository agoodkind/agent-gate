package config

// CacheKeySelector returns the compiled field selector that keys the exec
// condition's cross-event result cache.
func (c *Condition) CacheKeySelector() FieldSelectorSpec { return c.cacheKeySelector }

// ForEachSelector returns the compiled field selector that expands the exec
// command across many target values.
func (c *Condition) ForEachSelector() FieldSelectorSpec { return c.forEachSelector }

// StdoutJSONEqualsValue returns the decoded scalar used by stdout_json_field.
func (c *Condition) StdoutJSONEqualsValue() TOMLScalarValue { return c.stdoutJSONValue }
