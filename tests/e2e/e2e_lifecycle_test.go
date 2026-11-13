package e2e

// e2e_lifecycle_test.go holds the scenarios of LifecycleIntegrationTestSuite
// (see e2e_lifecycle_suite_test.go for the parameters and the order) and the
// chain-data helpers they assert with: block times at given heights, the
// first height at which a phase or a queue entry changed (found by bisection
// over historical queries), and the events of a block.

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"time"

	cmted25519 "github.com/cometbft/cometbft/crypto/ed25519"

	sdk "github.com/cosmos/cosmos-sdk/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
)

const (
	// lifecycleConsumerID is the consumer the suite launches at setup.
	lifecycleConsumerID = "0"

	// lifecycleOwnerDeposit is what the owner deposits into consumer 0's fee
	// pool: above the min-deposit floor (fees_per_block x 14400 blocks) and
	// above what the run's epoch distributions and escrow draw from it.
	lifecycleOwnerDeposit = "30000000"
	// lifecyclePrelaunchDeposit is the deposit each registered-only consumer
	// of testPrelaunchRemoval holds when it is erased, above the floor.
	lifecyclePrelaunchDeposit = "20000000"

	// Gov proposal statuses as the gov query prints them.
	proposalStatusPassed = "PROPOSAL_STATUS_PASSED"
	proposalStatusFailed = "PROPOSAL_STATUS_FAILED"

	// Events the provider emits when a pending equivocation punishment is
	// executed or cancelled, when a withheld fee record is paid out and when
	// a consumer is removed, and the attributes naming the consumer and its
	// phase at the removal.
	eventEquivocationPunishmentExecuted  = "vaas_equivocation_punishment_executed"
	eventEquivocationPunishmentCancelled = "vaas_equivocation_punishment_cancelled"
	eventWithheldFeePaid                 = "vaas_withheld_fee_paid"
	eventRemoveConsumer                  = "vaas_remove_consumer"
	attributeConsumerID                  = "consumer_id"
	attributeConsumerPhase               = "consumer_phase"

	// lifecycleSilentSelfBond is the self-bond of the silent validators the
	// suite bonds, five units of power.
	lifecycleSilentSelfBond = "5000000"
)

// testFundFeePoolAndSweepLockedWhileLaunched has the owner fund consumer 0's
// fee pool, the deposit every later scenario's distributions, escrow and
// sweep draw on, and proves the owner's sweep is locked while the consumer is
// launched.
func (s *LifecycleIntegrationTestSuite) testFundFeePoolAndSweepLockedWhileLaunched() {
	s.Run("fee pool: the owner funds the launched consumer; a sweep is locked while launched", func() {
		s.Require().Equal(phaseLaunched, s.queryProviderConsumerPhase(lifecycleConsumerID))

		// The owner holds no fee denom at genesis.
		s.providerFundAddress(s.ownerAddr, "100000000"+feeDenom)
		s.providerFundConsumerFeePoolFrom(lifecycleConsumerID, "owner", lifecycleOwnerDeposit+feeDenom)
		s.Require().Positive(s.providerQueryBalance(s.feePoolAddr, feeDenom), "the deposit must reach the pool")

		s.requireSweepRefused(lifecycleConsumerID, s.ownerAddr, "locked while consumer")
	})
}

// testEquivocationThawAcrossPause queues a punishment against a validator
// whose consumer key the test holds (as the main suite does: the provider's
// one signing validator must stay out of jail, so the double vote is signed
// with a throwaway validator's key), then pauses the consumer by refusal
// across the punishment's maturity and resumes it by governance. The pause
// must leave the entry exactly as it was while its maturity passes, the
// resume must move executes_at forward by the pause's length as the chain
// measured it (resume block time minus pause block time), and the punishment
// must execute in the first block at or past the moved time.
func (s *LifecycleIntegrationTestSuite) testEquivocationThawAcrossPause() {
	s.Run("thaw: a refusal pause freezes a pending equivocation punishment and the resume moves it by the pause", func() {
		const consumerID = lifecycleConsumerID
		s.Require().Equal(phaseLaunched, s.queryProviderConsumerPhase(consumerID))

		// Start right after an epoch's VSC has been acked: the equivocator
		// is bonded and jailed inside one epoch, so it never reaches the
		// consumer's validator set and accrues no downtime accusations, and
		// the consumer's client of the provider was just updated, so the
		// pause ahead cannot age it past its trusting period.
		s.waitForNextEpochAck(consumerID)

		s.T().Log("bonding the equivocating validator and submitting a double vote signed with its key...")
		valoper, priv := s.createEquivocatingValidator("eqval", "5000000"+bondDenom)
		consAddr := sdk.ConsAddress(priv.PubKey().Address()).String()
		evidencePath, headerPath := s.fabricateDoubleVote(priv, s.cfg.consumerChainID, 1_000_000, "eqval")
		s.submitDoubleVoteEvidence(consumerID, evidencePath, headerPath)

		var queued pendingEquivocationJSON
		s.Require().Eventuallyf(func() bool {
			pending, err := s.queryPendingEquivocations(consumerID)
			if err != nil || len(pending) != 1 {
				return false
			}
			queued = pending[0]
			return true
		}, time.Minute, 2*time.Second, "the equivocation punishment never queued")
		originalExecutesAt := queued.ExecutesAt
		jailed, _ := s.stakingValidatorState(valoper)
		s.Require().True(jailed, "the accused validator is jailed at queue time")
		tokensQueued := s.mustValidatorTokens(valoper)
		s.T().Logf("punishment queued, executes at %s", originalExecutesAt)

		s.T().Log("refusing the consumer from val (all the signing power) to pause it...")
		heightBeforeRefusal, _ := s.providerLatestBlock()
		s.setConsumerRefusal(consumerID, true)
		s.Require().Eventuallyf(func() bool {
			return s.queryProviderConsumerPhase(consumerID) == phasePaused
		}, time.Minute, 2*time.Second, "the refusal did not pause the consumer")
		pauseHeight := s.firstProviderHeightInPhase(consumerID, phasePaused, heightBeforeRefusal)
		pausedAt := s.providerBlockTime(pauseHeight)
		s.Require().Truef(pausedAt.Before(originalExecutesAt),
			"the pause must begin before the punishment's original maturity (paused at %s, matures at %s)",
			pausedAt, originalExecutesAt)

		// Withdraw the refusal and open the resume vote, timed so its tally
		// lands after the original maturity: the maturity then passes with
		// the consumer still paused. A standing refusal would have the
		// resume refused, so it is withdrawn first; withdrawing it resumes
		// nothing by itself.
		s.waitForProviderBlockTime(originalExecutesAt.Add(-(s.cfg.govVotingPeriod - 5*time.Second)))
		s.setConsumerRefusal(consumerID, false)
		s.Require().Equal(phasePaused, s.queryProviderConsumerPhase(consumerID),
			"withdrawing the refusal must not resume the consumer by itself")
		proposalID := s.submitProposalAndVote(
			s.resumeProposalJSON(consumerID, "Resume consumer (lifecycle e2e, thaw)"), "yes")

		s.T().Log("letting the original maturity pass while the consumer is paused...")
		s.waitForProviderBlockTime(originalExecutesAt)
		// Read the state of the first block at or past the original maturity,
		// the one whose sweep would have executed the punishment: the resume
		// tallies later, so that block is still paused whatever the queries'
		// own latency.
		latest, _ := s.providerLatestBlock()
		maturityHeight := s.firstProviderHeightWhere(pauseHeight, latest, func(height int64) bool {
			return !s.providerBlockTime(height).Before(originalExecutesAt)
		})
		s.Require().Equal(phasePaused, s.providerConsumerPhaseAt(consumerID, maturityHeight),
			"the consumer must still be paused in the block of the original maturity")
		pending, err := s.queryPendingEquivocationsAt(consumerID, maturityHeight)
		s.Require().NoError(err)
		s.Require().Len(pending, 1, "the frozen punishment must still be pending at its original maturity")
		s.Require().Truef(pending[0].ExecutesAt.Equal(originalExecutesAt),
			"the pause must leave executes_at where it was (want %s, got %s)", originalExecutesAt, pending[0].ExecutesAt)
		s.Require().False(pending[0].Extended, "no removal vote ran, so nothing extended it")

		s.waitForProposalStatus(proposalID, proposalStatusPassed)
		s.Require().Eventuallyf(func() bool {
			return s.queryProviderConsumerPhase(consumerID) == phaseLaunched
		}, time.Minute, 2*time.Second, "the passed resume did not launch the consumer again")
		resumeHeight := s.firstProviderHeightInPhase(consumerID, phaseLaunched, pauseHeight)
		resumedAt := s.providerBlockTime(resumeHeight)
		pausedFor := resumedAt.Sub(pausedAt)
		s.Require().Truef(originalExecutesAt.Before(resumedAt),
			"the pause must span the original maturity (resumed at %s, matured at %s)", resumedAt, originalExecutesAt)

		pending = s.mustPendingEquivocations(consumerID)
		s.Require().Len(pending, 1, "the thaw must keep the punishment pending")
		s.Require().False(pending[0].Extended, "the thaw is not a deferral")
		movedExecutesAt := pending[0].ExecutesAt
		s.Require().Truef(originalExecutesAt.Add(pausedFor).Equal(movedExecutesAt),
			"the resume must move executes_at by exactly the pause's length %s (paused at %s, resumed at %s): %s -> %s",
			pausedFor, pausedAt, resumedAt, originalExecutesAt, movedExecutesAt)
		s.T().Logf("thaw: paused for %s (%s to %s); executes_at moved %s -> %s, by %s",
			pausedFor, pausedAt, resumedAt, originalExecutesAt, movedExecutesAt, movedExecutesAt.Sub(originalExecutesAt))
		s.Require().Equal(tokensQueued, s.mustValidatorTokens(valoper), "no stake moves before the moved maturity")
		jailed, _ = s.stakingValidatorState(valoper)
		s.Require().True(jailed, "the accused validator stays jailed across the thaw")

		s.T().Logf("waiting for the punishment to execute at its moved maturity (%s)...", movedExecutesAt)
		s.Require().Eventuallyf(func() bool {
			return s.pendingEquivocationsEmpty(consumerID)
		}, time.Until(movedExecutesAt)+2*time.Minute, 2*time.Second,
			"the punishment never executed after its moved maturity")
		latest, _ = s.providerLatestBlock()
		executedHeight := s.firstProviderHeightWhere(resumeHeight, latest, func(height int64) bool {
			pending, err := s.queryPendingEquivocationsAt(consumerID, height)
			return err == nil && len(pending) == 0
		})
		executedAt := s.providerBlockTime(executedHeight)
		s.Require().Falsef(executedAt.Before(movedExecutesAt),
			"the punishment executed at %s, before its moved maturity %s", executedAt, movedExecutesAt)
		s.Require().Truef(s.providerBlockTime(executedHeight-1).Before(movedExecutesAt),
			"the punishment must execute in the first block at or past its moved maturity %s, but the block before height %d was already past it",
			movedExecutesAt, executedHeight)
		s.Require().Truef(s.providerBlockHasEvent(executedHeight, eventEquivocationPunishmentExecuted, attributeConsumerID, consumerID),
			"block %d resolved the punishment without a %s event", executedHeight, eventEquivocationPunishmentExecuted)
		s.T().Logf("punishment executed at height %d (%s)", executedHeight, executedAt)

		s.Require().Eventuallyf(func() bool {
			return s.signingInfoTombstoned(consAddr)
		}, time.Minute, 2*time.Second, "the validator was not tombstoned at execution")
		// The double-sign fraction is 5% (see the provider genesis patch).
		s.Require().Equalf(tokensQueued-tokensQueued*5/100, s.mustValidatorTokens(valoper),
			"execution must slash 5%% of the bonded tokens (queued with %d)", tokensQueued)
		jailed, _ = s.stakingValidatorState(valoper)
		s.Require().True(jailed, "a tombstoned validator stays jailed")
	})
}

