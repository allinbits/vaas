package e2e

import (
	"encoding/json"
	"fmt"
	"strconv"

	rpchttp "github.com/cometbft/cometbft/rpc/client/http"

	cmtservice "github.com/cosmos/cosmos-sdk/client/grpc/cmtservice"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
)

// queryProviderConsumerPhase returns the phase string for a given consumer ID
// from the provider chain (e.g. phaseLaunched).
// Consumer phases as the provider's consumer-chain query prints them.
const (
	phaseLaunched = "CONSUMER_PHASE_LAUNCHED"
	phasePaused   = "CONSUMER_PHASE_PAUSED"
	phaseStopped  = "CONSUMER_PHASE_STOPPED"
	phaseDeleted  = "CONSUMER_PHASE_DELETED"
)

func (s *baseTestSuite) queryProviderConsumerPhase(consumerID string) string { //nolint:unparam // the suite drives one consumer; the helper stays generic
	stdout, _, err := s.dockerExec(s.providerValRes[0].Container.ID, []string{
		providerBinary, "query", "vaasprovider", "consumer-chain", consumerID,
		"--home", providerHomePath,
		"--output", "json",
	})
	if err != nil {
		s.T().Logf("queryProviderConsumerPhase(%s): exec error: %v", consumerID, err)
		return ""
	}
	var res struct {
		Phase string `json:"phase"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &res); err != nil {
		s.T().Logf("queryProviderConsumerPhase(%s): decode error: %v (raw: %s)", consumerID, err, stdout.String())
		return ""
	}
	return res.Phase
}

// providerRESTEndpoint returns the provider chain's REST HTTP endpoint using the Docker-assigned host port.
func (s *baseTestSuite) providerRESTEndpoint() string {
	return fmt.Sprintf("http://%s", s.providerValRes[0].GetHostPort("1317/tcp"))
}

// consumerRESTEndpoint returns the consumer chain's REST HTTP endpoint using the Docker-assigned host port.
func (s *baseTestSuite) consumerRESTEndpoint() string {
	return fmt.Sprintf("http://%s", s.consumerValRes[0].GetHostPort("1317/tcp"))
}

// providerRPCEndpoint returns the provider chain's RPC HTTP endpoint using the Docker-assigned host port.
func (s *baseTestSuite) providerRPCEndpoint() string {
	return fmt.Sprintf("http://%s", s.providerValRes[0].GetHostPort("26657/tcp"))
}

// consumerRPCEndpoint returns the consumer chain's RPC HTTP endpoint using the Docker-assigned host port.
func (s *baseTestSuite) consumerRPCEndpoint() string {
	return fmt.Sprintf("http://%s", s.consumerValRes[0].GetHostPort("26657/tcp"))
}

// providerCometClient returns a CometBFT RPC client on provider val0, for
// what the chain's REST API does not serve: past validator sets, block
// commits and the evidence a block carries, and evidence submission.
func (s *baseTestSuite) providerCometClient() *rpchttp.HTTP {
	if s.providerComet == nil {
		client, err := rpchttp.New(s.providerRPCEndpoint(), "/websocket")
		s.Require().NoError(err, "failed to create the provider's CometBFT RPC client")
		s.providerComet = client
	}
	return s.providerComet
}

// queryProviderBlockHeight returns the current block height of the provider chain.
func (s *baseTestSuite) queryProviderBlockHeight() (int64, error) {
	return queryBlockHeight(s.providerRPCEndpoint())
}

// queryConsumerBlockHeight returns the current block height of the consumer chain.
func (s *baseTestSuite) queryConsumerBlockHeight() (int64, error) {
	return queryBlockHeight(s.consumerRPCEndpoint())
}

// queryNetValidators queries the current consensus validator set via the REST API.
func (s *baseTestSuite) queryNetValidators(restEndpoint string) ([]*cmtservice.Validator, error) {
	body, err := httpGet(fmt.Sprintf("%s/cosmos/base/tendermint/v1beta1/validatorsets/latest", restEndpoint))
	if err != nil {
		return nil, fmt.Errorf("failed to execute HTTP request: %w", err)
	}

	var res cmtservice.GetLatestValidatorSetResponse
	if err := s.cdc.UnmarshalJSON(body, &res); err != nil {
		return nil, err
	}
	return res.Validators, nil
}

// queryProviderNetValidators queries the provider chain for the latest consensus validator set.
func (s *baseTestSuite) queryProviderNetValidators() ([]*cmtservice.Validator, error) {
	return s.queryNetValidators(s.providerRESTEndpoint())
}

// queryConsumerNetValidators queries the consumer chain for the latest consensus validator set.
func (s *baseTestSuite) queryConsumerNetValidators() ([]*cmtservice.Validator, error) {
	return s.queryNetValidators(s.consumerRESTEndpoint())
}

// queryValidators queries the staking validator set via the REST API.
func (s *baseTestSuite) queryValidators(restEndpoint string) ([]stakingtypes.Validator, error) {
	body, err := httpGet(fmt.Sprintf("%s/cosmos/staking/v1beta1/validators", restEndpoint))
	if err != nil {
		return nil, fmt.Errorf("failed to execute HTTP request: %w", err)
	}

	var res stakingtypes.QueryValidatorsResponse
	if err := s.cdc.UnmarshalJSON(body, &res); err != nil {
		return nil, err
	}
	return res.Validators, nil
}

// queryProviderValidators queries the provider for its staking validator set via the REST API.
func (s *baseTestSuite) queryProviderValidators() ([]stakingtypes.Validator, error) {
	return s.queryValidators(s.providerRESTEndpoint())
}

// queryConsumerProviderInfo queries the consumer chain for its provider info
// using Docker exec (since the consumer REST API may not be accessible for
// this specific query).
func (s *baseTestSuite) queryConsumerProviderInfo() (string, error) {
	stdout, _, err := s.dockerExec(s.consumerValRes[0].Container.ID, []string{
		consumerBinary, "query", "vaasconsumer", "provider-info",
		"--home", consumerHomePath,
		"--output", "json",
	})
	if err != nil {
		return "", err
	}
	return stdout.String(), nil
}

// consumerProviderClientID returns the id of the consumer's pinned IBC client
// of the provider, the destination of the provider's VSC packets.
func (s *baseTestSuite) consumerProviderClientID() string {
	info, err := s.queryConsumerProviderInfo()
	s.Require().NoError(err, "failed to query the consumer's provider info")
	var res struct {
		Consumer struct {
			ClientID string `json:"clientID"`
		} `json:"consumer"`
	}
	s.Require().NoErrorf(json.Unmarshal([]byte(info), &res), "failed to decode the consumer's provider info: %s", info)
	s.Require().NotEmpty(res.Consumer.ClientID, "the consumer has no pinned provider client")
	return res.Consumer.ClientID
}

// queryConsumerChainID returns the chain id the provider has a consumer
// registered under.
func (s *baseTestSuite) queryConsumerChainID(consumerID string) string {
	stdout, _, err := s.dockerExec(s.providerValRes[0].Container.ID, []string{
		providerBinary, "query", "vaasprovider", "consumer-chain", consumerID,
		"--home", providerHomePath,
		"--output", "json",
	})
	s.Require().NoErrorf(err, "failed to query consumer-chain %s", consumerID)
	var res struct {
		ChainID string `json:"chain_id"`
	}
	s.Require().NoErrorf(json.Unmarshal(stdout.Bytes(), &res), "failed to decode the consumer-chain response: %s", stdout.String())
	return res.ChainID
}

// previousChainIDJSON is one chain id a governance rename retired, with the
// declared client's height at the rename, as the consumer-chain query lists
// them.
type previousChainIDJSON struct {
	ChainID  string `json:"chain_id"`
	Boundary struct {
		RevisionNumber string `json:"revision_number"`
		RevisionHeight string `json:"revision_height"`
	} `json:"boundary"`
}

// queryConsumerPreviousChainIDs returns the chain ids the consumer carried
// before its governance renames, with the boundary of each.
func (s *baseTestSuite) queryConsumerPreviousChainIDs(consumerID string) []previousChainIDJSON {
	stdout, _, err := s.dockerExec(s.providerValRes[0].Container.ID, []string{
		providerBinary, "query", "vaasprovider", "consumer-chain", consumerID,
		"--home", providerHomePath,
		"--output", "json",
	})
	s.Require().NoErrorf(err, "failed to query consumer-chain %s", consumerID)
	var res struct {
		PreviousChainIDs []previousChainIDJSON `json:"previous_chain_ids"`
	}
	s.Require().NoErrorf(json.Unmarshal(stdout.Bytes(), &res), "failed to decode the consumer-chain response: %s", stdout.String())
	return res.PreviousChainIDs
}

// nextSequenceSend returns the sequence the provider's next IBC v2 packet over
// the client takes; it moves exactly when the provider sends a packet over it.
func (s *baseTestSuite) nextSequenceSend(clientID string) uint64 {
	stdout, stderr, err := s.dockerExec(s.providerValRes[0].Container.ID, []string{
		providerBinary, "query", "ibc", "channelv2", "next-sequence-send", clientID,
		"--prove=false",
		"--home", providerHomePath,
		"--output", "json",
	})
	s.Require().NoErrorf(err, "failed to query the next send sequence of client %s: %s", clientID, stderr.String())
	var res struct {
		NextSequenceSend string `json:"next_sequence_send"`
	}
	s.Require().NoErrorf(json.Unmarshal(stdout.Bytes(), &res), "failed to decode the next send sequence of client %s: %s", clientID, stdout.String())
	sequence, err := strconv.ParseUint(res.NextSequenceSend, 10, 64)
	s.Require().NoErrorf(err, "next send sequence of client %s: %q", clientID, res.NextSequenceSend)
	return sequence
}

// blockEvent is an event a block emitted outside any transaction, as the
// chain's block results report it: from its BeginBlock, where the provider
// pauses a consumer whose client tracks another chain id, or its EndBlock,
// where the provider's epoch send runs and a passed proposal executes.
type blockEvent struct {
	Type       string
	Attributes map[string]string
}

// providerBlockEvents returns the events the provider's block at height
// emitted outside any transaction.
func (s *baseTestSuite) providerBlockEvents(height int64) []blockEvent {
	body, err := httpGet(fmt.Sprintf("%s/block_results?height=%d", s.providerRPCEndpoint(), height))
	s.Require().NoErrorf(err, "failed to read the provider's block results at height %d", height)
	var res struct {
		Result struct {
			FinalizeBlockEvents []struct {
				Type       string `json:"type"`
				Attributes []struct {
					Key   string `json:"key"`
					Value string `json:"value"`
				} `json:"attributes"`
			} `json:"finalize_block_events"`
		} `json:"result"`
	}
	s.Require().NoErrorf(json.Unmarshal(body, &res), "failed to decode the provider's block results at height %d: %s", height, body)
	events := make([]blockEvent, 0, len(res.Result.FinalizeBlockEvents))
	for _, ev := range res.Result.FinalizeBlockEvents {
		attributes := make(map[string]string, len(ev.Attributes))
		for _, a := range ev.Attributes {
			attributes[a.Key] = a.Value
		}
		events = append(events, blockEvent{Type: ev.Type, Attributes: attributes})
	}
	return events
}

// providerBlockEventHeights returns, in order, the height of every event of
// the given type whose attributes match every entry of want that the
// provider's blocks from height from to height to inclusive emitted outside
// any transaction; a block emitting several lists once per event.
func (s *baseTestSuite) providerBlockEventHeights(from, to int64, eventType string, want map[string]string) []int64 {
	var heights []int64
	for height := from; height <= to; height++ {
		for _, ev := range s.providerBlockEvents(height) {
			if ev.Type == eventType && blockEventMatches(ev, want) {
				heights = append(heights, height)
			}
		}
	}
	return heights
}

// blockEventMatches reports whether every attribute in want is on the event
// with that value.
func blockEventMatches(ev blockEvent, want map[string]string) bool {
	for key, value := range want {
		if ev.Attributes[key] != value {
			return false
		}
	}
	return true
}

// queryProviderConsumerChains queries the provider for registered consumer chains.
func (s *baseTestSuite) queryProviderConsumerChains() (string, error) {
	stdout, _, err := s.dockerExec(s.providerValRes[0].Container.ID, []string{
		providerBinary, "query", "vaasprovider", "list-consumer-chains",
		"--home", providerHomePath,
		"--output", "json",
	})
	if err != nil {
		return "", err
	}
	return stdout.String(), nil
}

// queryBalance queries an account balance via the REST API.
func (s *baseTestSuite) queryBalance(restEndpoint, address, denom string) (string, error) {
	body, err := httpGet(fmt.Sprintf("%s/cosmos/bank/v1beta1/balances/%s", restEndpoint, address))
	if err != nil {
		return "", fmt.Errorf("failed to execute HTTP request: %w", err)
	}

	var res banktypes.QueryAllBalancesResponse
	if err := s.cdc.UnmarshalJSON(body, &res); err != nil {
		return "", err
	}

	for _, coin := range res.Balances {
		if coin.Denom == denom {
			return coin.Amount.String(), nil
		}
	}
	return "0", nil
}
