package daemon

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"

	"goodkind.io/agent-gate/api/daemonpb"
)

const (
	auditTraceAllowRequests    = 1000
	auditTraceBlockRequests    = 100
	auditTraceDeferredRequests = 100
	auditTraceDigest           = "af3feeef13dcf62464aa3a24dfb09e09e29c5b49ebfd805bd5a451861341aa08"
)

var auditTraceInputSizes = [...]int{1024, 16384, 262144}

type auditTraceCategory struct {
	marker string
	count  int
}

func benchmarkRequest(sequence uint64, payload string) *daemonpb.EvaluateHookRequest {
	return &daemonpb.EvaluateHookRequest{
		RawJson: []byte(fmt.Sprintf(
			`{"session_id":"performance","tool_use_id":"%d","hook_event_name":"PreToolUse","tool_name":"Shell","tool_input":{"command":%q}}`,
			sequence,
			payload,
		)),
		ProviderHint: "codex",
		EnvFingerprint: map[string]string{
			"CODEX_THREAD_ID": "performance",
		},
	}
}

func auditPerformanceTrace() []*daemonpb.EvaluateHookRequest {
	categories := [...]auditTraceCategory{
		{marker: "performance-allow", count: auditTraceAllowRequests},
		{marker: "performance-block", count: auditTraceBlockRequests},
		{marker: "performance-deferred", count: auditTraceDeferredRequests},
	}
	requests := make([]*daemonpb.EvaluateHookRequest, 0,
		auditTraceAllowRequests+auditTraceBlockRequests+auditTraceDeferredRequests)
	var sequence uint64
	for _, category := range categories {
		for index := range category.count {
			sequence++
			targetSize := auditTraceInputSizes[index%len(auditTraceInputSizes)]
			requests = append(requests,
				auditTraceRequest(sequence, category.marker, targetSize))
		}
	}
	return requests
}

func auditTraceRequest(sequence uint64, marker string, targetSize int) *daemonpb.EvaluateHookRequest {
	request := benchmarkRequest(sequence, marker)
	paddingSize := targetSize - len(request.RawJson)
	if paddingSize < 0 {
		panic("audit trace target is smaller than request envelope")
	}
	return benchmarkRequest(sequence, marker+strings.Repeat("x", paddingSize))
}

func auditPerformanceTraceDigest(requests []*daemonpb.EvaluateHookRequest) string {
	digest := sha256.New()
	for _, request := range requests {
		_, _ = digest.Write(request.RawJson)
		_, _ = digest.Write([]byte{'\n'})
	}
	return hex.EncodeToString(digest.Sum(nil))
}

func TestAuditPerformanceTraceIsFixed(t *testing.T) {
	requests := auditPerformanceTrace()
	wantCount := auditTraceAllowRequests + auditTraceBlockRequests +
		auditTraceDeferredRequests
	if len(requests) != wantCount {
		t.Fatalf("requests = %d, want %d", len(requests), wantCount)
	}
	for index, request := range requests {
		categoryIndex := index
		if index >= auditTraceAllowRequests+auditTraceBlockRequests {
			categoryIndex -= auditTraceAllowRequests + auditTraceBlockRequests
		} else if index >= auditTraceAllowRequests {
			categoryIndex -= auditTraceAllowRequests
		}
		wantSize := auditTraceInputSizes[categoryIndex%len(auditTraceInputSizes)]
		if got := len(request.GetRawJson()); got != wantSize {
			t.Fatalf("request %d bytes = %d, want %d", index, got, wantSize)
		}
	}
	if got := auditPerformanceTraceDigest(requests); got != auditTraceDigest {
		t.Fatalf("trace digest = %q, want %q", got, auditTraceDigest)
	}
}
