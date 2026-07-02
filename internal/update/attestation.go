package update

import (
	"context"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	in_toto "github.com/in-toto/attestation/go/v1"
	sigbundle "github.com/sigstore/sigstore-go/pkg/bundle"
	fulciocert "github.com/sigstore/sigstore-go/pkg/fulcio/certificate"
	sigroot "github.com/sigstore/sigstore-go/pkg/root"
	sigtuf "github.com/sigstore/sigstore-go/pkg/tuf"
	sigverify "github.com/sigstore/sigstore-go/pkg/verify"
	"google.golang.org/protobuf/types/known/structpb"
)

const (
	githubActionsOIDCIssuer               = "https://token.actions.githubusercontent.com"
	githubBuildProvenancePredicateType    = "https://slsa.dev/provenance/v1"
	githubHostedRunnerEnvironment         = "github-hosted"
	githubReleaseAttestationPredicateType = "https://in-toto.io/attestation/release/v0.2"
	githubReleaseAttestationSAN           = "https://dotcom.releases.github.com"
	githubReleaseTUFRepositoryURL         = "https://tuf-repo.github.com"
	goMakefileReleaseBuildWorkflowURI     = "https://github.com/agoodkind/go-makefile/.github/workflows/_release_build.yml@refs/heads/main"
)

//go:embed embed/tuf-repo.github.com/4.root.json
var githubReleaseTUFBootstrapRoot []byte

type githubGitObjectType string

const (
	githubGitObjectTypeCommit githubGitObjectType = "commit"
	githubGitObjectTypeTag    githubGitObjectType = "tag"
)

type githubAnnotatedTagResponse struct {
	Object githubGitObject `json:"object"`
}

type githubAttestationRecord struct {
	Bundle json.RawMessage `json:"bundle"`
}

type githubAttestationsResponse struct {
	Attestations []githubAttestationRecord `json:"attestations"`
}

type githubGitObject struct {
	SHA  string              `json:"sha"`
	Type githubGitObjectType `json:"type"`
	URL  string              `json:"url"`
}

type githubGitRefResponse struct {
	Object githubGitObject `json:"object"`
}

func verifyGitHubAttestations(ctx context.Context, options Options, latest release, asset releaseAsset, archivePath string) error {
	digestHex, err := sha256File(archivePath)
	if err != nil {
		return err
	}
	digestBytes, err := hex.DecodeString(digestHex)
	if err != nil {
		return fmt.Errorf("decode artifact digest: %w", err)
	}
	if err := verifyReleaseAssetAttestation(ctx, options, latest, asset, digestHex, digestBytes); err != nil {
		options.Log.WarnContext(ctx, "update release attestation verification failed", "asset", asset.Name, "tag", latest.TagName, "err", err)
		return err
	}
	if err := verifyBuildProvenanceAttestation(ctx, options, asset, digestHex, digestBytes); err != nil {
		options.Log.WarnContext(ctx, "update build provenance verification failed", "asset", asset.Name, "repo", options.Config.UpdateRepo(), "err", err)
		return err
	}
	return nil
}

func verifyReleaseAssetAttestation(ctx context.Context, options Options, latest release, asset releaseAsset, digestHex string, digestBytes []byte) error {
	repo := options.Config.UpdateRepo()
	tagCommitSHA, err := resolveReleaseTagCommitSHA(ctx, options.Client, repo, latest.TagName)
	if err != nil {
		options.Log.WarnContext(ctx, "update release tag resolve failed", "repo", repo, "tag", latest.TagName, "err", err)
		return err
	}
	attestations, err := fetchAttestations(ctx, options.Client, repo, "sha1:"+tagCommitSHA, "release")
	if err != nil {
		options.Log.WarnContext(ctx, "update release attestation fetch failed", "repo", repo, "tag", latest.TagName, "err", err)
		return fmt.Errorf("fetch release attestation: %w", err)
	}
	sanMatcher, err := sigverify.NewSANMatcher(githubReleaseAttestationSAN, "")
	if err != nil {
		return fmt.Errorf("build release SAN matcher: %w", err)
	}
	issuerMatcher, err := sigverify.NewIssuerMatcher("", ".*")
	if err != nil {
		return fmt.Errorf("build release issuer matcher: %w", err)
	}
	identity, err := sigverify.NewCertificateIdentity(sanMatcher, issuerMatcher, fulciocert.Extensions{})
	if err != nil {
		return fmt.Errorf("build release certificate identity: %w", err)
	}
	verifier, err := newGitHubReleaseVerifier()
	if err != nil {
		options.Log.WarnContext(ctx, "update release verifier create failed", "repo", repo, "tag", latest.TagName, "err", err)
		return err
	}
	policy := sigverify.NewPolicy(
		sigverify.WithArtifactDigest("sha256", digestBytes),
		sigverify.WithCertificateIdentity(identity),
	)
	var lastErr error
	for _, attestation := range attestations {
		result, verifyErr := verifyBundle(attestation.Bundle, verifier, policy)
		if verifyErr != nil {
			lastErr = verifyErr
			continue
		}
		if validateErr := validateReleaseAttestation(result, repo, latest.TagName, asset.Name, digestHex); validateErr != nil {
			lastErr = validateErr
			continue
		}
		return nil
	}
	if lastErr == nil {
		return fmt.Errorf("release attestation missing for %s", latest.TagName)
	}
	return fmt.Errorf("release attestation verification failed: %w", lastErr)
}

