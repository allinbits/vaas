# Consumer Refusal: Signals, Pauses, and Deferred Punishment

## Design rationale

Every provider validator validates every consumer chain: VAAS has no opt-in.
That mandate creates a dilemma this document addresses end to end. A validator
that vets a consumer binary, finds it malicious, and refuses to run it is
indistinguishable from an offline node, so the downtime machinery accuses it;
a validator that does run a malicious binary can have genuine-looking
equivocation evidence manufactured against it, because a binary holding an
in-process consumer key can sign arbitrary conflicting votes. Both problems
share one root: evidence originating from a consumer is only as trustworthy as
the consumer, and the consumer is the least trusted component in the system.

The model gives the protocol the mechanical responses and keeps the judgment
with humans, in three parts: a stake-weighted refusal signal that pauses a
consumer, deferral of downtime slashes behind a live removal vote, and an
execution delay with unbonding holds for equivocation punishment. A governance
removal is the common resolution: **a chain condemned by governance takes its
evidence down with it**, and nothing short of that verdict does.

## 1. The refusal signal

A bonded validator publicly refuses a consumer with:

```
providerd tx provider set-consumer-refusal <consumer-id> true --from <operator>
```

and withdraws the refusal with `false`. The signal (`MsgSetConsumerRefusal`)
is signed by the validator's operator account. Recording one needs a bonded
validator and a `LAUNCHED` or `PAUSED` consumer; withdrawing one needs
neither, so a validator that unbonded, or a consumer that stopped, never
leaves a signal its owner cannot take back. Deleting the consumer erases its
signals. The signal does two things:

- **Aggregation.** Every EndBlock the provider sums the bonded power behind
  each `LAUNCHED` consumer's refusals, using the validator powers x/staking
  last recorded, so delegation drift alone can cross the threshold. At
  `RefusalPauseThreshold` (default one third, reached at exactly one third)
  the consumer is paused through the standard lifecycle (`PAUSED`, the
  `MaxPauseDuration` auto-stop, governance resume), and a
  `consumer_refusal_threshold_reached` event names the refused power, the
  total, and the threshold. One third of the power refusing to sign halts the
  consumer's consensus physically anyway; the threshold turns that implicit
  halt into an explicit, attributable pause.
- **Accountability.** The refusal is a public stance, queryable by anyone
  (`providerd query provider consumer-refusals <consumer-id>`), which is what
  off-chain coordination needs to form.

A refusal is **not** an individual exemption from downtime evidence: it does
not excuse the refusing validator by itself, or the signal would be a free
opt-out and the mandate would be gone. Individual protection comes from the
deferral in section 2. What the threshold pause does is collective: like every
pause, it cancels every pending downtime slash and epoch downtime mark the
consumer has sourced, for every accused validator, because a chain the
refusal-threshold share of power refuses to run cannot have its accusations
trusted. Fee shares withheld while those accusations stood are not repaid;
only a successful challenge repays them (see
[consumer-downtime.md](consumer-downtime.md)).

Signals of validators x/staking no longer knows at all (removed after
unbonding to nothing) are pruned at evaluation; a validator that merely lost
its power, jailed or out of the set, keeps its stance.

Signals survive a pause. Withdrawing one is the validator's own act, and a
governance resume is refused while the coalition still stands at the
threshold (`MsgResumeConsumer` fails with `ErrConsumerRefused`): the
provider's EndBlock runs after gov's, so a resume against a standing
coalition would be paused again in the very block it lands, silently, while
resetting the pause's auto-stop clock. Refusing it outright gives governance a
reason instead. The consequence is that a coalition which never withdraws
holds the chain `PAUSED` until `MaxPauseDuration` lapses, after which it is
`STOPPED` and then `DELETED` with no vote; governance's levers against a
coalition it disagrees with are `MsgRemoveConsumer` and raising
`RefusalPauseThreshold` above the coalition's share. This is coherent as long
as the threshold is not set below the share that can halt the chain
physically: below one third a coalition gains a pause it could not otherwise
force, and with it the cancellation of the chain's pending downtime slashes.

## 2. Downtime slashes defer behind a removal vote

