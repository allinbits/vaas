package e2e

func (s *IntegrationTestSuite) TestVAAS() {
	s.testProviderBlockProduction()
	s.testConsumerBlockProduction()
	s.testConsumerOnProvider()
	s.testProviderOnConsumer()
	s.testValidatorSetSync()
	s.testConsumerDebtFlow()
	s.testDowntimeSlash()
	// Run last: stops the provider container and replaces it with a fresh
	// one started from the exported genesis.
	s.testGenesisRoundTrip()
}
