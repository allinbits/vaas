package e2e

import (
	"strconv"
	"time"

	"cosmossdk.io/math"

	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
)

// pendingSlashesStartingAtOrAfter counts the consumer's pending downtime
// slashes whose window began at or after height: the accusations a
// validator signing from that height on would have to answer for. Windows
// that began earlier may still arrive after a node restart and are not
// counted.
func (s *MultiValidatorIntegrationTestSuite) pendingSlashesStartingAtOrAfter(consumerID string, height int64) int {
	n := 0
	for _, slash := range s.queryPendingDowntimeSlashes(consumerID) {
		start, err := strconv.ParseInt(slash.WindowStartHeight, 10, 64)
		if err == nil && start >= height {
			n++
		}
	}
	return n
}

// testOfflineValidatorIsSlashedAndRecovers: val2's consumer node stops while
// the other three keep the consumer live; its real missed blocks become
// downtime evidence, the provider slashes it without jailing it, the
// consumer sees its reduced power, and once the node is back val2 signs
// again and is accused no more.
func (s *MultiValidatorIntegrationTestSuite) testOfflineValidatorIsSlashedAndRecovers() {
	const consumerID = "0"
	val2 := s.validator(2)
	var tokensBefore math.Int

	s.Run("the consumer stays live without val2's node", func() {
		var err error
		tokensBefore, err = s.getProviderValidatorTokensByAddr(val2.valoper)
		s.Require().NoError(err, "failed to read val2's tokens before the outage")
		s.Require().True(tokensBefore.IsPositive(), "val2 should have a positive stake before the outage")

		s.T().Log("stopping val2's consumer node...")
		s.stopConsumerNode(2)
		h, err := s.queryConsumerBlockHeight()
		s.Require().NoError(err, "failed to read the consumer height after stopping val2")
		s.Require().Eventuallyf(func() bool {
			now, err := s.queryConsumerBlockHeight()
			return err == nil && now > h+5
		}, 90*time.Second, 3*time.Second,
			"the consumer stopped producing blocks without val2 (stuck at height %d)", h)
	})

	s.Run("the provider queues and executes a downtime slash for val2", func() {
		s.Require().False(tokensBefore.IsNil(), "val2's stake before the outage was not read")

		s.T().Log("waiting for the provider to queue a pending downtime slash against val2...")
		s.Require().Eventuallyf(func() bool {
			return s.pendingSlashesOf(consumerID, val2.consAddr) > 0
		}, 6*time.Minute, 5*time.Second,
			"the provider never queued a pending downtime slash against val2 on consumer %s", consumerID)

		// The challenge window is 120s; the execution lands at the sweep after it.
		s.T().Log("waiting for the challenge window to mature and the slash to execute...")
		s.Require().Eventuallyf(func() bool {
			tokens, err := s.getProviderValidatorTokensByAddr(val2.valoper)
			return err == nil && tokens.LT(tokensBefore)
		}, 4*time.Minute, 5*time.Second,
			"val2's stake was never reduced by the matured downtime slash")

		vals, err := s.queryProviderValidators()
		s.Require().NoError(err, "failed to query the provider's validators")
		found := false
		for _, v := range vals {
			if v.OperatorAddress != val2.valoper {
				continue
			}
			found = true
			s.Require().Equal(stakingtypes.Bonded, v.Status, "a downtime slash must leave val2 bonded")
			s.Require().False(v.Jailed, "a downtime slash must not jail val2")
		}
		s.Require().Truef(found, "val2 (%s) disappeared from the provider's validators", val2.valoper)
	})

	s.Run("the consumer sees val2's reduced power", func() {
		s.Require().Eventuallyf(func() bool {
			power, ok := s.consumerPowerOf(val2.consAddr)
			return ok && power < multivalValidatorPower
		}, 2*time.Minute, 5*time.Second,
			"the consumer never reported val2 below %d power", multivalValidatorPower)
	})

	s.Run("val2 signs again after its node restarts", func() {
		s.T().Log("starting val2's consumer node again...")
		s.startConsumerNode(2)
		restartHeight, err := s.queryConsumerBlockHeight()
		s.Require().NoError(err, "failed to read the consumer height after restarting val2")

		// Accusations for windows that began before the restart may still be
		// in flight; none may name a window that began after it.
		deadline := time.Now().Add(120 * time.Second)
		for time.Now().Before(deadline) {
			s.Require().Zerof(s.pendingSlashesStartingAtOrAfter(consumerID, restartHeight),
				"a downtime accusation names a window that began after val2's restart at height %d", restartHeight)
			time.Sleep(5 * time.Second)
		}
	})
}
