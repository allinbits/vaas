# Consumer Transition (Standalone to VAAS Consumer)

> **Status: reserved / not yet implemented.** VAAS does **not** currently
> support transitioning an existing standalone Cosmos chain into a VAAS
> consumer; the consumer module only supports launching as a brand-new chain.
> This document is a forward-looking specification: it describes what such a
> changeover *would* do and the wiring a future implementation would have to
> add. It is **not** a description of shipped behavior. Transitioning
> non-canonical Cosmos chains may require additional design work.

A standalone-to-consumer transition would let an existing sovereign Cosmos
chain — one already producing blocks under its own `x/staking` module (or
equivalent) — swap its local proof-of-stake for the provider's validator set
without a chain-id change, a halt-and-restart, or a fork. The chain would keep
its account state, balances, and history; it would gain the provider's
validator set as the consensus signer.

The [`interchain-security`](https://github.com/cosmos/interchain-security)
implementation supports this path. VAAS inherited a partial copy of the wiring,
but it was never functional under the IBC-v2-only, `collections`-based rewrite,
so the dead Go was removed (see *Current code state* below). Only a reserved
genesis field is kept so the feature can be reintroduced cleanly. We expect
this transition path to be a requirement in the future.

---

## What a transition would do

Before transition, the chain is a standard Cosmos chain:

- Local `x/staking` selects validators from local bonded stake.
- Local validators sign blocks and earn local rewards.
- Local slashing handles equivocation and downtime.

After transition, the chain is a VAAS consumer:

- The provider's active validator set signs blocks (via VSC packets).
- Local staking remains *registered* (so validators that misbehaved while the
  chain was still standalone can still be slashed/jailed) but stops selecting
  block proposers.
- Slashing and unbonding-period semantics shift to the provider's parameters
  where applicable.

The transition would be **atomic at a specific block height**: at the chosen
height the consumer module receives the provider's initial validator set and
replaces the local set in `EndBlock`. There would be no observable downtime for
users, balances, or contracts.

---

## Consequences

**For chain operators**
- The chain commits to the provider's security guarantees and receives the full
  active provider validator set, with no cap.
- Local governance, fees, and application modules continue unchanged.
- The chain must coordinate the transition height in advance with the provider
  (via the `MsgCreateConsumer` lifecycle, off-chain coordination, or a
  governance proposal).

**For local validators**
- Validators that were local-only and are not in the provider's set lose
  block-signing rights at the transition height. They remain technically bonded
  for the unbonding period to allow slashing of past misbehaviour.
- Validators that exist in both the local set and the provider set should use
  `MsgAssignConsumerKey` ahead of the transition height so they continue signing
  under the same consensus identity.

**For delegators**
- Delegations to local validators continue to exist on-chain but cease earning
  local rewards once block-signing moves to the provider's set.
- Delegators may re-delegate to provider-side validators or unbond normally.

**For IBC connections to other chains**
- Existing IBC light clients on third-party chains that track this chain go
  **stale** at the transition height. Tendermint light clients accept a new
  header only if the validators signing it overlap with the previously trusted
  set by at least the client's trust level (1/3 by default). A
  standalone-to-consumer transition rotates the validator set wholesale to the
  provider's set, so the overlap is effectively zero and the light client cannot
  follow the update — it gets stuck at the pre-transition height and any packets
  relayed against it fail to verify.
- Counterparty chains have two recovery paths, both requiring off-chain
  coordination:
  - **Gov-gated client substitution** ([`MsgRecoverClient`](https://ibc.cosmos.network/main/ibc/proto-docs.html#ibc.core.client.v1.MsgRecoverClient)
    in IBC-go): the counterparty chain's governance votes to substitute the
    stale client with a freshly-created one tracking the new (provider-driven)
    validator set. This preserves the existing connection and channels, so
    balances and packet sequences are retained.
  - **Full reconnection**: tear down the existing client/connection/channels and
    create new ones from scratch. Cheaper to execute but loses channel state,
    in-flight packets, and any client-side invariants the counterparty relied
    on.
- New IBC v2 clients between the consumer (post-transition) and the provider
  must be created by the relayer as part of the standard consumer launch flow.
- **Operational implication.** Chain operators and counterparty teams must
  coordinate the transition height well in advance so counterparty governance
  proposals (or reconnection runbooks) can be staged and executed. This is the
  highest user-visible cost of a transition and should be treated as a hard
  prerequisite rather than a follow-up.

---

## Requirements for implementation

1. **Genesis-time `preVAAS` flag handling.** The consumer's `InitGenesis` would
   branch on the reserved `preVAAS` genesis field (still present on
   `GenesisState`; see *Current code state*). When set, the consumer module
   would:
   - Skip applying the provider's initial validator set to CometBFT (the local
     staking keeper keeps managing validators for one more block).
   - Mark the chain as previously-standalone for later cleanup and store the
     initial validator set.

2. **`standaloneStakingKeeper` plumbing.** The consumer module would need an
   explicit reference to the chain's prior `x/staking` keeper so it can:
   - Query the last local bonded validator set during the transition.
   - Let the slashing module jail/slash validators for infractions that occurred
     while the chain was standalone, even after the provider set takes over.
   The reference would be injected by the app after the keeper constructor (for
   example via a `SetStandaloneStakingKeeper` setter). Neither the field nor the
   setter exists today — both were removed and would be reintroduced.

3. **Consumer-side staking module.** The consumer app currently wires no staking
   keeper for the consumer module to lean on, so the standalone staking keeper
   above would always be nil today. A changeover implementation must first wire a
   real staking module into the consumer app to hold the residual local set.

4. **Upgrade handler.** The chain operator runs a coordinated software upgrade at
   the transition height that:
   - Adds the VAAS consumer module to the app.
   - Provides genesis state with `preVAAS = true` and the provider's
     client/consensus state.
   - Stops the local staking module from emitting validator-set updates.
     `x/vaas/no_valupdates_staking` is the wrapper module that does this, but it
     is wired on the provider app only; the reference consumer app wires no
     staking module at all, so a changeover would have to wire the wrapper on
     the consumer side as part of requirement 3.

5. **Provider-side `MsgCreateConsumer`.** The provider chain must already have
   the consumer registered through the standard lifecycle
   (`REGISTERED -> INITIALIZED -> LAUNCHED`) so that by the transition height the
   provider is ready to send VSC packets. The provider would also have to pass
   `preVAAS = true` in the consumer genesis it builds (today it always passes
   `false`).

6. **Relayer coordination.** The IBC v2 clients must exist on both sides at the
   transition height. In VAAS today, client creation is the relayer's
   responsibility; for a transition this needs to be scheduled to land just
   before the transition height.

7. **Counterparty client-recovery coordination.** Every third-party chain that
   runs an IBC light client tracking the transitioning chain must pre-stage a
   `MsgRecoverClient` governance proposal (or a full reconnection runbook)
   targeting the transition height — see the *For IBC connections to other
   chains* note above. Without this, existing connections go stale and packet
   traffic halts on those lanes. This is a prerequisite for transition rather
   than a follow-up.

8. **Slashing window for prior misbehaviour.** The provider must respect the
   chain's prior unbonding period for slashing equivocations that happened on the
   chain *before* the transition. Implementation needs to decide whether to
   forward this evidence to the consumer's residual local staking keeper or
   handle it provider-side.

---

## Current code state

The dead standalone-changeover Go was removed because it was non-functional
under the current architecture: the consumer app wires no staking keeper for the
consumer module to use as a standalone staking keeper, so the plumbing was always
nil, and a hand-crafted `preVAAS = true` genesis would have skipped the
validator-set application and bricked the chain rather than performing a
changeover.

**Reserved and kept (inert):**
- `proto/vaas/consumer/v1/genesis.proto` — `GenesisState.preVAAS` (field 5) is
  kept and documented-reserved. It is deliberately **not** deleted and **not**
  marked with the proto `reserved` keyword, so a future implementation can reuse
  the same field number without a wire-format clash. The consumer keeper does
  not read it today.
- `proto/vaas/v1/shared_consumer.proto` — `ConsumerGenesisState.preVAAS`
  (field 4) is likewise still present; `x/vaas/types/genesis.go` still validates
  it and the provider still passes `preVAAS = false` when building a consumer
  genesis. Removing this shared-type field and its validation is a separate
  cleanup.
- `x/vaas/consumer/keeper` — the `InitialValSet` collection with its
  `SetInitialValSet` / `GetInitialValSet` accessors. These were populated only by
  the old changeover branch and are now exercised only by unit tests; a future
  implementation can reuse them.

**Removed (was dead weight):**
- The `PreVAAS` and `PrevStandaloneChain` state collections.
- The `standaloneStakingKeeper` field and its `SetStandaloneStakingKeeper`
  setter.
- The keeper methods `IsPreVAAS`, `SetPreVAASTrue`, `DeletePreVAAS`,
  `MarkAsPrevStandaloneChain`, `IsPrevStandaloneChain`,
  `GetLastStandaloneValidators`, and the standalone-only
  `GetLastBondedValidators`.
- The `if state.PreVAAS { ... }` branches in the consumer `InitGenesis`.

Any future implementation will need a fresh design pass: the surrounding flow
(IBC v2, `cosmossdk.io/collections`, the simplified lifecycle) has changed
materially since the original ICS implementation, and the consumer app would
first have to wire a staking module for the residual local set. Treat the ICS
sources below as a sketch of the data dependencies, not a guide to the control
flow.

---

## References

- ICS implementation: [`x/ccv/consumer/keeper`](https://github.com/cosmos/interchain-security)
  (look for `PreCCV`, `SovereignChangeover`, and related state).
- ICS docs: [Sovereign chain to consumer chain changeover](https://cosmos.github.io/interchain-security/consumer-development/changeover-procedure).
- VAAS architecture: [`DESIGN_RATIONALE.md`](../DESIGN_RATIONALE.md),
  [`docs/consumer-lifecycle.md`](consumer-lifecycle.md).
