# Embedding VAAS in a Host Chain

Adding the VAAS provider or consumer module to an existing Cosmos SDK
application is **not** just a matter of registering the IBC v2 routes. Both
modules depend on host wiring they cannot install themselves, and most of that
wiring fails *silently* when it is missing -- the app builds, boots, syncs, and
behaves normally until the specific situation the missing piece was protecting
against arrives. One omission halts the provider chain outright, at an event
that is guaranteed to happen eventually.

This document is the checklist. For each duty it states what to wire, what
breaks without it, and whether the failure is silent, fatal at startup, or a
runtime panic.

## The reference apps are references, not templates

`app/provider` and `app/consumer` in this repository exist so the protocol can
be run and tested end to end. Read them as worked examples:

- **[`app/provider/app.go`](../app/provider/app.go) demonstrates the full
  wiring.** Everything in this document is present there, and it is the file to
  diff your own app against.
- **[`app/consumer/app.go`](../app/consumer/app.go) is deliberately reduced.**
  Most visibly it wires **no governance module at all** -- no `govkeeper`, no
  `gov.NewAppModule`, no gov store key, no gov entry in any module ordering. It
  imports `govtypes` for one purpose: to derive the authority address string.
  That is a legitimate choice for a demo chain, and it is exactly why
  `MsgRecoverClient` is unusable there (see
  [Governance as the IBC client authority](#4-governance-as-the-ibc-client-authority)).
  A real consumer chain must not copy that shape.

Both apps also assert `ibctesting.TestingApp` conformance in production code,
which pulls `ibc-go`'s testing package into the binary. Drop that assertion in a
real app.

---

## 1. `maccPerms` must contain the provider module -- omit it and the chain halts

**This is the one that halts the chain.** Wire it first.

```go
maccPerms = map[string][]string{
    // ... the usual SDK entries ...
    providertypes.ModuleName: nil,
}
```

No permissions are needed -- the provider module account never mints, burns, or
stakes. It is a pass-through that money hops through for exactly one step.
`providertypes.ModuleName` is `"vaasprovider"`.

**Why it is load-bearing.** The fee-pool sweep drains a consumer's whole pool
balance into the provider module account and fans it back out to depositors in
the same transaction (`SweepConsumerFeePoolDenom` in
[x/vaas/provider/keeper/fee_pool_shares.go](../x/vaas/provider/keeper/fee_pool_shares.go)).
Both hops are wrapped in `panic`, by design: the sweep runs on the consumer
deletion path, where returning an error would strand the consumer in `STOPPED`
with no way out, so state corruption is made loud instead of silent.

**The panic path when the entry is missing.** The SDK resolves a module name to
an address through the permissions map the account keeper was constructed with.
With no entry, `GetModuleAddress` returns nil and `bank`'s
`SendCoinsFromAccountToModule` / `SendCoinsFromModuleToAccount` **panic**
(`ErrUnknownAddress`, "module account does not exist") before VAAS's own wrappers
are even reached. In a transaction that panic is recovered by baseapp and the
transaction merely fails. In `BeginBlock` it is not: `baseapp.beginBlock` has no
`recover()`, so the panic escapes `FinalizeBlock` and every honest node dies
deterministically at the same height.

And `BeginBlock` is where it lands, because consumer deletion is a `BeginBlock`
event. `AppModule.BeginBlock` calls `BeginBlockRemoveConsumers`, which calls
`DeleteConsumerChain`, which auto-sweeps the pool. Deletion is reached three
ways, and only the first needs anybody to do anything:

1. governance `MsgRemoveConsumer`, then the removal queue drains one unbonding
   period later;
2. the **automatic liveness sweep** (`SweepUnresponsiveConsumers`), which stops
   any launched consumer that has not acked a VSC packet within the liveness
   grace period -- no governance, no relayer, no operator involved;
3. the **pause auto-stop**, when a paused consumer outlives `MaxPauseDuration`.

Any consumer that ever stops relaying is eventually deleted. There is no
deployment in which this never fires.

**Nothing checks for you.** The app boots fine; no startup validation covers the
map. Recovery requires a coordinated binary fix, because the pending deletion
re-fires immediately on restart.

**Second effect.** The provider module address is normally in the bank's blocked
set, which is built from `maccPerms`. Without the entry, users can also send
arbitrary coins directly to the provider module account.

The consumer app needs **no** `maccPerms` entry for its own module: the consumer
keeper moves fees only through the fee collector, which is already in every
app's map.

## 2. The bank send restriction on the provider

```go
app.BankKeeper.AppendSendRestriction(app.ProviderKeeper.FeePoolSendRestriction())
```

Register it right after constructing the provider keeper.
`FeePoolSendRestriction`
([send_restriction.go](../x/vaas/provider/keeper/send_restriction.go)) rejects
any bank send addressed at a registered consumer fee-pool address unless the
sender is the provider or distribution module account.

**Omitting it is silent, and user-triggerable.** Fee-pool addresses are
deterministic and publicly derivable, so without the restriction anyone can send
coins into a pool without going through `MsgFundConsumerFeePool`. Those coins
arrive as pool balance with no shares behind them, which the share accounting
cannot attribute to any depositor -- the state the restriction exists to
prevent. See [consumer-fee-pool.md](consumer-fee-pool.md).

## 3. The provider's staking hooks

```go
app.StakingKeeper.SetHooks(
    stakingtypes.NewMultiStakingHooks(
        app.DistrKeeper.Hooks(),
        app.SlashingKeeper.Hooks(),
        app.ProviderKeeper.Hooks(),
    ),
)
```

Three of the provider's hook methods carry real behavior; the rest are no-ops:

- `AfterValidatorCreated` -- rejects creating a validator whose consensus key is
  already in use as some validator's assigned consumer key. This is the *only*
  enforcement point for that rule at validator creation.
- `AfterConsensusPubKeyUpdate` -- after a consensus-key rotation, migrates the
  validator's per-consumer provider-consensus-address-keyed state onto the new
  address (`MigrateStateOnConsPubKeyRotation`) and hands the rotated key to the
  consumers whose view of the validator it changes, right away rather than at the
  next epoch boundary (`QueueConsPubKeyRotationSnapshots`). See
  [key-assignment.md](key-assignment.md#rotating-your-provider-consensus-key).
- `AfterValidatorRemoved` -- deletes the removed validator's key assignments and
  reverse mappings.

**Omitting the hooks is entirely silent.** `ValidatorConsensusKeyInUse`
([key_assignment.go](../x/vaas/provider/keeper/key_assignment.go)),
`MigrateStateOnConsPubKeyRotation` (`cons_pubkey_rotation.go`),
and `QueueConsPubKeyRotationSnapshots`
([relay.go](../x/vaas/provider/keeper/relay.go)) have no other production caller,
so there is no error, no log, and no startup check. What you lose: two validators
can end up at the same consensus address on a consumer chain; a rotated
validator's assigned consumer key silently stops resolving, so cross-chain
evidence stops attributing to it; its downtime and fee bookkeeping is left
stranded at the old address, so an epoch's downtime exclusion goes unseen and a
withheld fee share is never reconciled; consumers learn a rotated key only at the
next epoch boundary, so the validator accrues missed blocks in the meantime; and
key-assignment rows leak for removed validators, keeping a dead validator's old
consumer address resolvable.

**A wiring hazard specific to this line.** `Keeper.Hooks()` has a pointer
receiver, so `app.ProviderKeeper.Hooks()` captures `&app.ProviderKeeper`. That
is what makes it legal to register the hooks *before* assigning the keeper --
the reference app does exactly that, because the staking keeper must exist first.
If your app stores the provider keeper in a local variable, or copies it by
value, the hooks object points at a zero keeper and every hook nil-dereferences.

## 4. Governance as the IBC client authority

```go
app.IBCKeeper = ibckeeper.NewKeeper(
    appCodec,
    runtime.NewKVStoreService(keys[ibcexported.StoreKey]),
    app.GetSubspace(ibcexported.ModuleName),
    app.UpgradeKeeper,
    authtypes.NewModuleAddress(govtypes.ModuleName).String(),
)
```

Passing the gov module address is only half the job: **a real chain must also
wire `x/gov` itself**, so that something can actually execute a message signed
by that address.

**Why VAAS cares.** The only way to replace a dead IBC client on either side is
ibc-go's governance client recovery, `MsgRecoverClient`, which substitutes the
client state under the same client id so the consumer's pin and the provider's
latch both survive it (see [security-model.md](security-model.md)). VAAS routes
operators to it explicitly: `ResumeConsumerChain` pre-flights the client and, if
it is expired or frozen, fails the resume with instructions to bundle
`MsgRecoverClient` into the same governance proposal.

**Omitting governance is silent until recovery is needed, and then terminal.**
ibc-go's `RecoverClient` handler compares `msg.Signer` against the keeper's
authority and rejects anything else with `ErrUnauthorized`. That authority is a
module address with no private key, so the only thing that can produce such a
message is a passed governance proposal. With no gov module there is no
`MsgSubmitProposal` route, no proposal, and no recovery: an expired provider
client on the consumer side is permanent. `MsgIBCSoftwareUpgrade` is gated the
same way.

This is the concrete reason the reference consumer app cannot be used as a
template here. Note also that the consumer's message filter deliberately allows
`/cosmos.gov.*` while a consumer is restricted, precisely so governance can
recover the chain -- an allowance that does nothing if there is no gov module to
route those messages to.

A consumer chain that does not want `x/gov` must pass an authority it can
actually act as. Passing the conventional gov address and wiring no gov module
is the one combination that looks correct and cannot work.

## 5. `x/evidence` on the provider

```go
evidenceKeeper := evidencekeeper.NewKeeper(
    appCodec,
    runtime.NewKVStoreService(keys[evidencetypes.StoreKey]),
    app.StakingKeeper,
    app.SlashingKeeper,
    app.AccountKeeper.AddressCodec(),
    runtime.ProvideCometInfoService(),
)
app.EvidenceKeeper = *evidenceKeeper
```

plus `evidence.NewAppModule(app.EvidenceKeeper)` in the module manager, the
store key, and entries in `SetOrderBeginBlockers` and `SetOrderInitGenesis`.
`runtime.ProvideCometInfoService()` is what lets the begin-blocker read the
block's misbehaviour reports.

**What it punishes.** Exactly one infraction class: **double-signing by a
validator on the provider chain itself**, reported by CometBFT as
`DuplicateVoteEvidence`. Every consumer-sourced infraction -- consumer
double-signs, consumer light-client attacks, consumer downtime -- runs through
VAAS's own machinery and does not touch `x/evidence`.

**Omitting it is completely silent.** CometBFT still gossips the evidence and
baseapp still threads it through, but nothing reads it: the offender keeps its
stake, its bond, and its seat. Note the asymmetry -- *forgetting the
`SetOrderBeginBlockers` entry* while wiring the module does panic at startup
(the module manager refuses an incomplete ordering), but leaving the module out
altogether produces no signal at all.

The consumer app wires no `x/evidence`, correctly: a consumer has no stake to
slash, and its ante chain blocks `/cosmos.evidence` and `/cosmos.slashing`
messages outright.

## 6. The consumer's message-filter decorator

```go
anteDecorators := []sdk.AnteDecorator{
    ante.NewSetUpContextDecorator(),
    ante.NewExtensionOptionsDecorator(nil),
    consumerante.NewDisabledModulesDecorator("/cosmos.evidence", "/cosmos.slashing"),
    ante.NewValidateBasicDecorator(),
    consumerante.NewMsgFilterDecorator(options.ConsumerKeeper),
    // ...
}
```

`consumerante` is `x/vaas/consumer/ante`. `NewMsgFilterDecorator` takes a narrow
interface (`GetProviderClientID`, `IsConsumerInDebt`, `IsVSCStale`), so a host
can satisfy it with the consumer keeper or its own adapter. It has three modes
([msg_filter_ante.go](../x/vaas/consumer/ante/msg_filter_ante.go)):

- **Before the provider client exists**, only `/ibc.`-prefixed messages are
  admitted -- the chain can be wired up and nothing else.
- **While the consumer is in debt or its validator set is stale**, only
  `/ibc.core.` and `/cosmos.gov.` messages are admitted, rejected otherwise with
  `ErrConsumerInDebt`. Note the narrower `/ibc.core.` prefix: ICS-20 v1
  `MsgTransfer` is blocked in this mode, while an ICS-20 v2 transfer riding
  `/ibc.core.channel.v2.MsgSendPacket` is allowed. `MsgExec` is not unwrapped, so
  authz-wrapped IBC messages are rejected too.
- **Otherwise** the transaction passes through untouched.

**Omitting it is silent, and it deletes two whole mechanisms.** `IsVSCStale` and
the consumer's `IsConsumerInDebt` have **no other production caller**. Without
the decorator the keeper keeps faithfully recording the in-debt flag the provider
pushes on every VSC packet and keeps the VSC clock ticking, and nothing ever
reads either. Safe mode does not exist, debt gating does not exist, and the
provider's only lever against a consumer that stops paying its fees evaporates.
A consumer whose validator set is arbitrarily stale keeps accepting normal user
traffic. There is no observable symptom until the day it matters.

Related: `safe_mode_threshold` must be positive. Zero makes `IsVSCStale`
trivially true and pins the chain in restricted mode forever.

## 7. The provider's consensus-key rotation ante decorator

```go
anteDecorators := []sdk.AnteDecorator{
    ante.NewSetUpContextDecorator(),
    ante.NewExtensionOptionsDecorator(nil),
    ante.NewValidateBasicDecorator(),
    providerante.NewConsPubKeyRotationDecorator(options.ProviderKeeper),
    // ...
}
```

`providerante` is `x/vaas/provider/ante`, in
`cons_pubkey_rotation_ante.go`. The decorator inspects
`MsgRotateConsPubKey`, including nested inside
`authz.MsgExec`, and rejects a rotation whose new consensus key is already
assigned as some validator's consumer key, with `ErrConsumerKeyInUse`. It sits
immediately after `NewValidateBasicDecorator` so a rejected rotation costs the
sender its transaction and nothing else.

Transaction admission is the right enforcement point because the staking module
applies a recorded rotation from `EndBlock`, not from the message handler -- by
the time VAAS's `AfterConsensusPubKeyUpdate` hook runs, the rotation is already
committed and there is nothing left to reject.

**Omitting it is silent, and it cannot halt the chain.** The hook logs the
anomaly it can no longer prevent, and the consumer validator-set computation
deterministically drops one of two entries that would collide on a consumer
consensus address, so no chain is ever handed a duplicate set. What you get
instead is a rotation that succeeds when it should have been refused, and an
affected validator quietly dropped from that consumer's validator set.

For the operator-facing side of a rotation -- what changes on the consumers, when
to swap the node signing key, and where the validator's state ends up -- see
[key-assignment.md](key-assignment.md#rotating-your-provider-consensus-key).

## 8. Module substitutions and ordering on the provider

The provider computes and returns the provider validator set itself, in
`EndBlock`. The SDK's module manager refuses two producers of validator updates,
so the stock staking and genutil modules have to be replaced:

```go
no_valupdates_genutil.NewAppModule(app.AccountKeeper, app.StakingKeeper, app, txConfig),
no_valupdates_staking.NewAppModule(appCodec, app.StakingKeeper, app.AccountKeeper,
    app.BankKeeper, app.GetSubspace(stakingtypes.ModuleName)),
```

Both embed the SDK modules and register under the stock module names, so the
ordering lists still name `stakingtypes.ModuleName` and
`genutiltypes.ModuleName`. They suppress only the validator-update return values;
errors still propagate.

**Omitting them is fatal at genesis, with an unhelpful message.** The module
manager returns "validator InitGenesis updates already set by a previous module"
and the chain cannot produce its first block. The message names neither VAAS nor
the fix.

Three ordering constraints:

- **`SetOrderInitGenesis`:** the provider module must come **after** staking and
  genutil, since it builds the genesis validator set from the staking bonded set.
  Put it earlier and it returns an empty set, and the module manager fails with
  "validator set is empty after InitGenesis".
- **`SetOrderEndBlockers`:** the provider module must come **after** staking,
  because `EndBlockVSU` reads `GetBondedValidatorsByPower` and bond/unbond
  transitions are applied by the staking end-blocker. Getting this wrong is
  **silent**: the provider consensus set and every VSC packet lag by one block.
  The reference app places the provider module last.
- **`SetOrderBeginBlockers`:** `evidencetypes.ModuleName` after slashing and
  distribution, per the SDK's own convention; the provider module last.

On the consumer, the VAAS consumer module is last in all three orderings and is
the sole producer of validator updates. It needs neither `no_valupdates`
substitution: it runs no real staking module, and stock genutil is safe because
the consumer keeper's staking stand-in produces no updates of its own.

## 9. The IBC v2 routes

```go
ibcRouterV2 := ibcapi.NewRouter()
ibcRouterV2.AddRoute(ibctransfertypes.PortID, transferv2.NewIBCModule(app.TransferKeeper))
ibcRouterV2.AddRoute(vaastypes.ProviderAppID, ibcprovider.NewIBCModule(&app.ProviderKeeper))
app.IBCKeeper.SetRouterV2(ibcRouterV2)
```

and on the consumer, `vaastypes.ConsumerAppID` with
`ibcconsumer.NewIBCModule(&app.ConsumerKeeper)`. The application ids are
`"vaasprovider"` and `"vaasconsumer"`, declared as `ProviderAppID` and
`ConsumerAppID` in [x/vaas/types/keys.go](../x/vaas/types/keys.go). They happen
to equal the module names but are separate constants -- use the `AppID` ones,
since they are the on-wire identifiers and a mismatch is a protocol break.

Both `NewIBCModule` constructors take a **pointer** to the keeper. A value copy
freezes the keeper as of that line.

VAAS registers nothing on the IBC v1 router; it is IBC v2 only.

**Omitting a route is silent at startup and total at packet time.** Everything
boots, the relayer connects, and no VSC or evidence packet can be routed.

## 10. The provider fee denom

The provider keeper's last constructor argument is the per-block consumer fee
denom, fixed for the lifetime of the binary. It is **the one argument validated
at startup**: an invalid or empty denom panics in `NewKeeper` with a clear
message. A valid-but-wrong denom is silent, and every consumer fee pool would
then be denominated in a token nobody deposits. See
[params-reference.md](params-reference.md) section 1 -- only the fee *amount* is
governable.

The keeper also takes a `PhotonKeeper` for the conversion rate used to price
downtime slashes. The reference provider app passes a stub pinned at a rate of 1
([photon_stub.go](../app/provider/photon_stub.go)); an embedding application
wires its real `x/photon` keeper in its place. Copy the stub and you silently
pin the rate.

## 11. Consumer keeper construction order

The consumer has no staking module: `ConsumerKeeper` stands in as the staking
keeper for `x/slashing` and `x/genutil`. That creates a genuine circular
dependency the host must break the same way the reference app does:

1. `NewNonZeroKeeper` -- a collections-only consumer keeper, because
   `ibckeeper.NewKeeper` panics on a zero keeper;
2. `ibckeeper.NewKeeper`;
3. the real `ibcconsumerkeeper.NewKeeper`, taking the IBC client and channel
   keepers and the slashing keeper.

Skip the pre-initialization and the app panics at startup.

Two further order traps, both silent:

- the slashing keeper must be given `&app.ConsumerKeeper` (a pointer), so the
  later reassignment is visible to it;
- `ibcconsumer.NewAppModule` takes the keeper **by value**, so it must be
  constructed *after* `SetHooks`. Build it earlier and the module holds a
  pre-hooks copy and the slashing hooks never fire. The provider's
  `NewAppModule` takes a pointer, so order does not matter there -- the two
  modules differ, and it is easy to get backwards.

---

## Failure-mode summary

| Duty | Omission is | Where it bites |
|---|---|---|
| `providertypes.ModuleName` in `maccPerms` | **runtime panic, chain halt** | first consumer deletion, in `BeginBlock` -- unrecoverable without a binary fix |
| `AppendSendRestriction(FeePoolSendRestriction())` | silent, user-triggerable | unattributable fee-pool balance |
| `ProviderKeeper.Hooks()` on staking | silent | duplicate consumer consensus addresses; orphaned key assignments and stranded downtime/fee bookkeeping after rotation or removal; no immediate post-rotation snapshot |
| Governance wired *and* set as IBC client authority | silent until recovery is needed, then terminal | `MsgRecoverClient` unreachable; a dead client is permanent |
| `x/evidence` on the provider | silent | provider-native double-signs unpunished |
| `consumerante.NewMsgFilterDecorator` | silent | safe mode and debt gating cease to exist |
| `providerante.NewConsPubKeyRotationDecorator` | silent | a colliding rotation succeeds; the validator is dropped from that consumer's set |
| `no_valupdates_staking` / `no_valupdates_genutil` on the provider | **fatal at genesis** | "validator InitGenesis updates already set by a previous module" |
| Provider module after staking in `SetOrderEndBlockers` | silent | provider valset and every VSC packet lag one block |
| Provider module after staking/genutil in `SetOrderInitGenesis` | **fatal at genesis** | "validator set is empty after InitGenesis" |
| `ibcRouterV2` app-id routes | silent at startup, total at packet time | no VSC or evidence packet routes |
| Consumer two-phase keeper init | **panic at startup** | `ibckeeper.NewKeeper` on a zero keeper |
| Provider fee denom | **fatal at startup** if malformed; silent if valid-but-wrong | `NewKeeper` panics, or pools use a denom nobody holds |
| Consumer module built before `SetHooks` | silent | slashing hooks never fire |
