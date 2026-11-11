package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	clienttypes "github.com/cosmos/ibc-go/v10/modules/core/02-client/types"
)

// Client statuses as the provider's ibc client status query prints them.
const (
	clientActive  = "Active"
	clientExpired = "Expired"
)

// forgeBlocksPerEpoch is the provider's blocks_per_epoch in the suites
// running several validators (patchFourValidatorProviderGenesis): the
// provider queues and sends VSC packets at every multiple of it.
const forgeBlocksPerEpoch = 5

// mismatchEventType is the event the provider emits, once per epoch, for a
// launched consumer whose declared client tracks another chain id.
const mismatchEventType = "vaas_consumer_client_chain_id_mismatch"

// testRevisionBumpIsServedOnlyAfterTheRename: the consumer restarts from an
// export under the next revision of its chain id, the upgrade pattern of a
// Cosmos chain. The provider's declared client cannot verify the renamed
// chain's headers and expires; governance recovers it onto a client of the
// renamed chain, which leaves it tracking a chain id the consumer is not
// registered under, and the provider refuses to serve the consumer until
// governance renames it too: no VSC packet leaves the provider over the
// client meanwhile, and every epoch says why. The rename, a MsgUpdateConsumer
// signed by the governance authority, puts the record back in lockstep with
// the client: the consumer stays launched, the packets flow and the renamed
// chain follows the provider's validator set; and a double vote signed over
// the old chain id is still accepted, since the old id stays an evidence
// domain of the consumer.
func (s *ByzantineConsumerIntegrationTestSuite) testRevisionBumpIsServedOnlyAfterTheRename() {
	s.Run("a consumer that bumped its chain id is served again only once governance renames it", func() {
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
		s.Require().Eventuallyf(func() bool { return s.clientStatus(subject) == clientExpired },
			12*time.Minute, 10*time.Second, "the declared client %s never expired", subject)
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
		s.removeContainerByName(s.tsRelayerContainerName())
		s.setupTSRelayer()
		substitute := s.providerClientOfTheRelayerPath()
		s.Require().Equalf(newChainID, s.clientChainID(substitute), "the substitute must track the renamed chain")
		s.Require().Equalf(clientActive, s.clientStatus(substitute), "the substitute must be active")
		s.tsRelayerAddExistingPath(subject, pinned)

		// 3. Governance recovers the declared client onto the substitute.
		// ibc-go copies the substitute's chain id into it, so the declared
		// client now tracks a chain id the consumer is not registered under:
		// the provider serves no VSC packet over it, and says so every epoch.
		s.T().Logf("recovering the expired client %s onto the substitute %s", subject, substitute)
		s.submitAndPassProposal(s.recoverClientProposalJSON(subject, substitute))
		s.Require().Equalf(clientActive, s.clientStatus(subject), "the recovered client must be active")
		s.Require().Equalf(newChainID, s.clientChainID(subject), "the recovered client must track the renamed chain")
		s.Require().Equalf(phaseLaunched, s.queryProviderConsumerPhase(consumerID), "a recovery changes no phase")
		s.Require().Equalf(oldChainID, s.queryConsumerChainID(consumerID), "a recovery renames no consumer")
		sentBefore, appliedBefore := s.requireConsumerNotServed(subject, map[string]string{
			"consumer_id":        consumerID,
			"consumer_client_id": subject,
			"client_chain_id":    newChainID,
			"consumer_chain_id":  oldChainID,
		})

		// 4. Governance renames the consumer to the chain id its declared
		// client tracks, which the owner cannot do; the provider serves it
		// again from the next epoch.
		renameSubmittedAt, err := s.queryProviderBlockHeight()
		s.Require().NoError(err, "failed to read the provider height")
		s.submitAndPassProposal(s.renameConsumerProposalJSON(consumerID, newChainID))
		s.Require().Equalf(phaseLaunched, s.queryProviderConsumerPhase(consumerID), "a rename changes no phase")
		s.Require().Equalf(newChainID, s.queryConsumerChainID(consumerID), "the consumer must be registered under the renamed chain id")
		renamedAt := s.requireRenameEvent(renameSubmittedAt, consumerID, oldChainID, newChainID)
		s.T().Logf("the consumer was renamed from %s to %s at provider height %d", oldChainID, newChainID, renamedAt)

		s.Require().Eventuallyf(func() bool { return s.nextSequenceSend(subject) > sentBefore },
			2*time.Minute, 3*time.Second, "the provider sent no VSC packet over client %s after the rename", subject)
		s.Require().Eventuallyf(func() bool { return s.highestAppliedVscID() > appliedBefore },
			3*time.Minute, 3*time.Second, "the renamed consumer applied no VSC packet after the rename")
		s.waitForConsumerSync(consumerID)
		s.requireConsumerFollowsADelegation(consumerID)
		latest, err := s.queryProviderBlockHeight()
		s.Require().NoError(err, "failed to read the provider height")
		s.Require().Emptyf(s.providerBlockEventHeights(renamedAt+1, latest, mismatchEventType, map[string]string{"consumer_id": consumerID}),
			"the provider still reports a chain id mismatch after the rename")

		// 5. The old chain id stays an evidence domain of the consumer: a
		// double vote signed over it at a height of the old chain is still
		// accepted. val3 signs it, as a malicious binary on its node could
		// have; the removal that ends the suite cancels the punishment it
		// queues, like val2's.
		val3 := s.validator(3)
		_, tokensBefore := s.stakingValidatorState(val3.valoper)
		s.Require().Zero(s.pendingEquivocationsOf(consumerID, val3.consAddr), "val3 is already accused of a double-sign")
		evidencePath, headerPath := s.fabricateDoubleVote(s.consensusPrivKey(3), oldChainID, haltedAt-5, "forge-val3-old-chain-id")
		s.submitDoubleVoteEvidence(consumerID, evidencePath, headerPath)
		s.Require().Eventuallyf(func() bool {
			jailed, _ := s.stakingValidatorState(val3.valoper)
			return jailed
		}, time.Minute, 2*time.Second, "val3 was not jailed for the double vote signed over the old chain id")
		s.Require().Equalf(1, s.pendingEquivocationsOf(consumerID, val3.consAddr),
			"the double vote signed over the old chain id must queue one punishment against val3")
		_, tokensAfter := s.stakingValidatorState(val3.valoper)
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

// requireConsumerNotServed watches the provider over a window of epochs and
// requires that it serves the consumer nothing: the declared client's send
// sequence stays put, every epoch boundary in the window emits the chain id
// mismatch event with the given attributes, and the consumer's applied valset
// update id stops advancing. The relayer may still be delivering the packets
// the provider sent before the client expired, which the consumer applies
// late; the watch starts over while that backlog lands. Returns the send
// sequence and the applied valset update id the window settled at.
func (s *ByzantineConsumerIntegrationTestSuite) requireConsumerNotServed(subject string, mismatch map[string]string) (sent, applied uint64) {
	const epochs = 6
	const window = epochs * forgeBlocksPerEpoch
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
			"the provider sent a VSC packet over client %s while it tracks another chain id", subject)
		// A boundary falls on every multiple of the epoch length; the window
		// holds epochs of them, less one at the edges.
		mismatches := len(s.providerBlockEventHeights(from+1, to, mismatchEventType, mismatch))
		s.Require().GreaterOrEqualf(mismatches, epochs-1,
			"the provider reported the chain id mismatch %d times over %d epochs, expected one per epoch", mismatches, epochs)

		if now := s.highestAppliedVscID(); now != applied {
			s.T().Logf("the consumer applied VSC packets sent before its client expired (valset update id %d -> %d); watching again", applied, now)
			s.Require().Truef(time.Now().Before(deadline), "the consumer's valset update id never settled")
			continue
		}
		return sent, applied
	}
}

// requireRenameEvent finds, in the provider's blocks from height from on,
// the update event of the consumer's rename from oldChainID to newChainID,
// and returns the height of the block that executed it.
func (s *ByzantineConsumerIntegrationTestSuite) requireRenameEvent(from int64, consumerID, oldChainID, newChainID string) int64 { //nolint:unparam // the suite drives one consumer; the helper stays generic
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
func (s *ByzantineConsumerIntegrationTestSuite) requireConsumerFollowsADelegation(consumerID string) { //nolint:unparam // the suite drives one consumer; the helper stays generic
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

// renameConsumerProposalJSON builds the proposal that renames a launched
// consumer to the chain id its declared client tracks: MsgUpdateConsumer
// signed by the governance authority, the one signer allowed to.
func (s *ByzantineConsumerIntegrationTestSuite) renameConsumerProposalJSON(consumerID, chainID string) string { //nolint:unparam // the suite drives one consumer; the helper stays generic
	return fmt.Sprintf(`{
  "messages": [{
    "@type": "/vaas.provider.v1.MsgUpdateConsumer",
    "owner": %q,
    "consumer_id": %q,
    "new_chain_id": %q
  }],
  "metadata": "ipfs://test",
  "deposit": "10000000%s",
  "title": "Rename the consumer to the chain id its client tracks",
  "summary": "revision bump"
}`, s.queryGovAuthority(), consumerID, chainID, bondDenom)
}
