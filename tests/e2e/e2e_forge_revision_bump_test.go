package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	clienttypes "github.com/cosmos/ibc-go/v10/modules/core/02-client/types"
)

// Client statuses as the provider's ibc client status query prints them.
const (
	clientActive  = "Active"
	clientExpired = "Expired"
)

// Provider events the revision bump drill reads, with the reason the pause
// event names for a declared client tracking another chain id.
const (
	mismatchEventType   = "vaas_consumer_client_chain_id_mismatch"
	pausedEventType     = "vaas_consumer_paused"
	resumedEventType    = "vaas_consumer_resumed"
	pauseReasonMismatch = "PAUSE_REASON_CLIENT_CHAIN_ID_MISMATCH"
)

// testRevisionBumpIsServedOnlyAfterTheRename: the consumer restarts from an
// export under the next revision of its chain id, the upgrade pattern of a
// Cosmos chain. The provider's declared client cannot verify the renamed
// chain's headers and expires; governance recovers it onto a client of the
// renamed chain, which leaves it tracking a chain id the consumer is not
// registered under. The next provider block pauses the consumer for the
// mismatch, says so once, and serves it nothing while it stays paused. One
// governance proposal renames the consumer to the client's chain id (a
// MsgUpdateConsumer signed by the governance authority) and resumes it: the
// consumer is launched again, the packets flow and the renamed chain follows
// the provider's validator set; and a double vote signed over the old chain
// id is still accepted, since the old id stays an evidence domain of the
// consumer.
func (s *ByzantineConsumerIntegrationTestSuite) testRevisionBumpIsServedOnlyAfterTheRename() {
	s.Run("a consumer that bumped its chain id is paused until governance renames and resumes it", func() {
		const consumerID = "0"
		subject := s.queryConsumerClientID(consumerID)
		s.Require().Equalf(phaseLaunched, s.queryProviderConsumerPhase(consumerID), "the bump starts from a launched consumer")
		s.Require().Equalf(clientActive, s.clientStatus(subject), "the bump starts from an active declared client")
		pinned := s.consumerProviderClientID()
		oldChainID := s.cfg.consumerChainID
		newChainID := nextRevisionChainID(oldChainID)

		// 1. The consumer restarts under the next revision of its chain id,
		// continuing its heights.
		haltedAt := s.bumpConsumerRevision(newChainID)
		s.T().Logf("the consumer halted at height %d as %s and continues as %s", haltedAt, oldChainID, newChainID)

		// 2. The declared client cannot follow: the renamed chain's headers
		// carry a chain id and a revision it does not track, so it goes
		// unupdated and expires with its trusting period (two thirds of the
		// consumer's unbonding period, create_consumer_forge.json).
		s.T().Logf("waiting for the declared client %s to expire...", subject)
		s.Require().Eventuallyf(func() bool {
			status, err := s.tryClientStatus(subject)
			return err == nil && status == clientExpired
		}, 12*time.Minute, 10*time.Second, "the declared client %s never expired", subject)
		s.Require().Equalf(phaseLaunched, s.queryProviderConsumerPhase(consumerID), "an expired client changes no phase")

		// The relayer is set up again for the renamed chain, as an operator
		// would set theirs: a fresh instance keyed by the new chain id, whose
		// path creates the substitute, a client of the renamed chain on the
		// provider, and a path on the declared client and the consumer's
		// pinned client, which the relayer adopts once the recovery below
		// makes the declared client track the renamed chain.
		s.stopTSRelayer()
		s.cfg.consumerChainID = newChainID
		s.consumer.id = newChainID
		s.setupTSRelayer()
		substitute := s.providerClientOfTheRelayerPath()
		s.Require().Equalf(newChainID, s.clientChainID(substitute), "the substitute must track the renamed chain")
		s.Require().Equalf(clientActive, s.clientStatus(substitute), "the substitute must be active")
		s.tsRelayerAddExistingPath(subject, pinned)

		// 3. Governance recovers the declared client onto the substitute.
		// ibc-go copies the substitute's chain id into it, so the declared
		// client now tracks a chain id the consumer is not registered under,
		// and the next provider BeginBlock pauses the consumer for it.
		recoverySubmittedAt, err := s.queryProviderBlockHeight()
		s.Require().NoError(err, "failed to read the provider height")
		s.T().Logf("recovering the expired client %s onto the substitute %s", subject, substitute)
		s.submitAndPassProposal(s.recoverClientProposalJSON(subject, substitute))
		s.Require().Equalf(clientActive, s.clientStatus(subject), "the recovered client must be active")
		s.Require().Equalf(newChainID, s.clientChainID(subject), "the recovered client must track the renamed chain")
		s.Require().Equalf(oldChainID, s.queryConsumerChainID(consumerID), "a recovery renames no consumer")
		s.Require().Eventuallyf(func() bool { return s.queryProviderConsumerPhase(consumerID) == phasePaused },
			time.Minute, 2*time.Second, "the provider never paused the consumer whose client tracks another chain id")
		pausedAt := s.requireMismatchPause(recoverySubmittedAt, consumerID, map[string]string{
			"consumer_id":        consumerID,
			"consumer_client_id": subject,
			"client_chain_id":    newChainID,
			"consumer_chain_id":  oldChainID,
		})
		s.T().Logf("the consumer was paused for the chain id mismatch at provider height %d", pausedAt)
		// No accusation is pending against the paused consumer. None was
		// before the recovery either (four live validators, the lie long
		// over, val1's accusation challenged away), so this shows the pause
		// adding none, not discarding any; the discard is pinned by
		// TestPauseConsumersWithMismatchedClients in the provider keeper's
		// tests.
		s.Require().Emptyf(s.queryPendingDowntimeSlashes(consumerID), "no downtime accusation may be pending against the paused consumer")

		// The paused consumer is served nothing, and the mismatch check does
		// not act on it again meanwhile: the pause is reported once. The
		// liveness sweep is out of this window's reach whatever it did, the
		// suite's grace being two thirds of the provider's 21-day unbonding
		// period, so the watch says nothing about it. The pause also
		// schedules the consumer's auto-stop max_pause_duration out, which
		// this suite leaves at its default of 45 days.
		sentBefore, appliedBefore := s.requireConsumerNotServed(subject, consumerID, pausedAt)

		// 4. One governance proposal renames the consumer to the chain id its
		// declared client tracks, which the owner cannot do, and resumes it:
		// the resume sends the consumer a snapshot of the provider's set.
		renameSubmittedAt, err := s.queryProviderBlockHeight()
		s.Require().NoError(err, "failed to read the provider height")
		s.submitAndPassProposal(s.renameAndResumeProposalJSON(consumerID, newChainID))
		s.Require().Equalf(phaseLaunched, s.queryProviderConsumerPhase(consumerID), "the proposal must resume the consumer")
		s.Require().Equalf(newChainID, s.queryConsumerChainID(consumerID), "the consumer must be registered under the renamed chain id")
		renamedAt := s.requireRenameEvent(renameSubmittedAt, consumerID, oldChainID, newChainID)
		s.Require().Equalf([]int64{renamedAt}, s.providerBlockEventHeights(renameSubmittedAt, renamedAt, resumedEventType, map[string]string{"consumer_id": consumerID}),
			"the consumer must be resumed in the block that renames it")
		s.T().Logf("the consumer was renamed from %s to %s and resumed at provider height %d", oldChainID, newChainID, renamedAt)

		// The retired id is on record with the client's height at the rename,
		// in the renamed chain's revision: below it, evidence signed over the
		// old id is still verified (the double vote at the end of the drill).
		previous := s.queryConsumerPreviousChainIDs(consumerID)
		s.Require().Lenf(previous, 1, "one rename, one retired chain id: %+v", previous)
		s.Require().Equal(oldChainID, previous[0].ChainID)
		s.Require().Equal(strconv.FormatUint(clienttypes.ParseChainID(newChainID), 10), previous[0].Boundary.RevisionNumber,
			"the boundary is in the renamed chain's revision")
		boundaryHeight, err := strconv.ParseInt(previous[0].Boundary.RevisionHeight, 10, 64)
		s.Require().NoError(err)
		s.Require().Positive(boundaryHeight, "the boundary is the client's height at the rename")
		s.Require().LessOrEqual(boundaryHeight, s.clientLatestHeight(subject), "the boundary cannot be past the client")

		s.Require().Eventuallyf(func() bool {
			sent, err := s.tryNextSequenceSend(subject)
			return err == nil && sent > sentBefore
		}, 2*time.Minute, 3*time.Second, "the provider sent no VSC packet over client %s after the rename", subject)
		s.Require().Eventuallyf(func() bool { return s.highestAppliedVscID() > appliedBefore },
			3*time.Minute, 3*time.Second, "the renamed consumer applied no VSC packet after the rename")
		s.waitForConsumerSync(consumerID)
		s.requireConsumerFollowsADelegation(consumerID)
		latest, err := s.queryProviderBlockHeight()
		s.Require().NoError(err, "failed to read the provider height")
		s.Require().Emptyf(s.providerBlockEventHeights(pausedAt+1, latest, mismatchEventType, map[string]string{"consumer_id": consumerID}),
			"the provider reported the chain id mismatch again after the pause")
		s.Require().Emptyf(s.providerBlockEventHeights(pausedAt+1, latest, pausedEventType, map[string]string{"consumer_id": consumerID}),
			"the provider paused the renamed consumer again")

		// 5. The old chain id stays an evidence domain of the consumer: a
		// double vote signed over it at a height of the old chain is still
		// accepted. val2 signs it, jailed since the fabricated double-sign,
		// so the chains keep three signing validators; the punishment queues
		// beside its first, and the removal that ends the suite cancels both.
		val2 := s.validator(2)
		jailed, tokensBefore := s.stakingValidatorState(val2.valoper)
		s.Require().True(jailed, "val2 is jailed since the fabricated double-sign")
		s.Require().Equalf(1, s.pendingEquivocationsOf(consumerID, val2.consAddr), "val2 carries the fabricated double-sign's punishment")
		evidencePath, headerPath := s.fabricateDoubleVote(s.consensusPrivKey(2), oldChainID, haltedAt-5, "forge-val2-old-chain-id")
		s.submitDoubleVoteEvidence(consumerID, evidencePath, headerPath)
		s.Require().Eventuallyf(func() bool {
			n, err := s.tryPendingEquivocationsOf(consumerID, val2.consAddr)
			return err == nil && n == 2
		}, time.Minute, 2*time.Second, "the double vote signed over the old chain id must queue a second punishment against val2")
		_, tokensAfter := s.stakingValidatorState(val2.valoper)
		s.Require().Equalf(tokensBefore, tokensAfter, "the punishment must be deferred, not executed")
	})
}

