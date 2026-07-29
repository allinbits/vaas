# Consumer Lifecycle

This document describes the full lifecycle of a consumer chain in VAAS, from registration to deletion.

For how an unresponsive or lagging launched consumer is handled -- the liveness grace period, removal sweep, snapshot resync, and consumer safe mode -- see [consumer-liveness.md](consumer-liveness.md).

## Phases

```
REGISTERED -> INITIALIZED -> LAUNCHED -> STOPPED -> DELETED
                               |  ^
                               v  |
                              PAUSED
```

A consumer always progresses forward through these phases, with three exceptions: a failed
launch resets the consumer back to REGISTERED so the owner can retry; a successful
downtime challenge moves a LAUNCHED consumer to PAUSED, from which governance can either
resume it back to LAUNCHED or remove it (see
[consumer-downtime.md](consumer-downtime.md)); and a consumer that has not launched can be
terminated outright with `MsgRetireConsumer`, which skips STOPPED and moves it straight to
DELETED (see [Retiring a consumer before launch](#retiring-a-consumer-before-launch)).

---

## Phase 1: REGISTERED

**Trigger:** `MsgCreateConsumer` submitted by any account on the provider chain.

**Required fields.** Only two:
- `chain_id` — unique identifier for the consumer chain (must not be in use; a chain ID
  claimed by a consumer that is later deleted is released and can be claimed again)
- `metadata` — name, description, metadata blob

**`initialization_parameters` is optional**, and it is all-or-nothing. Omit the
whole block and `MsgCreateConsumer` substitutes
`DefaultConsumerInitializationParameters()` wholesale — including a zero
`spawn_time`, so the consumer stays in `REGISTERED` until the owner supplies one
with `MsgUpdateConsumer`. Supply the block and every field in it is validated
(`ValidateInitializationParameters` plus the cross-chain bounds in
`validateConsumerInitParams`), so a partially-filled block is rejected rather
than defaulted field by field:

- `initial_height` — the height at which the consumer chain starts (must be non-zero)
- `spawn_time` — the provider block time at which the consumer is launched (zero means "not scheduled")
- `unbonding_period` — unbonding period for the consumer (positive, and not above the provider's own unbonding period)
- `vaas_timeout_period` — timeout on the evidence packets the consumer sends the provider (positive, at most 24h)
- `historical_entries` — number of historical entries to keep (positive)
- `safe_mode_threshold` — how long the consumer tolerates a stale provider validator set before entering safe mode (positive, and strictly below the provider's liveness grace period)

See [params-reference.md](params-reference.md) section 4 for the full bounds and
defaults.

**What happens on-chain:**
1. A unique `consumer_id` is assigned (auto-incremented sequence).
2. The submitter address is stored as the consumer owner.
3. Metadata, chain ID, and initialization parameters are stored.
4. Phase is set to `REGISTERED`.
5. If `spawn_time` is non-zero, the consumer immediately transitions to `INITIALIZED` (see next phase).

**Who can submit:** any account. The submitter becomes the owner.

---

## Phase 2: INITIALIZED

**Trigger:** automatic, during `MsgCreateConsumer` or `MsgUpdateConsumer` if `spawn_time` is set.

**What happens on-chain:**
1. Phase is set to `INITIALIZED`.
2. The consumer is added to an internal time-indexed queue keyed by `spawn_time`.

**Note:** `MsgUpdateConsumer` (owner only) can update initialization parameters including
`spawn_time` at any point before launch. Updating `spawn_time` moves the consumer to the
new position in the queue. Only the owner address can submit `MsgUpdateConsumer`.

**To call the launch off entirely**, rather than push `spawn_time` out indefinitely, the
owner (or governance) submits `MsgRetireConsumer`: see
[Retiring a consumer before launch](#retiring-a-consumer-before-launch).

---

## Phase 3: LAUNCHED

**Trigger:** automatic, at the first `BeginBlock` where `block_time >= spawn_time`.

**What happens on-chain:**
1. Up to 200 due consumers are dequeued per block.
2. For each consumer, `LaunchConsumer` runs in a cached context:
   - The current bonded validator set is snapshotted (all validators, no opt-in/out).
   - A consumer genesis state is built (`MakeConsumerGenesis`), containing:
     - Provider `ClientState` and `ConsensusState` at the current provider height — so the
       consumer can create a provider IBC client at genesis time. The client's trusting
       period is derived from the provider unbonding period and `trusting_period_fraction`.
     - The initial validator set.
     - Consumer parameters seeded from the consumer's `initialization_parameters`:
       `enabled` (set to true), `vaas_timeout_period`, `historical_entries`,
       `unbonding_period`, and `safe_mode_threshold`.
     - The provider-owned downtime parameters `signed_blocks_window` and
       `min_signed_per_window`, seeded from the provider's current values so the consumer
       starts in sync; later changes ride VSC packets (see
       [consumer-downtime.md](consumer-downtime.md) section 2).
   - The genesis is stored on the provider chain (queryable via `QueryConsumerGenesis`).
   - The equivocation evidence minimum height is set from `initial_height`.
   - Phase is set to `LAUNCHED`.
3. If `LaunchConsumer` fails, `spawn_time` is reset to zero and the phase is reset to
   `REGISTERED`. The owner must submit a new `spawn_time` via `MsgUpdateConsumer` to retry.

**What the operator must do after launch:**

1. **Fetch the consumer genesis** from the provider:
   ```
   providerd query vaasprovider consumer-genesis <consumer-id>
   ```
   This returns the `ConsumerGenesisState` built in step 2 above.

2. **Inject it** into the consumer chain's `genesis.json` under `app_state.vaasconsumer`.

3. **Start the consumer chain** with that genesis. On the first block, the consumer's
   `InitGenesis` runs:
   - Creates an IBC client pointing to the provider, using the embedded provider
     `ClientState` and `ConsensusState`.
   - Installs the initial validator set from the genesis.
   - The consumer is now live and tracking the provider.

**What the relayer must do after both chains are running:**

The ts-relayer creates an IBC v2 client on the **provider** pointing to the **consumer**,
and registers the counterparty on both sides (`add-path`). The provider does not create
this client itself — it only discovers it.

At the next epoch boundary, the provider scans IBC clients (`discoverActiveConsumerClient`)
to find one pointing to the consumer chain with a registered counterparty. Once found, it
is stored and used for all subsequent VSC packet delivery.

**VSC packet flow (ongoing, every epoch):**
1. Provider queues validator set changes for all launched consumers.
2. Provider sends VSC packets to each consumer via the discovered IBC v2 client.
3. The relayer relays the packets to the consumer.
4. The consumer applies the validator set changes on `EndBlock`.

VSC packets are diffs by default. If a consumer falls behind on acknowledgements, the provider instead sends an absolute snapshot of the full validator set so the consumer resyncs in a single packet, and the consumer is removed only after a sustained liveness failure rather than on a single packet timeout. See [consumer-liveness.md](consumer-liveness.md).

---

## Phase 4: PAUSED

**Trigger:** a successful `MsgChallengeConsumerDowntime` -- a cryptographic proof that the
consumer reported false downtime evidence (see [consumer-downtime.md](consumer-downtime.md)).

**Requirements:** consumer must be in `LAUNCHED` phase.

**What happens on-chain:**
1. Withheld fee shares from the false accusations are paid back from the consumer's fee pool.
2. Phase is set to `PAUSED`.
3. All pending downtime slashes from this consumer are cancelled and its epoch downtime
   marks cleared.
4. An automatic stop is scheduled at `block_time + MaxPauseDuration` (default 30 days).
5. No further VSC packets are queued or sent; fee distribution and downtime evidence from
   this consumer stop; the liveness sweep skips it.

**Exits:** `MsgResumeConsumer` (gov) returns the consumer to `LAUNCHED` with an immediate
snapshot resync (the resume pre-flights the IBC client and fails with `MsgRecoverClient`
guidance if the client expired during the pause); `MsgRemoveConsumer` (gov) or the scheduled
auto-stop moves it to `STOPPED`.

---

## Phase 5: STOPPED

**Triggers:** either
- `MsgRemoveConsumer` submitted by the governance authority (removing a consumer requires the gov authority), or
- the automatic liveness sweep, when a launched consumer has produced no successful VSC acknowledgement for longer than the liveness grace period (see [consumer-liveness.md](consumer-liveness.md)), or
- the pause auto-stop, when a paused consumer's `MaxPauseDuration` elapses without a governance resume (see [consumer-downtime.md](consumer-downtime.md)).

**Requirements:** consumer must be in `LAUNCHED` or `PAUSED` phase.

**What happens on-chain:**
1. Phase is set to `STOPPED`.
2. The consumer is added to a time-indexed removal queue keyed by
   `block_time + provider_unbonding_period`.
3. No further VSC packets are queued or sent to this consumer.

---

## Phase 6: DELETED

**Triggers:** either
- automatic, at the first `BeginBlock` where
  `block_time >= stopped_time + provider_unbonding_period`, for a consumer that went
  through `STOPPED`, or
- `MsgRetireConsumer`, for a consumer that never launched — no unbonding delay applies
  (see [Retiring a consumer before launch](#retiring-a-consumer-before-launch)).

**What happens on-chain:**
1. Up to 200 due consumers are dequeued per block.
2. For each consumer, `DeleteConsumerChain` runs. **Deletion moves money first:** it
   auto-sweeps the consumer's fee pool, distributing the remaining balance pro-rata to
   its depositors (see [consumer-fee-pool.md](consumer-fee-pool.md)). This is the last
   chance any depositor gets — withdrawals are rejected once the phase is `DELETED`.
   It then:
   - Deletes: IBC client ID mapping, consumer genesis, key assignments, equivocation
     evidence minimum height, init-chain height, pending VSC packets, validator set,
     previous-valset hash, removal time, the liveness state (last-ack time and the
     sent/acked VSC-id counters), the in-debt flag, the per-consumer fees-per-block
     override, the fee-pool-address reverse lookup, and the downtime state (pending
     downtime slashes, epoch downtime marks, withheld fee records, accepted windows,
     and window floors).
   - **Releases the chain ID**, as the last step before the phase changes, so the chain
     ID becomes registrable again (see below).
   - **Preserves** (for block explorer use): phase, owner address, metadata,
     initialization parameters.
3. Phase is set to `DELETED`.

**The chain ID is released, not reserved forever.** `MsgCreateConsumer` and
`MsgUpdateConsumer` reject a chain ID that is already claimed (`ChainIdInUse`), and
deletion drops this consumer's claim, so the same chain ID can be registered again by a
new consumer. By that point nothing can name the deleted consumer's chain any more: its
client mapping has just been removed, so an inbound packet can no longer be attributed
to it, and evidence, downtime accusations, and fee distribution all require phase
`LAUNCHED`. On the stop-then-remove path a full unbonding period has additionally
elapsed, so any infraction the chain could still be punished for is already outside the
slashable window. Holding the ID past this point would reserve it permanently, since
`DELETED` is terminal.

A deleted consumer therefore reports an **empty `chain_id`** from `consumer-chain` and
`list-consumer-chains`, and exports an empty chain ID in genesis. Its record is still
listed so the deletion stays visible without an archive node; identify it by
`consumer_id`, which is never reused.

---

## Retiring a consumer before launch

A registration is not permanent, and it does not tie up its chain ID until someone
launches the chain. `MsgRetireConsumer` terminates a consumer that has not launched.

**Requirements:** consumer must be in `REGISTERED` or `INITIALIZED` phase
(`IsConsumerPrelaunched`). A `LAUNCHED` or `PAUSED` consumer is not retirable — it is
removed with `MsgRemoveConsumer` (gov), which stops it first and defers the erasure by
a full unbonding period. A `STOPPED` or `DELETED` consumer is rejected too.

**Who can submit:** the consumer's **owner** or the **governance authority** — the same
owner-or-gov admission `MsgFundConsumerFeePool` and `MsgWithdrawConsumerFeePool` use.
The owner arm lets whoever registered a chain abandon one it no longer intends to
launch. The gov arm is the remedy when the owner key is lost, which would otherwise
leave the consumer, and the chain ID it holds, in place with nobody able to clear it.

```
providerd tx vaasprovider retire-consumer <consumer-id> --from <owner>
```

**What happens on-chain:** `RetireConsumerChain` first drops the consumer's entry from
the spawn-time launch queue if it was `INITIALIZED`, so the queue cannot later hand an
erased consumer to the launch sweep. It then runs the same `DeleteConsumerChain`
teardown a stopped consumer goes through: fee-pool auto-sweep, state cleanup, chain ID
released, phase `DELETED`. There is no `STOPPED` stopover and no unbonding delay,
because no validator ever validated this chain, no IBC client was ever adopted for it,
and no validator set was ever computed or sent — so there is nothing to keep slashable
while an unbonding period runs down.

**A funded fee pool is settled, not stranded.** The auto-sweep distributes the pool
pro rata to its depositors, with the per-denom truncation residue forwarded to the
community pool, so anyone who prepaid fees for a chain that never launched is paid out
at retirement and the owner does not have to run `MsgSweepConsumerFeePool` first (see
[consumer-fee-pool.md](consumer-fee-pool.md)). Key assignments the consumer accumulated
before launch — assignment is allowed while `REGISTERED` and `INITIALIZED` (see
[key-assignment.md](key-assignment.md)) — are cleared with the rest of its state.

**The chain ID is free immediately.** Unlike the stop-then-remove path, retirement is
not gated on an unbonding period, so a mistyped or abandoned registration can be
retired and its chain ID re-registered in the next block.

---

## Summary Table

| Phase | Trigger | Actor | Key on-chain effect |
|---|---|---|---|
| `REGISTERED` | `MsgCreateConsumer` | Any account | Consumer created, owner assigned (only `chain_id` and `metadata` are required) |
| `INITIALIZED` | `spawn_time` set | On-chain (automatic) | Queued for launch at spawn_time |
| `LAUNCHED` | `spawn_time` elapsed | On-chain (BeginBlock) | Genesis built; operator starts consumer; relayer creates IBC path |
| `PAUSED` | Successful downtime challenge | Any account (with proof) | Pending slashes cancelled, withheld fees repaid, auto-stop scheduled |
| `STOPPED` | `MsgRemoveConsumer` (gov), liveness sweep, or pause auto-stop | Governance / on-chain | Queued for deletion after unbonding period |
| `DELETED` | Unbonding period elapsed | On-chain (BeginBlock) | Fee pool auto-swept to depositors, state cleaned up, chain ID released |
| `DELETED` | `MsgRetireConsumer` on a consumer that never launched | Owner or governance | Same teardown, with no `STOPPED` stopover and no unbonding delay |
