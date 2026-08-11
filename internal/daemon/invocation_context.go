package daemon

import (
	"goodkind.io/agent-gate/api/daemonpb"
	"goodkind.io/agent-gate/internal/hook"
)

func invocationContextToProto(context hook.InvocationContext) *daemonpb.HookInvocationContext {
	result := &daemonpb.HookInvocationContext{
		HookSubcommand:      observedValueToProto(context.HookSubcommand),
		HookTags:            make([]*daemonpb.ObservedHookValue, 0, len(context.HookTags)),
		WorkingDirectory:    observedValueToProto(context.WorkingDirectory),
		Executable:          processEvidenceToProto(context.Executable),
		ParentProcess:       processEvidenceToProto(context.ParentProcess),
		Ancestors:           make([]*daemonpb.HookProcessEvidence, 0, len(context.Ancestors)),
		Environment:         make([]*daemonpb.HookEnvironmentEvidence, 0, len(context.Environment)),
		ManagedRegistration: observedValueToProto(context.ManagedRegistration),
		CollectionIssues:    make([]*daemonpb.HookCollectionIssue, 0, len(context.CollectionIssues)),
	}
	for _, tag := range context.HookTags {
		result.HookTags = append(result.HookTags, observedValueToProto(tag))
	}
	for _, process := range context.Ancestors {
		result.Ancestors = append(result.Ancestors, processEvidenceToProto(process))
	}
	for _, environment := range context.Environment {
		result.Environment = append(result.Environment, &daemonpb.HookEnvironmentEvidence{
			Name: environment.Name, Value: environment.Value, Category: environment.Category,
			Source: environment.Source, Provenance: environment.Provenance,
			Status: string(environment.Status),
		})
	}
	for _, issue := range context.CollectionIssues {
		result.CollectionIssues = append(result.CollectionIssues, &daemonpb.HookCollectionIssue{
			Source: issue.Source, Status: string(issue.Status), Detail: issue.Detail,
		})
	}
	return result
}

func invocationContextFromProto(context *daemonpb.HookInvocationContext) hook.InvocationContext {
	if context == nil {
		return hook.InvocationContext{
			HookSubcommand: hook.ObservedValue{
				Value: "", Source: "", Provenance: "", Status: "",
			},
			HookTags: nil,
			WorkingDirectory: hook.ObservedValue{
				Value: "", Source: "", Provenance: "", Status: "",
			},
			Executable: hook.ProcessEvidence{
				Name: "", ExecutablePath: "", Source: "", Provenance: "", Status: "",
			},
			ParentProcess: hook.ProcessEvidence{
				Name: "", ExecutablePath: "", Source: "", Provenance: "", Status: "",
			},
			Ancestors: nil, Environment: nil,
			ManagedRegistration: hook.ObservedValue{
				Value: "", Source: "", Provenance: "", Status: "",
			},
			CollectionIssues: nil,
		}
	}
	result := hook.InvocationContext{
		HookSubcommand:      observedValueFromProto(context.GetHookSubcommand()),
		HookTags:            make([]hook.ObservedValue, 0, len(context.GetHookTags())),
		WorkingDirectory:    observedValueFromProto(context.GetWorkingDirectory()),
		Executable:          processEvidenceFromProto(context.GetExecutable()),
		ParentProcess:       processEvidenceFromProto(context.GetParentProcess()),
		Ancestors:           make([]hook.ProcessEvidence, 0, len(context.GetAncestors())),
		Environment:         make([]hook.EnvironmentEvidence, 0, len(context.GetEnvironment())),
		ManagedRegistration: observedValueFromProto(context.GetManagedRegistration()),
		CollectionIssues:    make([]hook.CollectionIssue, 0, len(context.GetCollectionIssues())),
	}
	for _, tag := range context.GetHookTags() {
		result.HookTags = append(result.HookTags, observedValueFromProto(tag))
	}
	for _, process := range context.GetAncestors() {
		result.Ancestors = append(result.Ancestors, processEvidenceFromProto(process))
	}
	for _, environment := range context.GetEnvironment() {
		result.Environment = append(result.Environment, hook.EnvironmentEvidence{
			Name: environment.GetName(), Value: environment.GetValue(),
			Category: environment.GetCategory(), Source: environment.GetSource(),
			Provenance: environment.GetProvenance(),
			Status:     hook.SignalStatus(environment.GetStatus()),
		})
	}
	for _, issue := range context.GetCollectionIssues() {
		result.CollectionIssues = append(result.CollectionIssues, hook.CollectionIssue{
			Source: issue.GetSource(), Status: hook.SignalStatus(issue.GetStatus()),
			Detail: issue.GetDetail(),
		})
	}
	return result
}

func observedValueToProto(value hook.ObservedValue) *daemonpb.ObservedHookValue {
	return &daemonpb.ObservedHookValue{
		Value: value.Value, Source: value.Source, Provenance: value.Provenance,
		Status: string(value.Status),
	}
}

func observedValueFromProto(value *daemonpb.ObservedHookValue) hook.ObservedValue {
	if value == nil {
		return hook.ObservedValue{Value: "", Source: "", Provenance: "", Status: ""}
	}
	return hook.ObservedValue{
		Value: value.GetValue(), Source: value.GetSource(), Provenance: value.GetProvenance(),
		Status: hook.SignalStatus(value.GetStatus()),
	}
}

func processEvidenceToProto(value hook.ProcessEvidence) *daemonpb.HookProcessEvidence {
	return &daemonpb.HookProcessEvidence{
		Name: value.Name, ExecutablePath: value.ExecutablePath, Source: value.Source,
		Provenance: value.Provenance, Status: string(value.Status),
	}
}

func processEvidenceFromProto(value *daemonpb.HookProcessEvidence) hook.ProcessEvidence {
	if value == nil {
		return hook.ProcessEvidence{
			Name: "", ExecutablePath: "", Source: "", Provenance: "", Status: "",
		}
	}
	return hook.ProcessEvidence{
		Name: value.GetName(), ExecutablePath: value.GetExecutablePath(),
		Source: value.GetSource(), Provenance: value.GetProvenance(),
		Status: hook.SignalStatus(value.GetStatus()),
	}
}