// testWithheldFeeRecordAcrossEpoch bonds a second, permanently silent
// provider validator, which VAAS syncs into the consumer's set and which
// therefore misses every consumer block, and requires the withheld-fee record
// its accusation produces.
//
// What makes the record deterministic here is the ratio of the two clocks. An
// accepted accusation excludes the validator from the epoch distribution
// only when no distribution has run since its (anchored) window end; the
// anchor is the newest consensus state at or below the window end, that is
// the client update the previous window's evidence relay made, so the
// interval between a window's anchor and its acceptance is about one window
// plus relay latency, and consecutive windows' intervals cover the timeline
// with small overlaps. An epoch boundary falls inside at most two such
// intervals; with four windows per epoch, every epoch has a window whose
// interval holds no boundary, accepted before the epoch ends, so every epoch
// is marked. From the record's first write on, then, every epoch boundary
// withholds one more share (fees_per_epoch / bonded validators) into it,
// until the pair has no accusation pending (the shares then stay with the
// consumer) or the record lapses unrefreshed. The slash executes at the
// accusation's maturity regardless; with windows shorter than the challenge
// window a later window is always pending by then, so the record outlives
// the execution, as the keeper defines it. Its lapse is the next scenario's.
func (s *LifecycleIntegrationTestSuite) testWithheldFeeRecordAcrossEpoch() {
	s.Run("withheld fee: a silent validator's share is escrowed at every boundary its accusations span, and its slash executes", func() {
		const consumerID = lifecycleConsumerID
		s.Require().Equal(phaseLaunched, s.queryProviderConsumerPhase(consumerID))

		s.T().Log("bonding a second, permanently silent validator on the provider...")
		heightBeforeVal2, _ := s.providerLatestBlock()
		s.val2 = s.bondSilentValidator("val2")

		// The share as DistributeConsumerFees computes it: val and val2 are
		// bonded, the equivocator of the previous scenario is unbonding.
		s.Require().Equal(2, s.countBondedProviderValidators(), "val and val2 must be the bonded set")
		share := s.queryConsumerFeesPerBlock(consumerID) * s.queryBlocksPerEpoch() / 2
		s.Require().Positive(share)
		s.T().Logf("expecting one withheld share of %d%s per epoch boundary", share, feeDenom)
		tokensBefore := s.mustValidatorTokens(s.val2.valoper)

		first := s.waitForFirstAccusation(s.val2)
		firstMaturity, err := time.Parse(time.RFC3339Nano, first.MaturesAt)
		s.Require().NoError(err, "unparsable matures_at %q", first.MaturesAt)
		// An accusation matures one challenge window after its acceptance.
		firstAcceptedAt := firstMaturity.Add(-lifecycleDowntimeChallengeWindow)
		s.T().Logf("first accusation: window start %s, accepted at %s, matures at %s", first.WindowStartHeight, firstAcceptedAt, firstMaturity)
		s.Require().Equal(tokensBefore, s.mustValidatorTokens(s.val2.valoper), "a pending accusation touches no stake")

		record, firstWriteHeight := s.waitForFirstWithheldRecord(&s.val2, heightBeforeVal2)
		s.Require().Truef(s.providerBlockTime(firstWriteHeight).After(firstAcceptedAt),
			"the record (height %d) must follow the first acceptance (%s)", firstWriteHeight, firstAcceptedAt)
		s.requireWithheldRecordMatches(record, share, firstWriteHeight)
		s.Require().GreaterOrEqualf(s.providerQueryBalance(s.feePoolAddr, feeDenom), s.mustAmount(record.Amount),
			"the escrow must be backed by the pool")

		s.T().Log("waiting for the first accusation to mature and its slash to execute...")
		s.Require().Eventuallyf(func() bool {
			tokens, err := s.tryValidatorTokens(s.val2.valoper)
			return err == nil && tokens < tokensBefore
		}, time.Until(firstMaturity)+2*time.Minute, 2*time.Second,
			"the silent validator's stake was never reduced by the matured downtime slash")
		s.Require().Eventuallyf(func() bool {
			for _, p := range s.pendingDowntimeSlashesFor(consumerID, s.val2.consAddr) {
				if p.WindowStartHeight == first.WindowStartHeight {
					return false
				}
			}
			return true
		}, 30*time.Second, 2*time.Second, "the executed accusation's pending entry was not removed")

		// The validator is still silent, so later windows are pending at the
		// execution and the record outlives it, still growing by one share
		// per boundary; nothing is ever paid out of it.
		s.Require().NotEmpty(s.pendingDowntimeSlashesFor(consumerID, s.val2.consAddr),
			"later windows must be pending for the still-silent validator")
		record, ok := s.withheldFeeRecordFor(consumerID, s.val2.consAddr)
		s.Require().True(ok, "the record must outlive an execution that leaves accusations pending")
		s.requireWithheldRecordMatches(record, share, firstWriteHeight)
		s.Require().Equal(s.val2.feeBaseline, s.providerQueryBalance(s.val2.accAddr, feeDenom),
			"the silent validator is paid nothing from the first withholding on, and nothing out of the escrow")
	})
}

// requireWithheldRecordMatches asserts the record against the keeper's
// bookkeeping: its expiry is one challenge window past the block time of an
// epoch boundary, the last to refresh it, and its amount is one share for
// every epoch boundary from the one that wrote it (firstWriteHeight) through
// that one.
func (s *LifecycleIntegrationTestSuite) requireWithheldRecordMatches(record withheldFeeRecordJSON, share int64, firstWriteHeight int64) {
	s.Require().Equal(feeDenom, record.Amount.Denom)
	lastRefresh := record.ExpiresAt.Add(-lifecycleDowntimeChallengeWindow)
	latest, _ := s.providerLatestBlock()
	var boundaries int64
	var lastBoundary int64
	for h := firstWriteHeight; h <= latest; h += lifecycleBlocksPerEpoch {
		if s.providerBlockTime(h).After(lastRefresh) {
			break
		}
		boundaries++
		lastBoundary = h
	}
	s.Require().Positivef(boundaries, "the record's last refresh %s precedes its first write at height %d", lastRefresh, firstWriteHeight)
	s.Require().Truef(s.providerBlockTime(lastBoundary).Equal(lastRefresh),
		"the record's expiry must be one challenge window past an epoch boundary; the last boundary before it (height %d) is at %s, the expiry implies %s",
		lastBoundary, s.providerBlockTime(lastBoundary), lastRefresh)
	s.Require().Equalf(boundaries*share, s.mustAmount(record.Amount),
		"the record must hold one share (%d) per epoch boundary from height %d through %d (%d boundaries)",
		share, firstWriteHeight, lastBoundary, boundaries)
	s.T().Logf("withheld record: %s%s over %d boundary(ies) from height %d, expires %s",
		record.Amount.Amount, feeDenom, boundaries, firstWriteHeight, record.ExpiresAt)
}

