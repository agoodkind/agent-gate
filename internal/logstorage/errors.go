package logstorage

type operationError struct {
	Operation string
	Cause     error
}

func (failure *operationError) Error() string {
	return failure.Operation + ": " + failure.Cause.Error()
}

func (failure *operationError) Unwrap() error {
	return failure.Cause
}

func wrapError(operation string, err error) error {
	if err == nil {
		return nil
	}
	return &operationError{Operation: operation, Cause: err}
}
