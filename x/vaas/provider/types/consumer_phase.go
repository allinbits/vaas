package types

// Prelaunched reports whether the phase is one a consumer holds before it
// launches: registered or initialized.
func (p ConsumerPhase) Prelaunched() bool {
	return p == CONSUMER_PHASE_REGISTERED || p == CONSUMER_PHASE_INITIALIZED
}
