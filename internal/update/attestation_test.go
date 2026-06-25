package update

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	in_toto "github.com/in-toto/attestation/go/v1"
	fulciocert "github.com/sigstore/sigstore-go/pkg/fulcio/certificate"
	sigverify "github.com/sigstore/sigstore-go/pkg/verify"
	"google.golang.org/protobuf/types/known/structpb"
)

func TestStatementHasSubjectDigest(t *testing.T) {
	subjects := []*in_toto.ResourceDescriptor{
		{Name: "", Digest: map[string]string{"sha1": "abc123"}},
		{Name: "agent-gate_darwin_arm64.tar.gz", Digest: map[string]string{"sha256": "deadbeef"}},
	}
	if !statementHasSHA256SubjectDigest(subjects, "agent-gate_darwin_arm64.tar.gz", "deadbeef") {
		t.Fatal("statementHasSHA256SubjectDigest() = false, want true")
	}
	if statementHasSHA256SubjectDigest(subjects, "agent-gate_linux_arm64.tar.gz", "deadbeef") {
		t.Fatal("statementHasSHA256SubjectDigest() = true, want false")
	}
}

func TestValidateReleaseAttestation(t *testing.T) {
	predicate, err := structpb.NewStruct(map[string]any{
		"repository": "agoodkind/agent-gate",
		"tag":        "v1.2.3",
	})
	if err != nil {
		t.Fatalf("NewStruct() error: %v", err)
	}
	result := &sigverify.VerificationResult{
		Statement: &in_toto.Statement{
			PredicateType: githubReleaseAttestationPredicateType,
			Predicate:     predicate,
			Subject: []*in_toto.ResourceDescriptor{
				{Name: "agent-gate_darwin_arm64.tar.gz", Digest: map[string]string{"sha256": "deadbeef"}},
			},
		},
	}
	if err := validateReleaseAttestation(result, "agoodkind/agent-gate", "v1.2.3", "agent-gate_darwin_arm64.tar.gz", "deadbeef"); err != nil {
		t.Fatalf("validateReleaseAttestation() error: %v", err)
	}
}

func TestValidateReleaseAttestationRejectsMismatches(t *testing.T) {
	predicate, err := structpb.NewStruct(map[string]any{
		"repository": "agoodkind/agent-gate",
		"tag":        "v1.2.3",
	})
	if err != nil {
		t.Fatalf("NewStruct() error: %v", err)
	}
	baseResult := &sigverify.VerificationResult{
		Statement: &in_toto.Statement{
			PredicateType: githubReleaseAttestationPredicateType,
			Predicate:     predicate,
			Subject: []*in_toto.ResourceDescriptor{
				{Name: "agent-gate_darwin_arm64.tar.gz", Digest: map[string]string{"sha256": "deadbeef"}},
			},
		},
	}
	testCases := []struct {
		name      string
		repo      string
		tag       string
		assetName string
		digestHex string
		want      string
	}{
		{
			name:      "wrong repo",
			repo:      "agoodkind/not-agent-gate",
			tag:       "v1.2.3",
			assetName: "agent-gate_darwin_arm64.tar.gz",
			digestHex: "deadbeef",
			want:      "did not match",
		},
		{
			name:      "wrong tag",
			repo:      "agoodkind/agent-gate",
			tag:       "v9.9.9",
			assetName: "agent-gate_darwin_arm64.tar.gz",
			digestHex: "deadbeef",
			want:      "did not match",
		},
		{
			name:      "missing subject digest",
			repo:      "agoodkind/agent-gate",
			tag:       "v1.2.3",
			assetName: "agent-gate_linux_arm64.tar.gz",
			digestHex: "deadbeef",
			want:      "did not include",
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			err := validateReleaseAttestation(baseResult, testCase.repo, testCase.tag, testCase.assetName, testCase.digestHex)
			if err == nil {
				t.Fatal("validateReleaseAttestation() error = nil, want mismatch")
			}
			if !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("validateReleaseAttestation() error = %v, want substring %q", err, testCase.want)
			}
		})
	}
}

