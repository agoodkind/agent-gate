package daemon

type reloadDecodeError struct {
	reason string
}

func (failure *reloadDecodeError) Error() string {
	return "config did not decode; keeping previous config: " + failure.reason
}

type reloadAuditStorageError struct {
	reason string
}

func (failure *reloadAuditStorageError) Error() string {
	return "audit storage config invalid: " + failure.reason
}