Downtime accusations keep flowing and queue as pending slashes (see
[consumer-downtime.md](consumer-downtime.md)); the deferral applies at
execution. A matured pending slash whose consumer has a `MsgRemoveConsumer`
proposal **in its voting period** is not executed but pushed past the voting
end: its maturity becomes the later of its current value and the voting end
plus `RemovalVoteDeferralMargin`, and the entry records the proposal it waits
for (with several removal votes live at once, the latest-ending one).

- Removal passes: the consumer is stopped, and stopping cancels every pending
  downtime slash it sourced. The refuser loses no stake (fee shares withheld
  while it stood accused are not repaid; only a successful challenge does
  that).
- Removal is rejected, or no proposal exists: the slash executes at its
  (possibly extended) maturity.
- The vote is still open at the extended maturity, because gov moved its end
  after a late quorum or because no block landed inside the margin: the entry
  keeps waiting for that same proposal's tally. No other proposal ever defers
  it again, so serial proposals cannot defer a slash indefinitely: one voting
  period of grace per accusation is the ceiling.
- Only the voting period shields. A proposal in its deposit period, or one
  that reaches its voting period after the entry matured, defers nothing.
- The pair's withheld fee record stays claimable for as long as the entry is
  pending, so a challenge won during the deferral still repays it.

Timing for the validator playbook against a malicious binary: refuse from
launch, signal the refusal, and submit the removal proposal so that it is in
its voting period by the time the first accusation matures. At defaults the
first downtime slash matures `DowntimeGracePeriod` plus
`DowntimeChallengeWindow` (14 days) after the consumer spawns, and an
equivocation punishment `EquivocationExecutionDelay` (7 days) after its
evidence is accepted, so the deposit must be met before then. The grace period
covers the start, accusations that mature during the vote defer, and a passed
removal erases the rest.

## 3. Equivocation punishment waits, jailed and held

Verified double-vote evidence (`MsgSubmitConsumerDoubleVoting`) does not slash
or tombstone in the submission transaction. Instead:

1. **The validator is jailed immediately.** Jailing is reversible, so it can
   neutralize a possibly-guilty validator during the window without
   prejudging evidence a malicious binary can fabricate wholesale. The jail
   lasts until the execution time plus a 24-hour margin, and every later
   change to the entry moves it: an extension behind a vote carries it past
   the new execution time, a pause moves it to the pause's expiration, and
   nothing ever shortens it while a punishment is pending (a second
   accusation, or any other jail, only extends).
2. **The slash and tombstone queue** behind `EquivocationExecutionDelay`
   (default 7 days, mirroring the downtime challenge window), extended past a
   live removal vote for the consumer exactly like a downtime slash, with the
   same once-per-proposal rule.
3. **The validator's unbonding operations are held** for the whole pending
   window, with the staking module's on-hold machinery: undelegations,
   redelegations out, and the validator's own unbonding neither complete nor
   escape, so the deferred slash always finds the stake it would have found
   at submission, even on a provider whose voting period exceeds its
   unbonding period. Operations started while the punishment is pending are
   held by the `AfterUnbondingInitiated` staking hook. Stake delegated to the
   validator after the queue is slashed at execution with the rest: the jail
   is public, and delegating into it is delegating into the slash.
4. **Resolution.** Only a governance removal cancels the punishment: either
   the removal executing against a launched or paused consumer, or the vote
   the entry deferred behind passing. The deferral records the proposal it
   waits for, so the verdict counts even when the consumer had already stopped
   for another reason by the time the vote ended: gov then records the
   proposal as `FAILED`, its `MsgRemoveConsumer` having found nothing left to
   remove, and that is read as the passed vote it is. A `FAILED` proposal
   whose consumer is still running is a removal that did not happen (another
   message in it failed), and the punishment executes. Cancelling the
   validator's last pending entry releases its holds and opens the jail (the
   validator unjails with the standard `MsgUnjail`); cancelling one of
   several shrinks the jail to what the rest require.

   A paused consumer freezes its entries in place until the pause resolves:
   the pause is a governance deliberation window and an irreversible
   punishment must not pre-empt the verdict. A consumer stopped for any other
   reason, by the liveness sweep or by a lapsed pause, is not a verdict on its
   evidence, and its entries keep running on their own clock: the punishment
   executes at maturity, slash and tombstone at the double-sign parameters as
   one unit, sized from the validator's own tokens (a jailed validator has no
   recorded power to size from), holds released, the (now slashed) unbonding
   operations complete. Once a validator is tombstoned its other pending
   entries are dropped with it, so their holds do not outlive the punishment;
   an entry whose validator x/staking no longer knows is dropped the same way
   rather than retried forever. A validator tombstoned by provider-native
   evidence while its consumer is paused keeps its frozen entries, and their
   holds, until the pause resolves and the sweep reaches them.

   This is deliberately stricter than downtime, whose pending slashes any
   stop does cancel. A downtime accusation can only be disproved by a
   challenge, which needs the accused chain alive to fetch headers from, so
   a dead chain leaves it unfair to execute; equivocation evidence is
   self-contained signatures that need nothing from the chain to stand. It
   also closes an abuse: a coalition able to halt a chain (a third of the
   power, the same bar as the refusal pause) must not thereby void
   punishments already queued against its members.