// nextRevisionChainID returns the chain id of the next revision: the IBC
// revision number is the numeric suffix of the chain id (none is revision
// 0), so the suffix is bumped, or appended.
func nextRevisionChainID(chainID string) string {
	revision := clienttypes.ParseChainID(chainID)
	if clienttypes.IsRevisionFormat(chainID) {
		chainID = strings.TrimSuffix(chainID, fmt.Sprintf("-%d", revision))
	}
	return fmt.Sprintf("%s-%d", chainID, revision+1)
}

// bumpConsumerRevision takes the consumer through a revision bump: every
// node is stopped, val0 last so its data dir holds the height the chain
// halted at, the chain's state is exported at that height, the export's chain
// id is rewritten to newChainID, and every node is recreated from the
// rewritten export, continuing the heights under the new chain id. The nodes
// run the reference consumer again: the forge's lie is long past. Returns the
// height the chain halted at, its last under the old chain id.
func (s *ByzantineConsumerIntegrationTestSuite) bumpConsumerRevision(newChainID string) int64 {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	// The chain halts once fewer than two thirds of its validators remain,
	// so val0, which keeps committing until then, holds the halt height.
	for i := s.validatorCount() - 1; i > 0; i-- {
		s.stopConsumerNode(i)
	}
	s.stopConsumerNode(0)

	s.T().Log("exporting the consumer's state via an ephemeral container...")
	exported := s.exportConsumerGenesis(ctx, s.consumerHome(0))
	exported, initialHeight := s.rewriteGenesisChainID(exported, newChainID)

	oldHomes := slices.Clone(s.consumer.homes)
	for i, r := range s.consumerValRes {
		s.Require().NoErrorf(s.dkrPool.Purge(r), "failed to remove consumer val%d's container", i)
	}
	s.T().Logf("starting %d consumer nodes as %s from height %d...", len(oldHomes), newChainID, initialHeight)
	for i := range s.consumerValRes {
		s.consumerValRes[i] = s.startRestartedConsumerNode(i, s.bootstrapRestartedConsumerDir(oldHomes[i], exported))
	}
	waitCtx, waitCancel := context.WithTimeout(ctx, 3*time.Minute)
	defer waitCancel()
	err := s.waitForChainHeight(waitCtx, "http://localhost:"+s.cfg.consumerRPCPort, initialHeight+1)
	if err != nil {
		s.logNodeTails(s.consumerValRes, 80)
	}
	s.Require().NoError(err, "the renamed consumer produced no block from the rewritten export")
	s.waitForNodesCaughtUp(s.consumerValRes, consumerBinary, consumerHomePath, 3*time.Minute)
	s.Require().Equalf(newChainID, s.consumerNodeChainID(), "the consumer must run under the new chain id")
	return initialHeight - 1
}