// testWithheldFeeRecordLapsesWhileLaunched has the silent validator leave the
// bonded set and proves the other end of its record on a consumer that stays
// LAUNCHED: once no boundary refreshes it, the record lapses one challenge
// window after the last boundary that withheld, swept with nothing paid,
// while accusations accepted after that boundary are still pending; they
// execute later, against the stake the validator has in flight.
//
// The validator undelegates down to a remainder under one unit of power: a
// validator with no power is not bonded, so it leaves the bonded set at that
// block's staking EndBlock without being jailed. The boundary that follows
// sends the consumer a set without it and neither pays nor withholds its
// share, so the record's expiry stays where the last boundary that withheld
// left it. The consumer keeps accusing the validator until it applies that
// set, and the provider accepts those windows until the same boundary
// rebuilds its copy of the consumer's set, so accusations accepted after the
// last withholding boundary exist, and, accepted after it, they mature after
// the record's expiry. The sweep therefore removes the record with them still
// pending (the execution-time deletion only ever runs for a pair with nothing
// left pending), and they are executed rather than dropped: the departing
// stake is slashable, and their execution slashes the unbonding entry.
func (s *LifecycleIntegrationTestSuite) testWithheldFeeRecordLapsesWhileLaunched() {
	s.Run("withheld fee: once the silent validator leaves the bonded set, its record lapses unpaid one challenge window after the last boundary that withheld, and its accusations still execute", func() {
		const consumerID = lifecycleConsumerID
		s.Require().Equal(phaseLaunched, s.queryProviderConsumerPhase(consumerID))
		s.Require().NotEmpty(s.val2.consAddr, "the withheld-record scenario must have run first")
		setBefore := s.mustCountConsumerNetValidators()

		// Leave right after a boundary: the whole epoch that follows is there
		// for the consumer's accusations to be accepted in.
		lastBoundary := s.waitForNextEpochBoundary()
		tokens := s.mustValidatorTokens(s.val2.valoper)
		remainder := sdk.DefaultPowerReduction.Int64() / 2
		s.T().Logf("undelegating %d of the silent validator's %d%s, leaving a remainder under one unit of power...",
			tokens-remainder, tokens, bondDenom)
		leaveHeight := s.unbondFrom("val2", s.val2.valoper, strconv.FormatInt(tokens-remainder, 10)+bondDenom)
		s.Require().Truef(lastBoundary < leaveHeight && leaveHeight < lastBoundary+lifecycleBlocksPerEpoch,
			"the undelegation must land inside the epoch after boundary %d, got height %d", lastBoundary, leaveHeight)
		s.Require().Eventuallyf(func() bool {
			v, err := s.tryProviderValidator(s.val2.valoper)
			return err == nil && v.Status == stakingtypes.Unbonding
		}, time.Minute, 2*time.Second, "the silent validator did not leave the bonded set")
		jailed, _ := s.stakingValidatorState(s.val2.valoper)
		s.Require().False(jailed, "leaving by undelegation jails nothing")
		s.Require().Equal(1, s.countBondedProviderValidators(), "val must be the bonded set again")
		s.T().Logf("left the bonded set at height %d", leaveHeight)

		record, ok := s.withheldFeeRecordFor(consumerID, s.val2.consAddr)
		s.Require().True(ok, "the record must stand when the validator leaves")
		expiresAt := record.ExpiresAt
		s.Require().Truef(expiresAt.Add(-lifecycleDowntimeChallengeWindow).Equal(s.providerBlockTime(lastBoundary)),
			"the record's expiry must trail the last boundary that withheld (height %d, %s) by the challenge window, got %s",
			lastBoundary, s.providerBlockTime(lastBoundary), expiresAt)
		s.T().Logf("the record holds %s%s and expires at %s", record.Amount.Amount, feeDenom, expiresAt)

		nextBoundary := lastBoundary + lifecycleBlocksPerEpoch
		s.T().Logf("waiting for the boundary at height %d: it must neither pay the validator nor refresh the record...", nextBoundary)
		s.waitForProviderHeight(nextBoundary + 1)
		at, ok := s.withheldFeeRecordForAt(consumerID, s.val2.consAddr, nextBoundary)
		s.Require().True(ok, "the record must still stand at the boundary after the validator left")
		s.Require().Truef(at.ExpiresAt.Equal(expiresAt) && at.Amount == record.Amount,
			"a boundary after the validator left must leave the record as it was (was %+v, now %+v)", record, at)
		s.Require().Equal(s.val2.feeBaseline, s.providerQueryBalanceAt(s.val2.accAddr, feeDenom, nextBoundary),
			"a boundary after the validator left pays it nothing")

		s.T().Log("waiting for the consumer to drop the validator from its set...")
		s.Require().Eventuallyf(func() bool {
			vals, err := s.queryConsumerNetValidators()
			return err == nil && len(vals) == setBefore-1
		}, 3*time.Minute, 3*time.Second, "the consumer never dropped the validator from its set")

		s.T().Logf("waiting for the record to lapse at %s...", expiresAt)
		s.Require().Eventuallyf(func() bool {
			_, ok := s.withheldFeeRecordFor(consumerID, s.val2.consAddr)
			return !ok
		}, time.Until(expiresAt)+2*time.Minute, 2*time.Second, "the record never lapsed")
		latest, _ := s.providerLatestBlock()
		lapseHeight := s.firstProviderHeightWhere(leaveHeight, latest, func(height int64) bool {
			_, ok := s.withheldFeeRecordForAt(consumerID, s.val2.consAddr, height)
			return !ok
		})
		lapsedAt := s.providerBlockTime(lapseHeight)
		s.Require().Falsef(lapsedAt.Before(expiresAt), "the record was removed at %s, before its expiry %s", lapsedAt, expiresAt)
		s.Require().Truef(s.providerBlockTime(lapseHeight-1).Before(expiresAt),
			"the record must lapse in the first block at or past its expiry %s, but the block before height %d was already past it",
			expiresAt, lapseHeight)
		pendingAtLapse := s.pendingDowntimeSlashesForAt(consumerID, s.val2.consAddr, lapseHeight)
		s.Require().NotEmpty(pendingAtLapse,
			"accusations accepted after the last withholding boundary must still be pending at the lapse: the sweep, not an execution, removes the record")
		for _, p := range pendingAtLapse {
			maturity, err := time.Parse(time.RFC3339Nano, p.MaturesAt)
			s.Require().NoError(err, "unparsable matures_at %q", p.MaturesAt)
			s.Require().Truef(maturity.After(expiresAt),
				"an accusation pending at the lapse was accepted after the last withholding boundary, so it matures after the expiry %s, got %s",
				expiresAt, maturity)
		}
		s.Require().False(s.providerBlockHasEvent(lapseHeight, eventWithheldFeePaid, attributeConsumerID, consumerID),
			"a lapsed record pays nothing")
		s.Require().Equal(s.val2.feeBaseline, s.providerQueryBalanceAt(s.val2.accAddr, feeDenom, lapseHeight),
			"the validator is paid nothing at the lapse")
		if lapseHeight%lifecycleBlocksPerEpoch != 0 {
			s.Require().Equal(s.providerQueryBalanceAt(s.feePoolAddr, feeDenom, lapseHeight-1),
				s.providerQueryBalanceAt(s.feePoolAddr, feeDenom, lapseHeight), "the escrow stays in the pool")
		}
		s.T().Logf("the record lapsed at height %d (%s) with %d accusation(s) still pending", lapseHeight, lapsedAt, len(pendingAtLapse))

		lastMaturity, err := time.Parse(time.RFC3339Nano, pendingAtLapse[len(pendingAtLapse)-1].MaturesAt)
		s.Require().NoError(err)
		unbondingAtLapse := s.mustUnbondingBalanceAt(s.val2.accAddr, s.val2.valoper, lapseHeight)
		s.T().Logf("waiting for the last of them to mature (%s) and execute against the departing stake...", lastMaturity)
		s.Require().Eventuallyf(func() bool {
			return len(s.pendingDowntimeSlashesFor(consumerID, s.val2.consAddr)) == 0
		}, time.Until(lastMaturity)+2*time.Minute, 2*time.Second, "the accusations pending at the lapse never resolved")
		s.Require().Lessf(s.mustUnbondingBalance(s.val2.accAddr, s.val2.valoper), unbondingAtLapse,
			"the accusations pending at the lapse must execute against the unbonding entry (%d at the lapse)", unbondingAtLapse)
		_, ok = s.withheldFeeRecordFor(consumerID, s.val2.consAddr)
		s.Require().False(ok, "an execution with nothing left pending finds no record to delete, and writes none")
		s.Require().Equal(s.val2.feeBaseline, s.providerQueryBalance(s.val2.accAddr, feeDenom),
			"the validator is paid nothing, from the first withholding to the end")
	})
}