func verifyBuildProvenanceAttestation(ctx context.Context, options Options, asset releaseAsset, digestHex string, digestBytes []byte) error {
	repo := options.Config.UpdateRepo()
	attestations, err := fetchAttestations(ctx, options.Client, repo, "sha256:"+digestHex, githubBuildProvenancePredicateType)
	if err != nil {
		options.Log.WarnContext(ctx, "update build provenance fetch failed", "repo", repo, "asset", asset.Name, "err", err)
		return fmt.Errorf("fetch build provenance attestation: %w", err)
	}
	sourceRepositoryURI := githubRepositoryURI(repo)
	sanMatcher, err := sigverify.NewSANMatcher(goMakefileReleaseBuildWorkflowURI, "")
	if err != nil {
		return fmt.Errorf("build provenance SAN matcher: %w", err)
	}
	issuerMatcher, err := sigverify.NewIssuerMatcher(githubActionsOIDCIssuer, "")
	if err != nil {
		return fmt.Errorf("build provenance issuer matcher: %w", err)
	}
	identity, err := sigverify.NewCertificateIdentity(sanMatcher, issuerMatcher, fulciocert.Extensions{
		BuildSignerURI:      goMakefileReleaseBuildWorkflowURI,
		RunnerEnvironment:   githubHostedRunnerEnvironment,
		SourceRepositoryURI: sourceRepositoryURI,
	})
	if err != nil {
		return fmt.Errorf("build provenance certificate identity: %w", err)
	}
	verifier, err := newBuildProvenanceVerifier()
	if err != nil {
		options.Log.WarnContext(ctx, "update build provenance verifier create failed", "repo", repo, "asset", asset.Name, "err", err)
		return err
	}
	policy := sigverify.NewPolicy(
		sigverify.WithArtifactDigest("sha256", digestBytes),
		sigverify.WithCertificateIdentity(identity),
	)
	var lastErr error
	for _, attestation := range attestations {
		result, verifyErr := verifyBundle(attestation.Bundle, verifier, policy)
		if verifyErr != nil {
			lastErr = verifyErr
			continue
		}
		if validateErr := validateBuildProvenance(result, repo, asset.Name, digestHex); validateErr != nil {
			lastErr = validateErr
			continue
		}
		return nil
	}
	if lastErr == nil {
		return fmt.Errorf("build provenance attestation missing for %s", asset.Name)
	}
	return fmt.Errorf("build provenance verification failed: %w", lastErr)
}

func newBuildProvenanceVerifier() (*sigverify.Verifier, error) {
	trustedRoot, err := sigroot.FetchTrustedRoot()
	if err != nil {
		slog.Warn("update sigstore trusted root fetch failed", "err", err)
		return nil, fmt.Errorf("fetch sigstore trusted root: %w", err)
	}
	return newSigstoreVerifier(
		trustedRoot,
		sigverify.WithIntegratedTimestamps(1),
		sigverify.WithTransparencyLog(1),
	)
}

func newGitHubReleaseVerifier() (*sigverify.Verifier, error) {
	tufOptions := sigtuf.DefaultOptions().
		WithRepositoryBaseURL(githubReleaseTUFRepositoryURL).
		WithRoot(githubReleaseTUFBootstrapRoot)
	trustedRoot, err := sigroot.FetchTrustedRootWithOptions(tufOptions)
	if err != nil {
		slog.Warn("update GitHub release trusted root fetch failed", "err", err)
		return nil, fmt.Errorf("fetch GitHub release trusted root: %w", err)
	}
	return newSigstoreVerifier(
		trustedRoot,
		sigverify.WithObserverTimestamps(1),
	)
}

