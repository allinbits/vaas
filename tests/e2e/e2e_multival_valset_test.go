package e2e

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// validatorIdentity names one of the suite's validators in the forms the
// scenarios need: its index (node val<index> on both chains), its provider
// consensus address, which is also its consumer address until it assigns a
// key, and its operator address.
type validatorIdentity struct {
	index    int
	consAddr string
	valoper  string
}

// validator resolves validator i from the consensus key its provider node
// runs under (the node's priv_validator_key.json on the host) and the staking
// entry that key belongs to.
func (s *MultiValidatorIntegrationTestSuite) validator(i int) validatorIdentity {
	raw, err := os.ReadFile(filepath.Join(s.providerHome(i), "config", "priv_validator_key.json"))
	s.Require().NoErrorf(err, "failed to read provider val%d's consensus key", i)
	var keyFile struct {
		PubKey struct {
			Value string `json:"value"`
		} `json:"pub_key"`
	}
	s.Require().NoErrorf(json.Unmarshal(raw, &keyFile), "failed to decode provider val%d's consensus key", i)
	pub, err := base64.StdEncoding.DecodeString(keyFile.PubKey.Value)
	s.Require().NoErrorf(err, "failed to decode provider val%d's public key", i)
	consAddr := consAddrFromEd25519PubKey(pub)
	valoper := s.providerValoperForConsAddr(consAddr)
	s.Require().NotEmptyf(valoper, "provider val%d (%s) has no bonded validator", i, consAddr)
	return validatorIdentity{index: i, consAddr: consAddr, valoper: valoper}
}

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