// rewriteGenesisChainID rewrites the chain id of an exported genesis, leaving
// every other field as exported, and returns it with the export's initial
// height.
func (s *ByzantineConsumerIntegrationTestSuite) rewriteGenesisChainID(exported []byte, chainID string) ([]byte, int64) {
	var genesis map[string]json.RawMessage
	s.Require().NoError(json.Unmarshal(exported, &genesis), "the exported genesis is not valid JSON")
	var initialHeight json.Number
	s.Require().NoError(json.Unmarshal(genesis["initial_height"], &initialHeight), "the exported genesis has no initial height")
	height, err := initialHeight.Int64()
	s.Require().NoErrorf(err, "the exported initial height: %q", initialHeight)
	chainIDJSON, err := json.Marshal(chainID)
	s.Require().NoError(err)
	genesis["chain_id"] = chainIDJSON
	out, err := json.Marshal(genesis)
	s.Require().NoError(err, "failed to encode the rewritten genesis")
	return out, height
}

// consumerNodeChainID reads the chain id the consumer's val0 reports.
func (s *ByzantineConsumerIntegrationTestSuite) consumerNodeChainID() string {
	body, err := httpGet(s.consumerRPCEndpoint() + "/status")
	s.Require().NoError(err, "failed to read the consumer's status")
	var res struct {
		Result struct {
			NodeInfo struct {
				Network string `json:"network"`
			} `json:"node_info"`
		} `json:"result"`
	}
	s.Require().NoErrorf(json.Unmarshal(body, &res), "failed to decode the consumer's status: %s", body)
	return res.Result.NodeInfo.Network
}

