package rules

import "context"

func execResponseTargetFromContext(ctx context.Context, action string) string {
	if ctx == nil {
		return ""
	}
	resolve, _ := ctx.Value(execResponseTargetKey{}).(func(string) string)
	if resolve == nil {
		return ""
	}
	return resolve(action)
}

func (r *ExecRuntime) lastUserMessage(system string, fields FieldSet) (string, bool) {
	if r == nil || r.temporal == nil {
		return "", false
	}
	identity := temporalConversationIdentity(system, fields)
	if identity == "" {
		return "", false
	}
	return r.temporal.load(temporalKey("prompt", identity))
}

func (r *ExecRuntime) lastResponseOutput(
	system string,
	fields FieldSet,
	eventName string,
	_ string,
	target string,
) (string, bool) {
	if r == nil || r.temporal == nil {
		return "", false
	}
	identity := temporalConversationIdentity(system, fields)
	if identity == "" || eventName == "" || target == "" {
		return "", false
	}
	key := temporalKey("response", identity, eventName, target)
	return r.temporal.load(key)
}

func (s *execTemporalStore) load(key string) (string, bool) {
	if s == nil || s.cache == nil || key == "" {
		return "", false
	}
	entry, found, err := s.cache.Get(execTemporalNamespace, key)
	if err != nil || !found {
		return "", false
	}
	_, available, value, valid := decodeTemporalRecord(entry.Value)
	if !valid || !available {
		return "", false
	}
	return value, true
}
