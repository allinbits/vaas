package e2e

// validator_identity_helpers_test.go translates between the address forms the
// same validator carries across the two chains: its provider operator
// (valoper) address, its provider consensus (valcons) address, the consumer
// consensus address it validates a consumer under (its own, or an assigned key
// -- see e2e_key_assignment_test.go), and the raw hex CometBFT reports in
// /validators. The key-assignment and downtime-challenge tests both need to
// name one validator in several of these forms at once.

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	sdk "github.com/cosmos/cosmos-sdk/types"
)

// parseInt64 parses a decimal string field from a JSON query response.
func parseInt64(t *testing.T, s string) int64 {
	t.Helper()
	n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil {
		t.Fatalf("failed to parse %q as int64: %v", s, err)
	}
	return n
}

// consAddrFromEd25519PubKey derives a validator's consensus address from its
// ed25519 public key, exactly as CometBFT does (first 20 bytes of the SHA256
// digest), and returns it bech32-encoded. Both apps configure the default
// "cosmos" bech32 prefixes (see app/cmd/{provider,consumer}/main.go), which
// this test binary also uses, so the encoding matches what the chains accept
// and print.
func consAddrFromEd25519PubKey(pubKey []byte) string {
	digest := sha256.Sum256(pubKey)
	return sdk.ConsAddress(digest[:20]).String()
}

// generateConsumerConsensusKey generates a fresh ed25519 keypair and returns
// the public half as an inline Cosmos SDK Any-JSON pubkey blob (the format
// `tx provider assign-consensus-key` expects) together with the consensus
// address it derives. The private half is discarded: the key is only ever
// assigned to a validator that runs no consumer node (see
// e2e_key_assignment_test.go), so nothing needs to sign with it.
func generateConsumerConsensusKey(t *testing.T) (pubKeyJSON, consAddr string) {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("failed to generate ed25519 key: %v", err)
	}
	pubKeyJSON = fmt.Sprintf(`{"@type":"/cosmos.crypto.ed25519.PubKey","key":%q}`,
		base64.StdEncoding.EncodeToString(pub))
	return pubKeyJSON, consAddrFromEd25519PubKey(pub)
}

// consAddrToCometHex converts a bech32 consensus address to the uppercase hex
// form CometBFT's /validators RPC reports.
func consAddrToCometHex(bech32ConsAddr string) (string, error) {
	addr, err := sdk.ConsAddressFromBech32(bech32ConsAddr)
	if err != nil {
		return "", fmt.Errorf("decoding consensus address %q: %w", bech32ConsAddr, err)
	}
	return strings.ToUpper(hex.EncodeToString(addr)), nil
}

// consAddrFromBase64 bech32-encodes a raw consensus address that a proto JSON
// query response carried as base64 bytes (e.g. PendingDowntimeSlash's
// provider_cons_addr).
func consAddrFromBase64(b64 string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return "", fmt.Errorf("base64-decoding consensus address %q: %w", b64, err)
	}
	return sdk.ConsAddress(raw).String(), nil
}

// providerValidatorConsAddr returns the bech32 consensus address of the
// provider validator with the given operator address, failing the test if it
// cannot be resolved.
func (s *IntegrationTestSuite) providerValidatorConsAddr(valoperAddr string) string {
	consAddr, err := s.tryProviderValidatorConsAddr(valoperAddr)
	s.Require().NoErrorf(err, "failed to resolve the consensus address of validator %s", valoperAddr)
	return consAddr
}