// testMaxPauseAutoStop bonds a third silent validator, so a withheld record
// is live when the pause begins (the second validator's lapsed in the previous
// scenario), pauses the consumer by refusal and waits out max_pause_duration:
// the consumer must stay paused until then, with the record frozen, and stop
// on its own in the first block at or past the deadline. Double-voting
// evidence submitted during the pause is accepted, and queues a punishment
// whose clock starts at the pause; the stop ends the freeze, so the
// punishment runs one full delay from the stop. The stop cancels the pending
// downtime slashes, and the record, now measured against the running clock,
// has lapsed: it is swept with nothing paid and the escrow still in the
// pool. A resume proposal then passes its vote but its message is refused.
func (s *LifecycleIntegrationTestSuite) testMaxPauseAutoStop() {
	s.Run("auto-stop: a pause that outlives max_pause_duration stops the consumer on its own; a resume is then refused", func() {
		const consumerID = lifecycleConsumerID
		s.Require().Equal(phaseLaunched, s.queryProviderConsumerPhase(consumerID))

		s.T().Log("bonding a third silent validator, so a withheld record is live when the pause begins...")
		heightBeforeVal3, _ := s.providerLatestBlock()
		s.val3 = s.bondSilentValidator("val3")
		s.waitForFirstAccusation(s.val3)
		s.waitForFirstWithheldRecord(&s.val3, heightBeforeVal3)

		s.T().Log("refusing the consumer from val to pause it...")
		heightBeforeRefusal, _ := s.providerLatestBlock()
		s.setConsumerRefusal(consumerID, true)
		s.Require().Eventuallyf(func() bool {
			return s.queryProviderConsumerPhase(consumerID) == phasePaused
		}, time.Minute, 2*time.Second, "the refusal did not pause the consumer")
		pauseHeight := s.firstProviderHeightInPhase(consumerID, phasePaused, heightBeforeRefusal)
		pausedAt := s.providerBlockTime(pauseHeight)
		autoStopAt := pausedAt.Add(lifecycleMaxPauseDuration)
		s.T().Logf("paused at height %d (%s); the auto-stop is due at %s", pauseHeight, pausedAt, autoStopAt)

		s.requireSweepRefused(consumerID, s.ownerAddr, "locked while consumer")
		frozen, ok := s.withheldFeeRecordFor(consumerID, s.val3.consAddr)
		s.Require().True(ok, "a refusal pause freezes the withheld record rather than discarding it")
		// No distribution runs while paused, so these balances are fixed
		// for the pause and the stop may not move them either.
		poolPaused := s.providerQueryBalance(s.feePoolAddr, feeDenom)
		val3FeePaused := s.providerQueryBalance(s.val3.accAddr, feeDenom)
		s.Require().Equal(s.val3.feeBaseline, val3FeePaused,
			"the silent validator was paid nothing since its share was first withheld")

		s.T().Log("bonding an equivocating validator and submitting its double vote while the consumer is paused...")
		s.pauseEquivocator = s.bondAndAccuseEquivocator("pauseq", 1_000_001)
		s.Require().Truef(s.pauseEquivocator.queued.ExecutesAt.Equal(pausedAt.Add(lifecycleEquivocationExecutionDelay)),
			"evidence accepted during the pause starts its clock at the pause (paused at %s), got executes_at %s",
			pausedAt, s.pauseEquivocator.queued.ExecutesAt)

		if wait := time.Until(autoStopAt) - 5*time.Second; wait > 0 {
			s.T().Logf("holding %s: the consumer must stay paused until the deadline...", wait)
			// A failed query reads back "", which is no phase change.
			s.Require().Never(func() bool {
				phase := s.queryProviderConsumerPhase(consumerID)
				return phase != "" && phase != phasePaused
			}, wait, 3*time.Second, "the consumer left the pause before max_pause_duration elapsed")
		}

		s.T().Log("waiting for the auto-stop...")
		s.Require().Eventuallyf(func() bool {
			return s.queryProviderConsumerPhase(consumerID) == phaseStopped
		}, time.Minute, 2*time.Second, "the lapsed pause did not stop the consumer")
		stopHeight := s.firstProviderHeightInPhase(consumerID, phaseStopped, pauseHeight)
		stoppedAt := s.providerBlockTime(stopHeight)
		s.Require().Falsef(stoppedAt.Before(autoStopAt),
			"the consumer stopped at %s, before the deadline %s", stoppedAt, autoStopAt)
		s.Require().Truef(s.providerBlockTime(stopHeight-1).Before(autoStopAt),
			"the auto-stop must fire in the first block at or past the deadline %s, but the block before height %d was already past it",
			autoStopAt, stopHeight)
		s.stopHeight = stopHeight
		s.stoppedAt = stoppedAt
		s.T().Logf("auto-stopped at height %d (%s)", stopHeight, stoppedAt)

		// The last paused block holds the record exactly as the pause froze it.
		still, ok := s.withheldFeeRecordForAt(consumerID, s.val3.consAddr, stopHeight-1)
		s.Require().True(ok, "the frozen record must survive the whole pause")
		s.Require().Truef(still.ExpiresAt.Equal(frozen.ExpiresAt) && still.Amount == frozen.Amount,
			"the pause must freeze the record (was %+v, at the last paused block %+v)", frozen, still)

		// The stop thaws the punishment queued during the pause: it runs one
		// full delay from the stop.
		pendingAtStop, err := s.queryPendingEquivocationsAt(consumerID, stopHeight)
		s.Require().NoError(err)
		thawed, found := pendingEquivocationFor(pendingAtStop, s.pauseEquivocator.consAddr)
		s.Require().True(found, "a stop by a lapsed pause is not a verdict: the punishment stays pending")
		s.Require().Truef(thawed.ExecutesAt.Equal(stoppedAt.Add(lifecycleEquivocationExecutionDelay)),
			"the stop must move the punishment queued during the pause to one delay after the stop (%s), got %s",
			stoppedAt.Add(lifecycleEquivocationExecutionDelay), thawed.ExecutesAt)
		s.pauseEquivocator.queued = thawed

		s.T().Log("verifying the stop cancelled the accusations and let the frozen record lapse unpaid...")
		s.Require().Eventuallyf(func() bool {
			return len(s.queryPendingDowntimeSlashes(consumerID)) == 0
		}, 30*time.Second, 2*time.Second, "the stop did not cancel the pending downtime slashes")
		s.Require().Eventuallyf(func() bool {
			_, ok := s.withheldFeeRecordFor(consumerID, s.val3.consAddr)
			return !ok
		}, 30*time.Second, 2*time.Second, "the lapsed withheld record was not swept after the stop")
		s.Require().Equal(val3FeePaused, s.providerQueryBalance(s.val3.accAddr, feeDenom),
			"a lapsed record pays nothing")
		s.Require().Equal(poolPaused, s.providerQueryBalance(s.feePoolAddr, feeDenom),
			"the escrow stays in the pool")

		s.T().Log("voting a resume of the stopped consumer: the vote passes, the resume is refused...")
		s.submitProposalWithVote(s.resumeProposalJSON(consumerID, "Resume consumer (lifecycle e2e, after the auto-stop)"),
			"yes", proposalStatusFailed)
		s.Require().Equal(phaseStopped, s.queryProviderConsumerPhase(consumerID),
			"a stopped consumer cannot be resumed")
	})
}

