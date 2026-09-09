package e2e

// e2e_evidence_requeue_test.go proves, against real IBC wiring, that downtime
// evidence survives a genuine packet timeout: the consumer re-queues a
// timed-out evidence packet from the MsgTimeout callback payload and retries
// it, and the provider ultimately accepts the evidence for every downtime
// window that closed while delivery was down. Without the re-queue, evidence
// is deleted from the pending queue when the packet is sent, so a timed-out
// packet means the accusation is lost forever and the provider could never
// accept those windows.
//
// The scenario (runs inside LivenessIntegrationTestSuite, whose consumer
// produces ~1s blocks, closes a downtime window every 30 of them, and stamps
// consumer-sent packets with a 20s vaas_timeout):
//
//  1. Bond a second, permanently-silent provider validator (small stake, so
//     both chains keep single-node consensus). VAAS syncs it into the
//     consumer's validator set, where it accumulates real missed-block
//     evidence; wait until the provider has accepted a first downtime window
//     for it, proving the whole pipeline works before any timeout is forced.
//  2. Pause the ts-relayer across two full consumer downtime windows. The
//     consumer keeps producing blocks: both windows close, both evidence
//     packets are sent, and both expire on the consumer's clock (>> the 20s
//     packet timeout) while undelivered.
//  3. Unpause the relayer: it submits MsgTimeout for the expired packets. The
//     consumer's OnTimeoutPacket re-queues each packet's evidence -- asserted
//     via the "requeued timed-out evidence packet" log lines, correlated back
//     to the exact windows through the packet sequences in the "evidence
//     packet sent" lines. Pending evidence is keyed by (validator, window-end
//     height), so the two windows queue side by side rather than coalescing.
//  4. The re-queued packets are re-sent and delivered: the provider queues a
//     pending downtime slash covering each timed-out window, asserted via the
//     pending-downtime-slashes query.
//
// A real IBC timeout requires the packet to expire on the consumer's own
// clock while undelivered, so the relayer is paused rather than either chain
// (mirroring testForcedTimeoutSnapshotResync). The pause is phase-aligned to
// the consumer's window boundaries so it spans exactly two window closes plus
// the packet-timeout margin, keeping the total outage (~70-80s at nominal
// block times) safely inside both the IBC client trusting period (~132s) and
// the provider's liveness grace (~150s) -- overshooting either would kill the
// suite's clients or sweep the consumer mid-test.

import (
	"bufio"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	// evidenceExpiryMargin is how long to keep the relayer paused after the
	// second window's evidence packet is observed sent, so the packet is
	// provably expired before the relayer comes back. It comfortably exceeds
	// the 20s vaas_timeout_period that
	// testdata/create_consumer_short_unbonding.json stamps on consumer-sent
	// evidence packets.
	evidenceExpiryMargin = 30 * time.Second
)

var evidenceLogSequenceRe = regexp.MustCompile(`sequence=(\d+)`)

var evidenceLogWindowEndRe = regexp.MustCompile(`window_end_height=(\d+)`)

// ansiEscapeRe matches ANSI SGR escape sequences. The chains log with the
// SDK's console format, which colorizes field names by default, so a raw
// docker log line reads e.g. "\x1b[36msequence=\x1b[0m2" -- the escapes must
// be stripped before key=value extraction.
var ansiEscapeRe = regexp.MustCompile(`\x1b\[[0-9;]*m`)

// parseEvidencePacketLog scans a consumer container log for the evidence
// packet lifecycle lines and returns the sequence -> window-end mapping from
// every "evidence packet sent" line, plus the packet sequences from every
// "requeued timed-out evidence packet" line. Together they attribute each
// requeue to the exact downtime window it carries: a re-send after a requeue
// gets a fresh sequence, so a window that timed out appears under multiple
// sequences, all mapping to the same window-end height.
func parseEvidencePacketLog(log string) (sentWindowEnds map[uint64]int64, requeuedSeqs []uint64) {
	sentWindowEnds = make(map[uint64]int64)

	scanner := bufio.NewScanner(strings.NewReader(log))
	scanner.Buffer(make([]byte, 0, 1024*1024), 1024*1024)
	for scanner.Scan() {
		line := ansiEscapeRe.ReplaceAllString(scanner.Text(), "")
		switch {
		case strings.Contains(line, "evidence packet sent"):
			seqMatch := evidenceLogSequenceRe.FindStringSubmatch(line)
			endMatch := evidenceLogWindowEndRe.FindStringSubmatch(line)
			if seqMatch == nil || endMatch == nil {
				continue
			}
			seq, err := strconv.ParseUint(seqMatch[1], 10, 64)
			if err != nil {
				continue
			}
			windowEnd, err := strconv.ParseInt(endMatch[1], 10, 64)
			if err != nil {
				continue
			}
			sentWindowEnds[seq] = windowEnd
		case strings.Contains(line, "requeued timed-out evidence packet"):
			seqMatch := evidenceLogSequenceRe.FindStringSubmatch(line)
			if seqMatch == nil {
				continue
			}
			seq, err := strconv.ParseUint(seqMatch[1], 10, 64)
			if err != nil {
				continue
			}
			requeuedSeqs = append(requeuedSeqs, seq)
		}
	}

	return sentWindowEnds, requeuedSeqs
}

