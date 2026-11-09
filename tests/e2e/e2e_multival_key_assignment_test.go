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
// downtime for the switch. The order matters: the old address misses at
// most the blocks between the assignment and the switch, and the new one
// misses none because the node already runs under it.
func (s *MultiValidatorIntegrationTestSuite) testKeyAssignmentWithNodeRestart() {
	s.Run("a key assignment survives val3's node restarting under the new key", func() {
		const consumerID = "0"
		val3 := s.validator(3)
		pubKeyJSON, assigned, keyDoc := generateConsumerConsensusKeyPair(s.T())
		s.T().Logf("assigning val3 (%s) the consumer key %s", val3.consAddr, assigned)

		s.assignConsumerKey(3, pubKeyJSON)
		s.Require().Equalf(assigned, s.queryValidatorConsumerAddr(consumerID, val3.consAddr),
			"the provider does not report val3's assigned consumer address")

		s.restartConsumerNodeWithKey(3, keyDoc)
		restartHeight, err := s.queryConsumerBlockHeight()
		s.Require().NoError(err, "failed to read the consumer height after restarting val3")

		s.Require().Eventuallyf(func() bool {
			addrs, err := s.consumerValsetConsAddrs()
			return err == nil && slices.Contains(addrs, assigned) && !slices.Contains(addrs, val3.consAddr)
		}, 4*time.Minute, 5*time.Second,
			"the consumer's set never switched val3 from %s to %s", val3.consAddr, assigned)

		switchHeight, err := s.queryConsumerBlockHeight()
		s.Require().NoError(err, "failed to read the consumer height after the switch")
		s.Require().Eventuallyf(func() bool {
			now, err := s.queryConsumerBlockHeight()
			return err == nil && now > switchHeight+1
		}, 90*time.Second, 3*time.Second,
			"the consumer stopped producing blocks after the switch (stuck at height %d)", switchHeight)

		// No accusation may name a window that began after the restart.
		deadline := time.Now().Add(120 * time.Second)
		for time.Now().Before(deadline) {
			s.Require().Zerof(s.pendingSlashesStartingAtOrAfter(consumerID, restartHeight),
				"a downtime accusation names a window that began after val3's restart at height %d", restartHeight)
			time.Sleep(5 * time.Second)
		}

		s.Require().Equalf(phaseLaunched, s.queryProviderConsumerPhase(consumerID),
			"consumer %s left the LAUNCHED phase during the key assignment", consumerID)
	})
}
