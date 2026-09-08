# Consumer Lifecycle

This document describes the full lifecycle of a consumer chain in VAAS, from registration to deletion.

For how an unresponsive or lagging launched consumer is handled -- the liveness grace period, removal sweep, snapshot resync, and consumer safe mode -- see [consumer-liveness.md](consumer-liveness.md).

## Phases

```
REGISTERED → INITIALIZED → LAUNCHED → STOPPED → DELETED
                               |  ^
                               v  |
                              PAUSED
```

A consumer always progresses forward through these phases, with two exceptions: a failed
launch resets the consumer back to REGISTERED so the owner can retry, and a successful
downtime challenge moves a LAUNCHED consumer to PAUSED, from which governance can either
resume it back to LAUNCHED or remove it (see
[consumer-downtime.md](consumer-downtime.md)).

---

## Phase 1: REGISTERED

**Trigger:** `MsgCreateConsumer` submitted by any account on the provider chain.

**Required fields:**
- `chain_id` — unique identifier for the consumer chain (must not be in use)
- `metadata` — name, description, metadata blob
- `initialization_parameters.initial_height` — the height at which the consumer chain starts
- `initialization_parameters.spawn_time` — the provider block time at which the consumer is launched
- `initialization_parameters.unbonding_period` — unbonding period for the consumer
- `initialization_parameters.vaas_timeout_period` — VSC packet timeout duration
- `initialization_parameters.historical_entries` — number of historical entries to keep
- `infraction_parameters` — slash/jail parameters for double-sign and downtime

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

---

## Phase 3: LAUNCHED

**Trigger:** automatic, at the first `BeginBlock` where `block_time >= spawn_time`.

**What happens on-chain:**
1. Up to 200 due consumers are dequeued per block.
2. For each consumer, `LaunchConsumer` runs in a cached context:
   - The current bonded validator set is snapshotted (all validators, no opt-in/out).
   - A consumer genesis state is built (`MakeConsumerGenesis`), containing:
     - Provider `ClientState` and `ConsensusState` at the current provider height — so the
       consumer can create a provider IBC client at genesis time.
     - The initial validator set.
     - Consumer parameters (timeout period, unbonding period, historical entries).
   - The genesis is stored on the provider chain (queryable via `QueryConsumerGenesis`).
   - The equivocation evidence minimum height is set from `initial_height`.
   - Phase is set to `LAUNCHED`.
3. If `LaunchConsumer` fails, `spawn_time` is reset to zero and the phase is reset to
   `REGISTERED`. The owner must submit a new `spawn_time` via `MsgUpdateConsumer` to retry.

**What the operator must do after launch:**

1. **Fetch the consumer genesis** from the provider:
   ```
   providerd query provider consumer-genesis <consumer-id>
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

**Trigger:** automatic, at the first `BeginBlock` where
`block_time >= stopped_time + provider_unbonding_period`.

**What happens on-chain:**
1. Up to 200 due consumers are dequeued per block.
2. For each consumer, `DeleteConsumerChain` runs:
   - Deletes: IBC client ID mapping, consumer genesis, key assignments, equivocation
     evidence minimum height, pending VSC packets, validator set, removal time, the
     liveness state (last-ack time and the sent/acked VSC-id counters), and the downtime
     state (pending downtime slashes, epoch downtime marks, withheld fee records, and
     punished-window bookkeeping).
   - **Preserves** (for block explorer use): chain ID, phase, owner address, metadata,
     initialization parameters.
3. Phase is set to `DELETED`.

---

## Summary Table

| Phase | Trigger | Actor | Key on-chain effect |
|---|---|---|---|
| `REGISTERED` | `MsgCreateConsumer` | Any account | Consumer created, owner assigned |
| `INITIALIZED` | `spawn_time` set | On-chain (automatic) | Queued for launch at spawn_time |
| `LAUNCHED` | `spawn_time` elapsed | On-chain (BeginBlock) | Genesis built; operator starts consumer; relayer creates IBC path |
| `PAUSED` | Successful downtime challenge | Any account (with proof) | Pending slashes cancelled, withheld fees repaid, auto-stop scheduled |
| `STOPPED` | `MsgRemoveConsumer` (gov), liveness sweep, or pause auto-stop | Governance / on-chain | Queued for deletion after unbonding period |
| `DELETED` | Unbonding period elapsed | On-chain (BeginBlock) | State cleaned up |