// testEquivocationEvidenceOnTheStoppedConsumer proves the double-voting
// evidence rules on a consumer the lapsed pause stopped, which is not a
// verdict on its evidence. The punishment queued during the pause executes
// in the first block at or past one delay after the stop. Evidence submitted
// after the stop is accepted and queues a punishment; a governance removal
// of the stopped consumer then passes, cancels that punishment in its tally
// block and records the verdict, after which fresh evidence is refused. The
// consumer stays STOPPED, its deletion where the stop scheduled it (the
// deletion scenario checks it against the stop).
func (s *LifecycleIntegrationTestSuite) testEquivocationEvidenceOnTheStoppedConsumer() {
	s.Run("evidence: accepted after a lapsed pause stopped the consumer, until a governance removal of the stopped consumer voids it", func() {
		const consumerID = lifecycleConsumerID
		s.Require().Equal(phaseStopped, s.queryProviderConsumerPhase(consumerID))
		s.Require().NotEmpty(s.pauseEquivocator.consAddr, "the auto-stop scenario must have run first")

		paused := s.pauseEquivocator
		s.T().Logf("waiting for the punishment queued during the pause to execute at %s...", paused.queued.ExecutesAt)
		s.Require().Eventuallyf(func() bool {
			pending, err := s.queryPendingEquivocations(consumerID)
			if err != nil {
				return false
			}
			_, found := pendingEquivocationFor(pending, paused.consAddr)
			return !found
		}, time.Until(paused.queued.ExecutesAt)+2*time.Minute, 2*time.Second,
			"the punishment queued during the pause never executed after the stop")
		latest, _ := s.providerLatestBlock()
		executedHeight := s.firstProviderHeightWhere(s.stopHeight, latest, func(height int64) bool {
			pending, err := s.queryPendingEquivocationsAt(consumerID, height)
			if err != nil {
				return false
			}
			_, found := pendingEquivocationFor(pending, paused.consAddr)
			return !found
		})
		s.Require().Falsef(s.providerBlockTime(executedHeight).Before(paused.queued.ExecutesAt),
			"the punishment executed at height %d, before its time %s", executedHeight, paused.queued.ExecutesAt)
		s.Require().Truef(s.providerBlockTime(executedHeight-1).Before(paused.queued.ExecutesAt),
			"the punishment must execute in the first block at or past %s, but the block before height %d was already past it",
			paused.queued.ExecutesAt, executedHeight)
		s.Require().Truef(s.providerBlockHasEvent(executedHeight, eventEquivocationPunishmentExecuted, attributeConsumerID, consumerID),
			"block %d resolved the punishment without a %s event", executedHeight, eventEquivocationPunishmentExecuted)
		s.Require().Eventuallyf(func() bool {
			return s.signingInfoTombstoned(paused.consAddr)
		}, time.Minute, 2*time.Second, "the validator accused during the pause was not tombstoned at execution")
		s.Require().Equalf(paused.tokens-paused.tokens*5/100, s.mustValidatorTokens(paused.valoper),
			"execution must slash 5%% of the bonded tokens (queued with %d)", paused.tokens)

		s.T().Log("bonding an equivocating validator and submitting its double vote against the stopped consumer...")
		stopped := s.bondAndAccuseEquivocator("stopeq", 1_000_002)

		s.T().Log("removing the stopped consumer by governance: the vote passes and the removal is the verdict...")
		heightBeforeRemoval, _ := s.providerLatestBlock()
		s.submitProposalWithVote(s.removalProposalJSON(consumerID, "Remove the stopped consumer (lifecycle e2e)"),
			"yes", proposalStatusPassed)
		latest, _ = s.providerLatestBlock()
		cancelHeight := s.firstProviderHeightWhere(heightBeforeRemoval, latest, func(height int64) bool {
			pending, err := s.queryPendingEquivocationsAt(consumerID, height)
			return err == nil && len(pending) == 0
		})
		s.Require().Truef(s.providerBlockHasEvent(cancelHeight, eventRemoveConsumer, attributeConsumerPhase, phaseStopped),
			"the punishment was resolved at height %d without the removal of the stopped consumer in that block", cancelHeight)
		s.Require().Truef(s.providerBlockHasEvent(cancelHeight, eventEquivocationPunishmentCancelled, attributeConsumerID, consumerID),
			"block %d removed the punishment without a %s event", cancelHeight, eventEquivocationPunishmentCancelled)
		s.Require().False(s.signingInfoTombstoned(stopped.consAddr), "a cancelled punishment tombstones nothing")
		s.Require().Equal(phaseStopped, s.queryProviderConsumerPhase(consumerID),
			"the removal of a stopped consumer leaves it stopped")

		s.T().Log("submitting fresh evidence against the removed consumer: it is refused...")
		evidencePath, headerPath := s.fabricateDoubleVote(stopped.priv, s.cfg.consumerChainID, 1_000_003, "stopeq-after-removal")
		_, stderr, err := s.dockerExec(s.providerValRes[0].Container.ID, []string{
			providerBinary, "tx", "vaasprovider", "submit-consumer-double-voting",
			consumerID, evidencePath, headerPath,
			"--from", s.mustKeyAddress("val"),
			"--home", providerHomePath,
			"--keyring-backend", "test",
			"--chain-id", s.cfg.providerChainID,
			"--fees", "10000" + bondDenom,
			"--dry-run",
			"-y",
		})
		s.Require().Containsf(stderr.String(), "removed by governance",
			"evidence against a consumer governance removed must be refused: stderr=%q err=%v", stderr.String(), err)
		s.Require().True(s.pendingEquivocationsEmpty(consumerID), "the refused evidence queues nothing")
	})
}

// testFeePoolSweepWhenStopped proves the sweep's admission once the consumer
// is STOPPED: a non-owner's is refused, the owner's returns the whole
// remaining pool, which the owner alone deposited into, to the owner.
func (s *LifecycleIntegrationTestSuite) testFeePoolSweepWhenStopped() {
	s.Run("fee pool: once stopped, only the owner's sweep returns the pool, to the owner", func() {
		const consumerID = lifecycleConsumerID
		s.Require().Equal(phaseStopped, s.queryProviderConsumerPhase(consumerID))

		poolBalance := s.providerQueryBalance(s.feePoolAddr, feeDenom)
		s.Require().Positive(poolBalance, "the pool must still hold the undistributed deposit")
		ownerBefore := s.providerQueryBalance(s.ownerAddr, feeDenom)

		s.requireSweepRefused(consumerID, s.mustKeyAddress("val"), "only consumer owner")

		s.T().Log("sweeping the pool as the owner...")
		stdout, stderr, err := s.dockerExec(s.providerValRes[0].Container.ID, []string{
			providerBinary, "tx", "vaasprovider", "sweep-consumer-fee-pool", consumerID,
			"--from", "owner",
			"--home", providerHomePath,
			"--keyring-backend", "test",
			"--chain-id", s.cfg.providerChainID,
			"--gas", "auto",
			"--gas-adjustment", "1.5",
			"--fees", "10000" + bondDenom,
			"-y",
			"-o", "json",
		})
		s.Require().NoErrorf(err, "failed to broadcast the owner's sweep: stderr=%s", stderr.String())
		s.requireTxCommitted(stdout.Bytes())

		s.Require().Eventuallyf(func() bool {
			return s.providerQueryBalance(s.feePoolAddr, feeDenom) == 0
		}, 30*time.Second, 2*time.Second, "the owner's sweep did not empty the pool")
		s.Require().Equalf(ownerBefore+poolBalance, s.providerQueryBalance(s.ownerAddr, feeDenom),
			"the sweep must return the whole pool (%d) to the owner", poolBalance)
	})
}

// testStoppedToDeleted waits out the provider unbonding from the auto-stop:
// the consumer must be erased in the first block at or past that time (the
// governance removal of the stopped consumer left the schedule alone), its
// chain id released, so a new consumer registers under it.
func (s *LifecycleIntegrationTestSuite) testStoppedToDeleted() {
	s.Run("deletion: the stopped consumer is erased once the provider unbonding elapses, releasing its chain id", func() {
		const consumerID = lifecycleConsumerID
		s.Require().Equal(phaseStopped, s.queryProviderConsumerPhase(consumerID))
		s.Require().False(s.stoppedAt.IsZero(), "the auto-stop scenario must have run first")

		removalAt := s.stoppedAt.Add(lifecycleProviderUnbonding)
		s.T().Logf("waiting for the removal time %s (%s from now)...", removalAt, time.Until(removalAt).Round(time.Second))
		s.Require().Eventuallyf(func() bool {
			return s.queryProviderConsumerPhase(consumerID) == phaseDeleted
		}, time.Until(removalAt)+2*time.Minute, 5*time.Second, "the stopped consumer was never deleted")
		deleteHeight := s.firstProviderHeightInPhase(consumerID, phaseDeleted, s.stopHeight)
		deletedAt := s.providerBlockTime(deleteHeight)
		s.Require().Falsef(deletedAt.Before(removalAt),
			"the consumer was deleted at %s, before its removal time %s", deletedAt, removalAt)
		s.Require().Truef(s.providerBlockTime(deleteHeight-1).Before(removalAt),
			"the deletion must land in the first block at or past the removal time %s, but the block before height %d was already past it",
			removalAt, deleteHeight)
		s.T().Logf("deleted at height %d (%s)", deleteHeight, deletedAt)

		info := s.queryConsumerChainInfo(consumerID)
		s.Require().Equal(phaseDeleted, info.Phase)
		s.Require().Empty(info.ChainId, "a deleted consumer answers with no chain id")

		s.T().Log("registering a new consumer under the released chain id...")
		newID := s.createRegisteredConsumer(s.cfg.consumerChainID)
		info = s.queryConsumerChainInfo(newID)
		s.Require().Equal(phaseRegistered, info.Phase)
		s.Require().Equal(s.cfg.consumerChainID, info.ChainId)
		s.prelaunchConsumerID = newID
	})
}