func TestValidateBuildProvenanceCertificate(t *testing.T) {
	summary := &fulciocert.Summary{
		SubjectAlternativeName: goMakefileReleaseWorkflowURI,
		Extensions: fulciocert.Extensions{
			Issuer:              githubActionsOIDCIssuer,
			BuildSignerURI:      goMakefileReleaseWorkflowURI,
			RunnerEnvironment:   githubHostedRunnerEnvironment,
			SourceRepositoryURI: githubRepositoryURI("agoodkind/agent-gate"),
		},
	}
	if err := validateBuildProvenanceCertificate(summary, "agoodkind/agent-gate"); err != nil {
		t.Fatalf("validateBuildProvenanceCertificate() error: %v", err)
	}
}

func TestValidateBuildProvenanceCertificateRejectsMismatches(t *testing.T) {
	testCases := []struct {
		name    string
		summary *fulciocert.Summary
		repo    string
		want    string
	}{
		{
			name: "wrong signer workflow",
			summary: &fulciocert.Summary{
				SubjectAlternativeName: "https://github.com/agoodkind/go-makefile/.github/workflows/not-real.yml@refs/heads/main",
				Extensions: fulciocert.Extensions{
					Issuer:              githubActionsOIDCIssuer,
					BuildSignerURI:      goMakefileReleaseWorkflowURI,
					RunnerEnvironment:   githubHostedRunnerEnvironment,
					SourceRepositoryURI: githubRepositoryURI("agoodkind/agent-gate"),
				},
			},
			repo: "agoodkind/agent-gate",
			want: "SAN",
		},
		{
			name: "wrong repo",
			summary: &fulciocert.Summary{
				SubjectAlternativeName: goMakefileReleaseWorkflowURI,
				Extensions: fulciocert.Extensions{
					Issuer:              githubActionsOIDCIssuer,
					BuildSignerURI:      goMakefileReleaseWorkflowURI,
					RunnerEnvironment:   githubHostedRunnerEnvironment,
					SourceRepositoryURI: githubRepositoryURI("agoodkind/go-makefile"),
				},
			},
			repo: "agoodkind/agent-gate",
			want: "source repository URI",
		},
		{
			name: "wrong issuer",
			summary: &fulciocert.Summary{
				SubjectAlternativeName: goMakefileReleaseWorkflowURI,
				Extensions: fulciocert.Extensions{
					Issuer:              "https://issuer.example.invalid",
					BuildSignerURI:      goMakefileReleaseWorkflowURI,
					RunnerEnvironment:   githubHostedRunnerEnvironment,
					SourceRepositoryURI: githubRepositoryURI("agoodkind/agent-gate"),
				},
			},
			repo: "agoodkind/agent-gate",
			want: "issuer",
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			err := validateBuildProvenanceCertificate(testCase.summary, testCase.repo)
			if err == nil {
				t.Fatal("validateBuildProvenanceCertificate() error = nil, want mismatch")
			}
			if !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("validateBuildProvenanceCertificate() error = %v, want substring %q", err, testCase.want)
			}
		})
	}
}

func TestValidateBuildProvenanceRejectsMissingCertificateSummary(t *testing.T) {
	result := &sigverify.VerificationResult{
		Statement: &in_toto.Statement{
			PredicateType: githubBuildProvenancePredicateType,
			Subject: []*in_toto.ResourceDescriptor{
				{Name: "agent-gate_darwin_arm64.tar.gz", Digest: map[string]string{"sha256": "deadbeef"}},
			},
		},
	}
	err := validateBuildProvenance(result, "agoodkind/agent-gate", "agent-gate_darwin_arm64.tar.gz", "deadbeef")
	if err == nil {
		t.Fatal("validateBuildProvenance() error = nil, want missing certificate summary")
	}
	if !strings.Contains(err.Error(), "certificate summary missing") {
		t.Fatalf("validateBuildProvenance() error = %v", err)
	}
}

func TestVerifyDarwinCodeSignatureRejectsUnsignedCandidate(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("Darwin-only codesign test")
	}
	candidatePath := filepath.Join(t.TempDir(), "agent-gate")
	content := "#!/bin/sh\nprintf 'version: unsigned\\n'\n"
	if err := os.WriteFile(candidatePath, []byte(content), 0o755); err != nil {
		t.Fatalf("WriteFile() error: %v", err)
	}
	err := verifyDarwinCodeSignature(context.Background(), candidatePath)
	if err == nil {
		t.Fatal("verifyDarwinCodeSignature() error = nil, want unsigned candidate failure")
	}
	if !strings.Contains(err.Error(), "codesign verify failed") {
		t.Fatalf("verifyDarwinCodeSignature() error = %v", err)
	}
}
