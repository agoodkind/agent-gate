# Setup and Verification Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Guide a new user from an installed binary to verified hook, daemon, evaluation, and audit operation.

**Architecture:** Installation first retains a complete immutable plan. Setup adds typed provider selection, then uses the same plan as repair commands. Verification re-reads each installed lifecycle command, executes a harmless labeled payload, and queries durable SQLite state by a unique setup session ID. Interactive and automated paths share one coordinator.

**Tech Stack:** Go, the existing installer, atomic file replacement, launchd, systemd, gRPC over a Unix socket, SQLite, POSIX shell, and Go integration tests.

The [approved design](../specs/2026-08-11-audit-lifecycle-design.md) defines the product contract.

## Global Constraints

- Implement AGATE-45 through AGATE-49 in order.
- Complete AGATE-31 before AGATE-47 and AGATE-38 before AGATE-48.
- Keep existing install commands as supported repair paths.
- Prepare every selected provider before any write.
- Preserve unrelated hooks and configuration.
- Use atomic configuration and hook writes.
- Verify the installed command, not an internal evaluator shortcut.
- Use a harmless lifecycle event with a unique setup session ID.
- Exit zero only after every selected provider has durable receipt, evaluation, and decision evidence.
- Keep command flags in command help.
- Run make check before each ticket commit.
- Create one signed commit per ticket. Each commit can become one pull request.

## File Structure

AGATE-45:

~~~text
internal/config/defaults_plan.go
internal/config/defaults_plan_test.go
internal/config/update.go
internal/config/config_test.go
internal/install/plan.go
internal/install/plan_test.go
internal/install/install.go
internal/install/install_test.go
cmd/agent-gate/install.go
cmd/agent-gate/install_test.go
~~~

AGATE-46:

~~~text
internal/install/provider.go
internal/install/provider_test.go
internal/install/install.go
internal/install/install_test.go
cmd/agent-gate/install.go
cmd/agent-gate/install_test.go
~~~

AGATE-47:

~~~text
internal/install/registration.go
internal/install/registration_test.go
internal/setup/probe.go
internal/setup/probe_test.go
cmd/agent-gate/setup_e2e_test.go
~~~

AGATE-48:

~~~text
internal/setup/coordinator.go
internal/setup/coordinator_test.go
cmd/agent-gate/setup.go
cmd/agent-gate/setup_test.go
cmd/agent-gate/main.go
cmd/agent-gate/main_test.go
~~~

AGATE-49:

~~~text
internal/setup/providers.go
internal/setup/providers_test.go
cmd/agent-gate/setup_prompt.go
cmd/agent-gate/setup_prompt_test.go
cmd/agent-gate/setup.go
install.sh
scripts/test-install-setup.sh
documentation_test.go
~~~

## Task 1: Prepare and atomically apply one installation plan

**Ticket:** AGATE-45

**Files:** Use the AGATE-45 file set above.

- [ ] **Step 1: Write failing complete-plan tests**

Use temporary homes with realistic provider files and service paths.

~~~go
func TestPrepareInstallationValidatesEveryLayerBeforeWrites(t *testing.T)
func TestApplyInstallationUsesPreparedBytes(t *testing.T)
func TestApplyInstallationReportsStageAndRepairCommand(t *testing.T)
func TestApplyDefaultsReplacesConfigurationAtomically(t *testing.T)
func TestInstallAllUsesSharedInstallationPlan(t *testing.T)
~~~

Inject a malformed last provider and assert configuration, service, and every hook file remain byte-identical.

Run:

~~~sh
go test ./internal/config ./internal/install ./cmd/agent-gate -run Installation
~~~

Expected: FAIL because config and service plans do not exist.

- [ ] **Step 2: Retain a validated configuration plan**

~~~go
type DefaultsPlan struct {
    Path    string
    Content []byte
    Config  *Config
}

