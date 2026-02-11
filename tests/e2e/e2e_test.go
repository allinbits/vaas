package e2e


var (
	runVAASTest                   = true
)

func (s *IntegrationTestSuite) TestVAAS() {
	if !runVAASTest {
		s.T().Skip()
	}
	s.testProviderBlockProduction()
	s.testConsumerBlockProduction()
}