// testPrelaunchRemoval erases consumers that never launched: the one
// registered under the released chain id is funded and removed by its owner,
// a second one registered in its place is funded and removed by governance.
// Both are DELETED at once, with no STOPPED stopover, and their deposits
// return to the owner.
func (s *LifecycleIntegrationTestSuite) testPrelaunchRemoval() {
	s.Run("pre-launch removal: a registered consumer is erased at once by its owner, and by governance, with its pool returned", func() {
		s.Require().NotEmpty(s.prelaunchConsumerID, "the deletion scenario must have registered the consumer")

		s.T().Log("removing the registered consumer as its owner...")
		s.fundAndErasePrelaunchConsumer(s.prelaunchConsumerID, func(consumerID string) {
			stdout, stderr, err := s.dockerExec(s.providerValRes[0].Container.ID, []string{
				providerBinary, "tx", "vaasprovider", "remove-consumer", consumerID,
				"--from", "owner",
				"--home", providerHomePath,
				"--keyring-backend", "test",
				"--chain-id", s.cfg.providerChainID,
				"--gas", "auto",
				"--gas-adjustment", "1.5",
				"--fees", "10000" + bondDenom,
				"-y",
				"-o", "json",
			})
			s.Require().NoErrorf(err, "failed to broadcast the owner's removal: stderr=%s", stderr.String())
			s.requireTxCommitted(stdout.Bytes())
		})

		s.T().Log("registering the chain id again and removing the consumer by governance...")
		govRemoved := s.createRegisteredConsumer(s.cfg.consumerChainID)
		s.fundAndErasePrelaunchConsumer(govRemoved, func(consumerID string) {
			s.submitAndPassProposal(s.removalProposalJSON(consumerID, "Remove the registered consumer (lifecycle e2e)"))
		})
	})
}

// fundAndErasePrelaunchConsumer has the owner fund the registered consumer's
// pool, runs the removal, and asserts the consumer is erased with the
// deposit back in the owner's account.
func (s *LifecycleIntegrationTestSuite) fundAndErasePrelaunchConsumer(consumerID string, remove func(consumerID string)) {
	info := s.queryConsumerChainInfo(consumerID)
	s.Require().Equal(phaseRegistered, info.Phase, "consumer %s must be registered only", consumerID)
	poolAddr := info.FeePoolAddress

	ownerBefore := s.providerQueryBalance(s.ownerAddr, feeDenom)
	s.providerFundConsumerFeePoolFrom(consumerID, "owner", lifecyclePrelaunchDeposit+feeDenom)
	deposit, err := strconv.ParseInt(lifecyclePrelaunchDeposit, 10, 64)
	s.Require().NoError(err)
	// A consumer that never launched distributes nothing, so the pool holds
	// exactly the deposit until it is erased.
	s.Require().Equal(deposit, s.providerQueryBalance(poolAddr, feeDenom))
	s.Require().Equal(ownerBefore-deposit, s.providerQueryBalance(s.ownerAddr, feeDenom))

	remove(consumerID)

	s.Require().Eventuallyf(func() bool {
		return s.queryProviderConsumerPhase(consumerID) == phaseDeleted
	}, time.Minute, 2*time.Second, "the pre-launch consumer %s was not erased", consumerID)
	info = s.queryConsumerChainInfo(consumerID)
	s.Require().Empty(info.ChainId, "the erased consumer must have released its chain id")
	s.Require().Zero(s.providerQueryBalance(poolAddr, feeDenom), "the erased consumer's pool must be empty")
	s.Require().Equalf(ownerBefore, s.providerQueryBalance(s.ownerAddr, feeDenom),
		"the deposit must return to the owner on erasure")
}

// ---- helpers ---------------------------------------------------------------

// silentValidator is a provider validator bonded without a node (see
// createSilentValidator): VAAS syncs it into the consumer's validator set,
// where it misses every block, so the consumer accuses it window after window
// for as long as it is in the set.
type silentValidator struct {
	accAddr  string
	valoper  string
	consAddr string
	// feeBaseline is the validator's fee-denom balance as of the boundary
	// that first withheld its share. The boundaries before it paid the
	// validator like any bonded one; from it on the validator is paid
	// nothing, and nothing is ever paid out of the escrow.
	feeBaseline int64
}

// bondSilentValidator bonds a silent validator under key with the suite's
// self-bond and waits for VAAS to sync it into the consumer's validator set.
func (s *LifecycleIntegrationTestSuite) bondSilentValidator(key string) silentValidator {
	setBefore := s.mustCountConsumerNetValidators()
	accAddr, valoper := s.createSilentValidator(key, lifecycleSilentSelfBond+bondDenom)
	v := silentValidator{accAddr: accAddr, valoper: valoper, consAddr: s.providerValidatorConsAddr(valoper)}

	s.T().Logf("waiting for the consumer to sync %s into its validator set...", key)
	s.Require().Eventuallyf(func() bool {
		vals, err := s.queryConsumerNetValidators()
		return err == nil && len(vals) == setBefore+1
	}, 3*time.Minute, 3*time.Second, "the consumer never synced %s into its validator set", key)
	return v
}

// equivocator is a provider validator bonded without a node whose consensus
// key the test holds, so it can sign fabricated double votes, with the
// punishment its evidence queued and its bonded tokens at the queue.
type equivocator struct {
	valoper  string
	consAddr string
	priv     cmted25519.PrivKey
	queued   pendingEquivocationJSON
	tokens   int64
}

// bondAndAccuseEquivocator bonds an equivocating validator under key, submits
// a double vote signed with its key at the given consumer height, and returns
// it with the punishment the evidence queued.
func (s *LifecycleIntegrationTestSuite) bondAndAccuseEquivocator(key string, height int64) equivocator {
	valoper, priv := s.createEquivocatingValidator(key, lifecycleSilentSelfBond+bondDenom)
	e := equivocator{valoper: valoper, consAddr: sdk.ConsAddress(priv.PubKey().Address()).String(), priv: priv}
	evidencePath, headerPath := s.fabricateDoubleVote(priv, s.cfg.consumerChainID, height, key)
	s.submitDoubleVoteEvidence(lifecycleConsumerID, evidencePath, headerPath)
	s.Require().Eventuallyf(func() bool {
		pending, err := s.queryPendingEquivocations(lifecycleConsumerID)
		if err != nil {
			return false
		}
		queued, found := pendingEquivocationFor(pending, e.consAddr)
		e.queued = queued
		return found
	}, time.Minute, 2*time.Second, "the evidence against %s queued no punishment", key)
	s.Require().False(e.queued.Extended)
	jailed, _ := s.stakingValidatorState(valoper)
	s.Require().True(jailed, "the accused validator is jailed at queue time")
	e.tokens = s.mustValidatorTokens(valoper)
	s.T().Logf("%s's punishment queued, executes at %s", key, e.queued.ExecutesAt)
	return e
}

// waitForFirstAccusation waits for the provider to accept a downtime
// accusation against the silent validator and returns the earliest pending
// one.
func (s *LifecycleIntegrationTestSuite) waitForFirstAccusation(v silentValidator) pendingDowntimeSlashJSON {
	s.T().Log("waiting for the provider to accept a downtime accusation against the silent validator...")
	var first pendingDowntimeSlashJSON
	s.Require().Eventuallyf(func() bool {
		entries := s.pendingDowntimeSlashesFor(lifecycleConsumerID, v.consAddr)
		if len(entries) == 0 {
			return false
		}
		first = entries[0]
		return true
	}, 4*time.Minute, 2*time.Second, "the provider never queued a downtime slash for the silent validator")
	return first
}

// waitForFirstWithheldRecord waits for the epoch boundary that first withholds
// the silent validator's share, given a height before the validator was
// bonded, records the validator's fee baseline as of that boundary and returns
// the record with the boundary's height.
func (s *LifecycleIntegrationTestSuite) waitForFirstWithheldRecord(v *silentValidator, heightBefore int64) (withheldFeeRecordJSON, int64) {
	s.T().Log("waiting for the epoch boundary to withhold the silent validator's share...")
	var record withheldFeeRecordJSON
	s.Require().Eventuallyf(func() bool {
		r, ok := s.withheldFeeRecordFor(lifecycleConsumerID, v.consAddr)
		record = r
		return ok
	}, 3*time.Minute, 2*time.Second, "no withheld fee record was written for the silent validator")
	latest, _ := s.providerLatestBlock()
	firstWriteHeight := s.firstProviderHeightWhere(heightBefore, latest, func(height int64) bool {
		_, ok := s.withheldFeeRecordForAt(lifecycleConsumerID, v.consAddr, height)
		return ok
	})
	s.Require().Zerof(firstWriteHeight%lifecycleBlocksPerEpoch,
		"the record must be written at an epoch boundary, got height %d", firstWriteHeight)
	v.feeBaseline = s.providerQueryBalanceAt(v.accAddr, feeDenom, firstWriteHeight)
	return record, firstWriteHeight
}

// waitForNextEpochBoundary waits for the provider to reach its next epoch
// boundary and returns the boundary's height.
func (s *LifecycleIntegrationTestSuite) waitForNextEpochBoundary() int64 {
	height, _ := s.providerLatestBlock()
	boundary := (height/lifecycleBlocksPerEpoch + 1) * lifecycleBlocksPerEpoch
	s.T().Logf("waiting for the epoch boundary at height %d...", boundary)
	s.waitForProviderHeight(boundary)
	return boundary
}

// waitForNextEpochAck waits for the next epoch boundary on the provider and
// for the consumer's ack of the VSC packet that boundary sent, so the caller
// starts right after a delivery.
func (s *LifecycleIntegrationTestSuite) waitForNextEpochAck(consumerID string) {
	boundary := s.waitForNextEpochBoundary()
	boundaryTime := s.providerBlockTime(boundary)
	s.Require().Eventuallyf(func() bool {
		ack, err := s.queryConsumerLastAck(consumerID)
		return err == nil && ack.After(boundaryTime)
	}, 2*time.Minute, 2*time.Second, "the VSC sent at height %d was never acked", boundary)
}

