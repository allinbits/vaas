package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/ory/dockertest/v3"
)

// testNodeFeeFloor proves the node fee floor end to end on the photon-fees
// consumer. A validator of such a chain launches with zero minimum-gas-prices
// (the voucher denom is not knowable before the provider client is pinned)
// and raises the floor to the voucher denom once it is. This scenario does
// exactly that, restarting the suite's only consumer validator with a floor
// of 0.01 voucher per gas unit, and then checks the floor the way
// InfrastructureExemptTxFeeChecker applies it:
//
//  1. a user transaction paying less than the floor in the voucher denom is
//     rejected at CheckTx, which also proves the floor is live on the
//     restarted node;
//  2. a user transaction paying exactly the floor commits;
//  3. the relayer, which pays the consumer in the native denom and never in
//     vouchers, keeps delivering: the VSC flow resumes and a fresh photon
//     transfer lands, something no relayer transaction could do through a
//     floor that priced /ibc.core.* traffic.
//
// The floor stays in place for the rest of the suite, so every later
// scenario, the consumer genesis round-trip included (it copies app.toml),
// runs its relayer traffic through it.
//
// Runs right after testPhotonFeeEnforcement, which leaves the user holding
// vouchers and the policy enforcing.
func (s *IntegrationTestSuite) testNodeFeeFloor() {
	user := s.consumerUserBech32()
	voucherDenom := photonVoucherDenom(s.consumerPinnedProviderClientID())

	// 0.01 voucher per gas unit prices the 300000-gas sends below at 3000.
	const gasLimit = 300000
	const floorFee = gasLimit / 100
	s.restartConsumerWithMinGasPrices("0.01" + voucherDenom)

	// Guard: the msg filter's debt and staleness gates sit before the fee
	// decorators, so wait for normal mode or their errors mask the floor. The
	// fee-less dry-run is policy-neutral: simulation skips the fee checker.
	s.Require().Eventuallyf(func() bool {
		out, err := s.consumerBankSendDryRun()
		if err != nil {
			return false
		}
		return !strings.Contains(out, "consumer chain is in debt") &&
			!strings.Contains(out, "stale validator set")
	}, 2*time.Minute, 5*time.Second,
		"consumer never reached normal mode after the restart; the floor cannot be observed")

	bankSend := func(fee string) ([]byte, string, error) {
		stdout, stderr, err := s.dockerExec(s.consumerValRes[0].Container.ID, []string{
			consumerBinary, "tx", "bank", "send", user, user, "1" + bondDenom,
			"--from", "user",
			"--home", consumerHomePath,
			"--keyring-backend", "test",
			"--chain-id", s.cfg.consumerChainID,
			"--gas", strconv.Itoa(gasLimit),
			"--fees", fee,
			"--broadcast-mode", "sync",
			"-y",
			"-o", "json",
		})
		return stdout.Bytes(), stderr.String(), err
	}

	s.Run("a voucher fee below the floor is rejected at CheckTx", func() {
		stdout, stderr, err := bankSend(fmt.Sprintf("%d%s", floorFee-1, voucherDenom))
		s.Require().NoError(err, "broadcast failed to run: %s", stderr)

		var res struct {
			Code   uint32 `json:"code"`
			RawLog string `json:"raw_log"`
		}
		s.Require().NoError(json.Unmarshal(stdout, &res),
			"failed to decode broadcast response: %s", string(stdout))
		s.Require().NotZero(res.Code, "a fee below the floor must be rejected, got: %s", string(stdout))
		s.Require().Contains(res.RawLog, "insufficient fee",
			"rejection must come from the fee floor, got: %s", res.RawLog)
		s.Require().Contains(res.RawLog, voucherDenom,
			"the floor must be priced in the voucher denom, got: %s", res.RawLog)
	})

	s.Run("a voucher fee at the floor commits", func() {
		stdout, stderr, err := bankSend(fmt.Sprintf("%d%s", floorFee, voucherDenom))
		s.Require().NoError(err, "broadcast failed to run: %s", stderr)
		s.requireTxCommittedOn(s.consumerValRes[0].Container.ID, consumerBinary, consumerHomePath, stdout)
	})

	s.Run("relayer traffic is exempt from the floor", func() {
		// VSC packets reach the consumer in relayer transactions paying the
		// native denom: the flow resuming past the restart means they are
		// still admitted.
		s.waitForConsumerSync("0")

		// The sharper proof: the packet delivering a fresh transfer must pass
		// the floored mempool for the vouchers to arrive.
		before := s.consumerVoucherBalance(user, voucherDenom)
		s.transferPhotonToConsumer(user, "500000")
		s.Require().Eventuallyf(func() bool {
			return s.consumerVoucherBalance(user, voucherDenom) > before
		}, 5*time.Minute, 5*time.Second,
			"photon voucher transfer never arrived through the floored consumer mempool")
	})
}

