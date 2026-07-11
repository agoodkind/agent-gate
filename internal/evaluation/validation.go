package evaluation

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
)

const sha256Prefix = "sha256:"

func validateLayerSemantics(kind string, status string, outcome string) error {
	if status != "complete" && status != "error" && status != "skipped" {
		return fmt.Errorf("layer status %q is invalid", status)
	}
	predicate := isPredicateLayerKind(kind)
	if status == "complete" && predicate {
		if outcome != "match" && outcome != "nonmatch" {
			return errors.New("complete predicate layer requires match or nonmatch outcome")
		}
		return nil
	}
	if outcome != "" {
		return errors.New("nonpredicate, error, or skipped layer must not have an outcome")
	}
	return nil
}

func validateReadLayerSemantics(
	kind string,
	status string,
	outcome string,
	outcomeKnown bool,
) error {
	if !outcomeKnown && outcome == "" && status == "complete" && isPredicateLayerKind(kind) {
		return nil
	}
	return validateLayerSemantics(kind, status, outcome)
}

func isPredicateLayerKind(kind string) bool {
	return kind == "deterministic" || kind == "inference"
}

func validateLayerOutputHash(output []byte, outputHash string) error {
	if len(outputHash) != len(sha256Prefix)+sha256.Size*2 ||
		outputHash[:len(sha256Prefix)] != sha256Prefix {
		return errors.New("layer output hash format is invalid")
	}
	digestBytes, err := hex.DecodeString(outputHash[len(sha256Prefix):])
	if err != nil || len(digestBytes) != sha256.Size {
		return errors.New("layer output hash format is invalid")
	}
	expected := sha256.Sum256(output)
	if string(digestBytes) != string(expected[:]) {
		return errors.New("layer output hash does not match output JSON")
	}
	return nil
}