// requireSweepRefused simulates a sweep of the consumer's fee pool signed by
// the given address and requires the handler to refuse it for the given
// reason. --dry-run runs the full simulation, handler included, without
// paying for a doomed transaction; it takes a bech32 address, not a key
// name, since simulation does not read the keyring.
func (s *LifecycleIntegrationTestSuite) requireSweepRefused(consumerID, fromAddr, want string) {
	_, stderr, err := s.dockerExec(s.providerValRes[0].Container.ID, []string{
		providerBinary, "tx", "vaasprovider", "sweep-consumer-fee-pool", consumerID,
		"--from", fromAddr,
		"--home", providerHomePath,
		"--keyring-backend", "test",
		"--chain-id", s.cfg.providerChainID,
		"--fees", "10000" + bondDenom,
		"--dry-run",
		"-y",
	})
	s.Require().Containsf(stderr.String(), want,
		"the sweep by %s must be refused with %q: stderr=%q err=%v", fromAddr, want, stderr.String(), err)
}

// createRegisteredConsumer registers a consumer under chainID from the owner
// with no spawn time, so it stays REGISTERED, and returns its consumer id
// from the create event. The unbonding period must not exceed the
// provider's and must exceed the downtime challenge horizon.
func (s *LifecycleIntegrationTestSuite) createRegisteredConsumer(chainID string) string {
	createJSON := fmt.Sprintf(`{
  "chain_id": %q,
  "metadata": {
    "name": "prelaunch",
    "description": "lifecycle e2e consumer that never launches",
    "metadata": "{}"
  },
  "initialization_parameters": {
    "initial_height": {"revision_number": 0, "revision_height": 1},
    "genesis_hash": "",
    "binary_hash": "",
    "unbonding_period": %d,
    "vaas_timeout_period": 3600000000000,
    "safe_mode_threshold": 120000000000,
    "historical_entries": 10000
  }
}`, chainID, lifecycleProviderUnbonding.Nanoseconds())
	containerID := s.providerValRes[0].Container.ID
	s.dockerExecMust(containerID, []string{
		"sh", "-c", fmt.Sprintf("echo %s | base64 -d > /tmp/create_prelaunch.json",
			base64.StdEncoding.EncodeToString([]byte(createJSON))),
	})
	stdout, stderr, err := s.dockerExec(containerID, []string{
		providerBinary, "tx", "vaasprovider", "create-consumer", "/tmp/create_prelaunch.json",
		"--from", "owner",
		"--home", providerHomePath,
		"--keyring-backend", "test",
		"--chain-id", s.cfg.providerChainID,
		"--gas", "auto",
		"--gas-adjustment", "1.5",
		"--fees", "10000" + bondDenom,
		"-y",
		"-o", "json",
	})
	s.Require().NoErrorf(err, "failed to broadcast create-consumer for %s: stderr=%s", chainID, stderr.String())
	s.requireTxCommitted(stdout.Bytes())
	consumerID := s.mustTxEventAttribute(stdout.Bytes(), "vaas_create_consumer", attributeConsumerID)
	s.T().Logf("registered consumer %s under chain id %s", consumerID, chainID)
	return consumerID
}

// providerQueryBalanceAt is providerQueryBalance as of a past provider
// height.
func (s *baseTestSuite) providerQueryBalanceAt(addr, denom string, height int64) int64 { //nolint:unparam // the lifecycle scenarios read fee balances; the helper mirrors providerQueryBalance
	stdout, stderr, err := s.dockerExec(s.providerValRes[0].Container.ID, []string{
		providerBinary, "query", "bank", "balances", addr,
		"--height", strconv.FormatInt(height, 10),
		"--home", providerHomePath,
		"--output", "json",
	})
	s.Require().NoErrorf(err, "failed to query the balance of %s at height %d: %s", addr, height, stderr.String())
	var res struct {
		Balances []coinJSON `json:"balances"`
	}
	s.Require().NoErrorf(json.Unmarshal(stdout.Bytes(), &res),
		"failed to decode the balance of %s at height %d: stdout=%s stderr=%s", addr, height, stdout.String(), stderr.String())
	for _, b := range res.Balances {
		if b.Denom == denom {
			n, err := strconv.ParseInt(b.Amount, 10, 64)
			s.Require().NoErrorf(err, "unparsable balance %q", b.Amount)
			return n
		}
	}
	return 0
}

// mustAmount parses a coin's amount.
func (s *LifecycleIntegrationTestSuite) mustAmount(c coinJSON) int64 {
	v, err := strconv.ParseInt(c.Amount, 10, 64)
	s.Require().NoErrorf(err, "unparsable amount %q", c.Amount)
	return v
}

// consumerChainInfoJSON mirrors the consumer-chain query fields the suite
// reads together.
type consumerChainInfoJSON struct {
	ChainId        string `json:"chain_id"`
	Phase          string `json:"phase"`
	OwnerAddress   string `json:"owner_address"`
	FeePoolAddress string `json:"fee_pool_address"`
}

// queryConsumerChainInfo returns the consumer-chain query for the consumer.
func (s *baseTestSuite) queryConsumerChainInfo(consumerID string) consumerChainInfoJSON {
	stdout, stderr, err := s.dockerExec(s.providerValRes[0].Container.ID, []string{
		providerBinary, "query", "vaasprovider", "consumer-chain", consumerID,
		"--home", providerHomePath,
		"--output", "json",
	})
	s.Require().NoErrorf(err, "failed to query consumer-chain %s: %s", consumerID, stderr.String())
	var info consumerChainInfoJSON
	s.Require().NoErrorf(json.Unmarshal(stdout.Bytes(), &info),
		"failed to decode consumer-chain %s: stdout=%s stderr=%s", consumerID, stdout.String(), stderr.String())
	return info
}

