package update

import (
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
