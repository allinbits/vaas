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
genesis, and -- once the provider client has been pinned -- arrives over exactly
that pinned client (see the client-authentication model below). A malformed
packet -- an undecodable consensus pubkey or a negative power -- is rejected
with an error acknowledgement on receipt, never applied.

**Consumer -> provider: evidence.** Downtime evidence travels as IBC evidence
packets over the declared client; the provider prices any accepted downtime into a slash
held behind a challenge window. If the provider rejects an evidence packet, the
consumer surfaces the rejection (a `vaas_consumer_evidence_rejected` event)
rather than retrying it indefinitely; a packet that merely times out is retried.
Double-voting and light-client evidence are submitted as ordinary provider
transactions and independently re-verified, so a malformed or dishonest
submission can at worst waste the submitter's gas.

### Client authentication: owner-declared clients, permanently pinned

Neither side trusts a chain-id string alone, and neither side infers a client
from delivered traffic. The party that registered the consumer declares both
bindings explicitly.

**Provider side.** The provider binds a client to a consumer exactly once, when
the consumer's owner (or governance) declares it through the optional
`client_id` of `MsgUpdateConsumer`. The declaration is validated before it
binds: the client must exist, be an active tendermint client of the consumer's
registered chain id, hold a registered IBC v2 counterparty (an IBC v2 packet
can only route over a counterparty-linked client), and its trusting period must
exceed the downtime challenge horizon, so every accepted downtime accusation
stays disprovable by a header the client can still verify. Until a valid
declaration lands the provider sends nothing (fail closed -- the liveness sweep
owns a consumer that never gets served). Once bound, the client is latched
permanently: expiry, freezing, or counterparty loss halt traffic rather than
reopening the binding.

**Consumer side.** The mirror declaration. The consumer genesis creates no
client and seeds two facts from the provider: the provider's chain id and the
owner's address. `MsgSetProviderClient`, signed by that owner (or governance,
where the embedding app wires it), pins the provider client once, under the
symmetric validations (existing, tendermint, chain-id match against the seeded
value, active, counterparty-linked). Every later packet must arrive over the
pinned client or it is rejected before any state changes. Until the pin lands,
a message filter keeps the chain in bootstrap: only IBC messages, governance,
and the pin message itself are accepted.

Both bindings rest on the owner's signature rather than on anything inferred
from delivered content, so no relayer, and no chain that merely copies the
chain-id string, can steer either side onto a client of its choosing. A
dishonest owner can only mis-declare the consumer that owner registered.

**Re-keying.** The only path to replace a dead client, on either side, is
IBC's governance client recovery (`MsgRecoverClient`), which substitutes the
client state under the same client id -- so the pin and the latch survive it.
No automatic re-binding exists.

## Infractions and punishment

| Infraction | Detection | Punishment |
|---|---|---|
| Double-sign (duplicate vote) on a consumer | `MsgSubmitConsumerDoubleVoting`, re-verified on the provider | slash + jail + tombstone at `InfractionParameters.DoubleSign` |
| Light-client attack (IBC misbehaviour) on a consumer | `MsgSubmitConsumerMisbehaviour`, re-verified on the provider | the consumer is paused, nobody is punished; the byzantine set is attributed on the event; governance resumes the chain or lets the pause expire into `STOPPED` |
| Downtime on a consumer | falsifiable IBC evidence packets | fee-priced slash held behind a challenge window; a successful `MsgChallengeConsumerDowntime` cancels it and moves the consumer to `PAUSED` |
| Double-sign on the provider itself | CometBFT `DuplicateVoteEvidence` via `x/evidence` | slash + jail + tombstone |

**A confirmed light-client attack punishes nobody, deliberately.** A malicious
consumer binary can orchestrate a fork in which every honest validator signs
each conflicting header once, so the byzantine set of a verified attack is
exactly as likely to be the victim set: the distinction does not exist in the
data, and any automatic punishment keyed on attacker-shaped evidence becomes a
targeting tool, at any threshold. The provider instead contains the chain: the
consumer is paused, VSC service stops, both evidence paths reject it, and its
pending downtime accusations are cancelled. Governance resumes the chain once
the fork is understood and fixed, or lets the pause expire into `STOPPED` via
`MaxPauseDuration`. See [equivocation-evidence.md](equivocation-evidence.md)
and the design contract on `HandleConsumerMisbehaviour`.

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

### The consumer binary is in the trust path

Mandatory validation means every provider validator runs every consumer's
binary: code the protocol never vets. Two exposures follow, and they are
accepted and bounded rather than solved:

- **Direct key abuse (the double-vote residual).** A validator that runs a
  consumer binary with an in-process signing key hands that key to the binary,
  which can then sign fabricated conflicting votes directly -- no fork, nothing
  for the misbehaviour path to contain -- and the resulting
  `MsgSubmitConsumerDoubleVoting` evidence is genuine by construction: the
  provider cannot distinguish it from a real equivocation, and slashes the
  validator's provider stake. The protocol deliberately does nothing automatic
  about this, for the same reason the light-client path punishes nobody. The
  defenses are operational: run consumer nodes behind an external signer whose
  double-sign guard the binary cannot bypass (this closes the vector
  completely), and vet the binary against the registered `binary_hash` before
  validating. The fork-based flavor of binary griefing, by contrast, is
  contained on-chain: the fork evidence itself pauses the consumer and closes
  its evidence pipeline.
- **The refusal dilemma.** A validator that vets a binary, finds it malicious,
  and refuses to run it is indistinguishable from an offline node, so the
  downtime path will accuse it. The current model bounds the cost: downtime
  accusations start only after the launch grace (`DowntimeGracePeriod`,
  default 7 days), every accepted accusation waits out the challenge window
  (`DowntimeChallengeWindow`, default 7 days) before executing, and pausing,
  stopping, or removing the consumer cancels all of its pending accusations.
  The playbook is: refuse from launch and submit the removal proposal
  immediately. Every accusation whose challenge window is still open when the
  removal passes dies with the chain; what executes is the windows that
  mature while governance deliberates. On a provider whose voting period
  exceeds the grace plus the challenge window -- AtomOne's does -- some
  windows will mature before any proposal can pass, so principled refusal has
  a real but bounded price: the per-window fraction (default `0.0001`, no
  tombstone) times the windows that close between grace expiry and removal.
  Reducing that price to zero needs protocol help (deferring downtime
  execution while a removal proposal is in voting) and is deliberately left
  to future work.

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
