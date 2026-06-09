# Graceful consumer handling on IBC timeout (issue #36)

## Problem

When the provider receives an IBC packet timeout — or an error acknowledgement, or a
local send failure — for a VSC packet to a consumer, it used to **immediately and
irreversibly** remove the consumer chain. `OnTimeoutPacketV2`, the error branch of
`OnAcknowledgementPacketV2`, and the send-failure branch of `SendVSCPacketsToChain` all
called `StopAndPrepareForConsumerRemoval`, which moves the consumer to `STOPPED` (no path
back to `LAUNCHED`, only forward to `DELETED` after the unbonding period).

This is too severe:

1. A timeout only means "no delivery within the packet timeout" — a transient consumer
   outage, or the single relayer RPC being down/poisoned. It is **not** misbehaviour.
2. The effective timeout is **always 24h**: `SendVSCPacketsToChain` uses
   `min(VaasTimeoutPeriod, channeltypesv2.MaxTimeoutDelta)` and IBC v2 hard-caps
   `MaxTimeoutDelta` at 24h. The old `DefaultVAASTimeoutPeriod = 4 weeks` was inert and its
   "ensure channel doesn't close" comment was misleading. So a single 24h RPC blip
   permanently retired a consumer.

A related correctness issue surfaces *as a consequence* of relaxing removal. VSC packets
carry **diffs**, and the provider advanced its record of the consumer's set
**optimistically at queue time** (`SetConsumerValSet(next)` in `ComputeConsumerNextValSet`)
whether or not the packet was delivered. A lost packet's change was therefore never
re-sent: e.g. a validator that *left* during the outage was dropped from the provider's
record, but the only "remove" packet was the one that timed out, so the consumer would keep
that validator forever — attackable with no slashable stake. Today this is masked because
the consumer is deleted on the first timeout; once we keep the consumer alive, it becomes a
real exploit window. So "don't remove on timeout" is only *correct* when paired with a
resync mechanism.

## Solution

Three coordinated changes.

### A. Derived liveness grace + periodic sweep

- Track a per-consumer **last successful-ack time** (`ConsumerLastLivenessTime`). It is
  refreshed only on a *success* acknowledgement, and initialized at launch. Absent entries
  lazily default to the current block time (so existing consumers are not mass-removed at
  the upgrade block).
- Timeouts, error-acks, and send failures **no longer remove** the consumer. They log and
  return; the next epoch retries (and resyncs — see B).
- A single removal path: `BeginBlockRemoveUnresponsiveConsumers` sweeps launched consumers
  every block and removes any whose time since the last successful ack exceeds the grace
  period. This is necessary because a fully dead relayer fires **no** callback, so a
  callback-only check would never trigger removal.
- The grace period is **derived**, not a new param:
  `LivenessGracePeriod = CalculateTrustPeriod(providerUnbondingTime, GraceFraction)` with
  `GraceFraction = "0.33"` (≈ ⅓ of the unbonding period ≈ ~7 days at the 21-day default).
  It is far more lenient than 24h yet comfortably inside the slashable-stake window
  (`StopAndPrepareForConsumerRemoval` already schedules deletion at provider unbonding).
  `GraceFraction` is the one tunable knob.
- Per decision: **error-acks share the same time-based grace** as timeouts. An error-ack
  means the consumer received and rejected the packet (e.g. version/wire mismatch); it does
  not refresh liveness, so a consumer that only ever error-acks is removed once the grace
  period elapses.

### B. Last-acked-baseline resync (self-healing diffs)

Compute each epoch's VSC diff against the **last acknowledged** consumer set instead of the
optimistically-advanced one. Because validator updates carry absolute power and the
consumer applies them idempotently, a packet computed against the acked baseline is a
*complete* delta from a state the consumer is known to have — so any delta lost to a
timeout is re-expressed every epoch until an ack confirms receipt. The consumer code, the
wire format, and the global VSC-id counter are all **unchanged**.

Implementation (lean variant — no per-id snapshots, bounded O(1) state per consumer):

- `AckedConsumerValidators` — the baseline set the diff is computed against
  (`ComputeConsumerNextValSet` now diffs `DiffValidators(ackedBaseline, next)`). Lazily
  seeded to the current set when absent, so first-packet and upgrade behaviour matches the
  pre-feature diffing until the first ack.