// providerClientOfTheRelayerPath returns the provider-side client of the
// relayer's one path between the chains.
func (s *ByzantineConsumerIntegrationTestSuite) providerClientOfTheRelayerPath() string {
	paths := s.tsRelayerDumpPaths()
	s.Require().Lenf(paths, 1, "the relayer must hold one path, got %d", len(paths))
	switch p := paths[0]; s.cfg.providerChainID {
	case p.ChainIdA:
		return p.ClientA
	case p.ChainIdB:
		return p.ClientB
	}
	s.Require().Fail("the relayer's path is not between the chains")
	return ""
}

// clientChainID returns the chain id a provider-side IBC client tracks.
func (s *ByzantineConsumerIntegrationTestSuite) clientChainID(clientID string) string {
	stdout, stderr, err := s.dockerExec(s.providerValRes[0].Container.ID, []string{
		providerBinary, "query", "ibc", "client", "state", clientID, "--home", providerHomePath, "-o", "json",
	})
	s.Require().NoErrorf(err, "failed to query client %s: %s", clientID, stderr.String())
	var res struct {
		ClientState struct {
			ChainID string `json:"chain_id"`
		} `json:"client_state"`
	}
	s.Require().NoErrorf(json.Unmarshal(stdout.Bytes(), &res), "failed to decode client %s: %s", clientID, stdout.String())
	return res.ClientState.ChainID
}

// highestAppliedVscID returns the highest valset update id the consumer's
// val0 has applied since its container started, read from its log; zero
// before it applies any.
func (s *ByzantineConsumerIntegrationTestSuite) highestAppliedVscID() uint64 {
	var highest uint64
	for _, id := range appliedVscIDs(s.consumerLogs()) {
		highest = max(highest, id)
	}
	return highest
}

