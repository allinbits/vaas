package e2e


func (s *IntegrationTestSuite) TestVAAS() {
	s.testProviderBlockProduction()
	s.testConsumerBlockProduction()
	s.testConsumerOnProvider()
	s.testProviderOnConsumer()
	s.testValidatorSetSync()
}
