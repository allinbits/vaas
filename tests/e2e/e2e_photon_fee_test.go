package e2e

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// photonVoucherDenom derives the ICS-20 voucher denom for one-hop uphoton
// received over the given consumer-side client:
// ibc/UPPERHEX(SHA256("transfer/<clientID>/uphoton")). It mirrors the
// consumer ante's ExpectedPhotonDenom (the e2e module does not depend on the
// chain module; the unit suite pins the same wire format independently).
func photonVoucherDenom(clientID string) string {
	sum := sha256.Sum256([]byte("transfer/" + clientID + "/" + feeDenom))
	return "ibc/" + strings.ToUpper(hex.EncodeToString(sum[:]))
}

// testPhotonFeeEnforcement proves the photon-only fee policy end to end. The
// suite's consumer runs with photon_fees_enabled from genesis, so by this
// point (pin routable, VSC flowing) the policy is enforcing:
//
//  1. a user transaction paying the native denom is rejected by the ante;
//  2. an ICS-20 v2 transfer bridges uphoton from the provider while
//     enforcement is on, proving the relayer's exempt traffic still delivers
//     packets (without the infrastructure exemption this transfer could
//     never arrive and the policy would deadlock the chain);
//  3. a user transaction paying the bridged voucher commits.
func (s *IntegrationTestSuite) testPhotonFeeEnforcement() {
	user := s.consumerUserBech32()

	// Guard: the msg filter's debt and staleness gates sit before the photon
	// decorator, so wait for normal mode or their errors mask the fee policy.
	// The fee-less dry-run is policy-neutral (simulation permits empty fees).
	s.Require().Eventuallyf(func() bool {
		out, err := s.consumerBankSendDryRun()
		if err != nil {
			return false
		}
		return !strings.Contains(out, "consumer chain is in debt") &&
			!strings.Contains(out, "stale validator set")
	}, 2*time.Minute, 5*time.Second,
		"consumer never reached normal mode; the fee policy cannot be observed")

	s.Run("native-denom fees are rejected while enforcing", func() {
		stdout, stderr, err := s.dockerExec(s.consumerValRes[0].Container.ID, []string{
			consumerBinary, "tx", "bank", "send", user, user, "1" + bondDenom,
			"--from", "user",
			"--home", consumerHomePath,
			"--keyring-backend", "test",
			"--chain-id", s.cfg.consumerChainID,
			"--gas", "300000",
			"--fees", "5000" + bondDenom,
			"--broadcast-mode", "sync",
			"-y",
			"-o", "json",
		})
		s.Require().NoError(err, "broadcast failed to run: %s", stderr.String())

		var res struct {
			Code   uint32 `json:"code"`
			RawLog string `json:"raw_log"`
		}
		s.Require().NoError(json.Unmarshal(stdout.Bytes(), &res),
			"failed to decode broadcast response: %s", stdout.String())
		s.Require().NotZero(res.Code, "a native-denom fee must be rejected, got: %s", stdout.String())
		s.Require().Contains(res.RawLog, "not the photon denom",
			"rejection must come from the photon fee ante, got: %s", res.RawLog)
	})

	consumerClientID := s.tendermintClientTracking(
		s.consumerValRes[0].Container.ID, consumerBinary, consumerHomePath, s.cfg.providerChainID)
	voucherDenom := photonVoucherDenom(consumerClientID)

	s.Run("bridged photon arrives while enforcement is on", func() {
		providerClientID := s.tendermintClientTracking(
			s.providerValRes[0].Container.ID, providerBinary, providerHomePath, s.cfg.consumerChainID)

		stdout, stderr, err := s.dockerExec(s.providerValRes[0].Container.ID, []string{
			providerBinary, "tx", "ibc-transfer", "transfer", "transfer", providerClientID,
			user, "500000" + feeDenom,
			"--from", "val",
			"--home", providerHomePath,
			"--keyring-backend", "test",
			"--chain-id", s.cfg.providerChainID,
			// IBC v2 packets carry only a timestamp timeout, in unix seconds.
			// The CLI's relative-timeout path emits nanoseconds (the v1 unit),
			// which channelv2 reads as seconds and rejects as beyond its 24h
			// cap, so the timeout must be passed as an absolute seconds value.
			"--absolute-timeouts",
			"--packet-timeout-height", "0-0",
			"--packet-timeout-timestamp", fmt.Sprintf("%d", time.Now().Add(10*time.Minute).Unix()),
			"--gas", "300000",
			"--fees", "10000" + bondDenom,
			"--broadcast-mode", "sync",
			"-y",
			"-o", "json",
		})
		s.Require().NoError(err, "ibc-transfer failed to run: %s", stderr.String())
		s.requireTxCommittedOn(s.providerValRes[0].Container.ID, providerBinary, providerHomePath, stdout.Bytes())

		s.Require().Eventuallyf(func() bool {
			amount, err := s.queryBalance(s.consumerRESTEndpoint(), user, voucherDenom)
			if err != nil {
				return false
			}
			return amount != "0"
		}, 5*time.Minute, 5*time.Second,
			"photon voucher %s never arrived on the consumer", voucherDenom)
	})

	s.Run("voucher-denom fees commit", func() {
		stdout, stderr, err := s.dockerExec(s.consumerValRes[0].Container.ID, []string{
			consumerBinary, "tx", "bank", "send", user, user, "1" + bondDenom,
			"--from", "user",
			"--home", consumerHomePath,
			"--keyring-backend", "test",
			"--chain-id", s.cfg.consumerChainID,
			"--gas", "300000",
			"--fees", "5000" + voucherDenom,
			"--broadcast-mode", "sync",
			"-y",
			"-o", "json",
		})
		s.Require().NoError(err, "broadcast failed to run: %s", stderr.String())
		s.requireTxCommittedOn(s.consumerValRes[0].Container.ID, consumerBinary, consumerHomePath, stdout.Bytes())
	})
}
