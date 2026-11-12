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
  `gov.NewAppModule`, no gov store key, no gov entry in any module ordering.
  Its authority is the VAAS consumer module account instead, which the
  consumer's owner acts as through `MsgOwnerExec` (see
  [The chain's authority](#4-the-chains-authority)). That is a legitimate
  choice for a chain run by its owner; a consumer with a community of its own
  wires governance and names it instead.

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

Four of the provider's hook methods carry real behavior; the rest are no-ops:

- `AfterValidatorCreated` -- rejects creating a validator whose consensus key is
  already in use as some validator's assigned consumer key. This is the *only*
  enforcement point for that rule at validator creation.
- `BeforeConsensusPubKeyRotation` -- refuses a consensus-key rotation onto a key
  already in use as some validator's assigned consumer key, before `x/staking`
  records the rotation (section 7).
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

## 4. The chain's authority

```go
consumerAuthority := ibcconsumertypes.OwnerExecAuthority().String()

app.IBCKeeper = ibckeeper.NewKeeper(
    appCodec,
    runtime.NewKVStoreService(keys[ibcexported.StoreKey]),
    app.GetSubspace(ibcexported.ModuleName),
    app.UpgradeKeeper,
    consumerAuthority,
)
// ...every other keeper's authority likewise...
app.ConsumerKeeper.SetMessageRouter(app.MsgServiceRouter())
```

Every keeper takes an `authority`: the one account whose signature its
authority-gated messages accept. **It must be an account something on the chain
can act as.** A module address has no private key, so a message signed by it
only ever comes from a module that executes messages on its behalf: `x/gov`
for the gov module account, after a passed proposal, or the VAAS consumer
module for its own account, on its owner's say-so.

**Why VAAS cares.** The only way to replace a dead IBC client on either side is
ibc-go's client recovery, `MsgRecoverClient`, which substitutes the client
state under the same client id so the consumer's pin and the provider's latch
both survive it (see [security-model.md](security-model.md)). VAAS routes
operators to it explicitly: `ResumeConsumerChain` pre-flights the client and, if
it is expired or frozen, fails the resume with instructions to bundle
`MsgRecoverClient` into the same governance proposal. `MsgIBCSoftwareUpgrade`,
`MsgSoftwareUpgrade` and every module's `MsgUpdateParams` are gated the same
way. ibc-go's `RecoverClient` handler compares `msg.Signer` against the keeper's
authority and rejects anything else with `ErrUnauthorized`; an authority no one
can act as makes an expired provider client on the consumer side permanent.

The reference consumer names the consumer module account, and wires
x/consensus so the block parameters are among what that account governs. Its
owner (the account that registered the consumer on the provider, which the
provider seeds into the consumer params) acts as it through `MsgOwnerExec`:
the owner submits the messages, each signed by the consumer module account,
and the consumer module executes them in order through the app's message
router, all or nothing. Nothing else is impersonated: a carried message signed
by any other account, the gov module's included, is refused, so the owner
holds exactly the authorities the app grants the consumer module and no more.
The consumer module account is a blocked address in the bank keeper, so no
funds can reach it for the owner to move. The admission and fee rules judge an
owner exec by the messages it carries (a client recovery is IBC traffic
whoever submits it; a parameter update is not), and the disabled modules stay
disabled inside it. `query vaasconsumer owner-authority` prints the account to
sign with and the owner that may submit.

An exec is refused if it leaves either of two states behind: the pinned
provider client tracking a chain id other than the provider's, which a
recovery could substitute and nothing could move back (so the pin's coherence
rule holds for its recovery too); or an `owner_address` nobody can sign as,
empty, undecodable or the consumer module account itself, which would leave
the chain with no authority for good.

The owner hands the consumer over with the same message: the consumer's
`owner_address` is a consumer param, carried in a `MsgUpdateParams`, and it is
seeded once by the provider, so a change of owner on the provider
(`MsgUpdateConsumer`) does not reach the consumer by itself. **A
`MsgUpdateParams` carries the full parameter set**, not a diff: every field it
leaves out is reset (`photon_fees_enabled` to off, `owner_address` to empty,
which the exec then refuses), and only the two provider-owned downtime fields
are kept from the store. Read the current set back first with
`consumerd query vaasconsumer params -o json` and edit it.

**A consumer with a community of its own wires `x/gov` and names the gov module
account instead.** The owner then holds nothing after the pin. Passing the
conventional gov address and wiring no gov module is the one combination that
looks correct and cannot work: it is silent until recovery is needed, and then
terminal.

**What restricted mode leaves the authority.** While a consumer is in debt or
its validator set is stale (section 6), on-chain recovery needs only relayer
traffic and, for an expired provider client, a client recovery: both pass, an
owner exec carrying `MsgRecoverClient` included. A fix that needs new code is
an off-chain coordinated halt or hard fork, not an on-chain upgrade, so
restricted mode admits no upgrade and no parameter change (ibc-go's
`MsgIBCSoftwareUpgrade` and `MsgUpdateParams` included), from the owner or from
governance: a gov v1 proposal passes only when every message it carries would
pass on its own, and a legacy content proposal (gov v1beta1
`MsgSubmitProposal`, or `MsgExecLegacyContent`) is refused, since its content
cannot be judged. Votes and deposits stay admitted. On a gov-wired consumer, a
proposal submitted before restricted mode began still executes in `EndBlock`
when its voting period ends, outside the ante, and restricted mode cannot bound
it.

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
    consumerante.NewPhotonFeeDecorator(options.ConsumerKeeper),
    ante.NewDeductFeeDecorator(options.AccountKeeper, options.BankKeeper, options.FeegrantKeeper,
        consumerante.NewBootstrapExemptTxFeeChecker(app.ConsumerKeeper)),
    // ...
}
```

`consumerante` is `x/vaas/consumer/ante`. `NewMsgFilterDecorator` takes a narrow
interface (`GetProviderClientID`, `IsConsumerInDebt`, `IsVSCStale`), so a host
can satisfy it with the consumer keeper or its own adapter. It has three modes
([msg_filter_ante.go](../x/vaas/consumer/ante/msg_filter_ante.go)):

- **Before the provider client exists**, only `/ibc.`-prefixed messages are
  admitted -- the chain can be wired up and nothing else.
- **While the consumer is in debt or its validator set is stale**, only IBC
  relayer and client-recovery messages and `/cosmos.gov.` messages are admitted,
  rejected otherwise with `ErrConsumerInDebt`; a governance proposal passes only
  when every message it carries would, and a legacy content proposal never does
  (section 4). The IBC messages are an explicit list of ibc-go v10 core
  messages, not a prefix: client creation, update, upgrade, misbehaviour and
  recovery, the IBC v1 connection and channel handshakes (minus a channel close
  initiated here) and packet messages, and the IBC v2 counterparty registration
  and packet messages. ICS-20 v1 `MsgTransfer` is blocked in this mode, and so
  is the raw ICS-20 v2 transfer riding `/ibc.core.channel.v2.MsgSendPacket`:
  restricted mode is for recovery, and value stays in. ibc-go's
  `MsgIBCSoftwareUpgrade` and every `MsgUpdateParams` are refused too. One bank
  message passes: a `MsgSend` made only of the photon voucher, the path by which
  a relayer that ran out of vouchers is refilled by any account holding some.
  `MsgExec` is not unwrapped, so authz-wrapped IBC messages are rejected too.
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

The two fee entries belong together. `NewPhotonFeeDecorator` enforces the
photon-only fee policy while the `photon_fees_enabled` consumer param is on and
is a no-op otherwise, so it is wired unconditionally; it sits immediately
before fee deduction so the denom it vets is the one deducted
([photon_fee_ante.go](../x/vaas/consumer/ante/photon_fee_ante.go)).
`NewBootstrapExemptTxFeeChecker(consumerKeeper)` builds the `TxFeeChecker`
handed to `NewDeductFeeDecorator`: atomone-sdk's minimum-gas-prices floor at
CheckTx, waived for bootstrap traffic (IBC core minus the raw ICS-20 v2 send,
governance, the pin) only while no routable provider client is pinned
([fee_checker.go](../x/vaas/consumer/ante/fee_checker.go)), the same split the
photon policy makes. A pin set through `MsgSetProviderClient` is routable
from the start. After the pin everything pays, the relayer included: it is
funded with vouchers before the pin and runs with a gas price in the voucher
denom after it ([consumer-launch-runbook.md](consumer-launch-runbook.md)
section 4). One state reopens the exemption for both: `photon_fees_enabled` on
and a bank supply of zero for the voucher denom, where no transaction could
pay and the relayer delivery that would bring the first voucher would be
refused too. It closes as soon as one voucher exists; vouchers paid as fees
stay in the fee collector (the reference consumer wires no `x/distribution`)
and keep it closed. With atomone-sdk's default checker in its place, a floor
set before the pin would turn the bootstrap traffic away at that node's
mempool.

## 7. The provider's consensus-key rotation refusal

The refusal is one of the staking hooks of section 3, not an ante decorator:
`BeforeConsensusPubKeyRotation` runs from the `MsgRotateConsPubKey` handler,
before `x/staking` records the rotation, and fails one whose new consensus key
is already assigned as some validator's consumer key, with
`ErrConsumerKeyInUse`. It runs wherever the message executes, so a rotation
wrapped in `authz.MsgExec` is covered, and a rejected rotation costs the sender
its transaction and nothing else.

The hook is the right enforcement point because the staking module applies a
recorded rotation from `EndBlock`, not from the message handler -- by the time
VAAS's `AfterConsensusPubKeyUpdate` hook runs, the rotation is already
committed and there is nothing left to reject. The hook exists in atomone-sdk's
`x/staking` (upstream has no pre-rotation hook), which the reference apps
replace the SDK with; a staking module that does not ask it refuses nothing.
That staking module also charges the rotation fee into the community pool
through a distribution keeper handed to it after construction
(`StakingKeeper.SetDistributionKeeper`); without that call every rotation
fails at the fee step, refused or not, so the hook never gets a rotation to
admit.

**A staking module that does not ask the hook is silent, and cannot halt the
chain.** `AfterConsensusPubKeyUpdate` logs the anomaly it can no longer
prevent, and the consumer validator-set computation deterministically drops
one of two entries that would collide on a consumer consensus address, so no
chain is ever handed a duplicate set. What you get instead is a rotation that
succeeds when it should have been refused, and an affected validator quietly
dropped from that consumer's validator set.

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
  distribution, per atomone-sdk's own convention; the provider module last.

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
| An authority the chain can act as: `x/gov` wired and named, or the consumer module account named with `ConsumerKeeper.SetMessageRouter` | silent until recovery is needed, then terminal | `MsgRecoverClient` unreachable; a dead client is permanent (without the router, every `MsgOwnerExec` fails with "the app wires no message router") |
| `x/evidence` on the provider | silent | provider-native double-signs unpunished |
| `consumerante.NewMsgFilterDecorator` | silent | safe mode and debt gating cease to exist |
| `consumerante.NewPhotonFeeDecorator` | silent | the photon-only fee policy ceases to exist; any denom pays |
| `consumerante.NewBootstrapExemptTxFeeChecker` in `NewDeductFeeDecorator` | silent, per node | a floor set before the pin turns the bootstrap traffic away at that node's mempool |
| `x/staking` from atomone-sdk, which asks `BeforeConsensusPubKeyRotation` | silent | a colliding rotation succeeds; the validator is dropped from that consumer's set |
| `no_valupdates_staking` / `no_valupdates_genutil` on the provider | **fatal at genesis** | "validator InitGenesis updates already set by a previous module" |
| Provider module after staking in `SetOrderEndBlockers` | silent | provider valset and every VSC packet lag one block |
| Provider module after staking/genutil in `SetOrderInitGenesis` | **fatal at genesis** | "validator set is empty after InitGenesis" |
| `ibcRouterV2` app-id routes | silent at startup, total at packet time | no VSC or evidence packet routes |
| Consumer two-phase keeper init | **panic at startup** | `ibckeeper.NewKeeper` on a zero keeper |
| Provider fee denom | **fatal at startup** if malformed; silent if valid-but-wrong | `NewKeeper` panics, or pools use a denom nobody holds |
| Consumer module built before `SetHooks` | silent | slashing hooks never fire |