func PrepareDefaults(options EnsureDefaultsOptions) (*DefaultsPlan, error)
func ApplyDefaults(plan *DefaultsPlan) (string, error)
~~~

Extend EnsureDefaultsOptions with `AuditProfile config.AuditStorageProfile`. Prepare reads the current bytes, merges storage and update choices, decodes the result, runs configuration-owned validation, and retains replacement bytes. Apply uses a same-directory temporary file, file sync, atomic rename, and directory sync.

An empty AuditProfile preserves an existing profile and inserts balanced only when storage configuration is absent. A nonempty setup selection replaces the profile while preserving explicit storage overrides.

Keep EnsureDefaults as a compatibility wrapper around both functions.

- [ ] **Step 3: Retain a service installation plan**

~~~go
type ServiceInstallationPlan struct {
    Platform   string
    TargetPath string
    Content    []byte
    Options    ServiceOptions
}

func PrepareServiceInstallation(ServiceOptions) (*ServiceInstallationPlan, error)
func ApplyServiceInstallation(*ServiceInstallationPlan) error
~~~

Prepare renders and validates once. Apply writes the retained bytes, performs service commands, and waits for the expected binary identity.

- [ ] **Step 4: Compose one installation plan**

~~~go
type Stage string

const (
    StageConfig  Stage = "config"
    StageService Stage = "service"
    StageHooks   Stage = "hooks"
)

type InstallationOptions struct {
    Config  *config.EnsureDefaultsOptions
    Hooks   *HooksOptions
    Service *ServiceOptions
}

type InstallationPlan struct {
    Config  *config.DefaultsPlan
    Service *ServiceInstallationPlan
    Hooks   *HookInstallationPlan
}

type ApplyResult struct {
    Completed []Stage
}

type ApplyError struct {
    Stage         Stage
    RepairCommand string
    Err           error
}

func PrepareInstallation(InstallationOptions) (*InstallationPlan, error)
func ApplyInstallation(*InstallationPlan) (ApplyResult, error)
~~~

After configuration preparation, PrepareInstallation runs `hook.ValidateConfig` on the retained decoded configuration. This keeps the hook-to-config dependency acyclic and stops the complete plan before writes.

Apply order is config, service readiness, then hooks. Do not roll back a healthy service just because a later hook write fails. Return the exact repair command for the failed stage.

- [ ] **Step 5: Route existing repair commands**

Make install hooks, install service, and install all create subsets of the shared plan. Preserve current flags, output, exit codes, and unrelated file behavior.

Run:

~~~sh
go test ./internal/config ./internal/install ./cmd/agent-gate -run "Installation|Install"
make test
make check
~~~

Expected: all commands exit zero.

- [ ] **Step 6: Commit**

~~~sh
git add internal/config internal/install cmd/agent-gate
git commit -S -m "Unify installation planning and apply" -m "Co-authored-by: Codex <noreply@openai.com>"
~~~

## Task 2: Add typed provider selection

**Ticket:** AGATE-46

**Depends on:** AGATE-45

**Files:** Use the AGATE-46 file set above.

- [ ] **Step 1: Write provider selection tests**

~~~go
func TestParseProvidersReturnsCanonicalOrder(t *testing.T)
func TestParseProvidersRejectsUnknownAndDuplicateNames(t *testing.T)
func TestPrepareHooksIgnoresMalformedUnselectedProvider(t *testing.T)
func TestInstallHooksProvidersChangesOnlySelectedFiles(t *testing.T)
func TestLegacyNoProviderFlagsRemainCompatible(t *testing.T)
~~~

Run:

~~~sh
go test ./internal/install ./cmd/agent-gate -run Provider
~~~

Expected: FAIL because selection uses five negative booleans.

- [ ] **Step 2: Define provider identity once**

~~~go
type Provider string

const (
    ProviderClaude  Provider = "claude"
    ProviderCodex   Provider = "codex"
    ProviderCursor  Provider = "cursor"
    ProviderGemini  Provider = "gemini"
    ProviderCopilot Provider = "copilot"
)

