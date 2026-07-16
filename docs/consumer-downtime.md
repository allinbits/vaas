# Consumer Downtime: Detection, Verifiable Evidence, and Challenges

This document describes how validator downtime on a consumer chain is detected, reported to
the provider, priced, and punished -- and how a falsely accused validator can cancel the
punishment with a cryptographic counter-proof that also condemns the consumer that produced
the false report.

It complements [consumer-liveness.md](consumer-liveness.md), which covers the liveness of the
consumer *chain* as a whole. This document is about the liveness of individual *validators*
on a live consumer, and about what keeps the consumer honest when it reports on them.

## Design rationale

Downtime punishment on a consumer chain has a trust problem the provider cannot wish away:
the missed-block data originates on the consumer, computed by consumer code the provider
does not run. Two facts shape the design:

1. **Downtime cannot be proven, only disproven.** Proving a validator signed nothing across
   a window would require shipping every block's commit to the provider -- unworkable. But a
   single claimed-missed height can be *disproven* by exhibiting the validator's signature
   for that height, sealed into the consumer chain itself.
2. **Only chain-sealed signatures are unforgeable.** A bare vote signature proves nothing
   about when it was created (a validator can retro-sign anything). A signature inside the
   `LastCommit` whose hash is committed by a light-client-verified header at the next height
   cannot be forged or retro-inserted without breaking the light client's guarantees.

So the provider accepts downtime evidence *optimistically* -- structured so that every
claimed-missed height is individually falsifiable -- and delays the stake slash behind a
challenge window. Anyone who can exhibit one chain-sealed signature for one claimed-missed
height cancels every pending slash from that consumer and pauses it: a source that lies once
cannot be trusted about anything else. The party with both the incentive and the data to
challenge is the falsely accused validator itself: if it was actually online, its own
consumer node's block store contains the proof; if it was actually down, no such proof
exists.

## 1. Detection: tumbling windows on the consumer

The consumer's VAAS module tracks missed blocks itself, reading each block's `LastCommit`
vote flags in `BeginBlock`. Detection does not use `x/slashing`'s sliding window: consumers
cannot jail (the validator set is provider-owned), and a sliding window without jail
re-triggers on every block once below threshold. The consumer's `x/slashing` downtime calls
into the VAAS keeper are log-only.

Windows are **tumbling**: heights `[k*W, (k+1)*W)` where `W = SignedBlocksWindow`. Each
validator gets one bit per height (`1` = missed, absent from the commit). When the window
closes, any validator that missed more than

```
maxMissed = W - ceil(MinSignedPerWindow * W)
```

blocks gets exactly one evidence packet queued, then all bitmaps reset. A validator that
joins mid-window is tracked from its first observed height, and its packet's window is
trimmed to that start -- the full-window `maxMissed` still applies, which only makes the
threshold more lenient for partial windows.

The evidence packet carries the bitmap itself (`bit i` = height `window_start + i`), the
window's last height as `infraction_height`, and echoes the `SignedBlocksWindow` /
`MinSignedPerWindow` values the bitmap was computed under. The bitmap is what makes the
evidence challengeable: each set bit is an individually falsifiable claim.

## 2. Downtime parameters are provider-owned

`SignedBlocksWindow` and `MinSignedPerWindow` define the downtime SLA the provider slashes
on, so the provider owns them:

- At launch they are written into the consumer genesis the provider generates.
- Afterwards, every VSC packet (diff and snapshot) carries the provider's current values.
  The consumer stages an update on receipt -- ordered by the same monotonic VSC id that
  orders validator updates -- and activates it only at the next window boundary, so a window
  in flight is never reinterpreted. Snapshot resync heals missed updates for free.
- The consumer's `MsgUpdateParams` preserves the stored values; they are not locally
  changeable.

Because an update can race in-flight evidence, packets echo the params they were computed
under, and the provider accepts evidence computed under either its current values or the
immediately previous ones (within the evidence-age horizon). Staged values are validated
before activation: a malformed update (zero window, out-of-range fraction) is logged and
dropped rather than applied.

## 3. Provider validation of incoming evidence

`HandleConsumerDowntime` validates an evidence packet in this order, rejecting with an error
acknowledgement at the first failure:

1. **Echoed params acceptable** -- match current or immediately-previous downtime params.
2. **Threshold** -- the bitmap's missed count exceeds `maxMissed` computed from the echoed
   params.
3. **Identity** -- the consumer consensus address maps to a known provider validator that is
   in this consumer's validator set; the window end respects the consumer's minimum evidence
   height.
