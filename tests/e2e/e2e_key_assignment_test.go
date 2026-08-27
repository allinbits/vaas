package e2e

// e2e_key_assignment_test.go exercises MsgAssignConsumerKey end to end: the
// provider records the assignment, the next epoch's VSC packet carries the
// assigned consensus key to the consumer, and the consumer's CometBFT
// validator set switches the validator over to the assigned consensus address
// while the chain keeps producing blocks.
//
// Which validator gets the assignment matters. This suite runs a single
// consumer node, whose priv_validator_key is a copy of the provider's sole
// signing validator ("val", ~99.5% of the voting power): reassigning *that*
// validator's consumer key would move it to an address its node cannot sign
// with and halt the consumer chain outright. Launching a second consumer chain
// with the assigned key set up front (which would exercise the pre-launch
// assignment path) needs a second consumer container plus a second ts-relayer
// path -- neither of which the shared bring-up in base_suite_test.go supports.
//
// The assignment is therefore done on the permanently-silent second provider
// validator (see createSilentValidator in e2e_downtime_slash_test.go), which is
// in the consumer's validator set but runs no consumer node and holds ~0.5% of
// the power: its consensus address can change without stalling consensus, so
// the full provider-to-consumer path can be asserted -- including the consumer
// actually applying the assigned address -- rather than only the provider-side
// bookkeeping.

import (
	"encoding/json"
	"slices"
	"strings"
	"time"

	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
)

func (s *IntegrationTestSuite) testKeyAssignment() {
	s.Run("assigned consumer key replaces the validator's address in the consumer valset", func() {
		const consumerID = "0"

		_, valoper := s.ensureSilentValidator("val2", "5000000"+bondDenom)
		providerConsAddr := s.providerValidatorConsAddr(valoper)
		s.T().Logf("assigning a consumer key for validator %s (provider consensus address %s)",
			valoper, providerConsAddr)

		// Without an assignment a validator validates a consumer under its own
		// provider consensus address; that is the state this test changes.
		s.Require().Eventuallyf(func() bool {
			addrs, err := s.consumerValsetConsAddrs()
			return err == nil && slices.Contains(addrs, providerConsAddr)
		}, 3*time.Minute, 5*time.Second,
			"validator %s never appeared in the consumer validator set under its provider consensus address %s",
			valoper, providerConsAddr)
		s.Require().Emptyf(s.queryValidatorConsumerAddr(consumerID, providerConsAddr),
			"validator %s already has an assigned consumer key for consumer %s", valoper, consumerID)

		pubKeyJSON, assignedConsAddr := generateConsumerConsensusKey(s.T())
		s.T().Logf("assigned consumer consensus address will be %s", assignedConsAddr)

		stdout, stderr, err := s.dockerExec(s.providerValRes[0].Container.ID, []string{
			providerBinary, "tx", "provider", "assign-consensus-key", consumerID, pubKeyJSON,
			"--from", "val2",
			"--home", providerHomePath,
			"--keyring-backend", "test",
			"--chain-id", providerChainID,
			"--gas", "auto",
			"--gas-adjustment", "1.5",
			"--fees", "10000" + bondDenom,
			"-y",
			"-o", "json",
		})
		s.Require().NoErrorf(err, "failed to submit assign-consensus-key: stderr=%s", stderr.String())
		s.requireTxCommitted(stdout.Bytes())

		// Provider-side state: both directions of the mapping.
		s.Require().Equalf(assignedConsAddr, s.queryValidatorConsumerAddr(consumerID, providerConsAddr),
			"provider does not report the assigned consumer address for validator %s", valoper)
		s.Require().Equalf(providerConsAddr, s.queryValidatorProviderAddr(consumerID, assignedConsAddr),
			"provider does not map the assigned consumer address back to validator %s", valoper)
		pairs := s.queryAllPairsValConsAddr(consumerID)
		s.Require().Equalf(assignedConsAddr, pairs[providerConsAddr],
			"address pairs for consumer %s do not carry the assignment: %v", consumerID, pairs)

		// Consumer-side: the next epoch's VSC packet swaps the address in the
		// live validator set.
		s.Require().Eventuallyf(func() bool {
			addrs, err := s.consumerValsetConsAddrs()
			if err != nil {
				return false
			}
			return slices.Contains(addrs, assignedConsAddr) && !slices.Contains(addrs, providerConsAddr)
		}, 4*time.Minute, 5*time.Second,
			"consumer validator set never switched validator %s from %s to its assigned address %s",
			valoper, providerConsAddr, assignedConsAddr)

		// And the consumer keeps producing blocks under the new set: a
		// validator set carrying a key the consumer node cannot sign with
		// would stall consensus instead.
		switchHeight, err := s.queryConsumerBlockHeight()
		s.Require().NoError(err, "failed to read consumer height after the key assignment took effect")
		s.Require().Eventuallyf(func() bool {
			h, herr := s.queryConsumerBlockHeight()
			return herr == nil && h > switchHeight+1
		}, 90*time.Second, 3*time.Second,
			"consumer stopped producing blocks after switching validator %s onto its assigned consensus address (stuck at height %d)",
			valoper, switchHeight)

		s.Require().Equalf("CONSUMER_PHASE_LAUNCHED", s.queryProviderConsumerPhase(consumerID),
			"consumer %s left the LAUNCHED phase during the key assignment", consumerID)
	})
}

