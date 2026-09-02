# Consumer Fee Pool

Every consumer chain on VAAS has a dedicated fee pool on the provider chain,
held at a deterministic account address derived from the consumer ID.
`GetConsumerFeePoolAddress`
([x/vaas/provider/keeper/fees.go](../x/vaas/provider/keeper/fees.go)) builds it
as:

    fee_pool_address = NewModuleAddress("vaasprovider-consumer-fee-pool-<consumer_id>")

where `vaasprovider` is the provider module name. The result is a plain account
address, not a registered module account.

**Do not derive this address by hand.** `NewModuleAddress` hashes its input, so
any error in the preimage -- a wrong module name, a missing separator, a
formatted consumer id -- yields a valid-looking but completely unrelated
address, and the send restriction described below does not protect it. Anything
that needs a pool address (a funding script, an ICA controller, an explorer)
should read it from the chain, where it is returned as the `fee_pool_address`
field of:

    provider query vaasprovider consumer-chain <consumer-id>
    provider query vaasprovider list-consumer-chains

This account funds the service charge the consumer pays for validation. Once
per epoch -- not every block -- the provider draws the consumer's fee from the
pool and distributes it to the bonded validators while the consumer is in
`CONSUMER_PHASE_LAUNCHED`. The pool must hold a full epoch's fee
(`fees_per_block * blocks_per_epoch`), net of the escrow described below,
before anything is paid at all; only the *eligible* validators' shares are then
actually drawn, so the shares of validators excluded for downtime stay in the
pool (`DistributeConsumerFees`,
[x/vaas/provider/keeper/fees.go](../x/vaas/provider/keeper/fees.go)). If the
unreserved balance cannot cover the full epoch fee, nothing is distributed, the
consumer is flagged as in-debt, and its ante gate blocks user transactions
until funding is restored.

## Funding

Funding the pool MUST go through `MsgFundConsumerFeePool`. Direct bank sends
to the fee pool address are rejected by a `bank.SendRestriction` registered
on the provider chain (`FeePoolSendRestriction` in
[send_restriction.go](../x/vaas/provider/keeper/send_restriction.go)) -- funds
sent that way will either bounce (IBC) or fail the transaction (direct
`MsgSend`) with `ErrUnsolicitedFeePoolDeposit`. This restriction exists
so the share-accounting (see below) never gets out of sync with the actual pool
balance.

