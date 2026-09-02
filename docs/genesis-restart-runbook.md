# Genesis / Restart Runbook

How to export a VAAS chain's state and start a new chain from it -- the flow
behind a coordinated halt-and-upgrade or a state-export migration -- and what
each VAAS module carries across the boundary so the restarted chain behaves
identically to the one it replaced.

This is the operational companion to [consumer-lifecycle.md](consumer-lifecycle.md)
and [consumer-liveness.md](consumer-liveness.md). It covers both the provider
(`vaas-provider`) and a consumer (`vaas-consumer`).

Both daemons expose the stock Cosmos SDK `export` command; the examples below
use `provider` / `consumer` as the binary names.

## The round-trip contract

A state-export restart must be a *fixed point*: exporting, re-importing into a
fresh node, and re-exporting must yield the same genesis, and the validator
sets CometBFT runs must not diverge. VAAS keeps IBC state (clients, connections,
and any in-flight or still-committed packets) across a coordinated restart, so
the VAAS genesis has to preserve every piece of state those packets are
interpreted against. The per-module tables below list what is round-tripped.

## Export

Export at the last committed height (the height at which the new chain will run
`InitChain`):

```bash
provider export > provider_exported.json
consumer export > consumer_exported.json
```

`--height <h>` exports at a specific height. `--for-zero-height` additionally
scrubs height-relative state (it resets signing-info start heights) for a chain
that will restart at height 0; use it only for a genuine height-zero relaunch,
not for a normal halt-and-continue.

Always validate an exported (or hand-edited) genesis before starting a node
from it:

```bash
provider genesis validate provider_exported.json
consumer genesis validate consumer_exported.json
```

Validation runs the module `GenesisState.Validate` checks. It is a CLI-only
gate; nodes do not re-run it at boot, so a genesis that skips this step can
still fail (or panic) at `InitChain`.

## Provider module

`ExportGenesis` writes, and `InitGenesis` restores:

- the global valset-update-id counter;
- every consumer's `ConsumerState`: phase, owner, metadata, init params,
  client id, consumer genesis, pending VSC packets, removal / pause-expiration
  times, the liveness clock (`LastAckTime`, `HighestSentVscId`,
  `HighestAckedVscId`), and the in-debt flag;
- key-assignment state (per-consumer consensus keys, the reverse address index,
  and the addresses-to-prune queue);
- params, per-consumer `fees_per_block` overrides, and fee-pool shares;
- the downtime pipeline: pending downtime slashes, accepted-window records,
  window floors, epoch-downtime marks, withheld-fee records, epoch-share
  records, the infraction params, and the previous-downtime-params snapshot.

The in-debt flag is exported rather than re-derived because the only thing that
recomputes it is the per-epoch fee distribution, which runs at epoch boundaries
and only for `LAUNCHED` consumers. Every VSC packet is stamped with the flag's
current value, and that stamp is what gates transactions on the consumer, so a
`PAUSED` consumer resumed before its first post-restart distribution would
otherwise be sent a snapshot clearing a debt it still owes.

Two things are deliberately **not** exported and are rebuilt at import:

- **`LastProviderConsensusVals`** (the provider's record of the set it last gave
  CometBFT) is rebuilt at `InitGenesis` from the staking module's bonded set --
  the same computation, over the same source, that `EndBlock` runs every block.
  The rebuilt record is exactly the set `InitChain` hands CometBFT, so the first
  post-restart `EndBlock` diffs against a matching baseline and emits no
  spurious update.
- **The per-consumer `ConsumerValSet`** (the provider's record of the set each
  consumer last knew). Instead of exporting it, `QueueVSCPackets` treats an
  empty stored valset for a launched consumer as *must-snapshot*: the first
  post-restart epoch sends a full snapshot rather than a diff. This matters
  because a validator can unbond during the outage; diffing the live bonded set
  against an empty stored set would emit only additions and never the power-0
  removal for the departed validator, leaving it with consensus power on the
  consumer indefinitely. The snapshot reconciles the consumer's set regardless
  of what it held before. (The same path also covers a consumer's very first
  epoch, where a snapshot equals the all-additions diff it would produce.)

