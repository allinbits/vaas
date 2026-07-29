# Security Model and Trust Assumptions

This document states what a VAAS deployment trusts, what it punishes, and the
residual assumptions that remain. It describes the behavior that ships today.

VAAS lets a provider chain lease its proof-of-stake security to consumer chains.
Every bonded provider validator validates every launched consumer -- there is no
opt-in, opt-out, or power shaping. Security therefore rests on the provider's
validator set and on the integrity of the cross-chain messages between provider
and consumer.

## Cross-chain messages and their authentication

VAAS runs on IBC v2 only. Two message flows matter for security.

**Provider -> consumer: validator-set-change (VSC) packets.** The consumer
applies the validator set these packets carry, so their authenticity is what
keeps a consumer's set honest. The consumer accepts a VAAS packet only when it
carries the provider application's source port, arrives over an IBC client
whose tracked chain id matches the provider chain id pinned in the consumer's
genesis, and -- once a provider client has been adopted -- arrives over exactly
that pinned client (see the client-authentication model below). A malformed
packet -- an undecodable consensus pubkey or a negative power -- is rejected
with an error acknowledgement on receipt, never applied.

**Consumer -> provider: evidence.** Downtime evidence travels as IBC evidence
packets; the provider discovers the consumer's client at an epoch boundary
(`discoverActiveConsumerClient`) and prices any accepted downtime into a slash
held behind a challenge window. If the provider rejects an evidence packet, the
consumer surfaces the rejection (a `vaas_consumer_evidence_rejected` event)
rather than retrying it indefinitely; a packet that merely times out is retried.
Double-voting and light-client evidence are submitted as ordinary provider
transactions and independently re-verified, so a malformed or dishonest
submission can at worst waste the submitter's gas.

### Client authentication: content-bound adoption, then a permanent pin

Neither side trusts a chain-id string alone.

**Provider side.** The provider adopts a client for a consumer only once, and
only after verifying its content: the candidate must be an active tendermint
client of the consumer's chain id, counterparty-linked, and its latest consensus
state's `NextValidatorsHash` must equal the CometBFT hash of the validator set
the provider itself last computed and stored for that consumer (or the set
before that, to tolerate a validator-set change whose packet is still in
flight). The comparison is against the provider's own stored set, not against
delivery: `expectedConsumerValSetHashes` hashes `ConsumerValSet` and reads the
retained `ConsumerPrevValSetHash`. A chain that
copies the chain-id string cannot make the provider's own validators sign its
blocks, so its consensus states cannot carry the right hash and keep advancing;
a chain-id match that fails the content check is logged as a look-alike. If no
candidate verifies, the provider adopts nothing and retries at the next epoch
(fail closed -- the liveness sweep owns a consumer that never gets served). Once
adopted, the client is latched permanently: expiry, freezing, or counterparty
loss halt traffic rather than reopening adoption.

**Consumer side.** The provider client id is pinned. The client created from
provider-produced state at genesis cannot itself receive packets (IBC v2 only
routes to clients whose counterparty was registered by their creator, and a
genesis-created client has none), so the pin moves exactly once: from that
unroutable genesis client to the first client that actually delivers a VSC --
already proof-verified and counterparty-linked by IBC, and chain-id-gated by
VAAS -- and is then permanent. Every later packet must arrive over the pinned
client or it is rejected before any state changes. The residual
trust-on-first-use window is the interval between consumer start and its first
VSC delivery.

**Re-keying.** The only path to replace a dead client, on either side, is
IBC's governance client recovery (`MsgRecoverClient`), which substitutes the
client state under the same client id -- so the pin and the latch survive it.
No automatic re-adoption exists.

## Infractions and punishment

| Infraction | Detection | Punishment |
|---|---|---|
| Double-sign (duplicate vote) on a consumer | `MsgSubmitConsumerDoubleVoting`, re-verified on the provider | slash + jail + tombstone at `InfractionParameters.DoubleSign` |
| Light-client attack (IBC misbehaviour) on a consumer | `MsgSubmitConsumerMisbehaviour`, re-verified on the provider | identifiable byzantine signers slashed + jailed + tombstoned at the double-sign level; a confirmed attack that leaves no punishable validator stops the consumer and schedules it for removal, terminally |
| Downtime on a consumer | falsifiable IBC evidence packets | fee-priced slash held behind a challenge window; a successful `MsgChallengeConsumerDowntime` cancels it and moves the consumer to `PAUSED` |
| Double-sign on the provider itself | CometBFT `DuplicateVoteEvidence` via `x/evidence` | slash + jail + tombstone |

**The light-client escalation is terminal.** A confirmed light-client attack
where the provider could punish nobody -- an amnesia attack, which has no
byzantine set by construction, but equally a byzantine set whose members have
all unbonded and so cannot be jailed -- moves the consumer to `STOPPED` via
`escalateUnpunishableLightClientAttack`. Nothing takes a consumer back out of
`STOPPED`: `MsgResumeConsumer` requires `PAUSED`, `MsgRemoveConsumer` requires
`LAUNCHED` or `PAUSED`, and there is no cancel or veto message. The consumer is
deleted once the provider unbonding period elapses. That is the intended
posture -- a consumer that demonstrably produced conflicting valid headers has
forfeited the benefit of the doubt -- but it means an escalation cannot be
undone, and a chain that wants to run again must register afresh.

Provider-native equivocation is punished only if the embedding chain wires the
Cosmos SDK `x/evidence` module and its CometBFT evidence handling. This
repository's provider app wires it; a real embedding chain must do the same, or
provider-level double-signs go unpunished. See [embedding.md](embedding.md) for
that and the other host duties a real deployment has to carry.

Downtime slashing is deliberately falsifiable and conservative: the slash is
priced from foregone fees, not a flat stake fraction, is capped at
`InfractionParameters.Downtime.SlashFraction`, never jails, and can be cancelled
by the accused validator within the challenge window by proving liveness. See
[consumer-downtime.md](consumer-downtime.md).

## Fee escrow

An accepted downtime accusation withholds the accused validator's fee share for
the infraction epoch. The withheld amount never leaves the consumer's fee pool,
so the pool itself escrows a possible refund: a successful challenge pays the
withheld share back in full, while an accusation that matures unchallenged
forfeits it. Ordinary distribution and withdrawal reserve this outstanding
escrow, so the funds backing a live challenge can never be spent out from under
it. See [consumer-fee-pool.md](consumer-fee-pool.md).

## Assumptions and out of scope

- **Provider validator honesty.** VAAS inherits the provider chain's
  2/3-honest assumption. Collusion of 2/3+ of the provider's own validators can
  forge a consumer light-client history; that is the provider's own security
  boundary, not a VAAS-specific one.
- **Relayer liveness, not trust.** Consumer launch and evidence delivery depend
  on a relayer moving packets, but a relayer cannot forge or alter them. A
  launched consumer that stops receiving VSC packets eventually enters safe mode,
  and one that goes silent is eventually stopped for liveness (see
  [consumer-liveness.md](consumer-liveness.md)).
- **Consumer bootstrap window.** Client trust roots in the content commitment
  (see the client-authentication model above); the chain-id gate is
  defense-in-depth, not the trust root. The remaining assumption is the
  consumer's trust-on-first-use window between chain start and the first
  delivered VSC, after which the provider client is pinned for good.