func newSigstoreVerifier(
	trustedRoot sigroot.TrustedMaterial,
	options ...sigverify.VerifierOption,
) (*sigverify.Verifier, error) {
	verifier, err := sigverify.NewVerifier(trustedRoot, options...)
	if err != nil {
		slog.Warn("update sigstore verifier create failed", "err", err)
		return nil, fmt.Errorf("create sigstore verifier: %w", err)
	}
	return verifier, nil
}

func verifyBundle(bundleJSON json.RawMessage, verifier *sigverify.Verifier, policy sigverify.PolicyBuilder) (*sigverify.VerificationResult, error) {
	var attestationBundle sigbundle.Bundle
	if err := json.Unmarshal(bundleJSON, &attestationBundle); err != nil {
		slog.Warn("update attestation bundle decode failed", "err", err)
		return nil, fmt.Errorf("decode attestation bundle: %w", err)
	}
	result, err := verifier.Verify(&attestationBundle, policy)
	if err != nil {
		slog.Warn("update attestation bundle verify failed", "err", err)
		return nil, fmt.Errorf("verify attestation bundle: %w", err)
	}
	return result, nil
}

func fetchAttestations(ctx context.Context, client *http.Client, repo string, digestQualifier string, predicateType string) ([]githubAttestationRecord, error) {
	owner, name, err := splitRepository(repo)
	if err != nil {
		return nil, err
	}
	query := url.Values{}
	query.Set("per_page", "100")
	if predicateType != "" {
		query.Set("predicate_type", predicateType)
	}
	requestURL := releaseAPIBaseURL() + "/repos/" + owner + "/" + name + "/attestations/" + digestQualifier + "?" + query.Encode()
	var response githubAttestationsResponse
	if err := fetchGitHubJSON(ctx, client, requestURL, func(decoder *json.Decoder) error {
		return decoder.Decode(&response)
	}); err != nil {
		return nil, err
	}
	if len(response.Attestations) == 0 {
		return nil, fmt.Errorf("no attestations returned for %s", digestQualifier)
	}
	return response.Attestations, nil
}

func resolveReleaseTagCommitSHA(ctx context.Context, client *http.Client, repo string, tag string) (string, error) {
	owner, name, err := splitRepository(repo)
	if err != nil {
		return "", err
	}
	requestURL := releaseAPIBaseURL() + "/repos/" + owner + "/" + name + "/git/ref/tags/" + url.PathEscape(tag)
	var ref githubGitRefResponse
	if err := fetchGitHubJSON(ctx, client, requestURL, func(decoder *json.Decoder) error {
		return decoder.Decode(&ref)
	}); err != nil {
		slog.WarnContext(ctx, "update release tag ref fetch failed", "repo", repo, "tag", tag, "err", err)
		return "", fmt.Errorf("resolve tag %s: %w", tag, err)
	}
	switch ref.Object.Type {
	case githubGitObjectTypeCommit:
		if ref.Object.SHA == "" {
			return "", fmt.Errorf("tag %s commit SHA missing", tag)
		}
		return ref.Object.SHA, nil
	case githubGitObjectTypeTag:
		if ref.Object.URL == "" {
			return "", fmt.Errorf("tag %s object URL missing", tag)
		}
		var annotated githubAnnotatedTagResponse
		if err := fetchGitHubJSON(ctx, client, ref.Object.URL, func(decoder *json.Decoder) error {
			return decoder.Decode(&annotated)
		}); err != nil {
			slog.WarnContext(ctx, "update annotated tag fetch failed", "repo", repo, "tag", tag, "err", err)
			return "", fmt.Errorf("resolve annotated tag %s: %w", tag, err)
		}
		if annotated.Object.SHA == "" {
			return "", fmt.Errorf("annotated tag %s target SHA missing", tag)
		}
		return annotated.Object.SHA, nil
	default:
		return "", fmt.Errorf("unsupported tag object type %q for %s", ref.Object.Type, tag)
	}
}

