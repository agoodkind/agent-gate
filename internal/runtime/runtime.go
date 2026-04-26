package runtime

// Adapter describes provider-specific runtime wrapper behavior. Only Claude
// currently implements active session settings isolation.
type Adapter interface {
	Name() string
}
