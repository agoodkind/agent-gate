package config

import (
	"errors"
	"fmt"
)

var (
	errEmptySearchTool                = errors.New("search_tools entries must be non-empty")
	errInferCacheTTLNegative          = errors.New("cache_ttl_ms must be non-negative")
	errInferResponsePredicateRequired = errors.New("response_json_equals is required")
)

type inferTimeoutError struct {
	contextLabel string
	timeoutMS    int
	maximumMS    int
}

func (failure *inferTimeoutError) Error() string {
	return fmt.Sprintf(
		"%s: timeout_ms %d exceeds performance.timeouts.infer_max_ms %d",
		failure.contextLabel,
		failure.timeoutMS,
		failure.maximumMS,
	)
}

type inferBlockOnError struct {
	contextLabel string
	value        string
}

func (failure *inferBlockOnError) Error() string {
	return fmt.Sprintf(
		"%s: block_on %q must be %q or %q",
		failure.contextLabel,
		failure.value,
		BlockOnMatch,
		"nonmatch",
	)
}

type inferFailurePolicyError struct {
	contextLabel string
	value        string
}

func (failure *inferFailurePolicyError) Error() string {
	return fmt.Sprintf(
		"%s: on_error %q must be %q or %q",
		failure.contextLabel,
		failure.value,
		OnErrorOpen,
		OnErrorClosed,
	)
}