// consumerPinnedProviderClientID returns the provider client the consumer has
// pinned, the client the photon voucher denom is derived from. provider-info
// reports it under the consumer's own entry: it is the client the consumer
// holds, pointing at the provider.
func (s *IntegrationTestSuite) consumerPinnedProviderClientID() string {
	out, err := s.queryConsumerProviderInfo()
	s.Require().NoError(err, "failed to query provider info from the consumer")
	var res struct {
		Consumer struct {
			ClientID string `json:"clientID"`
		} `json:"consumer"`
	}
	s.Require().NoErrorf(json.Unmarshal([]byte(out), &res), "failed to decode provider info: %s", out)
	s.Require().NotEmptyf(res.Consumer.ClientID, "consumer reports no pinned provider client: %s", out)
	return res.Consumer.ClientID
}

// consumerVoucherBalance returns the user's balance in the given denom on the
// consumer, as a number; a query failure reads as zero so an Eventually loop
// simply retries.
func (s *IntegrationTestSuite) consumerVoucherBalance(user, denom string) uint64 {
	amount, err := s.queryBalance(s.consumerRESTEndpoint(), user, denom)
	if err != nil {
		return 0
	}
	n, err := strconv.ParseUint(amount, 10, 64)
	if err != nil {
		return 0
	}
	return n
}

// restartConsumerWithMinGasPrices stops the suite's consumer validator,
// rewrites minimum-gas-prices in its app.toml, and starts it again on the same
// data directory, container name and ports, the way an operator raises a
// node's fee floor. The chain halts for the duration (it is the only
// validator) and resumes from its own state; the relayer is never touched and
// reconnects on its own, as it does across the consumer genesis round-trip.
func (s *IntegrationTestSuite) restartConsumerWithMinGasPrices(minGasPrices string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	preHeight, err := s.queryConsumerBlockHeight()
	s.Require().NoError(err, "failed to query the consumer height before the restart")

	old := s.consumerValRes[0]
	s.T().Logf("stopping the consumer to set minimum-gas-prices = %q...", minGasPrices)
	s.Require().NoError(s.dkrPool.Client.StopContainer(old.Container.ID, 30),
		"failed to stop the consumer container")

	s.patchAppTomlMinGasPrices(filepath.Join(s.consumer.dataDir, "config", "app.toml"), minGasPrices)

	// The replacement reuses the old container's name and port bindings, so
	// the old one goes first.
	s.Require().NoError(s.dkrPool.Purge(old), "failed to purge the stopped consumer container")
	s.consumerValRes = []*dockertest.Resource{s.startRestartedConsumer(s.consumer.dataDir)}

	s.Require().NoError(
		s.waitForChainHeight(ctx, "http://localhost:"+s.cfg.consumerRPCPort, preHeight+2),
		"consumer did not resume producing blocks after the restart")
	s.T().Logf("consumer restarted with minimum-gas-prices = %q", minGasPrices)
}

var minGasPricesLineRegexp = regexp.MustCompile(`(?m)^minimum-gas-prices = .*$`)

// patchAppTomlMinGasPrices rewrites the minimum-gas-prices line of an app.toml
// in place.
func (s *IntegrationTestSuite) patchAppTomlMinGasPrices(path, minGasPrices string) {
	bz, err := os.ReadFile(path)
	s.Require().NoError(err, "failed to read app.toml")
	s.Require().Truef(minGasPricesLineRegexp.Match(bz), "%s has no minimum-gas-prices line", path)
	out := minGasPricesLineRegexp.ReplaceAll(bz, []byte(fmt.Sprintf("minimum-gas-prices = %q", minGasPrices)))
	s.Require().NoError(os.WriteFile(path, out, 0o600), "failed to write app.toml")
}
