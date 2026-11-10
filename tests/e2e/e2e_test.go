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
	s.testFeeDistributionAccrual()
	// After the fee assertion, whose bonded count this may change, and before
	// the challenge test, which needs the assignment settled.
	s.testKeyAssignment()
	s.testDowntimeChallengeWithoutSealedSignature()
	// Refusal-protection model (docs/consumer-refusal.md): all of these need
	// consumer "0" LAUNCHED and, except the last pair, leave it LAUNCHED.
	s.testConsumerRefusalPauseAndResume()
	s.testDowntimeDeferralBehindRejectedRemoval()
	s.testEquivocationDeferralBehindRejectedRemoval()
	// Stops the consumer container and replaces it with a fresh one started
	// from its exported genesis at a continuing height, then verifies VSC
	// flow resumes. Needs consumer "0" LAUNCHED, so it must run before
	// testLivenessRemoval. It also runs after every scenario that needs
	// consumer-to-provider traffic: the restarted node keeps no history at
	// the provider client's trusted height, so no relayer can update that
	// client afterwards and consumer-originated packets stay undelivered.
	s.testConsumerGenesisRoundTrip()
	// Queue a punishment the explicit removal below will cancel, with an
	// undelegation on hold behind it; both stay live across the provider
	// restart in between.
	s.testEquivocationQueuedBeforeRemoval()
	// Stops the provider container and replaces it with a fresh one started
	// from its exported genesis, taken while the punishment is pending and
	// the hold is live. Runs once every scenario that needs the consumer is
	// done: the restarted provider keeps no history at the consumer client's
	// trusted height, so nothing crosses IBC afterwards, and only
	// provider-local scenarios follow.
	s.testGenesisRoundTrip()
	// Explicitly remove consumer "0"; verify STOPPED (DELETED if removal_time
	// has elapsed). Must run after all tests that rely on consumer "0" being
	// LAUNCHED.
	s.testLivenessRemoval()
	// The passed vote cancels the punishment and releases the hold the
	// restarted provider imported, through the operation index x/staking
	// rebuilt at import.
	s.testEquivocationCancelledByRemoval()
}