4. **Time anchoring** -- the window-end timestamp is taken from the smallest stored consensus
   state at or above the window-end height (the IBC client's own verified history). Against
   that anchor: the window must end after the launch grace period
   (`SpawnTime + DowntimeGracePeriod`), and must be no older than `DowntimeEvidenceMaxAge`
   at receipt -- old windows are rejected because their challenge data availability decays.
5. **No re-acceptance** -- the window must not intersect any window already accepted for
   this validator on this consumer (`AcceptedDowntimeWindows`, one record per accepted
   window, written at acceptance), and must start above the pair's pruned acceptance floor
   (`DowntimeWindowFloors`, below). A consumer cannot re-submit an already accepted window
   -- whether its slash is still pending, has executed, or was cancelled -- and cannot
   submit a window intersecting one. Acceptance is order-independent: windows are compared
   by height intersection against every retained record, not against a single boundary, so
   delivery order cannot void earlier windows (IBC v2 delivery is out-of-order and relaying
   is permissionless -- an accused validator relaying its newest window first gains
   nothing). Accepted windows for a (consumer, validator) pair are therefore pairwise
   disjoint but arrive in any order. Records older than the retention horizon
   (`DowntimeChallengeWindow + DowntimeEvidenceMaxAge`) are pruned each block (a record
   whose pending slash is still maturing is kept until that slash executes or is
   cancelled), and pruning advances the pair's floor to the pruned window end: anything at
   or below the floor is rejected outright. The floor is sound unconditionally: any window
   intersecting a pruned record starts at or below that record's window end, which is at or
   below the floor, so the floor check rejects it regardless of timestamps or configuration
   -- ancient windows hit the floor, live windows hit the records, and no window is ever
   accepted twice.

There is no one-pending-at-a-time limit: every disjoint window is accepted on arrival, so a
validator can have several slashes pending at once -- one per offence window -- each keyed by
(consumer, validator, window end) and each maturing on its own clock. A sustained offender is
priced for every window it misses rather than getting later windows discounted because an
earlier one is still waiting out its challenge window.

Note what is deliberately *not* checked: the truth of the bitmap. That is the challenge
mechanism's job (section 6).

On acceptance the provider does three things: it marks the validator for **fee exclusion**
in the current epoch (section 5), it prices and stores a **pending slash** (section 4), and
it emits an event carrying the claimed window and bitmap so the accused validator can see
exactly which heights it must disprove.

## 4. Pricing and execution: P*M/C behind a challenge window

The slash is priced at receipt, from foregone fees rather than a flat stake fraction:

```
slash_tokens = (P * M) / C
```

- `P` -- the per-validator fee share for this consumer, priced at the epoch the window ended
  in. Each epoch distribution records the share it paid (zero for an epoch where nothing was
  paid out); evidence for a past epoch resolves against that record, evidence for the
  current epoch prices live. Downtime during an epoch the consumer did not pay for prices to
  zero -- no fees were foregone, so the slash is vacuous (the fee exclusion still applies).
- `C` -- the photon conversion rate (photons per bond token), read at receipt from the
  embedding application's `x/photon` via an injected `PhotonKeeper`; this repository's
  standalone provider app wires a fixed rate of 1.
- `M` -- `missed_count / span`, the fraction of the window actually missed.

Because `P` and `C` are resolved at receipt, the exact at-stake amount is fixed and visible
(in the acceptance event and the pending-slash query) from the moment the evidence is
accepted -- an accused validator knows precisely what a challenge is worth. When several
windows are pending for the same validator, each is priced independently at its own
arrival: `P` resolves against the epoch its own window ended in, so back-to-back offence
windows each carry their own fee-derived amount.

Each pending slash matures `DowntimeChallengeWindow` after its own acceptance. A
`BeginBlock` sweep executes matured entries -- several can execute for the same validator,
in the same sweep or across sweeps, one per matured window. For each entry the token amount
converts to a stake fraction, **capped** by `InfractionParameters.Downtime.SlashFraction` --
never the price itself: under honest pricing `P*M/C` sits far below the cap, which only bites
when fee overrides or conversion-rate anomalies would otherwise turn a fee-sized number into a
stake-threatening one. The default cap (`0.0001`, matching the Cosmos Hub's
`slash_fraction_downtime`) keeps the worst case for a single mispriced window
at liveness-fault scale, far below the double-sign fraction -- but each pending slash is capped independently, so repeated windows
compound with no aggregate bound. A downtime slash **never jails**. Entries whose validator has
meanwhile unbonded, been tombstoned, or has no slashable stake are dropped with an event rather
than executed; a zero-token slash is likewise dropped as vacuous.

## 5. Fee exclusion and the pool as escrow

Fee exclusion is immediate: an accepted evidence packet excludes the validator from the
current epoch's distribution for that consumer. The excluded share is simply never drawn
from the consumer's fee pool -- the other validators' shares do not grow (no incentive to
DOS a competitor), and the consumer does not pay for validation work it did not receive.

Because the withheld money never leaves the pool, the pool itself acts as the escrow for a
possible successful challenge (see [consumer-fee-pool.md](consumer-fee-pool.md) for the
pool's share-accounting model and withdrawal locks). Each exclusion writes a
`WithheldFeeRecord` (consumer, validator, amount, expiry = the challenge window);
a later exclusion for the same pair sums
into the unexpired record and refreshes its expiry, so the record accumulates across epochs
while windows keep pending. The record ends one of three ways:

- If no successful challenge arrives before expiry, the record is deleted; the funds stay
  with the consumer.
- When the sweep executes a slash for the pair and no window remains pending for it
  afterward, the record is deleted immediately: the executed accusations were never
  disproven, so there is nothing left the escrow could be owed against. While any window is
  still pending, the record survives an execution -- the remaining accusations might yet be
  disproven and retro-paid.
- On a successful challenge, every unexpired record for that consumer is paid from the pool
  to its validator before anything else happens (see section 6) -- restoring what the false
  accusation withheld. Payment is best-effort against the pool balance for the edge where
  the consumer was stopped through an unrelated path in between.

Records are only written when the distribution actually had the funds to cover the epoch;
an underfunded epoch marks debt and writes no records, so a record is always backed by money
the pool genuinely retained. The `MsgWithdrawConsumerFeePool` depositor lock covers the
`PAUSED` phase as well as `LAUNCHED`, so escrowed funds cannot be raced out by depositors
between a challenge and its payout.

## 6. The challenge

`MsgChallengeConsumerDowntime` is permissionless. It names the accused validator, one
claimed-missed height `H`, and carries three artifacts: the consumer's signed header for
`H+1`, the canonical commit for `H` (which is block `H+1`'s `LastCommit`), and the accused
validator's consumer consensus public key. Verification:

1. Among the validator's pending slashes on this consumer -- possibly several, one per
   window -- the one whose window contains `H` exists (accepted windows are disjoint, so
   there is at most one), and its bitmap actually claims `H` was missed.
2. The header is for height `H+1` and the consumer's registered chain id, and verifies
   against the provider's IBC client for this consumer -- the same light-client verification
   the misbehaviour machinery uses.
3. The supplied commit is for height `H` and its hash equals the verified header's
   `LastCommitHash`. This is the soundness core: the commit's every signature is now sealed
   by the consumer chain itself; nothing in it can be substituted or retro-signed without
   breaking the light client.
4. The public key is self-authenticating (its address equals the accused validator's
   consumer address -- no reliance on key-assignment state, so later key rotations cannot
   break a challenge), and the commit contains that validator's signature (flag `Commit` or
   `Nil`; both prove consensus participation at `H`) which verifies against the key over the
   canonical vote bytes.

A signature that satisfies all of this proves the validator participated at a height the
consumer claimed it missed -- the report is false. Since the report's only basis was trust
in the consumer, one false bit indicts everything from that source. On success, in order:

1. All unexpired withheld fee shares for this consumer are paid back to their validators
   (before the phase flip, while the depositor withdraw-lock still holds).
2. Every pending downtime slash from this consumer is cancelled, and its current epoch
   downtime marks are cleared.
3. The consumer is paused (section 7).

A failed challenge changes nothing; transaction gas is the anti-spam. There is no challenger
reward: the challenged validator's own recovery is the incentive.

Assembling a challenge needs only a consumer full node: `/commit?height=H` returns the
canonical commit for `H`, `/commit?height=H+1` the header, `/validators` the key. The CLI
does the assembly:

```
providerd tx provider challenge-consumer-downtime <consumer-id> <validator-cons-addr> <height> \
    --consumer-rpc http://consumer-node:26657
```

## 7. The PAUSED phase

A successful challenge moves the consumer from `LAUNCHED` to `PAUSED` -- proven-corrupt
reporting is grounds for suspension, but a bug deserves a recovery path that does not force
re-registration. While paused:

- No VSC packets are queued or sent; no fees are distributed; downtime evidence from the
  consumer is rejected.
- The liveness sweep skips it (the pause is not ack-silence), and the consumer side needs no
  new mechanism: its VSC clock goes stale and the existing safe mode restricts it
  automatically.
- An **auto-stop** is scheduled at `now + MaxPauseDuration`: a pause governance never
  resolves becomes `STOPPED` (then `DELETED`) like any other dead consumer. No consumer can
  be paused forever.

Two governance exits:

- **`MsgResumeConsumer`** returns it to `LAUNCHED`: cancels the auto-stop, reseeds the
  liveness clock (the consumer was rightfully silent while paused), and immediately queues
  *and sends* a full-snapshot VSC so the consumer's validator set resyncs without waiting
  for an epoch boundary. The send is enforced: if the snapshot cannot be handed to IBC the
  whole resume fails and rolls back, because reporting success while the snapshot silently
  stayed queued would let the next epoch send a diff against a set the consumer never
  received. The resume also pre-flights the IBC client: with defaults, `MaxPauseDuration`
  (30 days) exceeds the client trusting period, so a long pause with idle relayers can
  expire the client. An expired or frozen client fails the resume with instructions to
  bundle ibc-go's `MsgRecoverClient` (client substitution, already governance-gated) in the
  same proposal. Client expiry during a pause is therefore a recoverable inconvenience, not
  a death sentence -- which is why `MaxPauseDuration` is not bounded by the trusting period.
- **`MsgRemoveConsumer`** accepts a paused consumer and routes it into the ordinary
  `STOPPED -> DELETED` teardown.

Pausing, stopping, or deleting a consumer cancels its in-flight downtime state (pending
slashes and epoch marks): once no epoch or challenge window remains to resolve them against,
they are cancelled outright. The accepted-window records and the pruned floor survive a
pause or stop -- a disproven or cancelled window must not become re-submittable, while
later disjoint windows remain submittable after a resume as usual. Full deletion erases
them along with the escrow records.

## 8. Parameters and bounds

| Parameter | Where | Bound | Default |
|---|---|---|---|
| `SignedBlocksWindow` | provider `InfractionParameters` (mirrored to consumers) | `> 0` | 600 blocks |
| `MinSignedPerWindow` | provider `InfractionParameters` (mirrored to consumers) | `(0, 1)` | `0.5` |
| `Downtime.SlashFraction` | provider `InfractionParameters` | `[0, 1]` | `0.0001` (a cap, not the slash) |
| `DowntimeGracePeriod` | provider `InfractionParameters` | `>= 0` | 7 days |
| `DowntimeChallengeWindow` | provider `InfractionParameters` | `> 0` | 7 days |
| `DowntimeEvidenceMaxAge` | provider `InfractionParameters` | `(0, DowntimeChallengeWindow]` | 3 days |
| `MaxPauseDuration` | provider module param | `> 0` | 30 days |

`DowntimeEvidenceMaxAge <= DowntimeChallengeWindow` is enforced at validation time: were
evidence allowed to be older than the challenge window, a consumer could re-submit a window
whose previous slash had already matured while the window was still fresh enough to accept.
The sum of the two must also stay below the consumer client trusting period (checked against
the default consumer unbonding at genesis; per-consumer deviations are operator guidance),
so the oldest challengeable header remains light-client verifiable through the end of its
challenge window.

## 9. Operator guidance

- **Retain blocks.** A challenger must produce the commit for a height up to
  `DowntimeEvidenceMaxAge + DowntimeChallengeWindow` in the past (10 days at defaults).
  Validators should configure consumer-node retention (`min-retain-blocks`, pruning) to keep
  at least that span.
- **Watch the acceptance events.** An accepted evidence packet emits the claimed window,
  bitmap, and priced slash; the pending-slash and withheld-fee-record queries
  (`pending-downtime-slashes`, `withheld-fee-records`) show everything currently at stake.
  A validator that was online should challenge well inside the window.
- **Keep clients fresh during pauses.** Relayers have no packet traffic on a paused
  consumer; updating the clients anyway avoids the `MsgRecoverClient` step at resume time.

## 10. Trust boundaries

**Defended against**

- A consumer (buggy or malicious code, or captured block production) forging missed-block
  reports: every claimed height is falsifiable, the slash waits out the challenge window,
  and one disproven bit cancels all pending punishment and pauses the consumer.
- A validator retro-signing votes to fake liveness: a bare signature cannot enter the
  chain-sealed `LastCommit` after the fact.
- Re-submission of an already accepted window (even after its slash executed), overlapping
  windows, and evidence about windows too old to challenge.
- VSC-shaped packets from a chain that is not the provider: inbound VAAS packets must carry
  the provider application's source port, and the consumer accepts them only over a client
  whose tracked chain id matches the provider chain id pinned at genesis (or at first
  contact). See [consumer-liveness.md](consumer-liveness.md) for the delivery model.

**Out of scope**

- Collusion of 2/3+ of the consumer's voting power to seal a forged `LastCommit`. That is a
  light client attack by the provider's own validator set, detected and punished by the
  existing misbehaviour machinery.
- An attacker chain reusing the provider's exact chain-id string against the pinning check:
  distinguishing it requires a forged light-client history, which again lands in the
  misbehaviour machinery's domain.
- Sustained sub-threshold missing (a validator that signs just over `MinSignedPerWindow` of
  every window keeps full fees). The SLA is per-window by construction; burst misses aligned
  across a window boundary can total up to `2 * maxMissed` without triggering -- accepted,
  since a sliding window has the same sustained-miss blindness and cannot be used without
  jail semantics.
