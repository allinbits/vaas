package e2e

func (s *IntegrationTestSuite) TestVAAS() {
	s.testProviderBlockProduction()
	s.testConsumerBlockProduction()
	s.testConsumerOnProvider()
	s.testProviderOnConsumer()
	s.testValidatorSetSync()
	// Pause consumer briefly while provider VP changes; verify consumer stays
	// LAUNCHED and re-converges via snapshot resync after recovery.
	s.testLivenessTransientOutage()
	s.testConsumerDebtFlow()
	// Runs after the debt flow on purpose: that test both needs the consumer
	// to enter debt naturally (pre-funding the pool would stall it) and leaves
	// the pool funded, so the photon test observes the fee policy rather than
	// the debt gate.
	s.testPhotonFeeEnforcement()
	s.testDowntimeSlash()
	s.testFeePoolSendRestriction()
	s.testFeePoolFundAndLockEnforcement()
	s.testFeePoolGovSubsidyClawback()
	// Explicitly remove consumer "0"; verify STOPPED (DELETED if removal_time
	// has elapsed). Must run after all tests that rely on consumer "0" being
	// LAUNCHED and before testGenesisRoundTrip (which tolerates any phase).
	s.testLivenessRemoval()
	// Run last: stops the provider container and replaces it with a fresh
	// one started from the exported genesis.
	s.testGenesisRoundTrip()
}
