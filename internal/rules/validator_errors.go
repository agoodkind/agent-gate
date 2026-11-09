package rules

type validatorPanicError struct {
	value string
}

func (failure *validatorPanicError) Error() string {
	return "panic: " + failure.value
}