The restriction exempts two senders: the provider module account (which the
fee-pool machinery itself hops through) and the **distribution module account**.
The second exemption matters operationally: a governance community-pool spend
addressed straight at a fee-pool address is *not* rejected. Funds that arrive
that way land as pool balance with no shares behind them, which is precisely
the state the restriction exists to prevent -- the share table can no longer
account for the full balance, and no depositor has a claim on the surplus.
Governance should fund a pool the same way as everyone else, with
`MsgFundConsumerFeePool` -- see
[Funding from the community pool](#funding-from-the-community-pool), which
credits the distribution module account as the depositor and mints shares
for it.

`MsgFundConsumerFeePool` accepts a single `Coin` whose denom must match the
current `fees_per_block.Denom`. Anyone may sign. The signer is credited with
shares.

### Cross-chain funding via ICA

To fund a pool from another chain, register an Interchain Account on the
provider, IBC-transfer funds into the ICA's account, and have the controller
side send a `MsgFundConsumerFeePool` from the ICA. The ICA becomes the
depositor of record.

A direct IBC transfer addressed to a fee pool fails losslessly: the bank
send-restriction rejects the receive on the provider, the packet acks with an
error, and the source-chain transfer module refunds the sender via standard
IBC semantics. The funds are not lost, just not deposited.

### Funding from the community pool

A governance proposal containing `MsgFundConsumerFeePool` with the gov
module authority as `signer` will pull funds from the cosmos-sdk
distribution community pool and credit the distribution module account as
the depositor.

### Minimum deposit

`MsgFundConsumerFeePool` enforces a minimum deposit equal to
`effective_fees_per_block.Amount * min_deposit_blocks`, where
`min_deposit_blocks` is a provider-module parameter and
`effective_fees_per_block` is the per-consumer fee in effect (the
per-consumer override if one is set via `MsgSetConsumerFeesPerBlock`,
else the global `fees_per_block`). Because overrides can only raise a
consumer's per-block fee above the global default, consumers with an
override have a proportionally higher minimum deposit -- the floor
always reflects the actual per-block cost the deposit will cover.

Deposits below the floor are rejected with `ErrDepositBelowMinimum`.
Setting `min_deposit_blocks = 0` disables the check. The floor applies
to every depositor including the gov authority -- gov funds are subject
to the same minimum as any other funder. The default is 14400 blocks
(~1 day at a 6-second block time).

## Withdrawing

`MsgWithdrawConsumerFeePool` is locked while the consumer is in
`CONSUMER_PHASE_LAUNCHED` or `CONSUMER_PHASE_PAUSED`, with one
exception: the gov authority may withdraw during those phases, which
under the existing alias-to-distribution semantics pulls only the
community pool's own shares back to the community pool. This prevents
non-gov depositors from rug-pulling an active consumer mid-flight while
preserving a path for the community pool to withdraw subsidy support.
Covering PAUSED keeps the escrow honest: withheld fee
shares from downtime exclusions sit in the pool awaiting a possible
challenge payout (see [consumer-downtime.md](consumer-downtime.md)),
and a pause is exactly when that payout happens.

Outside of LAUNCHED and PAUSED -- in REGISTERED, INITIALIZED, or STOPPED -- any
depositor controls their own shares and can withdraw at any time. The
message accepts multi-denom `Coins` and is atomic: if any denom in the
request fails its share check, the whole transaction reverts.

`CONSUMER_PHASE_DELETED` is the one phase where nobody can withdraw, gov
authority included: the pool was already settled by the auto-sweep on
deletion, so the message is rejected with `ErrInvalidPhase` before the
gov exception is considered (`msgServer.WithdrawConsumerFeePool`).

### Share math (TL;DR)

- Shares are minted when you deposit. Initial deposit mints
  `shares = amount`; subsequent deposits mint
  `amount * total_shares / pool_balance` (balance BEFORE this deposit).
- Your claim at any time is
  `your_shares * pool_balance / total_shares`.
- **A withdraw is capped by the escrow.** Only the *unreserved* balance --
  the pool balance minus the amounts held against unexpired withheld-fee
  records -- can be drawn. A withdraw of `amount >= claim` burns all your
  shares and delivers your exact claim only when that claim fits inside the
  unreserved balance; otherwise it draws the unreserved amount and burns
  shares at the full-balance rate, leaving residual shares that still back
  the escrowed remainder. Partial withdraws (`amount < claim`) burn
  proportional shares and may deliver marginally less than requested due to
  integer truncation.

This is the same accounting pattern used by ERC-4626 vaults and liquid
staking modules: per-block fee consumption reduces share value, not share
count, so consumption is borne pro-rata by current share-holders.

### Errors you can hit

- `ErrDepositBelowMinimum` -- the deposit is under the minimum-deposit floor
  (see [Minimum deposit](#minimum-deposit)).
- `ErrDepositTooSmall` -- the deposit is large enough to clear the floor but
  too small, relative to the pool's current share-to-balance ratio, to mint
  even one share. VAAS refuses it rather than accepting funds that would
  credit nothing (`MintShares`). Deposit more.
- `ErrSubShareWithdraw` -- the requested withdraw would burn zero shares
  against the unreserved balance, i.e. the amount is below the granularity
  the pool can settle (`WithdrawShares`). Request more, or wait for the
  escrow to expire and free up unreserved balance.
- `ErrFeePoolLocked` -- withdraw or sweep attempted while the consumer is
  LAUNCHED or PAUSED.
- `ErrUnsolicitedFeePoolDeposit` -- a direct bank send to a fee-pool address
  (see [Funding](#funding)).

### Withheld downtime shares

When a validator is excluded from an epoch's distribution for downtime, its
share is never drawn from the pool; a `WithheldFeeRecord` tracks the amount
for the length of the downtime challenge window. If the exclusion goes
unchallenged the record expires and the funds simply stay with the consumer.
If a challenge proves the downtime evidence false, the recorded amounts are
paid from the pool back to their validators before the consumer is paused.
Records are only written when the pool's unreserved balance covered the full
epoch fee, so a record is always backed by funds the pool genuinely retained
beyond what it already owes. See
[consumer-downtime.md](consumer-downtime.md) for the full mechanism.

## Sweeping

The consumer owner can trigger a full settlement via
`MsgSweepConsumerFeePool` to distribute the pool pro-rata to all
share-holders. Sweep is available in REGISTERED, INITIALIZED, and
STOPPED, and locked while the consumer is LAUNCHED or PAUSED --
depositor withdrawals and the owner sweep both freeze while
governance deliberates a paused consumer, though the gov authority's
withdraw-clawback exception (see [Withdrawing](#withdrawing)) remains
available throughout. So an owner who wants to settle a pool before
launch can sweep straight away; once the consumer is launched, the
owner must wait for it to reach STOPPED (or rely on the auto-sweep that
runs on DELETED). Auto-stop bounds how long a pause can block the
sweep. Once the consumer is DELETED the message is rejected -- the pool
was already auto-swept. The message takes an optional list of denoms;
if empty, all denoms with shares or balance are swept.
Any truncation residue per denom is forwarded to the community pool.

The same sweep runs automatically when a consumer is deleted (auto-sweep
on `DeleteConsumerChain`), whether it reached deletion through the
post-launch stop or through a pre-launch removal, both `MsgRemoveConsumer` (see
[consumer-lifecycle.md](consumer-lifecycle.md)). So a depositor who
prepaid fees for a chain that never launched is paid out when the
registration is removed, without the owner having to sweep first.
The auto-sweep cannot fail under valid state --
the pool balance is moved into the provider module and distributed back out
in the same transaction, and depositors are never blocked accounts -- so
deletion is never silently aborted. The only failure mode is state
corruption, which panics rather than stranding the consumer in `STOPPED`.

## Trust model

- Producer governance has **no** unilateral authority over consumer-owned
  funds. Gov interacts as a single depositor (via the community pool path)
  using the same messages as everyone else.
- The consumer owner can trigger settlement but cannot redirect funds to
  arbitrary recipients -- pro-rata distribution to known depositors is the
  only outcome.
- Each depositor controls their own shares but cannot withdraw while
  the consumer is LAUNCHED or PAUSED. The gov authority is exempt for
  those two phases and can reclaim community-pool funding -- but only its
  own shares, never other depositors'. Once the consumer is DELETED
  nobody withdraws, the gov authority included: the auto-sweep on
  deletion has already settled the pool.
- A minimum deposit floor (`fees_per_block * min_deposit_blocks`)
  prevents share-table dusting and applies uniformly to every funder.

## Queries

- `providerd query vaasprovider consumer-fee-pool-claim <consumer-id> <depositor>`
  -- one depositor's claim across all denoms. Pass the gov authority address
  to query the community pool's holdings (the query aliases the gov authority
  to the distribution module account, which is the depositor of record for
  community-pool funding).
- `providerd query vaasprovider consumer-fee-pool-claims <consumer-id>` --
  paginated list of all depositors with non-zero claims. This one does *not*
  alias the gov authority: the community pool's position appears under the
  raw distribution module account address.
- `providerd query vaasprovider withheld-fee-records <consumer-id>` -- the
  amounts currently escrowed against open downtime challenge windows, which
  is what caps withdrawals and distribution.

See [queries-reference.md](queries-reference.md) for the full query surface.

## CLI examples

    # fund a pool with 1000uphoton from your key
    providerd tx vaasprovider fund-consumer-fee-pool 5 1000uphoton --from operator

    # withdraw a mix of denoms from your share in pool 5
    providerd tx vaasprovider withdraw-consumer-fee-pool 5 250uphoton,30uatone --from operator

    # owner sweeps all denoms with shares or balance
    providerd tx vaasprovider sweep-consumer-fee-pool 5 --from owner

    # owner sweeps only the listed denoms (comma-separated or repeated flag)
    providerd tx vaasprovider sweep-consumer-fee-pool 5 --denoms=uphoton,uatone --from owner
    providerd tx vaasprovider sweep-consumer-fee-pool 5 --denoms=uphoton --denoms=uatone --from owner

    # query a single depositor's claim
    providerd query vaasprovider consumer-fee-pool-claim 5 cosmos1...

    # paginated list of all depositors with non-zero claims
    providerd query vaasprovider consumer-fee-pool-claims 5 --page 1 --limit 100