func fetchGitHubJSON(ctx context.Context, client *http.Client, requestURL string, decode func(*json.Decoder) error) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		slog.WarnContext(ctx, "update GitHub request build failed", "url", requestURL, "err", err)
		return fmt.Errorf("build GitHub request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := client.Do(req)
	if err != nil {
		slog.WarnContext(ctx, "update GitHub request failed", "url", requestURL, "err", err)
		return fmt.Errorf("request GitHub API: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		slog.WarnContext(ctx, "update GitHub status failed", "url", requestURL, "status_code", resp.StatusCode)
		return fmt.Errorf("GitHub API %s: HTTP %d", requestURL, resp.StatusCode)
	}
	if err := decode(json.NewDecoder(resp.Body)); err != nil {
		slog.WarnContext(ctx, "update GitHub decode failed", "url", requestURL, "err", err)
		return fmt.Errorf("decode GitHub API response: %w", err)
	}
	return nil
}

func splitRepository(repo string) (string, string, error) {
	owner, name, ok := strings.Cut(repo, "/")
	if !ok || owner == "" || name == "" {
		return "", "", fmt.Errorf("repository %q must be owner/name", repo)
	}
	return owner, name, nil
}

func validateReleaseAttestation(result *sigverify.VerificationResult, repo string, tag string, assetName string, digestHex string) error {
	if result.Statement == nil {
		return fmt.Errorf("release attestation statement missing")
	}
	if result.Statement.GetPredicateType() != githubReleaseAttestationPredicateType {
		return fmt.Errorf("release attestation predicate type %q did not match %q", result.Statement.GetPredicateType(), githubReleaseAttestationPredicateType)
	}
	predicate := result.Statement.GetPredicate()
	if predicate == nil {
		return fmt.Errorf("release attestation predicate missing")
	}
	repository := predicateFieldString(predicate, "repository")
	if repository != repo {
		return fmt.Errorf("release attestation repository %q did not match %q", repository, repo)
	}
	releaseTag := predicateFieldString(predicate, "tag")
	if releaseTag != tag {
		return fmt.Errorf("release attestation tag %q did not match %q", releaseTag, tag)
	}
	if !statementHasSHA256SubjectDigest(result.Statement.GetSubject(), assetName, digestHex) {
		return fmt.Errorf("release attestation did not include %s with digest %s", assetName, digestHex)
	}
	return nil
}

func validateBuildProvenance(result *sigverify.VerificationResult, repo string, assetName string, digestHex string) error {
	if result.Statement == nil {
		return fmt.Errorf("build provenance statement missing")
	}
	if result.Statement.GetPredicateType() != githubBuildProvenancePredicateType {
		return fmt.Errorf("build provenance predicate type %q did not match %q", result.Statement.GetPredicateType(), githubBuildProvenancePredicateType)
	}
	if !statementHasSHA256SubjectDigest(result.Statement.GetSubject(), assetName, digestHex) {
		return fmt.Errorf("build provenance did not include %s with digest %s", assetName, digestHex)
	}
	if result.Signature == nil || result.Signature.Certificate == nil {
		return fmt.Errorf("build provenance certificate summary missing")
	}
	return validateBuildProvenanceCertificate(result.Signature.Certificate, repo)
}

func validateBuildProvenanceCertificate(summary *fulciocert.Summary, repo string) error {
	if summary.SubjectAlternativeName != goMakefileReleaseBuildWorkflowURI {
		return fmt.Errorf("build provenance SAN %q did not match %q", summary.SubjectAlternativeName, goMakefileReleaseBuildWorkflowURI)
	}
	if summary.Issuer != githubActionsOIDCIssuer {
		return fmt.Errorf("build provenance issuer %q did not match %q", summary.Issuer, githubActionsOIDCIssuer)
	}
	if summary.BuildSignerURI != goMakefileReleaseBuildWorkflowURI {
		return fmt.Errorf("build signer URI %q did not match %q", summary.BuildSignerURI, goMakefileReleaseBuildWorkflowURI)
	}
	if summary.RunnerEnvironment != githubHostedRunnerEnvironment {
		return fmt.Errorf("runner environment %q did not match %q", summary.RunnerEnvironment, githubHostedRunnerEnvironment)
	}
	sourceRepositoryURI := githubRepositoryURI(repo)
	if summary.SourceRepositoryURI != sourceRepositoryURI {
		return fmt.Errorf("source repository URI %q did not match %q", summary.SourceRepositoryURI, sourceRepositoryURI)
	}
	return nil
}

func githubRepositoryURI(repo string) string {
	return "https://github.com/" + repo
}

func predicateFieldString(predicate *structpb.Struct, key string) string {
	if predicate == nil {
		return ""
	}
	value, ok := predicate.GetFields()[key]
	if !ok {
		return ""
	}
	return value.GetStringValue()
}

func statementHasSHA256SubjectDigest(subjects []*in_toto.ResourceDescriptor, name string, digestHex string) bool {
	for _, subject := range subjects {
		if subject.GetName() != name {
			continue
		}
		if subject.GetDigest()["sha256"] == digestHex {
			return true
		}
	}
	return false
}
