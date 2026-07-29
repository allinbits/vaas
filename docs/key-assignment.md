# Key Assignment

A provider validator validates every consumer chain. By default it does so with
the same consensus key it uses on the provider. **Key assignment** lets a
validator use a *different* consensus key on a given consumer chain, so it can
run the consumer node with a distinct signing key without touching its provider
key.

This is optional. A validator that assigns no key simply uses its provider
consensus key on the consumer, and the provider treats the consumer consensus
address as identical to the provider one
([x/vaas/provider/keeper/key_assignment.go](../x/vaas/provider/keeper/key_assignment.go)
`GetProviderAddrFromConsumerAddr`, lines 164-178).

That equivalence is what makes rotating the *provider* consensus key a
cross-chain event for a validator that assigned nothing:
[Rotating your provider consensus key](#rotating-your-provider-consensus-key) is
the procedure.

## Command

```
providerd tx vaasprovider assign-consensus-key <consumer-id> <consumer-pubkey> \
    --from <validator-key>
```

- The transaction must be signed by the validator's own account key. The rule is
  enforced in stateless validation, not in the message server:
  `MsgAssignConsumerKey.ValidateBasic` calls `validateProviderAddress`
  ([x/vaas/provider/types/msg.go](../x/vaas/provider/types/msg.go)), which
  requires `provider_addr` converted to an account address to equal the signer.
  The handler (`msgServer.AssignConsumerKey`) then only checks that the named
  validator already exists on the provider. One validator cannot assign a key on
  another's behalf.
- `<consumer-pubkey>` is a JSON-encoded ed25519 public key in the standard
  Cosmos form:

  ```json
  {"@type":"/cosmos.crypto.ed25519.PubKey","key":"<base64-encoded-pubkey>"}
  ```

  This is exactly what `consumerd tendermint show-validator` prints on the
  consumer node. The `@type` and `key` fields are parsed by
  `ParseConsumerKeyFromJson`
  ([x/vaas/provider/types/msg.go](../x/vaas/provider/types/msg.go) lines
  335-346).

CLI source: `NewAssignConsumerKeyCmd`
([x/vaas/provider/client/cli/tx.go](../x/vaas/provider/client/cli/tx.go) lines
64-105).

## Rules

All enforced in `Keeper.AssignConsumerKey`
([key_assignment.go](../x/vaas/provider/keeper/key_assignment.go) lines 65-162)
unless noted.

1. **ed25519 only.** Any other key type is rejected with
   `ErrValidatorPubKeyTypeNotSupported` (lines 44-49). This is a deliberate
   simplification carried over from Interchain Security; the consensus-params
   check that would otherwise decide supported types is disabled (lines 27-40).

2. **The consumer must be active.** Assignment is allowed only while the
   consumer is `REGISTERED`, `INITIALIZED`, or `LAUNCHED` (`IsConsumerActive`,
   [permissionless.go](../x/vaas/provider/keeper/permissionless.go) lines
   189-195); otherwise `ErrInvalidPhase` (lines 74-79). You cannot assign a key
   on a `PAUSED`, `STOPPED`, or `DELETED` consumer.

3. **No reuse across validators.** A consumer key already in use -- as another
   validator's provider consensus key, or as any validator's assigned consumer
   key on this consumer (including a key still awaiting pruning) -- is rejected
   with `ErrConsumerKeyInUse` (lines 93-100 and 112-119). This prevents two
   validators from colliding on one consumer consensus address, and prevents a
   validator from re-adopting a key it previously used on that consumer that is
   still being pruned.

4. **No assigning your default provider key as a consumer key** unless you have
   already assigned some other consumer key first -- `ErrCannotAssignDefaultKeyAssignment`
   (lines 101-109). This keeps the "no assignment means default key" invariant
   unambiguous.

5. **Reassignment prunes the old key after unbonding.** Assigning a new key when
   one is already assigned replaces the mapping. If the consumer has launched,
   the old consumer address is not deleted immediately -- it is queued for
   pruning at `block_time + unbonding_period` (lines 129-143), so it stays
   resolvable for any in-flight evidence referencing the old key while the
   validator is still slashable. If the consumer has not launched yet, the old
   mapping is removed at once (lines 144-148). Queued addresses are pruned in
   `PruneKeyAssignments`, which runs each `EndBlock` (lines 180-194).

6. **A new provider validator cannot take a consensus key already assigned as a
   consumer key.** The staking `AfterValidatorCreated` hook calls
   `ValidatorConsensusKeyInUse` (lines 216-245); if the new validator's
   consensus address is already some validator's assigned consumer address on an
   active consumer, validator creation is aborted.

## The consumer-address mapping

Two mappings back every assignment:

- `ValidatorConsumerPubKey`: `(consumerId, providerConsAddr) -> consumerKey`.
  Deleted automatically when the validator is removed from the staking module.
- `ValidatorByConsumerAddr`: `(consumerId, consumerConsAddr) -> providerConsAddr`.
  Removed through the pruning mechanism described above.

The provider consults these whenever it must translate a consumer consensus
address back to the provider validator -- for example, when attributing downtime
evidence or double-voting evidence to the right validator. When no mapping
exists, the consumer address *is* the provider address.

On consumer deletion, all key-assignment state for that consumer is cleared by
`DeleteKeyAssignments` (lines 196-214).

## Rotating your provider consensus key

Rotating your *provider* consensus key is an `x/staking` operation, not a VAAS
one:

```
providerd tx staking rotate-cons-pubkey <validator-addr> \
    '{"@type":"/cosmos.crypto.ed25519.PubKey","key":"<base64-encoded-pubkey>"}' \
    --from <validator-key>
```

`x/staking` charges its `key_rotation_fee` and limits how many rotations one
validator may perform within an unbonding period. What follows is only what the
rotation means for the consumers you validate.

### It changes your consumer identity only where you assigned no key

On a consumer where you assigned a distinct consumer key, that key is what the
consumer validates you under, and the rotation leaves it untouched: nothing
about that consumer node changes. On every consumer where you assigned nothing,
your provider consensus key *is* your consumer consensus key -- so rotating it
changes the identity all of those consumers expect to sign their blocks, and it
changes them all at once.

### Swap the node signing key at the rotation, not before and not long after

For the consumers whose view of you changes, the provider does not wait for the
epoch cadence. `Hooks.AfterConsensusPubKeyUpdate` calls
`QueueConsPubKeyRotationSnapshots`
([relay.go](../x/vaas/provider/keeper/relay.go)), which queues a full snapshot
VSC packet for each affected launched consumer and sends it in the same block,
so they learn the new key promptly instead of up to `blocks_per_epoch` blocks
later. Consumers where you *do* have an assigned key are skipped: their view is
unchanged, and a snapshot would cost a packet, a valset-update id, and a relayer
round trip to deliver a set identical to the one they already hold.

Your part is to have the matching signing key in place when the snapshot lands.
Swap it early and you sign with a key those consumers do not yet accept; leave
the old one in place afterwards and they count every block you produce against a
key you no longer hold. Either way you accumulate missed blocks on every
no-assigned-key consumer simultaneously, and the launch grace period cannot
absorb it: the grace period ends at `SpawnTime + DowntimeGracePeriod`, anchored
to the consumer's launch and not to your rotation
([consumer-downtime.md](consumer-downtime.md) section 3).

The snapshot rides the IBC client the provider already discovered for that
consumer -- rotation never triggers client discovery, which belongs to the epoch
path. A consumer with no discovered client yet, or one whose send fails, simply
keeps the packet queued for the next epoch rather than failing the block. A
paused or not-yet-launched consumer gets no snapshot at all; a governance resume
forces its own snapshot resync anyway
([consumer-downtime.md](consumer-downtime.md) section 7).

### Rotating onto a key already assigned as a consumer key is rejected

The provider's `ConsPubKeyRotationDecorator`
(`x/vaas/provider/ante/cons_pubkey_rotation_ante.go`)
inspects every `MsgRotateConsPubKey` at **transaction admission** -- including
one nested inside an `authz.MsgExec` -- and fails it with `ErrConsumerKeyInUse`
if the new consensus key is already some validator's assigned consumer key on
any consumer that is not deleted. Paused and stopped consumers count: they keep
their key assignments, and a paused one can be resumed.

Admission is the enforcement point because it is the last one available.
`x/staking` applies a recorded rotation from `EndBlock`, not from the message
handler, so by the time VAAS observes it in `AfterConsensusPubKeyUpdate` the
rotation is already committed and there is nothing left to refuse. A rejection
therefore costs you the transaction and nothing else. Note this holds only on a
chain that wired the decorator; without it such a rotation succeeds and one of
the two colliding validators is silently dropped from that consumer's validator
set ([embedding.md](embedding.md) section 7).

### Where your state ends up

`MigrateStateOnConsPubKeyRotation`
(`x/vaas/provider/keeper/cons_pubkey_rotation.go`)
re-keys what the provider holds under your provider consensus address, per
consumer, and deliberately does not move all of it.

- **Assigned consumer keys move with you.** On every consumer where you have an
  assignment, the forward mapping and every reverse mapping that named your old
  address are repointed at the new one -- including old consumer addresses still
  kept resolvable for pending evidence. The assignment keeps resolving in the
  validator-set computation and cross-chain evidence keeps attributing to you.
- **Fee bookkeeping always moves, regardless of key assignment.** The current
  epoch's downtime mark and any fee share escrowed against an open challenge
  window follow you on every consumer, because epoch fee distribution reads them
  under your *live* consensus address. Nothing you are owed, and nothing you are
  excluded for, is lost or counted twice.
- **Downtime acceptance bookkeeping moves only where you assigned a key.**
  Pending slashes, the windows already accepted against you, and the pruning
  floor move on the consumers whose view of you did not change. On the consumers
  where it *did* change -- the ones where you assigned no key -- they stay under
  your **pre-rotation** address on purpose: that is the identity the consumer
  validated you under, and the identity its accusations resolve to. Moving them
  would leave a queued slash unchallengeable and let an already-judged window be
  accepted a second time against your new address as if it were a fresh
  infraction.

The operator consequence is narrow but worth knowing: a downtime slash raised
before the rotation on a consumer where you assigned no key still sits under
your **old** consensus address. `pending-downtime-slashes` reports it there, and
`challenge-consumer-downtime` takes the consumer consensus address from the
evidence -- which on such a consumer is that same old address. Use it. The
challenge itself never consults key-assignment state; it authenticates against
whatever key signed the block ([consumer-downtime.md](consumer-downtime.md)
section 6).

## Operational notes

- Assign the key **before** the validator's consumer node needs to sign blocks
  under it -- ideally while the consumer is `REGISTERED`/`INITIALIZED`, so the
  key is in place at launch.
- After a reassignment on a launched consumer, keep the old consumer node key
  available until the unbonding period elapses: evidence about the pre-rotation
  period is still attributed to you, and a downtime challenge you might need to
  raise is self-authenticating against whatever key signed the block (see
  [consumer-downtime.md](consumer-downtime.md) section 6, which notes challenges
  do not rely on key-assignment state).
- Assigning a consumer key on every consumer is the way to insulate your
  consumer nodes from provider consensus-key rotations entirely: with an
  assignment in place, a rotation changes nothing a consumer can see. See
  [Rotating your provider consensus key](#rotating-your-provider-consensus-key).