// requireMismatchPause finds, in the provider's blocks from height from on,
// the pause of the consumer for the chain id mismatch, and returns the height
// of the block that paused it. The pause is reported once: one
// vaas_consumer_paused event naming the mismatch reason, followed in the same
// block by one vaas_consumer_client_chain_id_mismatch event carrying the
// mismatch attributes.
func (s *ByzantineConsumerIntegrationTestSuite) requireMismatchPause(from int64, consumerID string, mismatch map[string]string) int64 {
	latest, err := s.queryProviderBlockHeight()
	s.Require().NoError(err, "failed to read the provider height")
	paused := s.providerBlockEventHeights(from, latest, pausedEventType, map[string]string{"consumer_id": consumerID})
	s.Require().Lenf(paused, 1, "one pause of consumer %s expected in provider blocks %d..%d, got %v", consumerID, from, latest, paused)
	pausedAt := paused[0]
	s.Require().Equalf([]int64{pausedAt},
		s.providerBlockEventHeights(from, latest, pausedEventType, map[string]string{"consumer_id": consumerID, "reason": pauseReasonMismatch}),
		"the pause of consumer %s must name the chain id mismatch", consumerID)
	s.Require().Equalf([]int64{pausedAt}, s.providerBlockEventHeights(from, latest, mismatchEventType, mismatch),
		"one chain id mismatch event with %v expected, in the block that paused the consumer", mismatch)

	pauseIndex, mismatchIndex := -1, -1
	for i, ev := range s.providerBlockEvents(pausedAt) {
		switch {
		case ev.Type == pausedEventType && ev.Attributes["consumer_id"] == consumerID:
			pauseIndex = i
		case ev.Type == mismatchEventType && blockEventMatches(ev, mismatch):
			mismatchIndex = i
		}
	}
	s.Require().Lessf(pauseIndex, mismatchIndex, "the mismatch event must follow the pause event in block %d", pausedAt)
	return pausedAt
}

// requireConsumerNotServed watches the provider over a window of epochs after
// the consumer was paused at height pausedAt and requires that it serves the
// consumer nothing: the declared client's send sequence stays put, the
// consumer's applied valset update id stops advancing, the consumer stays
// paused, and the provider neither pauses it again nor repeats the mismatch
// event. The relayer may still be delivering the packets the provider sent
// before the client expired, which the consumer applies late; the watch
// starts over while that backlog lands. Returns the send sequence and the
// applied valset update id the window settled at.
func (s *ByzantineConsumerIntegrationTestSuite) requireConsumerNotServed(subject, consumerID string, pausedAt int64) (sent, applied uint64) {
	const epochs = 6
	const window = epochs * fourValidatorBlocksPerEpoch
	deadline := time.Now().Add(5 * time.Minute)
	for {
		from, err := s.queryProviderBlockHeight()
		s.Require().NoError(err, "failed to read the provider height")
		sent = s.nextSequenceSend(subject)
		applied = s.highestAppliedVscID()
		to := from + window
		s.T().Logf("watching provider blocks %d..%d: client %s at send sequence %d, the consumer at valset update id %d",
			from+1, to, subject, sent, applied)

		waitCtx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		err = s.waitForChainHeight(waitCtx, "http://localhost:"+s.cfg.providerRPCPort, to)
		cancel()
		s.Require().NoError(err, "the provider stopped producing blocks")

		s.Require().Equalf(sent, s.nextSequenceSend(subject),
			"the provider sent a VSC packet over client %s to the paused consumer", subject)
		s.Require().Equalf(phasePaused, s.queryProviderConsumerPhase(consumerID), "the consumer must stay paused")
		for _, eventType := range []string{mismatchEventType, pausedEventType} {
			s.Require().Emptyf(s.providerBlockEventHeights(pausedAt+1, to, eventType, map[string]string{"consumer_id": consumerID}),
				"the provider emitted %s again for the paused consumer", eventType)
		}

		if now := s.highestAppliedVscID(); now != applied {
			s.T().Logf("the consumer applied VSC packets sent before its client expired (valset update id %d to %d); watching again", applied, now)
			s.Require().Truef(time.Now().Before(deadline), "the consumer's valset update id never settled")
			continue
		}
		return sent, applied
	}
}

// requireRenameEvent finds, in the provider's blocks from height from on,
// the update event of the consumer's rename from oldChainID to newChainID,
// and returns the height of the block that executed it.
func (s *ByzantineConsumerIntegrationTestSuite) requireRenameEvent(from int64, consumerID, oldChainID, newChainID string) int64 {
	latest, err := s.queryProviderBlockHeight()
	s.Require().NoError(err, "failed to read the provider height")
	heights := s.providerBlockEventHeights(from, latest, "vaas_update_consumer", map[string]string{
		"consumer_id":       consumerID,
		"previous_chain_id": oldChainID,
		"consumer_chain_id": newChainID,
	})
	s.Require().NotEmptyf(heights, "no vaas_update_consumer event renaming consumer %s from %s to %s in provider blocks %d..%d",
		consumerID, oldChainID, newChainID, from, latest)
	return heights[0]
}