Also re-derived, from the per-consumer fields above rather than carried
separately: the spawn-time queue, the removal-time queue, the pause-expiration
queue, and each launched consumer's equivocation-evidence minimum height.

## Consumer module

`ExportGenesis` writes, and `InitGenesis` (restart branch) restores:

- params and the provider client id;
- the current cross-chain validator set (as the restart genesis
  `InitialValSet`, applied at `InitGenesis`);
- the pinned provider chain id (see `authenticateProviderChainID`), so the
  restarted consumer keeps rejecting packets from a client tracking a different
  chain id instead of leaving a window with no pin;
- both arms of the tx-admission gate: the VSC-staleness clock
  (`LastVSCRecvTime`), so safe mode is not reset by the restart, and the in-debt
  flag the provider last stamped on an accepted VSC packet, so a debt-gated
  consumer comes back gated instead of admitting ordinary transactions until the
  next packet re-asserts the flag;
- the in-progress downtime window (missed-block bitmaps and first-tracked
  heights), any staged downtime params, and queued-but-unsent evidence packets
  (closing a window clears the source bitmaps, so the queue is the only
  remaining copy);
- the **out-of-order dedup watermark** (`HighestValsetUpdateID`). This is the
  highest VSC id the consumer has applied; `OnRecvVSCPacketV2` skips any packet
  whose id is not greater than it. Restoring it means a stale diff still held in
  IBC state cannot be replayed over a newer set after the restart. A watermark
  of 0 is the absent case (a consumer that has not applied a VSC yet) and
  imports identically to a fresh node.

The exported consumer validator set carries each validator's consensus pubkey.
CometBFT's `GenesisDoc.ValidateAndComplete` dereferences every validator's
pubkey on load, so an export with null pubkeys would panic on reload; the export
unpacks the stored key exactly as `x/staking` does.

## Halt / upgrade flow

For a coordinated stop-and-restart (state-export upgrade):

1. Stop the provider and every consumer at the same agreed height (a governance
   software-upgrade halt, or a coordinated `halt-height`). A clean halt at a
   shared height keeps the IBC clients and any in-flight packets mutually
   consistent.
2. On each chain run `export` at that height and `genesis validate` the result.
3. Assemble the new `genesis.json` for each chain from its exported app state,
   carrying over `chain_id` (or bumping it per your upgrade policy) and the
   genesis time.
4. Distribute the genesis, reset only Tendermint/CometBFT block state
   (`comet unsafe-reset-all` / a fresh data dir), keep validator and node keys,
   and start the new binaries.
5. Restart the relayer against the same IBC clients. The provider rediscovers
   each consumer client at the next epoch boundary and, per the must-snapshot
   rule above, its first post-restart VSC to each launched consumer is a full
   snapshot.

**The relayer needs the restarted chain's pre-restart history.** Advancing an
IBC client past the restart requires a client update whose trusted validators
the relayer fetches from the restarted chain's RPC *at the pre-restart trusted
height* (ts-relayer builds every update this way). A node restarted with a
fresh data dir cannot serve those heights, so the counterparty's client of the
restarted chain can never be advanced and packet flow *from* the restarted
chain stalls: after a provider restart, VSC delivery to consumers; after a
consumer restart, acks and evidence back to the provider -- which keeps the
provider in snapshot mode and, since the liveness clock is ack-driven, will
eventually trip the unresponsive-consumer sweep. Either keep the restarted
chain's pre-restart block store queryable by the relayer until its clients have
advanced past the restart, or replace the stuck clients through the governance
client-recovery path.

Order the provider and consumers so the relayer can connect promptly after
start; the consumer safe-mode clock is preserved across the restart, so a long
gap before VSC traffic resumes is treated exactly as it would have been without
the restart.

## Notes

- VAAS is pre-release and undeployed: there are no genesis migrations. Export
  and import are same-version operations; a binary upgrade that changes state
  layout is out of scope here.
- The provider `ConsumerState` list preserves owner, metadata, and init params
  through `STOPPED` and `DELETED` so explorers can still describe removed
  consumers after a restart.