// ensureSilentValidator returns the account and operator addresses of the
// permanently-silent provider validator backed by the named keyring entry,
// creating it via createSilentValidator when it does not exist yet. The main
// suite creates "val2" in testDowntimeSlash; this keeps the sub-tests that
// need a validator with no consumer node runnable on their own (e.g. under
// `go test -run .../assigned_consumer_key`).
func (s *IntegrationTestSuite) ensureSilentValidator(key, selfBondAmount string) (accAddr, valoperAddr string) {
	stdout, _, err := s.dockerExec(s.providerValRes[0].Container.ID, []string{
		providerBinary, "keys", "show", key, "--bech", "val", "-a",
		"--home", providerHomePath,
		"--keyring-backend", "test",
	})
	if err == nil {
		valoper := strings.TrimSpace(stdout.String())
		vals, verr := s.queryProviderValidators()
		if verr == nil {
			for _, v := range vals {
				if v.OperatorAddress == valoper && v.Status == stakingtypes.Bonded {
					accStdout, _, aerr := s.dockerExec(s.providerValRes[0].Container.ID, []string{
						providerBinary, "keys", "show", key, "-a",
						"--home", providerHomePath,
						"--keyring-backend", "test",
					})
					s.Require().NoError(aerr, "failed to get %s account address", key)
					return strings.TrimSpace(accStdout.String()), valoper
				}
			}
		}
	}

	s.T().Logf("bonding a permanently-silent provider validator %q...", key)
	return s.createSilentValidator(key, selfBondAmount)
}

// queryValidatorConsumerAddr returns the consumer consensus address currently
// assigned to the provider validator with the given provider consensus
// address, or "" when it has no assignment for that consumer.
func (s *IntegrationTestSuite) queryValidatorConsumerAddr(consumerID, providerConsAddr string) string {
	stdout, _, err := s.dockerExec(s.providerValRes[0].Container.ID, []string{
		providerBinary, "query", "provider", "validator-consumer-key", consumerID, providerConsAddr,
		"--home", providerHomePath,
		"--output", "json",
	})
	if err != nil {
		return ""
	}
	var res struct {
		ConsumerAddress string `json:"consumer_address"`
	}
	if json.Unmarshal(stdout.Bytes(), &res) != nil {
		return ""
	}
	return strings.TrimSpace(res.ConsumerAddress)
}

// queryValidatorProviderAddr returns the provider consensus address a consumer
// consensus address maps back to, or "" when the mapping is unknown.
func (s *IntegrationTestSuite) queryValidatorProviderAddr(consumerID, consumerConsAddr string) string {
	stdout, _, err := s.dockerExec(s.providerValRes[0].Container.ID, []string{
		providerBinary, "query", "provider", "validator-provider-key", consumerID, consumerConsAddr,
		"--home", providerHomePath,
		"--output", "json",
	})
	if err != nil {
		return ""
	}
	var res struct {
		ProviderAddress string `json:"provider_address"`
	}
	if json.Unmarshal(stdout.Bytes(), &res) != nil {
		return ""
	}
	return strings.TrimSpace(res.ProviderAddress)
}

// queryAllPairsValConsAddr returns the provider-to-consumer consensus address
// pairs the provider tracks for a consumer, keyed by provider address.
func (s *IntegrationTestSuite) queryAllPairsValConsAddr(consumerID string) map[string]string {
	stdout, _, err := s.dockerExec(s.providerValRes[0].Container.ID, []string{
		providerBinary, "query", "provider", "all-pairs-valconsensus-address", consumerID,
		"--home", providerHomePath,
		"--output", "json",
	})
	s.Require().NoError(err, "failed to query address pairs for consumer %s", consumerID)

	var res struct {
		PairValConAddr []struct {
			ProviderAddress string `json:"provider_address"`
			ConsumerAddress string `json:"consumer_address"`
		} `json:"pair_val_con_addr"`
	}
	s.Require().NoErrorf(json.Unmarshal(stdout.Bytes(), &res),
		"failed to decode address pairs response: %s", stdout.String())

	pairs := make(map[string]string, len(res.PairValConAddr))
	for _, p := range res.PairValConAddr {
		pairs[p.ProviderAddress] = p.ConsumerAddress
	}
	return pairs
}