// requireConsumerFollowsADelegation changes the provider's validator set with
// a delegation to val0 and requires the consumer's consensus validator set to
// converge on the provider's, matched by consensus address through the
// provider's key assignments.
func (s *ByzantineConsumerIntegrationTestSuite) requireConsumerFollowsADelegation(consumerID string) {
	providerValsBefore, err := s.queryProviderNetValidators()
	s.Require().NoError(err, "failed to query the provider's validators before the delegation")
	powerBefore := powerSum(s.valPowerMap(providerValsBefore))

	val0 := s.validator(0)
	s.T().Logf("delegating to val0 (%s) on the provider to change its validator set...", val0.valoper)
	stdout, stderr, err := s.dockerExec(s.providerValRes[0].Container.ID, []string{
		// At least one unit of voting power (tokens / powerReduction,
		// default 1e6) so the validator's integer power actually changes.
		providerBinary, "tx", "staking", "delegate", val0.valoper, "100000000" + bondDenom,
		"--from", "user",
		"--home", providerHomePath,
		"--keyring-backend", "test",
		"--chain-id", s.cfg.providerChainID,
		"--fees", "10000" + bondDenom,
		"-y",
		"-o", "json",
	})
	s.Require().NoErrorf(err, "failed to delegate on the provider: stderr=%s", stderr.String())
	s.requireTxCommitted(stdout.Bytes())
	s.Require().Eventuallyf(func() bool {
		vals, err := s.queryProviderNetValidators()
		return err == nil && powerSum(s.valPowerMap(vals)) > powerBefore
	}, 30*time.Second, 2*time.Second, "the provider's voting power did not increase after the delegation")

	s.T().Log("waiting for the renamed consumer to converge on the provider's validator set...")
	assignedPairs := s.queryAllPairsValConsAddr(consumerID)
	s.Require().Eventuallyf(func() bool {
		consumerVals, err := s.queryConsumerNetValidators()
		if err != nil {
			return false
		}
		providerVals, err := s.queryProviderNetValidators()
		if err != nil {
			return false
		}
		consumerSet := s.valAddrPowerMap(consumerVals)
		expectedSet := make(map[string]int64, len(providerVals))
		for _, v := range providerVals {
			addr := v.Address
			if assigned, ok := assignedPairs[addr]; ok {
				addr = assigned
			}
			expectedSet[addr] = v.VotingPower
		}
		s.T().Logf("consumer valset: %v (want %v)", consumerSet, expectedSet)
		return len(consumerSet) > 0 && maps.Equal(consumerSet, expectedSet)
	}, 3*time.Minute, 3*time.Second, "the renamed consumer never converged on the provider's validator set")
}

// recoverClientProposalJSON builds the proposal that recovers an expired or
// frozen provider-side client onto a substitute: ibc-go's MsgRecoverClient.
func (s *ByzantineConsumerIntegrationTestSuite) recoverClientProposalJSON(subject, substitute string) string {
	return fmt.Sprintf(`{
  "messages": [{
    "@type": "/ibc.core.client.v1.MsgRecoverClient",
    "subject_client_id": %q,
    "substitute_client_id": %q,
    "signer": %q
  }],
  "metadata": "ipfs://test",
  "deposit": "10000000%s",
  "title": "Recover the renamed consumer's client",
  "summary": "revision bump"
}`, subject, substitute, s.queryGovAuthority(), bondDenom)
}

// renameAndResumeProposalJSON builds the proposal that brings back a
// consumer paused because its declared client tracks another chain id:
// MsgUpdateConsumer signed by the governance authority, the one signer
// allowed to rename a launched or paused consumer, renames it to the chain id
// the client tracks, then MsgResumeConsumer resumes it, in one proposal so
// neither lands without the other.
func (s *ByzantineConsumerIntegrationTestSuite) renameAndResumeProposalJSON(consumerID, chainID string) string {
	authority := s.queryGovAuthority()
	return fmt.Sprintf(`{
  "messages": [{
    "@type": "/vaas.provider.v1.MsgUpdateConsumer",
    "owner": %q,
    "consumer_id": %q,
    "new_chain_id": %q
  }, {
    "@type": "/vaas.provider.v1.MsgResumeConsumer",
    "authority": %q,
    "consumer_id": %q
  }],
  "metadata": "ipfs://test",
  "deposit": "10000000%s",
  "title": "Rename the consumer to the chain id its client tracks and resume it",
  "summary": "revision bump"
}`, authority, consumerID, chainID, authority, consumerID, bondDenom)
}
