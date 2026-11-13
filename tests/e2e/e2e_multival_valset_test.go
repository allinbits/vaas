package e2e

import (
	"time"
)

// testValidatorSetMirror: from launch, the consumer's validator set carries
// every provider validator with its power, and nothing else.
func (s *MultiValidatorIntegrationTestSuite) testValidatorSetMirror() {
	s.Run("the consumer's validator set mirrors the four provider validators", func() {
		s.Require().Eventuallyf(func() bool {
			vals, err := s.queryConsumerNetValidators()
			return err == nil && len(vals) == multivalValidators
		}, 3*time.Minute, 5*time.Second,
			"the consumer never reported %d validators", multivalValidators)

		consumerVals, err := s.queryConsumerNetValidators()
		s.Require().NoError(err, "failed to query the consumer's validator set")
		consumerPowers := s.valAddrPowerMap(consumerVals)
		providerVals, err := s.queryProviderNetValidators()
		s.Require().NoError(err, "failed to query the provider's validator set")
		providerPowers := s.valAddrPowerMap(providerVals)

		for i := range multivalValidators {
			v := s.validator(i)
			s.Require().Equalf(multivalValidatorPower, consumerPowers[v.consAddr],
				"consumer power of val%d (%s)", i, v.consAddr)
			s.Require().Equalf(multivalValidatorPower, providerPowers[v.consAddr],
				"provider power of val%d (%s)", i, v.consAddr)
		}
		s.Require().Len(consumerPowers, multivalValidators, "the consumer's set carries more than the four validators")
	})
}
