//go:build !darwin && !linux

package auditstorage

import "errors"

// ReadFileIdentity reports that cutover identities are unsupported on this platform.
func ReadFileIdentity(string) (FileIdentity, error) {
	return FileIdentity{}, errors.New("cutover file identity is unsupported")
}

func guardCommittedCutoverIdentity(string, FileIdentity) error {
	return errors.New("cutover file identity is unsupported")
}

func guardCommittedCutoverFilesystemIdentity(string, FileIdentity) error {
	return errors.New("cutover file identity is unsupported")
}