func AllProviders() []Provider
func ParseProviders(string) ([]Provider, error)
~~~

Canonical order is Claude, Codex, Cursor, Gemini, Copilot. Return a fresh slice.

- [ ] **Step 3: Replace internal boolean selection**

Add `Providers []Provider` to HooksOptions. A nil slice means the legacy default of all providers. A nonnil empty slice means no providers. Keep the existing booleans only at the command line compatibility boundary, then translate them to a nonnil typed list.

Add `--providers claude,codex` to repair commands. Reject use with any `--no-*` flag because the two selection forms conflict.

Test all five legacy opt-outs together. They must select no providers and must not fall back to all.

- [ ] **Step 4: Prepare only selected providers**

Do not read an unselected provider file. Preserve the current merge and atomic replacement behavior for selected files.

Run:

~~~sh
go test ./internal/install ./cmd/agent-gate -run "Provider|Install"
make check
~~~

Expected: both commands exit zero.

- [ ] **Step 5: Commit**

~~~sh
git add internal/install cmd/agent-gate
git commit -S -m "Add typed provider installation selection" -m "Co-authored-by: Codex <noreply@openai.com>"
~~~

## Task 3: Verify installed hooks through durable lifecycle probes

**Ticket:** AGATE-47

**Depends on:** AGATE-46 and AGATE-31

**Files:** Use the AGATE-47 file set above.

- [ ] **Step 1: Write registration reader tests**

Install each provider into a temporary home. Re-read one managed session-start command from each resulting file.

~~~go
type ManagedHookCommand struct {
    Provider   Provider
    EventName  string
    Executable string
    Arguments  []string
}

func ReadManagedLifecycleCommand(
    options HooksOptions,
    provider Provider,
) (ManagedHookCommand, error)
~~~

Assert the executable and arguments exactly match installed bytes. Test an installed binary path containing spaces. Use the provider's command representation instead of splitting on whitespace.

Validate each provider's expected lifecycle matcher set. Gemini has startup, resume, and clear registrations that share one distinct command. Accept that set and reject conflicting commands, missing matchers, or unrecognized registrations.

Run:

~~~sh
go test ./internal/install -run ManagedLifecycle
~~~

Expected: FAIL because no registration reader exists.

- [ ] **Step 2: Define harmless provider payloads**

Each payload carries one unique setup session ID and the provider's actual lifecycle event name.

~~~go
type ProbeRequest struct {
    SetupID   string
    Providers []installer.Provider
    HomeDir   string
    Config    *config.Config
    Timeout   time.Duration
}

type ProbeResult struct {
    Provider      installer.Provider
    IntakeEventID string
    ReceiptID     int64
    EvaluationID  string
    AuditEventID  string
    Decision      string
    ExitCode      int
}
~~~

Use SessionStart for Claude, Codex, and Gemini. Use sessionStart for Cursor and Copilot. Put the Copilot event name only in the installed command arguments because its payload does not identify the event. Do not include a tool command or user-authored content.

- [ ] **Step 3: Execute the installed commands**

Use `exec.CommandContext` with the installed executable and arguments. Pass the payload on standard input. Capture output and exit code. Do not invoke the daemon RPC directly.

After every process exits, poll read-only queries by the setup session ID:

1. Intake returns one event with the expected provider classification.
2. Evaluation returns one completed record and its numeric receipt ID for that event.
3. Audit returns a derived decision for the same session and provider.

Do not assume intake event ID equals derived audit event ID.

~~~go
func VerifyInstalledHooks(
    context.Context,
    ProbeRequest,
) ([]ProbeResult, error)
~~~

- [ ] **Step 4: Require the observe-only lifecycle result**

Lifecycle probes target observe-only events. Require exit code zero and a persisted allow decision. Treat every other exit code or decision as a verification failure.

Return errors with provider and stage:

