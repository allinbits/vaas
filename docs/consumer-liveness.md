# Consumer Liveness, Removal, and Resync

This document describes how the provider handles an unresponsive or lagging consumer
chain: when (and why) a consumer is removed, how a consumer that briefly falls behind
heals itself, and how a consumer protects itself while its validator set may be stale.

It complements [consumer-lifecycle.md](consumer-lifecycle.md), which covers the overall
`REGISTERED -> INITIALIZED -> LAUNCHED -> STOPPED -> DELETED` progression. This document
zooms in on what keeps a `LAUNCHED` consumer healthy and what happens when it is not.

## Why this exists

The provider sends a Validator Set Change (VSC) packet to each launched consumer every
epoch. Earlier, a single failure to deliver one of those packets -- an IBC packet timeout,
an error acknowledgement, or a local send failure -- immediately and irreversibly moved the
consumer to `STOPPED`. Two facts made that too severe:

1. **The packet timeout is effectively 24h, not the configured value.** VSC packets are
   sent with a timeout of `min(VaasTimeoutPeriod, MaxTimeoutDelta)`, and IBC v2 hard-caps
   `MaxTimeoutDelta` at 24h. So a 24h outage of a single relayer or RPC was enough to
   permanently retire a consumer.
2. **VSC packets are diffs.** Each packet carries only the validators that changed since
   the previous one, applied on top of the consumer's current set. If a packet is lost, its
   change is never re-sent on its own -- so dropping the eager removal would, by itself,
   leave a departed validator stuck in the consumer's active set (a validator that could
   later unbond on the provider and validate the consumer with no slashable stake).

The model below replaces the per-packet kill switch with a provider-local liveness clock,
pairs it with a self-healing resync so lost diffs cannot strand a stale validator, and adds
a consumer-side safe mode for the window in which a consumer's set may be out of date.

## 1. Liveness clock and removal sweep

The provider tracks, per consumer, the block time of the last **successful** VSC
acknowledgement (`lastAck`). It is seeded when the consumer enters `LAUNCHED` and refreshed
every time the consumer acknowledges a VSC packet. A successful ack is the strongest
possible progress signal: it proves the consumer received *and applied* the update.

Each `BeginBlock`, the provider sweeps every launched consumer and stops any whose `lastAck`
is older than a **grace period**:

```
grace = providerUnbondingPeriod * LivenessGraceFraction
```

derived from the provider's own unbonding period (about 14 days at the 21-day default and the
`0.66` default fraction). `LivenessGraceFraction` is a provider module param: its default
mirrors the IBC client trusting-period fraction, so the grace ends around the point where
recovery stops being possible anyway while still leaving margin below the unbonding
(slashable) window, but operators can lower it -- on short-lived test or dev chains, for
instance -- to make the sweep fire in seconds rather than days. The sweep runs on the
provider's own block clock, so it does not depend
on a live IBC client or an available relayer -- it removes a consumer whether the silence is
caused by a dead consumer, a dead relayer, or an expired client.

When the grace period is exceeded, the consumer is moved to `STOPPED` (then `DELETED` after
the unbonding period) exactly as a governance removal would do. See
[consumer-lifecycle.md](consumer-lifecycle.md) for the `STOPPED -> DELETED` mechanics.

**Guarantee:** a launched-but-unresponsive consumer is removed within the grace period,
which is strictly less than the provider's unbonding period. So every validator that was in
the consumer's set as of its last successful sync is still slashable on the provider at the
time of removal.

## 2. Snapshot resync (self-healing)

Because VSC packets are diffs, a consumer that misses packets during an outage could diverge
from the provider's true validator set. To prevent that, the provider tracks two per-consumer
counters: the highest VSC id it has **sent** and the highest the consumer has **acked**. A
consumer is **behind** when:

```
highestAcked < highestSent
```

While a consumer is behind, the provider does not send a diff. It sends an **absolute
snapshot**: the complete current validator set, flagged with `is_snapshot = true` on the
packet. The consumer, on receiving a snapshot, **replaces** its set with it -- setting each
listed validator to its absolute power and removing (power 0) any validator it currently has
that the snapshot omits. Applying a snapshot is idempotent: a behind consumer converges to
exactly the provider's current set in one packet, regardless of which intermediate packets it
missed. Once an ack catches `highestAcked` up to `highestSent`, the provider resumes sending
ordinary diffs.

The wire format gains only a single boolean (`is_snapshot`); the validator-update list is
reused (a diff when false, the full set when true). The global VSC id counter and the
consumer's out-of-order dedup are unchanged.

**Guarantee:** a transient outage no longer corrupts the consumer's set. As soon as
connectivity returns, the next snapshot heals it -- including removing any validator that
left the provider during the outage.