Entries and holds are keyed by the consensus address the validator runs now,
whatever key the evidence names, and a consensus-key rotation after the queue
moves them along, so the unbonding hook always finds them. Re-submitted
evidence is idempotent at every stage: already queued is a no-op, and an
already tombstoned validator is reported as such with nothing queued.

## 4. What was considered and rejected

- **Punishing the byzantine set of a light-client attack, at any threshold**:
  rejected in the misbehaviour path already; the binary shapes the evidence,
  so any automatic punishment keyed on it becomes a targeting tool.
- **Governance-gated punishment** (a proposal that executes a recorded
  punishment): social slashing; politics stays out of consensus. The removal
  vote deliberately inverts it: governance votes on the *chain*, and
  punishment remains automatic, merely sequenced after the chain's
  credibility is settled.
- **A coalition circuit-breaker** (auto-pause when many validators are
  accused at once): catches only mass griefing; a binary can aim at one
  validator and stay under any threshold. The refusal signal plus the
  execution delay cover both scales without threshold games.
- **Individual downtime exemption for refusers**: a free opt-out; the mandate
  would be gone.
- **Any stop cancelling equivocation punishment**: a liveness stop or a lapsed
  pause is not a governance verdict, and a coalition able to halt the chain
  could otherwise void punishments queued against its members. Only the
  removal vote's outcome cancels.

## 5. Operator surface

Queries: `providerd query provider consumer-refusals <consumer-id>` (refusers,
refused and total power, fraction, threshold) and
`providerd query provider pending-equivocation-punishments <consumer-id>`
(entries with their execution time, extension flag, and the proposal they
wait for). Events: `vaas_consumer_refusal_set`,
`vaas_consumer_refusal_threshold_reached`,
`vaas_equivocation_punishment_queued`, `vaas_equivocation_punishment_executed`,
`vaas_equivocation_punishment_cancelled` (with the verdict as
`cancel_reason`), and `vaas_equivocation_punishment_dropped` (with
`drop_reason`).

One limitation is guarded rather than solved here. x/staking's genesis import
rebuilds neither the unbonding-operation indexes nor the operation counter,
and a hold is only ever released through that index, so an export taken with
live holds cannot be imported without stranding the held stake. The
provider's `InitGenesis` therefore refuses such a genesis, naming the remedy:
resolve the pending punishments before exporting, or drop
`held_unbonding_ops` and the entries' `unbonding_on_hold_ref_count` from the
exported genesis, giving up the protection they gave (the punishments stay
pending and the accused stay jailed; only stake already unbonding can then
finish before execution). The durable fix belongs in the staking module's
genesis import, which has everything it needs in the entries it imports; once
it rebuilds the indexes and the counter the guard simply passes.

## 6. Parameters and bounds

| Parameter | Where | Bound | Default |
|---|---|---|---|
| `RefusalPauseThreshold` | provider module param | `(0, 1)`; keep it at or above the share that halts the chain physically (one third) | `0.333333333333333333` |
| `EquivocationExecutionDelay` | provider module param | `> 0` | 7 days |
| `RemovalVoteDeferralMargin` | provider module param | `> 0` | 1 hour |
| queue-time jail margin | constant | -- | 24 hours past the (extended) execution time |

`MaxPauseDuration` (see [consumer-downtime.md](consumer-downtime.md)) must
exceed the provider's governance latency, or a refusal-triggered pause can
never be resumed before it lapses into a stop.