~~~text
codex: durable evaluation was not recorded before 10s
~~~

- [ ] **Step 5: Add a real end-to-end test**

Use a temporary home and state directory. Install all five provider files. Start a real daemon and Unix socket. Run the generated commands. Assert five complete results and query the database after the daemon closes.

Run:

~~~sh
go test ./internal/install ./internal/setup ./cmd/agent-gate -run "Probe|SetupEndToEnd"
make check
~~~

Expected: both commands exit zero.

- [ ] **Step 6: Commit**

~~~sh
git add internal/install internal/setup cmd/agent-gate/setup_e2e_test.go
git commit -S -m "Verify installed hooks through durable probes" -m "Co-authored-by: Codex <noreply@openai.com>"
~~~

## Task 4: Add non-interactive setup automation

**Ticket:** AGATE-48

**Depends on:** AGATE-47, AGATE-33, and AGATE-38

**Files:** Use the AGATE-48 file set above.

- [ ] **Step 1: Write public setup command tests**

~~~go
func TestSetupNonInteractivePreviewsBeforeWrites(t *testing.T)
func TestSetupNonInteractiveRequiresProviderSelection(t *testing.T)
func TestSetupNonInteractiveRejectsEmptyProviderSelection(t *testing.T)
func TestSetupNonInteractiveReturnsTwoForPreflightFailure(t *testing.T)
func TestSetupNonInteractiveReturnsOneForApplyFailure(t *testing.T)
func TestSetupNonInteractiveReturnsOneForProbeFailure(t *testing.T)
func TestSetupNonInteractivePrintsRepairCommand(t *testing.T)
~~~

Run:

~~~sh
go test ./internal/setup ./cmd/agent-gate -run Setup
~~~

Expected: FAIL because setup routing does not exist.

- [ ] **Step 2: Define one coordinator**

~~~go
type Options struct {
    BinPath      string
    Providers    []installer.Provider
    AuditProfile config.AuditStorageProfile
    AutoUpdate   string
}

type Plan struct {
    Installation    *installer.InstallationPlan
    Providers       []installer.Provider
    EffectivePolicy config.AuditStoragePolicy
    Maintenance     *auditmaintenance.Plan
}

type Result struct {
    SetupID string
    Probes  []ProbeResult
}

func Prepare(
    context.Context,
    Options,
    Dependencies,
) (*Plan, error)

func Apply(
    context.Context,
    *Plan,
    Dependencies,
) (Result, error)
~~~

Prepare:

1. Resolves and validates the executable.
2. Prepares the requested configuration profile.
3. Prepares the service.
4. Prepares every selected hook.
5. Previews retention only when the audit database already exists.
6. Returns without writing.

A fresh install prints zero existing records without creating a database. Never use an empty provider slice for setup because the installer compatibility layer interprets it as all providers.

Apply uses the installation plan, then verifies all installed hooks.

- [ ] **Step 3: Add the non-interactive command**

Implement:

~~~sh
agent-gate setup \
    --non-interactive \
    --providers claude,codex,cursor,gemini,copilot \
    --audit-profile balanced \
    --auto-update apply
~~~

Require explicit nonempty `--providers` and `--audit-profile` in non-interactive mode. Detection belongs only to the later interactive ticket.

Print the effective policy and immediate deletion estimate before writes. Preflight failure exits two. Apply or verification failure exits one. Complete verification exits zero.

- [ ] **Step 4: Route the main command and help**

Add setup to command dispatch and top-level help. Keep exact flags in setup help.

Run:

~~~sh
go test ./internal/setup ./cmd/agent-gate -run Setup
make test
make check
~~~

Expected: all commands exit zero.

- [ ] **Step 5: Commit**

~~~sh
git add internal/setup cmd/agent-gate
git commit -S -m "Add non-interactive verified setup" -m "Co-authored-by: Codex <noreply@openai.com>"
~~~

## Task 5: Add interactive setup and route release installation

