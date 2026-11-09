package e2e

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// generateConsumerConsensusKeyPair is generateConsumerConsensusKey keeping
// the private half: besides the assignable pubkey blob and the consensus
// address, it returns the priv_validator_key.json document a consumer node
// runs under to sign with the key.
func generateConsumerConsensusKeyPair(t *testing.T) (pubKeyJSON, consAddr string, privValidatorKeyJSON []byte) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("failed to generate ed25519 key: %v", err)
	}
	pubKeyJSON = fmt.Sprintf(`{"@type":"/cosmos.crypto.ed25519.PubKey","key":%q}`,
		base64.StdEncoding.EncodeToString(pub))
	digest := sha256.Sum256(pub)
	privValidatorKeyJSON, err = json.Marshal(map[string]any{
		"address": strings.ToUpper(hex.EncodeToString(digest[:20])),
		"pub_key": map[string]string{
			"type":  "tendermint/PubKeyEd25519",
			"value": base64.StdEncoding.EncodeToString(pub),
		},
		"priv_key": map[string]string{
			"type":  "tendermint/PrivKeyEd25519",
			"value": base64.StdEncoding.EncodeToString(priv),
		},
	})
	if err != nil {
		t.Fatalf("failed to encode priv_validator_key.json: %v", err)
	}
	return pubKeyJSON, consAddrFromEd25519PubKey(pub), privValidatorKeyJSON
}

// assignConsumerKey assigns pubKeyJSON as validator i's consumer key on the
// consumer, signed by that validator's operator key in its own provider
// node, and waits for the transaction to commit.
func (s *MultiValidatorIntegrationTestSuite) assignConsumerKey(i int, pubKeyJSON string) {
	container := s.providerValRes[i].Container.ID
	stdout, stderr, err := s.dockerExec(container, []string{
		providerBinary, "tx", "vaasprovider", "assign-consensus-key", "0", pubKeyJSON,
		"--from", "val",
		"--home", providerHomePath,
		"--keyring-backend", "test",
		"--chain-id", s.cfg.providerChainID,
		"--gas", "auto",
		"--gas-adjustment", "1.5",
		"--fees", "10000" + bondDenom,
		"-y",
		"-o", "json",
	})
	s.Require().NoErrorf(err, "failed to broadcast val%d's key assignment: stderr=%s", i, stderr.String())
	s.requireTxCommittedOn(container, providerBinary, providerHomePath, stdout.Bytes())
}

// restartConsumerNodeWithKey stops consumer node i, installs the given
// priv_validator_key.json, and starts the node again under it.
func (s *MultiValidatorIntegrationTestSuite) restartConsumerNodeWithKey(i int, privValidatorKeyJSON []byte) {
	s.stopConsumerNode(i)
	keyPath := filepath.Join(s.consumerHome(i), "config", "priv_validator_key.json")
	s.Require().NoErrorf(os.WriteFile(keyPath, privValidatorKeyJSON, 0o644), //nolint:gosec // the node runs as nonroot in its container and must read this bind-mounted file
		"failed to install consumer val%d's new consensus key", i)
	s.startConsumerNode(i)
}

// testKeyAssignmentWithNodeRestart: val3 assigns a consumer key the test
// generated and restarts its consumer node under it right after the
// assignment commits; the next epoch's VSC switches the consumer's set to
// the assigned address, the chain never stops, and val3 is accused of no
// downtime for the switch. What the scenario measures is the gap: the old
// address misses the blocks between the assignment and the switch, the new
// one the blocks the node is down for, and both stay under the window's
// missed-block bar only if the stop and the start are quick. An accusation
// against val3 would show as a pending slash naming its provider address,
// then as a drop of its stake; neither may appear.
func (s *MultiValidatorIntegrationTestSuite) testKeyAssignmentWithNodeRestart() {
	s.Run("a key assignment survives val3's node restarting under the new key", func() {
		const consumerID = "0"
		val3 := s.validator(3)
		tokensBefore, err := s.getProviderValidatorTokensByAddr(val3.valoper)
		s.Require().NoError(err, "failed to read val3's tokens before the key assignment")
		pubKeyJSON, assigned, keyDoc := generateConsumerConsensusKeyPair(s.T())
		s.T().Logf("assigning val3 (%s) the consumer key %s", val3.consAddr, assigned)

		s.assignConsumerKey(3, pubKeyJSON)
		s.Require().Equalf(assigned, s.queryValidatorConsumerAddr(consumerID, val3.consAddr),
			"the provider does not report val3's assigned consumer address")

		s.restartConsumerNodeWithKey(3, keyDoc)

		s.Require().Eventuallyf(func() bool {
			addrs, err := s.consumerValsetConsAddrs()
			return err == nil && slices.Contains(addrs, assigned) && !slices.Contains(addrs, val3.consAddr)
		}, 4*time.Minute, 5*time.Second,
			"the consumer's set never switched val3 from %s to %s", val3.consAddr, assigned)

		// Over the next windows no accusation names val3, and the chain keeps
		// producing blocks under the switched set.
		loopStart, err := s.queryConsumerBlockHeight()
		s.Require().NoError(err, "failed to read the consumer height after the switch")
		deadline := time.Now().Add(120 * time.Second)
		for time.Now().Before(deadline) {
			s.Require().Zerof(s.pendingSlashesOf(consumerID, val3.consAddr),
				"a downtime accusation names val3 (%s) after its key assignment", val3.consAddr)
			time.Sleep(5 * time.Second)
		}
		loopEnd, err := s.queryConsumerBlockHeight()
		s.Require().NoError(err, "failed to read the consumer height after the watch")
		s.Require().Greaterf(loopEnd, loopStart+30,
			"the consumer produced too few blocks under the switched set (%d to %d in two minutes)", loopStart, loopEnd)

		tokensAfter, err := s.getProviderValidatorTokensByAddr(val3.valoper)
		s.Require().NoError(err, "failed to read val3's tokens after the key assignment")
		s.Require().Truef(tokensAfter.Equal(tokensBefore),
			"val3 was slashed across the key assignment: %s before, %s after", tokensBefore, tokensAfter)

		s.Require().Equalf(phaseLaunched, s.queryProviderConsumerPhase(consumerID),
			"consumer %s left the LAUNCHED phase during the key assignment", consumerID)
	})
}
