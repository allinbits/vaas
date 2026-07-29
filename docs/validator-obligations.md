# Validator Obligations

A provider validator validates *every* consumer chain -- there is no opt-in or
opt-out. Bonding on the provider therefore carries operational duties on each
launched consumer. This document is the checklist; each item links to the
subsystem doc that explains the mechanism.

## 1. Run a full node for every consumer chain

Every launched consumer must be validated with your validator key (or an
assigned key; see [key-assignment.md](key-assignment.md)). Beyond producing
blocks, the node's block store is what lets you defend yourself against a false
downtime accusation.

Downtime on a consumer is reported optimistically and slashed only after a
challenge window ([consumer-downtime.md](consumer-downtime.md)). A validator that
was actually online disproves a false report by exhibiting one chain-sealed
signature for a claimed-missed height -- data that lives only in a consumer full
node's block store. If you do not retain that block data, you cannot challenge,
and a false accusation matures into a real slash.

**Retention window.** A challenger must be able to produce the canonical commit
for a height as far back as `DowntimeEvidenceMaxAge + DowntimeChallengeWindow`
(10 days at the defaults: 3 + 7). Configure your consumer node's block retention
(`min-retain-blocks`, pruning settings) to keep at least that span of blocks.
See [consumer-downtime.md](consumer-downtime.md) section 9 and the parameter
defaults in [params-reference.md](params-reference.md) section 2.

## 2. Stay online, or expect fee exclusion and a priced slash

Downtime is measured per tumbling window (`SignedBlocksWindow` /
`MinSignedPerWindow`). Missing more than the threshold in a window gets you
excluded from that consumer's epoch fee distribution and queues a slash priced
from the foregone fees, held behind the challenge window
([consumer-downtime.md](consumer-downtime.md) sections 1, 4, 5). A downtime slash
never jails, but repeated windows compound. The launch grace period
(`DowntimeGracePeriod`, 7 days by default) suppresses downtime slashing right
after a consumer launches, giving you time to bring the node up.

## 3. Watch downtime acceptance events and pending slashes

When the provider accepts a downtime evidence packet it emits
**`vaas_pending_downtime_slash`**, carrying the claimed window, the missed-block
bitmap, the priced `slash_tokens`, and `matures_at` -- so the at-stake amount and
your deadline are both visible immediately. The related events to index are
`vaas_execute_consumer_chain_slash` (the slash actually ran),
`vaas_downtime_slash_dropped` (it was discarded instead, with a `reason`),
`vaas_downtime_challenge_succeeded`, and `vaas_withheld_fee_paid`. See
[events-reference.md](events-reference.md).

Two queries show what is currently at stake for a consumer:

```
providerd query vaasprovider pending-downtime-slashes <consumer-id>
providerd query vaasprovider withheld-fee-records <consumer-id>
```

(`CmdPendingDowntimeSlashes` and `CmdWithheldFeeRecords` in
[x/vaas/provider/client/cli/query.go](../x/vaas/provider/client/cli/query.go).)
If you were online for a window that shows up here,
**challenge it well inside the challenge window** with
`challenge-consumer-downtime` -- a successful challenge cancels every pending
slash from that consumer, repays your withheld fees, and pauses the consumer
(see [consumer-downtime.md](consumer-downtime.md) section 6). There is no
challenger reward beyond your own recovery, so no one else will do it for you.

You can also observe chain-level liveness (last ack, removal ETA, degraded flag)
as described in [consumer-liveness.md](consumer-liveness.md) section 6.

## 4. Do not equivocate

Double-signing on a consumer is punished on the provider by slash + jail +
tombstone once evidence is submitted -- permanent removal at the default
parameters ([equivocation-evidence.md](equivocation-evidence.md)). This is the
one infraction with no grace period and no recovery.

## 5. Keep IBC clients fresh, especially during a PAUSE

VSC packets and evidence flow over IBC v2 clients maintained by relayers. Under
normal traffic the clients stay updated as a side effect of relaying. During a
`PAUSED` consumer there is no packet traffic, so nothing refreshes the clients;
with the default `MaxPauseDuration` (30 days) exceeding the client trusting
period, a long pause with idle relayers can expire the provider's client to that
consumer. Keep relayers updating the clients through a pause so a governance
resume does not have to bundle `MsgRecoverClient`
([consumer-downtime.md](consumer-downtime.md) sections 7 and 9;
[consumer-liveness.md](consumer-liveness.md) section 3). Note the provider's own
liveness sweep runs on its block clock and will remove a consumer whose acks go
silent past the grace period regardless of client state.

## 6. Manage assigned consumer keys carefully

If you assign a distinct consensus key on a consumer, follow the reuse and
reassignment rules in [key-assignment.md](key-assignment.md): keys cannot be
reused across validators or consumers, and after a reassignment on a launched
consumer the old key stays resolvable (and you stay accountable for the
pre-rotation period) until the unbonding period elapses. Keep the old node key
available for that window.

## 7. Treat a provider consensus-key rotation as a consumer-side change

Rotating your provider consensus key with `tx staking rotate-cons-pubkey` also
changes the identity every consumer where you assigned **no** consumer key
expects to sign its blocks. The provider pushes an immediate snapshot to those
consumers rather than waiting for the epoch boundary, so **swap the node signing
key at the rotation** -- not before, not hours later. Getting the order wrong
accrues missed blocks on all of those consumers at once, and the launch grace
period will not cover it.

Consumers where you *do* have an assigned key see nothing change. A rotation onto
a consensus key that is already someone's assigned consumer key is rejected when
you submit the transaction.

The full procedure, including where your pending-slash and fee bookkeeping ends
up afterwards, is
[key-assignment.md](key-assignment.md#rotating-your-provider-consensus-key).

---

For the funding duties that fall on a consumer's *owner* rather than on
validators -- keeping the fee pool solvent so the consumer is not debt-gated --
see [consumer-launch-runbook.md](consumer-launch-runbook.md) and
[consumer-fee-pool.md](consumer-fee-pool.md).