- `LatestSentConsumerValidators` + `ConsumerLatestSentVscId` — a snapshot of the set and the
  vsc-id of the **most recent successfully-sent** packet, recorded in
  `SendVSCPacketsToChain`.
- On a **success ack for the latest-sent vsc-id**, the baseline advances to that snapshot
  (`advanceAckedBaseline`). Acks for older ids are ignored — the baseline simply stays put
  and the next epoch re-sends the delta against it.

**Why this converges:** each diff is a complete delta from the baseline (not an increment
relative to the previous packet). In steady state acks settle within a block, so by the next
epoch boundary the baseline equals the consumer's set and the diff is identical to the old
incremental one. During a two-way outage the consumer receives nothing and stays at the
baseline, so on recovery a single `Diff(baseline, set@now)` carries every change it missed
(including validator *removals*, the desync issue #36 is about) and the consumer converges.

**Known limitation (documented, not yet hardened).** The diff is computed against the *last
acknowledged* set, which assumes the consumer is at that baseline. The consumer can be
strictly *ahead* of the baseline only when packets reach it but their acknowledgements do
not reach the provider — a one-way relay failure, or the instant an outage begins inside the
sub-block window between a send and its ack. If, in addition, a validator's voting power
*oscillates* (e.g. 100 → 200 → 100) across that gap, the recovery diff can omit it (unchanged
baseline→now) and leave the consumer on the stale intermediate power. This requires an exotic
conjunction and is strictly less severe than the *guaranteed* permanent desync of the
pre-change code. It can be eliminated by sending a full absolute restatement of the set each
epoch (negligible bandwidth) instead of an incremental diff; the membership variant of the
same one-way-failure edge is fundamentally unfixable provider-side, since the provider cannot
know what an un-acknowledging consumer received. Tracked as a follow-up.

### C. Make `VaasTimeoutPeriod` honest

- `DefaultVAASTimeoutPeriod` is set to **24h** (the IBC v2 `MaxTimeoutDelta`), preserving
  the current *effective* timeout (important: a shorter value would shorten the window for
  evidence/slashing packets, which share this param). The misleading comment is corrected.
- A new `ValidateVAASTimeoutPeriod` rejects values **above** `MaxTimeoutDelta` at param-set
  time, so a misconfiguration is caught instead of being silently clamped. Applied in the
  provider `Params.Validate`, `ValidateInitializationParameters` (per-consumer), and the
  consumer `ConsumerParams.Validate`.

## Migration

No proto changes and no new `Params` fields (the grace period is derived) ⇒ **no migration
handler and no `ConsensusVersion` bump.** New collections are additive key prefixes with
safe lazy defaults:

- `ConsumerLastLivenessTime` absent ⇒ current block time (no mass removal at upgrade).
- `AckedConsumerValidators` absent ⇒ seeded with the current set (behaves like today until
  the first ack).

## Already resolved

The issue's last comment flagged a missing timeout cap on `EvidencePacket`; that was fixed
in PR #52 (`x/vaas/consumer/keeper/evidence_packet.go` already uses
`min(VaasTimeoutPeriod, MaxTimeoutDelta)`).

## Touched files

- `x/vaas/provider/keeper/relay.go` — grace rewiring of the three former removal sites;
  record latest-sent VSC on success; advance baseline / refresh liveness on success ack.
- `x/vaas/provider/keeper/validator_set_update.go` — diff against the acked baseline.
- `x/vaas/provider/keeper/consumer_liveness.go` (new) — liveness/baseline state accessors,
  derived grace, sweep.
- `x/vaas/provider/keeper/keeper.go`, `x/vaas/provider/types/keys.go` — new collections +
  prefixes.
- `x/vaas/provider/ibc_module.go` — decode acked vsc-id from the payload, pass it through.
- `x/vaas/provider/keeper/consumer_lifecycle.go` — init liveness at launch; clean up new
  state on delete.
- `x/vaas/provider/module.go` — wire the sweep into `BeginBlock`.
- `x/vaas/types/shared_params.go`, `x/vaas/provider/types/params.go`,
  `x/vaas/provider/types/msg.go`, `x/vaas/types/params.go` — timeout cap + honest default.
