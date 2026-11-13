package e2e

import (
	"strconv"
	"time"

	"github.com/ory/dockertest/v3"
)

// four_validator_helpers_test.go holds what the suites running several
// validator nodes per chain share: the provider genesis they run on, their
// bring-up, and the identity of each validator.

// fourValidatorBlocksPerEpoch is the provider's blocks_per_epoch in the
// suites running several validators (patchFourValidatorProviderGenesis): the
// provider queues and sends VSC packets at every multiple of it.
const fourValidatorBlocksPerEpoch = 5

// patchFourValidatorProviderGenesis applies the main suite's provider
// parameters to a multi-validator suite: fast epochs, a per-block fee, and
// the shortened downtime detection, so a tumbling window closes in seconds
// and a challenge window in minutes (see e2e_setup_test.go for why each
// value is what it is), with the suite's gov voting period. The provider's own
// downtime window is loosened, so a stopped consumer node never gets its
// validator jailed on the provider, whose node keeps running.
func (s *baseTestSuite) patchFourValidatorProviderGenesis(appState map[string]any) {
	s.setGovVotingPeriod(appState)

	params := s.genesisSection(appState, "vaasprovider", "params")
	params["blocks_per_epoch"] = strconv.Itoa(fourValidatorBlocksPerEpoch)
	params["fees_per_block_amount"] = "1000"
	params["infraction_parameters"] = map[string]any{
		"double_sign": map[string]any{
			"slash_fraction": "0.050000000000000000",
			"jail_duration":  "315360000s",
			"tombstone":      true,
		},
		"downtime": map[string]any{
			"slash_fraction": "0.010000000000000000",
			"jail_duration":  "0s",
			"tombstone":      false,
		},
		"downtime_grace_period":     "604800s",
		"signed_blocks_window":      "30",
		"min_signed_per_window":     "0.500000000000000000",
		"downtime_challenge_window": "120s",
		"downtime_evidence_max_age": "120s",
	}

	s.genesisSection(appState, "slashing", "params")["signed_blocks_window"] = "100000"

	// A consensus key rotation pays x/staking's key_rotation_fee from the
	// operator account. The genesis the init container writes prices that
	// fee in "stake" whatever --default-denom says, since x/staking fixes its
	// default fee at package init, before the flag renames the bond denom;
	// the validators hold uatone.
	s.genesisSection(appState, "staking", "params")["key_rotation_fee"] = map[string]any{
		"denom":  bondDenom,
		"amount": "1000000",
	}
}

// bringUpSuite starts everything a suite configured in s.cfg runs on: both
// chains, the consumer registered and launched, the relayer, the declared
// clients, the first VSC synced, and a funded fee pool (the downtime slash is
// priced from the epoch fee share, and the consumer must never run in debt).
func (s *baseTestSuite) bringUpSuite() {
	s.cdc = makeCodec()

	var err error
	s.dkrPool, err = dockertest.NewPool("")
	s.Require().NoError(err, "failed to create docker pool")
	s.dkrPool.MaxWait = 5 * time.Minute

	s.cleanupStaleContainers()

	s.dkrNet, err = s.dkrPool.CreateNetwork(s.cfg.dockerNetwork)
	s.Require().NoError(err, "failed to create the suite's docker network")

	s.T().Log("step 1: initializing the provider chain...")
	s.provider = &chain{id: s.cfg.providerChainID}
	s.initAndStartProvider()

	s.T().Log("step 2: registering the consumer on the provider...")
	s.registerConsumerOnProvider()

	s.T().Log("step 3: fetching the consumer genesis from the provider...")
	consumerGenesisJSON := s.fetchConsumerGenesis()

	s.T().Log("step 4: initializing the consumer chain...")
	s.consumer = &chain{id: s.cfg.consumerChainID}
	s.initAndStartConsumer(consumerGenesisJSON)

	s.T().Log("step 5: starting ts-relayer...")
	s.setupTSRelayer()

	s.T().Log("step 5b: declaring the relayer-created clients (owner, both chains)...")
	s.declareConsumerClients("0")

	s.T().Log("step 6: waiting for the consumer to sync its first VSC...")
	s.waitForConsumerSync("0")

	s.T().Log("step 7: funding the consumer's fee pool...")
	s.providerFundConsumerFeePool("0", "20000000"+feeDenom)
}

// validatorIdentity names one of a suite's validators in the forms the
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
func (s *baseTestSuite) validator(i int) validatorIdentity {
	pub, _ := nodeConsensusKey(s.T(), s.providerHome(i))
	consAddr := consAddrFromEd25519PubKey(pub)
	valoper := s.providerValoperForConsAddr(consAddr)
	s.Require().NotEmptyf(valoper, "provider val%d (%s) has no bonded validator", i, consAddr)
	return validatorIdentity{index: i, consAddr: consAddr, valoper: valoper}
}

// pendingSlashesOf counts the consumer's pending downtime slashes that accuse
// the validator at consAddr, its provider consensus address, which is what
// an accusation names even when the validator runs the consumer under an
// assigned key.
func (s *baseTestSuite) pendingSlashesOf(consumerID, consAddr string) int {
	n := 0
	for _, slash := range s.queryPendingDowntimeSlashes(consumerID) {
		accused, err := consAddrFromBase64(slash.ProviderConsAddr)
		if err == nil && accused == consAddr {
			n++
		}
	}
	return n
}
