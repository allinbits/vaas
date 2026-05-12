# VAAS Design Rationale

> This document supersedes [`PLAN.old.md`](PLAN.old.md), the original rewrite
> plan drafted before the port from `interchain-security`. `PLAN.old.md` is
> kept for historical reference only; the present document is the
> authoritative statement of why VAAS is shaped the way it is.

This is a forward-facing reference for contributors. For the per-feature
historical diff against the `interchain-security` codebase VAAS was ported
from, see [`REWRITE_SUMMARY.md`](REWRITE_SUMMARY.md); for protocol-level
mechanics, see [`docs/consumer-lifecycle.md`](docs/consumer-lifecycle.md) and
[`AGENTS.md`](AGENTS.md).

VAAS is a **simplified** Interchain Security: a Cosmos provider chain lends
its full validator set to one or more consumer chains, automatically, with no
opt-in/out and no power shaping. The simplification is the product, not a
work-in-progress.

---

## Guiding Principles

1. **All validators validate everything.** There is no per-consumer
   selection. The active provider set, capped at
   `MaxProviderConsensusValidators`, is the consumer set.
2. **No cross-chain economics inside the protocol.** No reward distribution,
   no per-consumer commission rates, no slash throttling, no slash meters.
   Consumers stand up their own fee/reward models; the protocol carries only
   validator-set state and security signals.
3. **IBC v2 only.** No channel handshake, no ordered channels, no port
   reservations. VAAS modules register on `ibcRouterV2` under the application
   IDs `vaasprovider` and `vaasconsumer`; consumer launch relies on a relayer
   creating the IBC v2 clients and the provider discovering its consumer
   client at the next epoch boundary.
4. **Forward-only consumer lifecycle.** `REGISTERED → INITIALIZED → LAUNCHED
   → STOPPED → DELETED`, with a single rollback path (failed launch → back to
   `REGISTERED`). No standalone-to-consumer changeover.
5. **Provider authority for control-plane messages.** `OnSendPacket` requires
   the keeper's authority as signer; consumers never send packets back
   (`OnRecvPacket` on the provider is a failure path; `OnSendPacket` on the
   consumer is rejected). Misbehaviour reports travel as ordinary provider
   transactions, not IBC packets.

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
throttle, the meter, and the retry queue. Consumer-initiated slashing for
downtime is *not currently performed by the provider*; equivocation evidence
(double-sign, light-client) is submitted as a provider transaction
(`MsgSubmitConsumerDoubleVoting`, `MsgSubmitConsumerMisbehaviour`) and
slashed using per-consumer infraction parameters. Downtime slashing via slash
packets is an open design question (see open issues / PRs).

### Removed: Consumer Reward Distribution

ICS pipes a fraction of consumer fees back to the provider as validator
rewards. VAAS leaves the consumer fee model entirely to the consumer chain.
This eliminates a category of cross-chain accounting and the related
provider-side state (reward denom registration, fee pool addressing, etc.).

### Kept: Per-Consumer Infraction Parameters

Each consumer carries its own `infraction_parameters` (double-sign and
downtime slash fractions, jail durations, tombstone flag). This is the one
place per-consumer customisation survives — the protocol cannot decide
slash severity centrally because consumers vary in security profile.

### Kept: Key Assignment

Validators may assign per-consumer consensus keys via
`MsgAssignConsumerKey`. Keys are ed25519 only and prune on unbonding, with
checks that prevent key reuse across consumers.

---

## Active Work and Open Questions

Live work is tracked in GitHub issues and pull requests, not in this file —
this list points to the rough areas, not the specific tickets.

- **Genesis import/export**. Several entry points in the provider and
  consumer modules still need correct round-trip support. The provider
  `InitGenesis` path uses the chain-ID field as the consumer-id key, which
  needs cleaning up.
- **Per-consumer infraction parameters at runtime**. The parameters are
  stored per consumer but the equivocation handling path needs to consume
  them consistently.
- **Provider-side downtime slashing.** Whether (and how) consumers should be
  able to request downtime slashing on the provider is an open design
  question; a draft PR proposes a slash-packet-based path.
- **Timeout policy.** A VSC packet timeout currently has heavy consequences
  (consumer removal); whether this is the right default is open.
- **Dead state and naming debt.** Some collections from earlier iterations
  (`PreVAAS`, `PrevStandaloneChain`) are still wired in but unused; some
  error codes registered in the shared types module are no longer raised.

## Explicit Non-Goals

The following are intentionally **not** part of VAAS and should not be added
back without a strong, documented reason:

- Partial Set Security (Top N, opt-in/out, allow/deny lists)
- Per-consumer power shaping (caps, priority lists)
- Slash packet throttling / slash meters
- Cross-chain reward distribution
- Standalone-to-consumer changeover
- Per-consumer commission rates
- IBC v1 channel routing for VAAS messages
- Inactive provider validators participating in consumer security (ADR-017)
