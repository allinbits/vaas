# Consumer Transition (Standalone → VAAS Consumer)

> **Status:** future consideration. VAAS does **not** currently support
> transitioning an existing standalone Cosmos chain into a VAAS consumer;
> the consumer module only supports launching as a new chain. This document
> captures the consequences, requirements, and current code state so that
> future work has a starting point. It is **not** an implementation
> specification. Moreover, transition from non-canonical Cosmos chains
> may require additional design work.

A standalone-to-consumer transition lets an existing sovereign Cosmos
chain — one already producing blocks under its own `x/staking` module
(or equivalent) — swap its local proof-of-stake for the provider's
validator set without a chain-id change, a halt-and-restart, or a fork.
The chain keeps its account state, balances, and history; it gains the
provider's validator set as the consensus signer.

The [`interchain-security`](https://github.com/cosmos/interchain-security)
implementation supports this path. VAAS inherited the wiring, then the
rewrite simplified it away on the assumption that consumers would only
ever launch fresh. However, this initial simplification will need to be
revisited, we expect this transition path to be a requirement in the future.

---

## What a transition does

Before transition, the chain is a standard Cosmos chain:

- Local `x/staking` selects validators from local bonded stake
- Local validators sign blocks and earn local rewards.
- Local slashing handles equivocation and downtime.

After transition, the chain is a VAAS consumer:

- The provider's active validator set signs blocks (via VSC packets).
- Local staking remains *registered* (for slashing/jailing of validators
  that misbehaved while the chain was still standalone) but stops
  selecting block proposers.
- Slashing and unbonding-period semantics shift to the provider's
  parameters where applicable.

The transition is **atomic at a specific block height**: at the chosen
height, the consumer module receives the provider's initial validator set
and replaces the local set in `EndBlock`. There is no observable downtime
for users, balances, or contracts.

---

## Consequences

**For chain operators**
- The chain commits to the provider's security guarantees and accepts the
  provider's MaxProviderConsensusValidators cap.
- Local governance, fees, and application modules continue unchanged.
- The chain must coordinate the transition height in advance with the
  provider (via `MsgCreateConsumer` lifecycle, off-chain coordination, or
  governance proposal).

**For local validators**
- Validators that were local-only and are not in the provider's set lose
  block-signing rights at the transition height. They remain technically
  bonded for the unbonding period to allow slashing of past misbehaviour.
- Validators that exist in both the local set and the provider set should
  use `MsgAssignConsumerKey` ahead of the transition height so they
  continue signing under the same consensus identity.

**For delegators**
- Delegations to local validators continue to exist on-chain but cease
  earning local rewards once block-signing moves to the provider's set.
- Delegators may re-delegate to provider-side validators or unbond
  normally.

**For IBC connections to other chains**
- IBC clients pointing at the chain's old validator set need to update
  past the transition height; this is the same kind of update they
  perform after any validator-set change.
- IBC connections to the new provider must be created (the relayer
  is responsible).

---

## Requirements for implementation

1. **Genesis-time `preVAAS` flag.** The consumer's `InitGenesis` must
   accept a flag indicating the chain is mid-transition. When set, the
   consumer module:
   - Skips applying the provider's initial validator set to CometBFT
     (the local staking keeper continues to manage validators for one
     more block).
   - Marks the chain as previously-standalone for later cleanup.

2. **`standaloneStakingKeeper` plumbing.** The consumer module needs an
   explicit reference to the chain's prior `x/staking` keeper so it can:
   - Query the last local bonded validator set during the transition.
   - Allow the slashing module to jail/slash validators for infractions
     that occurred while the chain was standalone, even after the
     provider set takes over.
   - This reference is set after the keeper constructor by the app via
     `SetStandaloneStakingKeeper`.

3. **Upgrade handler.** The chain operator runs a coordinated software
   upgrade at the transition height that:
   - Adds the VAAS consumer module to the app.
   - Provides genesis state with `preVAAS = true` and the provider's
     client/consensus state.
   - Stops the local staking module from emitting validator-set updates
     (handled today by `x/vaas/no_valupdates_staking`).

4. **Provider-side `MsgCreateConsumer`.** The provider chain must already
   have the consumer registered through the standard lifecycle
   (`REGISTERED → INITIALIZED → LAUNCHED`) so that by the transition height
   the provider is ready to send VSC packets.

5. **Relayer coordination.** The IBC v2 clients must exist on both sides
   at the transition height. In VAAS today, client creation is the
   relayer's responsibility; for a transition this needs to be scheduled
   to land just before the transition height.

6. **Slashing window for prior misbehaviour.** The provider must respect
   the chain's prior unbonding period for slashing equivocations that
   happened on the chain *before* the transition. Implementation needs
   to decide whether to forward this evidence to the consumer's residual
   local staking keeper or handle it provider-side.

---

## Current code state

The consumer module retains the following wiring as a reference; **none
of it is reachable** in the current launch flow:

- `consumer/keeper/keeper.go` — fields `PreVAAS`, `InitialValSet`,
  `PrevStandaloneChain`, `standaloneStakingKeeper`; methods
  `IsPreVAAS`, `SetPreVAASTrue`, `DeletePreVAAS`, `SetInitialValSet`,
  `GetInitialValSet`, `GetLastStandaloneValidators`,
  `GetLastBondedValidators`, `MarkAsPrevStandaloneChain`,
  `IsPrevStandaloneChain`, `SetStandaloneStakingKeeper`.
- `consumer/keeper/genesis.go` — the `if state.PreVAAS { … }` branches in
  `InitGenesis`.
- `consumer/types/keys.go` — `PreVAASPrefix`, `InitialValSetPrefix`,
  `PrevStandaloneChainPrefix`.
- `x/vaas/types/shared_consumer.proto` — `ConsumerGenesisState.preVAAS`
  field.
- `proto/vaas/consumer/v1/genesis.proto` — `GenesisState.preVAAS` field.
- The provider always passes `preVAAS = false` to
  `NewInitialConsumerGenesisState` in `provider/keeper/consumer_lifecycle.go`.

This code is preserved deliberately as a reference for the eventual
transition implementation. It is **not** a working blueprint: any future
implementation will need a fresh design pass because the surrounding flow
(IBC v2, `cosmossdk.io/collections`, the simplified lifecycle) has
changed materially since the original ICS implementation. Treat the
existing wiring as a sketch of the data dependencies, not a guide to the
control flow.

---

## References

- ICS implementation: [`x/ccv/consumer/keeper`](https://github.com/cosmos/interchain-security)
  (look for `PreCCV`, `SovereignChangeover`, and related state).
- ICS docs: [Sovereign chain to consumer chain changeover](https://cosmos.github.io/interchain-security/consumer-development/changeover-procedure).
- VAAS architecture: [`DESIGN_RATIONALE.md`](../DESIGN_RATIONALE.md),
  [`docs/consumer-lifecycle.md`](consumer-lifecycle.md).