// requeuedWindowEnds resolves the window-end heights whose evidence packets
// were re-queued after an IBC timeout, per the consumer log.
func requeuedWindowEnds(log string) map[int64]bool {
	sent, requeued := parseEvidencePacketLog(log)
	ends := make(map[int64]bool)
	for _, seq := range requeued {
		if windowEnd, ok := sent[seq]; ok {
			ends[windowEnd] = true
		}
	}
	return ends
}

// testEvidenceRequeueOnTimeout forces two downtime-evidence packets to
// genuinely time out and asserts the consumer re-queues both (per window) and
// the provider eventually accepts both windows. See the file header for the
// full scenario.
func (s *LivenessIntegrationTestSuite) testEvidenceRequeueOnTimeout() {
	s.Run("evidence requeue on timeout: timed-out downtime evidence is re-queued and accepted", func() {
		const consumerID = "0"
		const window = int64(downtimeSignedBlocksWindow)

		s.Require().Equalf("CONSUMER_PHASE_LAUNCHED", s.queryProviderConsumerPhase(consumerID),
			"consumer %s must be LAUNCHED before the evidence-requeue test", consumerID)

		// Top up the consumer fee pool: epochs are one ~1s block here, so the
		// per-epoch fee flows out continuously and downtime evidence pricing /
		// fee exclusion should not race pool exhaustion late in the suite.
		s.providerFundConsumerFeePool(consumerID, "20000000"+feeDenom)

		s.T().Log("bonding a second, permanently-silent validator on the provider...")
		_, val2Valoper := s.createSilentValidator("val2", "5000000"+bondDenom)
		s.T().Logf("silent validator bonded: %s", val2Valoper)

		s.T().Log("waiting for the silent validator to sync into the consumer's validator set...")
		s.Require().Eventuallyf(func() bool {
			vals, err := s.queryConsumerNetValidators()
			return err == nil && len(vals) >= 2
		}, 3*time.Minute, 5*time.Second,
			"consumer never synced the silent validator into its validator set")

		// Gate on a first provider-accepted downtime window while delivery is
		// still healthy. This isolates the timeout path from pipeline failures
		// and guarantees the silent validator has been tracked on the consumer
		// for at least one full window, so every window that closes during the
		// upcoming pause is a full-span offender window for it.
		s.T().Log("waiting for a first accepted downtime window (pipeline healthy before forcing timeouts)...")
		s.Require().Eventuallyf(func() bool {
			return len(s.queryPendingDowntimeSlashes(consumerID)) > 0
		}, 4*time.Minute, 3*time.Second,
			"provider never accepted downtime evidence for the silent validator while the relayer was live")

		// Diagnostic baseline: no evidence packet should have timed out while
		// the relayer was live (delivery takes seconds against a 20s timeout).
		if ends := requeuedWindowEnds(s.consumerLogs()); len(ends) > 0 {
			s.T().Logf("diagnostic: evidence requeues before the forced outage (unexpected but tolerated): %v", ends)
		}

		// If anything below aborts the sub-test with the relayer still paused,
		// do not leave it paused for the rest of the suite. In the success
		// path the relayer is already unpaused and this extra unpause fails
		// harmlessly.
		defer func() {
			_ = s.dkrPool.Client.UnpauseContainer(s.tsRelayerResource.Container.ID)
		}()

		// Phase-align the pause a few blocks before a window close, so the
		// pause spans exactly two closes (plus expiry margin) and stays well
		// inside the trusting period and liveness grace. A window [S, S+W-1]
		// closes -- and its evidence packet is sent -- in block S+W, i.e. at
		// heights that are multiples of the window size.
		var pausedHeight int64
		s.Require().Eventuallyf(func() bool {
			h, err := s.queryConsumerBlockHeight()
			if err != nil || h%window < window-6 || h%window > window-3 {
				return false
			}
			if err := s.dkrPool.Client.PauseContainer(s.tsRelayerResource.Container.ID); err != nil {
				s.T().Logf("failed to pause relayer container (will retry): %v", err)
				return false
			}
			// Re-read the height now that the pause is in effect and require it
			// strictly below the last height of the closing window: if the
			// chain slipped past the close boundary in between, the boundary
			// packet may have been sent pre-pause; and if it reached exactly
			// the window-end height, a pre-pause client update could anchor
			// that window's timestamp at pause time, aging the evidence by the
			// whole outage. Below window-end, every consensus state that can
			// anchor the paused windows is a fresh post-outage one. Back out
			// and re-align otherwise.
			pausedHeight, err = s.queryConsumerBlockHeight()
			if err == nil && pausedHeight%window >= window-6 && pausedHeight%window <= window-2 {
				return true
			}
			if err := s.dkrPool.Client.UnpauseContainer(s.tsRelayerResource.Container.ID); err != nil {
				s.T().Logf("failed to unpause relayer container after misaligned pause: %v", err)
			}
			return false
		}, 3*time.Minute, time.Second,
			"could not phase-align the relayer pause to a consumer window boundary")

		pauseStart := time.Now()
		firstClose := pausedHeight - pausedHeight%window + window
		secondClose := firstClose + window
		windowEnd1, windowEnd2 := firstClose-1, secondClose-1
		s.T().Logf("relayer paused at consumer height %d; expecting evidence sends at heights %d and %d (windows ending %d and %d)",
			pausedHeight, firstClose, secondClose, windowEnd1, windowEnd2)

		// Both closes must happen while paused. The caps also bound the total
		// outage: past ~2.5s/block the pause would start flirting with the
		// ~132s trusting period and ~150s liveness grace, and the suite's
		// timing assumptions are broken anyway.
		s.T().Log("waiting for two window closes while the relayer is paused...")
		s.Require().Eventuallyf(func() bool {
			h, err := s.queryConsumerBlockHeight()
			return err == nil && h >= firstClose
		}, 45*time.Second, 2*time.Second,
			"consumer never reached the first window close (height %d) while paused", firstClose)

		var secondCloseSeen time.Time
		s.Require().Eventuallyf(func() bool {
			h, err := s.queryConsumerBlockHeight()
			if err != nil || h < secondClose {
				return false
			}
			secondCloseSeen = time.Now()
			return true
		}, 85*time.Second-time.Since(pauseStart), 2*time.Second,
			"consumer never reached the second window close (height %d) while paused", secondClose)

		// Keep the relayer down until both packets are provably expired: the
		// second send happened no later than secondCloseSeen, so waiting the
		// expiry margin past it puts both timeouts (20s) comfortably behind.
		expiry := time.Until(secondCloseSeen.Add(evidenceExpiryMargin))
		s.T().Logf("both windows closed while paused; waiting %s for the evidence packets to expire...", expiry.Round(time.Second))
		time.Sleep(expiry)

		s.T().Log("unpausing relayer; it submits MsgTimeout for the expired evidence packets...")
		s.Require().NoError(s.dkrPool.Client.UnpauseContainer(s.tsRelayerResource.Container.ID),
			"failed to unpause relayer container")
		s.T().Logf("total relayer outage: %s", time.Since(pauseStart).Round(time.Second))

		// (a) The consumer must RE-QUEUE both timed-out packets rather than
		// drop them: one "requeued timed-out evidence packet" line per packet,
		// attributed to the exact windows via the packet sequences. Two
		// distinct window-end heights also prove the pending queue keys
		// evidence per window instead of coalescing.
		s.T().Log("waiting for the consumer to re-queue the timed-out evidence for both windows...")
		s.Require().Eventuallyf(func() bool {
			ends := requeuedWindowEnds(s.consumerLogs())
			return ends[windowEnd1] && ends[windowEnd2]
		}, 150*time.Second, 3*time.Second,
			"consumer never re-queued the timed-out evidence packets for both windows ending at %d and %d", windowEnd1, windowEnd2)
		s.T().Log("both timed-out evidence packets were re-queued")

		// (b) The re-queued evidence must be re-sent and accepted: the
		// provider queues a pending downtime slash covering each timed-out
		// window. Evidence deleted on send (never re-queued) could never get
		// here -- the accusations for these windows existed only in the
		// expired packets.
		s.T().Log("waiting for the provider to accept the downtime evidence for both timed-out windows...")
		s.Require().Eventuallyf(func() bool {
			slashes := s.queryPendingDowntimeSlashes(consumerID)
			covered1, covered2 := false, false
			for _, p := range slashes {
				if p.coversWindowEnd(windowEnd1) {
					covered1 = true
				}
				if p.coversWindowEnd(windowEnd2) {
					covered2 = true
				}
			}
			return covered1 && covered2
		}, 3*time.Minute, 3*time.Second,
			"provider never accepted the re-sent downtime evidence for the windows ending at %d and %d", windowEnd1, windowEnd2)
		s.T().Log("provider accepted the downtime evidence for both timed-out windows")

		// The outage must not have tripped the provider's liveness sweep; the
		// suite's remaining tests need a LAUNCHED consumer.
		s.Require().Equalf("CONSUMER_PHASE_LAUNCHED", s.queryProviderConsumerPhase(consumerID),
			"consumer %s must still be LAUNCHED after the evidence-requeue test", consumerID)
	})
}
