package config

// ConditionKind selects which evaluator applies to a rule condition.
type ConditionKind string

// ConditionKind variants.
const (
	ConditionKindCommand            ConditionKind = "command"
	ConditionKindDiff               ConditionKind = "diff"
	ConditionKindExec               ConditionKind = "exec"
	ConditionKindProject            ConditionKind = "project"
	ConditionKindRegex              ConditionKind = "regex"
	ConditionKindShellRead          ConditionKind = "shell_read_secret"
	ConditionKindShellWrite         ConditionKind = "shell_write"
	ConditionKindGitDefaultBranch   ConditionKind = "git_default_branch"
	ConditionKindGitPrimaryCheckout ConditionKind = "git_primary_checkout"
	ConditionKindGitRefMove         ConditionKind = "git_ref_move"
	ConditionKindInfer              ConditionKind = "infer"
)
