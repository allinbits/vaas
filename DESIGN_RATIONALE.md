# VAAS Design Rationale

This is a forward-facing reference for contributors: the authoritative
statement of why VAAS is shaped the way it is. For protocol-level mechanics,
see [`docs/consumer-lifecycle.md`](docs/consumer-lifecycle.md) and
[`AGENTS.md`](AGENTS.md).

VAAS is a **simplified** Interchain Security: a Cosmos provider chain lends
its full validator set to one or more consumer chains, automatically, with no
opt-in/out and no power shaping. The simplification is the product, not a
work-in-progress.

---

## Guiding Principles

1. **All validators validate everything.** There is no per-consumer
   selection. The full active provider set is the consumer set.
2. **Economics stay minimal and provider-side.** There is no ICS-style
   cross-chain reward pipe, no per-consumer commission rates, no slash
   throttling, and no slash meters. What VAAS does carry is a per-consumer
   provider-side fee pool: consumers prepay `fees_per_block`, and once per
   epoch the provider collects `fees_per_block * blocks_per_epoch` and
   distributes it to the bonded validators.
3. **IBC v2 only.** No channel handshake, no ordered channels, no port
   reservations. VAAS modules register on `ibcRouterV2` under the application
   IDs `vaasprovider` and `vaasconsumer`; consumer launch relies on a relayer
   creating the IBC v2 clients and the consumer's owner declaring them on
   each chain.
4. **Almost-forward-only consumer lifecycle.** `REGISTERED -> INITIALIZED ->
   LAUNCHED -> STOPPED -> DELETED`, with three exceptions: a failed launch rolls
   back to `REGISTERED`; a successful downtime challenge moves a `LAUNCHED`
   consumer to `PAUSED`, from which governance either resumes it to `LAUNCHED`
   or removes it to `STOPPED`; and `MsgRetireConsumer` takes a consumer that
   never launched straight to `DELETED`, since a chain no validator ever
   validated needs no unbonding delay before its state can be erased. Deletion
   releases the consumer's chain id, so a registration does not tie up a chain
   id permanently. Standalone-to-consumer changeover is not
   currently supported; see
   [`docs/consumer-transition.md`](docs/consumer-transition.md) for the
   future-work considerations.
5. **Authority-gated packet emission.** Both modules' `OnSendPacket` require
   the module authority as signer, so only a chain's own module can emit VAAS
   packets. The provider sends VSC packets to consumers; a consumer sends
   downtime evidence back to the provider (`EvidencePacketData`), which the
   provider's `OnRecvPacket` verifies and prices into a pending slash.
   Equivocation and light-client misbehaviour, by contrast, are reported as
   ordinary provider transactions (`MsgSubmitConsumerDoubleVoting`,
   `MsgSubmitConsumerMisbehaviour`), not IBC packets.

---

## Why the Simplifications

### Removed: Partial Set Security (PSS), Top N, Opt-in, Power Shaping

The `interchain-security` codebase supports renting *subsets* of the
validator set per consumer with caps, allowlists, denylists, priority lists,
and inactive-validator participation (ADR-017). VAAS targets deployments
where the provider guarantees its entire active set to every consumer.
Removing PSS deletes a large surface area: per-validator opt-in state,
per-consumer power-shaping parameters, "has-to-validate" queries, and the
messages that maintain them (`MsgOptIn`, `MsgOptOut`,
`MsgSetConsumerCommissionRate`).

The trade-off is rigidity: a consumer cannot pick a smaller validator set.
That is intentional. Smaller sets do not inherit the BFT assumption of the
full set, there is no assumption that can be made about smaller sets, no
security guarantee. The simplification also drastically reduces the
complexity of the system.

### Removed: Slash Packet Throttling, Slash Meters, Slash Retry

ICS throttles slash packets to bound the impact of a misbehaving or
adversarial consumer on the provider's validator set. VAAS removes the
throttle, the meter, and the retry queue. Downtime is handled without any of
them: a consumer reports offline validators to the provider as falsifiable
`EvidencePacketData` (not an ICS slash packet), and the provider queues a
priced slash behind a challenge window instead of slashing on receipt. A
successful `MsgChallengeConsumerDowntime` -- a cryptographic proof that the
evidence was false -- cancels the queued slash and moves the consumer to
`PAUSED`. Equivocation evidence (double-sign, light-client) is submitted as a
provider transaction (`MsgSubmitConsumerDoubleVoting`,
`MsgSubmitConsumerMisbehaviour`) and slashed using the infraction parameters.
See [`docs/consumer-downtime.md`](docs/consumer-downtime.md).

### Removed: ICS Cross-Chain Reward Distribution

ICS pipes a fraction of consumer fees back to the provider as validator
rewards, through a cross-chain reward denom and the provider-side registration
state that supports it. VAAS drops that cross-chain pipe. In its place it runs
a simpler provider-side model: each consumer prepays a fee pool
(`fees_per_block`), and once per epoch the provider collects
`fees_per_block * blocks_per_epoch` from the pool and distributes it to the
bonded validators. See [`docs/consumer-fee-pool.md`](docs/consumer-fee-pool.md).

### Removed: Per-Consumer Infraction Parameters

ICS stores `infraction_parameters` (double-sign and downtime slash fractions,
jail durations, tombstone flag) per consumer. VAAS collapses them into a
single module-wide set applied uniformly to every consumer -- the protocol
slashes every consumer's infractions at the same severity.

Per-consumer infraction parameters are a plausible **future** addition -- a
consumer with a different security profile could warrant a different slash
severity -- and nothing in the current design precludes reintroducing them.
Treat the module-wide set as the present behavior, not a permanent guarantee:
docs and integrations should not assume infraction parameters will always be
global.

### Kept: Key Assignment

Validators may assign per-consumer consensus keys via
`MsgAssignConsumerKey`. Keys are ed25519 only and prune on unbonding, with
checks that prevent key reuse across consumers.

---

## Explicit Non-Goals

The following are intentionally **not** part of VAAS and should not be added
back without a strong, documented reason:

- Partial Set Security (Top N, opt-in/out, allow/deny lists)
- Per-consumer power shaping (caps, priority lists)
- Slash packet throttling / slash meters
- Cross-chain reward distribution
- Per-consumer commission rates
- IBC v1 channel routing for VAAS messages
- Inactive provider validators participating in consumer security (ADR-017)