// tryProviderValidatorConsAddr derives a provider validator's consensus
// address from the consensus pubkey the staking module stores for it. It
// returns an error instead of failing the test so it can be called from inside
// an Eventually condition.
func (s *IntegrationTestSuite) tryProviderValidatorConsAddr(valoperAddr string) (string, error) {
	stdout, _, err := s.dockerExec(s.providerValRes[0].Container.ID, []string{
		providerBinary, "query", "staking", "validator", valoperAddr,
		"--home", providerHomePath,
		"--output", "json",
	})
	if err != nil {
		return "", fmt.Errorf("querying provider validator %s: %w", valoperAddr, err)
	}

	// Two shapes are tolerated for both the response envelope (bare or wrapped
	// in a "validator" field) and the packed consensus pubkey: the CLI renders
	// the Any as {"type","value"}, while the canonical proto JSON form is
	// {"@type","key"}. Either way the payload is the raw 32-byte ed25519 key.
	type pubKey struct {
		AtType string `json:"@type"`
		Key    string `json:"key"`
		Type   string `json:"type"`
		Value  string `json:"value"`
	}
	var res struct {
		ConsensusPubkey pubKey `json:"consensus_pubkey"`
		Validator       struct {
			ConsensusPubkey pubKey `json:"consensus_pubkey"`
		} `json:"validator"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &res); err != nil {
		return "", fmt.Errorf("decoding staking validator response %q: %w", stdout.String(), err)
	}

	key := ""
	for _, pk := range []pubKey{res.ConsensusPubkey, res.Validator.ConsensusPubkey} {
		if pk.Key != "" {
			key = pk.Key
			break
		}
		if pk.Value != "" {
			key = pk.Value
			break
		}
	}
	if key == "" {
		return "", fmt.Errorf("no consensus pubkey for validator %s: %s", valoperAddr, stdout.String())
	}

	raw, err := base64.StdEncoding.DecodeString(key)
	if err != nil {
		return "", fmt.Errorf("base64-decoding consensus pubkey %q: %w", key, err)
	}
	return consAddrFromEd25519PubKey(raw), nil
}

// providerValoperForConsAddr returns the operator address of the provider
// validator whose consensus address is consAddr, or "" when none matches.
func (s *IntegrationTestSuite) providerValoperForConsAddr(consAddr string) string {
	vals, err := s.queryProviderValidators()
	if err != nil {
		return ""
	}
	for _, v := range vals {
		addr, err := s.tryProviderValidatorConsAddr(v.OperatorAddress)
		if err == nil && addr == consAddr {
			return v.OperatorAddress
		}
	}
	return ""
}

// consumerValsetConsAddrs returns the bech32 consensus addresses of the
// consumer chain's current CometBFT validator set.
func (s *IntegrationTestSuite) consumerValsetConsAddrs() ([]string, error) {
	vals, err := s.queryConsumerNetValidators()
	if err != nil {
		return nil, err
	}
	addrs := make([]string, 0, len(vals))
	for _, v := range vals {
		addrs = append(addrs, v.Address)
	}
	return addrs, nil
}

// consumerValidatorHexAddrsAtHeight returns the consumer chain's validator set
// at a past height as uppercase hex addresses, read from the consumer's own
// CometBFT RPC (the same source a challenger's tooling reads).
func (s *IntegrationTestSuite) consumerValidatorHexAddrsAtHeight(height int64) ([]string, error) {
	body, err := httpGet(fmt.Sprintf("%s/validators?height=%d&per_page=100", s.consumerRPCEndpoint(), height))
	if err != nil {
		return nil, err
	}
	var res struct {
		Result struct {
			Validators []struct {
				Address string `json:"address"`
			} `json:"validators"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &res); err != nil {
		return nil, fmt.Errorf("failed to decode /validators response: %w", err)
	}
	addrs := make([]string, 0, len(res.Result.Validators))
	for _, v := range res.Result.Validators {
		addrs = append(addrs, strings.ToUpper(v.Address))
	}
	return addrs, nil
}

// consumerBlockValidatorHashes returns the validators_hash and
// next_validators_hash of the consumer's block at the given height.
func (s *IntegrationTestSuite) consumerBlockValidatorHashes(height int64) (valsHash, nextValsHash string, err error) {
	body, err := httpGet(fmt.Sprintf("%s/block?height=%d", s.consumerRPCEndpoint(), height))
	if err != nil {
		return "", "", err
	}
	var res struct {
		Result struct {
			Block struct {
				Header struct {
					ValidatorsHash     string `json:"validators_hash"`
					NextValidatorsHash string `json:"next_validators_hash"`
				} `json:"header"`
			} `json:"block"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &res); err != nil {
		return "", "", fmt.Errorf("failed to decode /block response: %w", err)
	}
	h := res.Result.Block.Header
	if h.ValidatorsHash == "" {
		return "", "", fmt.Errorf("no header returned for consumer height %d", height)
	}
	return h.ValidatorsHash, h.NextValidatorsHash, nil
}

// queryTxResult parses a `-o json` broadcast response, asserts it cleared
// CheckTx, then polls until the tx is committed and returns its committed
// (DeliverTx) code and raw log. Unlike requireTxCommitted it does not require
// success: callers assert on the outcome themselves, which is what a test of a
// deliberately-rejected message needs.
func (s *baseTestSuite) queryTxResult(broadcastOut []byte) (code int, rawLog string) {
	var bres struct {
		TxHash string `json:"txhash"`
		Code   int    `json:"code"`
		RawLog string `json:"raw_log"`
	}
	s.Require().NoErrorf(json.Unmarshal(broadcastOut, &bres),
		"decode broadcast response: %s", string(broadcastOut))
	s.Require().Equalf(0, bres.Code, "tx rejected at CheckTx: %s", bres.RawLog)
	s.Require().NotEmptyf(bres.TxHash, "broadcast returned empty txhash: %s", string(broadcastOut))

	var lastOut string
	s.Require().Eventuallyf(func() bool {
		out, _, qerr := s.dockerExec(s.providerValRes[0].Container.ID, []string{
			providerBinary, "query", "tx", bres.TxHash,
			"--home", providerHomePath,
			"--output", "json",
		})
		if qerr != nil || out.Len() == 0 {
			return false
		}
		lastOut = out.String()
		var qres struct {
			Code   int    `json:"code"`
			RawLog string `json:"raw_log"`
		}
		if json.Unmarshal(out.Bytes(), &qres) != nil {
			return false
		}
		code, rawLog = qres.Code, qres.RawLog
		return true
	}, 60*time.Second, 2*time.Second,
		"tx %s was not committed in time; last query output: %s", bres.TxHash, lastOut)

	return code, rawLog
}
