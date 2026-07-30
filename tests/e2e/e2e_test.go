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
	s.testDowntimeSlash()
	s.testFeePoolSendRestriction()
	s.testFeePoolFundAndLockEnforcement()
	s.testFeePoolGovSubsidyClawback()
	// Validators are actually paid their per-epoch share out of the consumer's
	// fee pool (the fee-pool tests above only cover money going in).
	s.testFeeDistributionAccrual()
	// Assign a consumer consensus key to the silent second validator and watch
	// the consumer's validator set switch over to it. Runs after the fee
	// assertion above, which measures a share computed from the bonded count
	// this may change, and before the challenge test below, which needs the
	// assigned-key mapping to already be settled.
	s.testKeyAssignment()
	// Challenge a queued downtime slash with real consumer chain data; the
	// validator really was absent, so the challenge must be rejected at the
	// sealed-signature step without pausing the consumer.
	s.testDowntimeChallengeWithoutSealedSignature()
	// Explicitly remove consumer "0"; verify STOPPED (DELETED if removal_time
	// has elapsed). Must run after all tests that rely on consumer "0" being
	// LAUNCHED and before testGenesisRoundTrip (which tolerates any phase).
	s.testLivenessRemoval()
	// Run last: stops the provider container and replaces it with a fresh
	// one started from the exported genesis.
	s.testGenesisRoundTrip()
}