## 3. IBC callbacks no longer remove

With the sweep as the single authority for removing unresponsive consumers, the IBC
callbacks that used to remove are demoted to logging:

- **Packet timeout** (`OnTimeoutPacketV2`): logs and returns; the next epoch retries.
- **Error acknowledgement**: logs and returns; the consumer keeps its grace budget.
- **Local send failure** (including an expired client): logs, leaves the pending packets
  queued, and returns; the next epoch retries.

This also closes a latent gap: previously an expired client left a consumer `LAUNCHED`
forever with no removal path. Now the sweep removes it once its grace elapses, because an
expired client produces no acks.

## 4. Consumer safe mode

While a consumer is behind, its local validator set may not match the provider's. The
consumer protects itself during that window. It records the block time of the last VSC packet
it received, and considers itself **stale** when it has not received one for longer than a
consumer-local threshold (`DefaultSafeModeThreshold`, 3 hours -- set well below the provider's
grace period).

While stale (or while flagged in debt for unpaid fees), the consumer's transaction admission
gate restricts incoming transactions to `/ibc.core.*` and `/cosmos.gov.*` messages only,
rejecting value-bearing application transactions. This bounds the damage of a possibly-stale
set: the consumer refuses to do value-bearing work under validators it can no longer trust,
while keeping the recovery path open -- `/ibc.core.*` still admits the incoming VSC packets
that let the consumer resync, and `/cosmos.gov.*` keeps governance available. A freshly
launched consumer is never treated as stale before its first VSC.

## 5. Parameters and bounds

| Parameter | Where | Bound | Default |
|---|---|---|---|
| `VaasTimeoutPeriod` | provider module param (VSC packet timeout) and per-consumer init param (consumer evidence packet timeout) | `[10m, MaxTimeoutDelta (24h)]` | 1h |
| consumer `UnbondingPeriod` | per-consumer init param | `(0, providerUnbondingPeriod]` | provider default minus 1 day |
| `LivenessGraceFraction` | provider module param | `(0, 1]` | `0.66` |
| consumer `SafeModeThreshold` | per-consumer init param | `> 0` | 3h |

`VaasTimeoutPeriod` is validated to `[10m, 24h]` at both the provider module param boundary
and the per-consumer initialization-parameter boundary, so the configured value is honest
rather than silently capped. The default was lowered from four weeks (which collapsed to 24h
under the cap) to one epoch-scale hour: an undelivered packet is superseded by the next
epoch's packet and a late one is dropped by the consumer's dedup, so a long timeout buys
nothing.

The consumer `UnbondingPeriod` is bounded against the provider's at `MsgCreateConsumer` /
`MsgUpdateConsumer` time (it must not exceed the provider's), so a consumer cannot be
configured to outlive the provider's slashable window. No lower floor is enforced: an
unbonding short enough to make the relayer-derived trusting period impractical is an
operator concern (and is useful for short-lived test or dev chains), not a protocol minimum.

## 6. Operator guidance: the liveness query

Operators can observe a consumer's liveness without waiting for removal:

```
providerd query provider consumer-liveness <consumer-id>
```

It returns, all derived (no stored extra state):

- `last_ack_time` -- block time of the consumer's last successful VSC ack.
- `grace_period` -- the derived grace for this provider.
- `removal_eta` -- `last_ack_time + grace_period`: when the sweep will remove the consumer
  if no further ack arrives.
- `degraded` -- true once `last_ack_time` is older than half the grace period; an early
  warning that a consumer is trending toward removal.

A `degraded` consumer that is genuinely reachable should recover on its own once VSC packets
flow again (the next snapshot resyncs it and refreshes `lastAck`). If it does not recover
before `removal_eta`, it will be stopped.

## 7. Guarantees and limitations

**Guarantees**

- A consumer is removed only after sustained ack-silence approaching, but staying within,
  the provider's slashable window -- never on a single transient packet failure.
- A consumer that recovers within the grace period self-heals to the provider's exact
  validator set via snapshot resync, with no operator or governance action.
- While a consumer's set may be stale, it refuses value-bearing transactions but keeps the
  IBC recovery path open.

**Out of scope (candidates for follow-up work)**

- **Owner/governance force-resume and an explicit `DEGRADED` phase.** Automatic recovery
  covers the common case; a manual "buy more time" path and a distinct degraded marker are
  deferred. The `lastAck` field is the groundwork for them.
- **Reviving a consumer whose IBC client has expired** (beyond the trusting period). This
  needs substitute-client / client-recovery governance and is a separate effort.
- **Relayer redundancy (multi-RPC / failover).** The single-RPC risk that motivated the
  original concern is a relayer-tooling matter, not a chain-side one.
