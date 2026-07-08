package config

// Judge authority modes decide which verdict the composer enforces when the
// deterministic oracle and the lm-review model both produce a verdict.
const (
	// JudgeAuthorityUnion blocks when either the model or the oracle blocks. It
	// is the superset of both blocks, so the model catches what the oracle
	// cannot parse (for example an MCP tool write) and the oracle catches what
	// the model misses. This is the default and safest mode.
	JudgeAuthorityUnion = "union"
	// JudgeAuthorityOracle enforces the oracle verdict and uses the model only
	// when the oracle cannot decide.
	JudgeAuthorityOracle = "oracle"
	// JudgeAuthorityLLM enforces the model verdict and uses the oracle only as a
	// safety net when the model has no verdict.
	JudgeAuthorityLLM = "llm"
)

// Judge holds lm-review and clyde integration settings for the rule composer.
type Judge struct {
	Enabled             bool   `toml:"enabled"`
	Authority           string `toml:"authority"`
	LMReviewGRPCAddress string `toml:"lm_review_grpc_address"`
	ClydeGRPCAddress    string `toml:"clyde_grpc_address"`
	DisagreementLogPath string `toml:"disagreement_log_path"`
}

// JudgeEnabled reports whether the lm-review model path should be called.
func (c *Config) JudgeEnabled() bool {
	return c != nil && c.Judge.Enabled
}

// JudgeAuthority returns how the composer combines the oracle and model
// verdicts. It returns JudgeAuthorityUnion for any unset or unrecognized value,
// so the default and any typo land on the safest superset-of-blocks mode.
func (c *Config) JudgeAuthority() string {
	if c == nil {
		return JudgeAuthorityUnion
	}
	switch c.Judge.Authority {
	case JudgeAuthorityOracle:
		return JudgeAuthorityOracle
	case JudgeAuthorityLLM:
		return JudgeAuthorityLLM
	default:
		return JudgeAuthorityUnion
	}
}

// JudgeLMReviewGRPCAddress returns the configured lm-review gRPC target.
func (c *Config) JudgeLMReviewGRPCAddress() string {
	if c == nil {
		return ""
	}
	return c.Judge.LMReviewGRPCAddress
}

// JudgeClydeGRPCAddress returns the configured clyde gRPC target.
func (c *Config) JudgeClydeGRPCAddress() string {
	if c == nil {
		return ""
	}
	return c.Judge.ClydeGRPCAddress
}

// JudgeDisagreementLogPath returns the disagreement JSONL path.
func (c *Config) JudgeDisagreementLogPath() string {
	if c != nil && c.Judge.DisagreementLogPath != "" {
		return c.Judge.DisagreementLogPath
	}
	return DefaultJudgeDisagreementLogPath()
}