// providerConsumerPhaseAt returns the consumer's phase as of a past provider
// height, or "" while the query fails.
func (s *baseTestSuite) providerConsumerPhaseAt(consumerID string, height int64) string {
	stdout, _, err := s.dockerExec(s.providerValRes[0].Container.ID, []string{
		providerBinary, "query", "vaasprovider", "consumer-chain", consumerID,
		"--height", strconv.FormatInt(height, 10),
		"--home", providerHomePath,
		"--output", "json",
	})
	if err != nil {
		return ""
	}
	var res struct {
		Phase string `json:"phase"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &res); err != nil {
		return ""
	}
	return res.Phase
}

// firstProviderHeightInPhase returns the first provider height at which the
// consumer read back in phase, given it was not at height before (a height
// read before the transition was triggered) and is at the latest height.
func (s *baseTestSuite) firstProviderHeightInPhase(consumerID, phase string, before int64) int64 { //nolint:unparam // the suites drive one consumer; the helper stays generic
	latest, _ := s.providerLatestBlock()
	return s.firstProviderHeightWhere(before, latest, func(height int64) bool {
		return s.providerConsumerPhaseAt(consumerID, height) == phase
	})
}

// firstProviderHeightWhere returns, by bisection over [lo, hi], the first
// height at which pred holds. pred must be false at lo, true at hi and
// change once in between; the chain state it reads must be committed, which
// the state at a block's EndBlock is by the time the next block is queried.
func (s *baseTestSuite) firstProviderHeightWhere(lo, hi int64, pred func(height int64) bool) int64 {
	s.Require().Lessf(lo, hi, "bisection needs a range, got [%d, %d]", lo, hi)
	s.Require().Falsef(pred(lo), "the condition already held at height %d", lo)
	s.Require().Truef(pred(hi), "the condition does not hold at height %d", hi)
	for hi-lo > 1 {
		mid := lo + (hi-lo)/2
		if pred(mid) {
			hi = mid
		} else {
			lo = mid
		}
	}
	return hi
}

// providerLatestBlock returns the provider's latest block height and time.
func (s *baseTestSuite) providerLatestBlock() (int64, time.Time) {
	body, err := httpGet(s.providerRPCEndpoint() + "/status")
	s.Require().NoError(err, "failed to query the provider status")
	var status struct {
		Result struct {
			SyncInfo struct {
				LatestBlockHeight string    `json:"latest_block_height"`
				LatestBlockTime   time.Time `json:"latest_block_time"`
			} `json:"sync_info"`
		} `json:"result"`
	}
	s.Require().NoErrorf(json.Unmarshal(body, &status), "failed to decode the provider status: %s", string(body))
	height, err := strconv.ParseInt(status.Result.SyncInfo.LatestBlockHeight, 10, 64)
	s.Require().NoErrorf(err, "unparsable latest height %q", status.Result.SyncInfo.LatestBlockHeight)
	return height, status.Result.SyncInfo.LatestBlockTime
}

// providerBlockTime returns the header time of the provider block at height,
// which is the block time every keeper deadline in that block was judged
// against.
func (s *baseTestSuite) providerBlockTime(height int64) time.Time {
	body, err := httpGet(fmt.Sprintf("%s/block?height=%d", s.providerRPCEndpoint(), height))
	s.Require().NoErrorf(err, "failed to query provider block %d", height)
	var res struct {
		Result struct {
			Block struct {
				Header struct {
					Time time.Time `json:"time"`
				} `json:"header"`
			} `json:"block"`
		} `json:"result"`
	}
	s.Require().NoErrorf(json.Unmarshal(body, &res), "failed to decode provider block %d: %s", height, string(body))
	s.Require().Falsef(res.Result.Block.Header.Time.IsZero(), "provider block %d carries no time: %s", height, string(body))
	return res.Result.Block.Header.Time
}

// waitForProviderBlockTime blocks until the provider's latest block time is
// at or past t.
func (s *baseTestSuite) waitForProviderBlockTime(t time.Time) {
	s.Require().Eventuallyf(func() bool {
		_, now := s.providerLatestBlock()
		return !now.Before(t)
	}, time.Until(t)+time.Minute, time.Second, "the provider block time never reached %s", t)
}

// providerBlockHasEvent reports whether the provider block at height emitted
// an event of the given type carrying the attribute, among the events of its
// BeginBlock and EndBlock.
func (s *baseTestSuite) providerBlockHasEvent(height int64, eventType, attrKey, attrValue string) bool {
	body, err := httpGet(fmt.Sprintf("%s/block_results?height=%d", s.providerRPCEndpoint(), height))
	if err != nil {
		s.T().Logf("failed to query provider block results %d: %v", height, err)
		return false
	}
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
	if err := json.Unmarshal(body, &res); err != nil {
		s.T().Logf("failed to decode provider block results %d: %v", height, err)
		return false
	}
	for _, ev := range res.Result.FinalizeBlockEvents {
		if ev.Type != eventType {
			continue
		}
		for _, attr := range ev.Attributes {
			if attr.Key == attrKey && attr.Value == attrValue {
				return true
			}
		}
	}
	return false
}

// queryConsumerLastAck returns the time of the provider's last recorded VSC
// ack from the consumer.
func (s *baseTestSuite) queryConsumerLastAck(consumerID string) (time.Time, error) {
	body, err := httpGet(fmt.Sprintf("%s/vaas/provider/consumer_liveness/%s", s.providerRESTEndpoint(), consumerID))
	if err != nil {
		return time.Time{}, err
	}
	var res struct {
		LastAckTime time.Time `json:"last_ack_time"`
	}
	if err := json.Unmarshal(body, &res); err != nil {
		return time.Time{}, err
	}
	return res.LastAckTime, nil
}

// queryPendingEquivocationsAt is queryPendingEquivocations as of a past
// provider height.
func (s *baseTestSuite) queryPendingEquivocationsAt(consumerID string, height int64) ([]pendingEquivocationJSON, error) { //nolint:unparam // the suites drive one consumer; the helper stays generic
	return s.queryPendingEquivocationsWith(consumerID, "--height", strconv.FormatInt(height, 10))
}

// pendingDowntimeSlashesFor returns the consumer's pending downtime slashes
// naming the validator with the given bech32 consensus address, earliest
// maturity first; an entry whose maturity does not parse is left out.
func (s *baseTestSuite) pendingDowntimeSlashesFor(consumerID, consAddr string) []pendingDowntimeSlashJSON { //nolint:unparam // the suites drive one consumer; the helper stays generic
	return pendingDowntimeSlashesOf(s.queryPendingDowntimeSlashes(consumerID), consAddr)
}

// pendingDowntimeSlashesForAt is pendingDowntimeSlashesFor as of a past
// provider height.
func (s *baseTestSuite) pendingDowntimeSlashesForAt(consumerID, consAddr string, height int64) []pendingDowntimeSlashJSON {
	return pendingDowntimeSlashesOf(s.queryPendingDowntimeSlashesAt(consumerID, height), consAddr)
}

// pendingDowntimeSlashesOf picks the entries naming the validator with the
// given bech32 consensus address out of a query's entries, earliest maturity
// first.
func pendingDowntimeSlashesOf(all []pendingDowntimeSlashJSON, consAddr string) []pendingDowntimeSlashJSON {
	var entries []pendingDowntimeSlashJSON
	var maturities []time.Time
	for _, p := range all {
		addr, err := consAddrFromBase64(p.ProviderConsAddr)
		if err != nil || addr != consAddr {
			continue
		}
		maturity, err := time.Parse(time.RFC3339Nano, p.MaturesAt)
		if err != nil {
			continue
		}
		entries = append(entries, p)
		maturities = append(maturities, maturity)
	}
	sort.Sort(byMaturity{entries: entries, maturities: maturities})
	return entries
}

// byMaturity orders pending downtime slashes by their parsed maturity.
type byMaturity struct {
	entries    []pendingDowntimeSlashJSON
	maturities []time.Time
}

func (b byMaturity) Len() int           { return len(b.entries) }
func (b byMaturity) Less(i, j int) bool { return b.maturities[i].Before(b.maturities[j]) }
func (b byMaturity) Swap(i, j int) {
	b.entries[i], b.entries[j] = b.entries[j], b.entries[i]
	b.maturities[i], b.maturities[j] = b.maturities[j], b.maturities[i]
}

// withheldFeeRecordFor returns the consumer's withheld fee record for the
// validator with the given bech32 consensus address, if there is one.
func (s *baseTestSuite) withheldFeeRecordFor(consumerID, consAddr string) (withheldFeeRecordJSON, bool) { //nolint:unparam // the suites drive one consumer; the helper stays generic
	return withheldFeeRecordOf(s.queryWithheldFeeRecords(consumerID), consAddr)
}

// withheldFeeRecordForAt is withheldFeeRecordFor as of a past provider
// height.
func (s *baseTestSuite) withheldFeeRecordForAt(consumerID, consAddr string, height int64) (withheldFeeRecordJSON, bool) { //nolint:unparam // the suites drive one consumer; the helper stays generic
	return withheldFeeRecordOf(s.queryWithheldFeeRecordsAt(consumerID, height), consAddr)
}

// withheldFeeRecordOf picks the record naming the validator with the given
// bech32 consensus address out of a query's records.
func withheldFeeRecordOf(records []withheldFeeRecordJSON, consAddr string) (withheldFeeRecordJSON, bool) {
	for _, r := range records {
		if addr, err := consAddrFromBase64(r.ProviderConsAddr); err == nil && addr == consAddr {
			return r, true
		}
	}
	return withheldFeeRecordJSON{}, false
}

// mustTxEventAttribute reads one attribute of one event type from a
// committed transaction, given its `-o json` broadcast response.
func (s *baseTestSuite) mustTxEventAttribute(broadcastOut []byte, eventType, key string) string {
	var bres struct {
		TxHash string `json:"txhash"`
	}
	s.Require().NoErrorf(json.Unmarshal(broadcastOut, &bres), "decode broadcast response: %s", string(broadcastOut))
	s.Require().NotEmpty(bres.TxHash)

	var value, lastOut string
	s.Require().Eventuallyf(func() bool {
		out, _, err := s.dockerExec(s.providerValRes[0].Container.ID, []string{
			providerBinary, "query", "tx", bres.TxHash,
			"--home", providerHomePath,
			"--output", "json",
		})
		if err != nil || out.Len() == 0 {
			return false
		}
		lastOut = out.String()
		v, ok := txEventAttribute(out.Bytes(), eventType, key)
		value = v
		return ok
	}, 30*time.Second, 2*time.Second,
		"tx %s carries no %s.%s attribute; last query output: %s", bres.TxHash, eventType, key, lastOut)
	return value
}

// tryProviderValidator returns the validator's x/staking record on the
// provider, whatever its bond status. It returns an error instead of failing
// the test so it can be called from inside an Eventually condition.
func (s *baseTestSuite) tryProviderValidator(valoper string) (stakingtypes.Validator, error) {
	vals, err := s.queryProviderValidators()
	if err != nil {
		return stakingtypes.Validator{}, fmt.Errorf("querying provider validators: %w", err)
	}
	for _, v := range vals {
		if v.OperatorAddress == valoper {
			return v, nil
		}
	}
	return stakingtypes.Validator{}, fmt.Errorf("no provider validator with operator address %s", valoper)
}

// tryValidatorTokens is mustValidatorTokens for Eventually conditions: it
// returns an error instead of failing the test.
func (s *baseTestSuite) tryValidatorTokens(valoper string) (int64, error) {
	v, err := s.tryProviderValidator(valoper)
	if err != nil {
		return 0, err
	}
	if !v.Tokens.IsInt64() {
		return 0, fmt.Errorf("validator %s holds %s tokens, more than an int64", valoper, v.Tokens)
	}
	return v.Tokens.Int64(), nil
}

// mustCountConsumerNetValidators returns the size of the consumer's current
// consensus validator set.
func (s *baseTestSuite) mustCountConsumerNetValidators() int {
	vals, err := s.queryConsumerNetValidators()
	s.Require().NoError(err, "failed to query the consumer's validator set")
	return len(vals)
}

// waitForProviderHeight blocks until the provider's latest height is at or
// past height.
func (s *baseTestSuite) waitForProviderHeight(height int64) {
	s.Require().Eventuallyf(func() bool {
		h, _ := s.providerLatestBlock()
		return h >= height
	}, 3*time.Minute, time.Second, "the provider never reached height %d", height)
}