**Ticket:** AGATE-49

**Depends on:** AGATE-48

**Files:** Use the AGATE-49 file set above.

- [ ] **Step 1: Write provider detection tests**

~~~go
type ProviderState struct {
    Provider            installer.Provider
    ClientPath          string
    ManagedRegistration bool
}

type DetectOptions struct {
    HomeDir  string
    LookPath func(string) (string, error)
}

func DetectProviders(DetectOptions) ([]ProviderState, error)
~~~

Use this executable map:

~~~go
var providerExecutables = map[installer.Provider]string{
    installer.ProviderClaude:  "claude",
    installer.ProviderCodex:   "codex",
    installer.ProviderCursor:  "cursor",
    installer.ProviderGemini:  "gemini",
    installer.ProviderCopilot: "copilot",
}
~~~

Iterate `installer.AllProviders` so results stay canonical. Detect these clients and existing managed blocks. Do not infer installation from an unrelated file.

Run:

~~~sh
go test ./internal/setup -run DetectProviders
~~~

Expected: FAIL because detection does not exist.

- [ ] **Step 2: Write scripted prompt tests**

~~~go
type Prompter interface {
    SelectProviders([]ProviderState) ([]installer.Provider, error)
    SelectAuditProfile(config.AuditStoragePolicy) (config.AuditStorageProfile, error)
    Confirm(PlanSummary) (bool, error)
}
~~~

Test defaults, changed selection, cancellation, invalid input recovery, and closed input. Balanced is the fresh-install profile default. An existing valid profile remains selected during repair. Detected clients and managed registrations are selected provider defaults.

Reject an empty confirmed selection before preparing the installation plan.

- [ ] **Step 3: Add interactive command behavior**

With no non-interactive flag:

1. Detect providers.
2. Prompt for providers.
3. Prompt for profile.
4. Prepare the shared plan.
5. Print effective retention and deletion estimates.
6. Confirm.
7. Apply and verify.
8. Print one provider result per line.

Cancellation performs no writes and exits zero after saying setup cancelled.

- [ ] **Step 4: Route the release installer safely**

The release script currently pipes the hosted installer into Bash and passes `install all` as the post-install command. Replace that pipeline with a downloaded temporary script so the wrapper controls setup input and cleanup.

Add a shell behavioral test that runs the wrapper with a fake downloaded installer. Prove:

~~~text
an available controlling terminal invokes agent-gate setup with that terminal as input
an invocation without a controlling terminal runs setup --non-interactive with every shipped provider and the balanced audit profile
explicit installer flags remain unchanged
setup failure returns nonzero
the temporary installer is removed after success, failure, or interruption
~~~

Use a real script file for the test helper. Do not embed shell inside Go.

Then update install.sh to pass setup as the hosted installer's post-install command. Use `/dev/tty` only when it is readable and writable. Keep the current flags before the `--` separator byte-for-byte. Trap exit and signals to remove the temporary directory.

- [ ] **Step 5: Run final epic verification**

~~~sh
bash scripts/test-install-setup.sh
make test
make lint
make check
~~~

Expected: all commands exit zero.

- [ ] **Step 6: Commit**

~~~sh
git add internal/setup cmd/agent-gate install.sh scripts/test-install-setup.sh documentation_test.go
git commit -S -m "Add interactive verified setup" -m "Co-authored-by: Codex <noreply@openai.com>"
~~~

## Epic Acceptance

- [ ] Every setup run completes preflight before writes.
- [ ] Existing install commands use the same planning and apply components.
- [ ] Configuration, service, and hook stages retain exact prepared bytes.
- [ ] Provider selection is typed and explicit.
- [ ] Every selected installed hook reaches the real daemon.
- [ ] Every probe has durable receipt, completed evaluation, and decision evidence.
- [ ] Automated and interactive setup share one coordinator.
- [ ] Release installation cannot hang when standard input is piped.
- [ ] Every ticket commit passes make check.
