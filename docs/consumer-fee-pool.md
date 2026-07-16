# Consumer Fee Pool

Every consumer chain on VAAS has a dedicated fee pool on the provider chain,
held at a deterministic account address derived from the consumer ID:

    fee_pool_address = NewModuleAddress("provider-consumer-fee-pool-<consumer_id>")

This account funds the per-block service charge that the provider drains
from the pool every block (`fees_per_block`) while the consumer is in
`CONSUMER_PHASE_LAUNCHED`. If the pool is short, the consumer is flagged as
in-debt and its ante gate blocks user transactions until funding is restored.

## Funding

Funding the pool MUST go through `MsgFundConsumerFeePool`. Direct bank sends
to the fee pool address are rejected by a `bank.SendRestriction` registered
on the provider chain -- funds sent that way will either bounce (IBC) or fail
the transaction (direct `MsgSend`). This restriction exists so the
share-accounting (see below) never gets out of sync with the actual pool
balance.

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
exception: the gov authority may always withdraw, which under the
existing alias-to-distribution semantics pulls only the community
pool's own shares back to the community pool. This prevents non-gov
depositors from rug-pulling an active consumer mid-flight while
preserving a path for the community pool to withdraw subsidy support
at any time. Covering PAUSED keeps the escrow honest: withheld fee
shares from downtime exclusions sit in the pool awaiting a possible
challenge payout (see [consumer-downtime.md](consumer-downtime.md)),
and a pause is exactly when that payout happens.

Outside of LAUNCHED and PAUSED -- in REGISTERED, INITIALIZED, or STOPPED -- any
depositor controls their own shares and can withdraw at any time. The
message accepts multi-denom `Coins` and is atomic: if any denom in the
request fails its share check, the whole transaction reverts.

### Share math (TL;DR)

- Shares are minted when you deposit. Initial deposit mints
  `shares = amount`; subsequent deposits mint
  `amount * total_shares / pool_balance` (balance BEFORE this deposit).
- Your claim at any time is
  `your_shares * pool_balance / total_shares`.
- A withdraw of `amount >= claim` burns all your shares and delivers your
  exact claim. Partial withdraws (`amount < claim`) burn proportional
  shares and may deliver marginally less than requested due to integer
  truncation.

This is the same accounting pattern used by ERC-4626 vaults and liquid
staking modules: per-block fee consumption reduces share value, not share
count, so consumption is borne pro-rata by current share-holders.

### Withheld downtime shares

When a validator is excluded from an epoch's distribution for downtime, its
share is never drawn from the pool; a `WithheldFeeRecord` tracks the amount
for the length of the downtime challenge window. If the exclusion goes
unchallenged the record expires and the funds simply stay with the consumer.
If a challenge proves the downtime evidence false, the recorded amounts are
paid from the pool back to their validators before the consumer is paused.
Records are only written when the pool actually held the epoch's fee, so a
record is always backed by retained funds. See
[consumer-downtime.md](consumer-downtime.md) for the full mechanism.

## Sweeping

The consumer owner can trigger a full settlement via
`MsgSweepConsumerFeePool` to distribute the pool pro-rata to all
share-holders. Sweep is locked while the consumer is LAUNCHED or
PAUSED -- depositor withdrawals and the owner sweep both freeze while
governance deliberates a paused consumer, though the gov authority's
withdraw-clawback exception (see [Withdrawing](#withdrawing)) remains
available throughout. The owner must wait for the consumer to
transition to STOPPED (or rely on the auto-sweep that runs on
DELETED). Auto-stop bounds how long a pause can block the sweep. The
message takes an optional list of denoms; if empty, all denoms with
shares or balance are swept.
Any truncation residue per denom is forwarded to the community pool.

The same sweep runs automatically when a consumer is deleted (auto-sweep
on `DeleteConsumerChain`). The auto-sweep cannot fail under valid state --
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
  the consumer is LAUNCHED. The gov authority is exempt and can always
  reclaim community-pool funding -- but only its own shares, never
  other depositors'.
- A minimum deposit floor (`fees_per_block * min_deposit_blocks`)
  prevents share-table dusting and applies uniformly to every funder.

## Queries

- `appd query provider consumer-fee-pool-claim <consumer-id> <depositor>`
  -- one depositor's claim across all denoms. Pass the gov authority address
  to query the community pool's holdings (the query aliases the gov authority
  to the distribution module account, which is the depositor of record for
  community-pool funding).
- `appd query provider consumer-fee-pool-claims <consumer-id>` --
  paginated list of all depositors with non-zero claims.

## CLI examples

    # fund a pool with 1000uphoton from your key
    appd tx provider fund-consumer-fee-pool 5 1000uphoton --from operator

    # withdraw a mix of denoms from your share in pool 5
    appd tx provider withdraw-consumer-fee-pool 5 250uphoton,30uatone --from operator

    # owner sweeps all denoms with shares or balance
    appd tx provider sweep-consumer-fee-pool 5 --from owner

    # owner sweeps only the listed denoms (comma-separated or repeated flag)
    appd tx provider sweep-consumer-fee-pool 5 --denoms=uphoton,uatone --from owner
    appd tx provider sweep-consumer-fee-pool 5 --denoms=uphoton --denoms=uatone --from owner

    # query a single depositor's claim
    appd query provider consumer-fee-pool-claim 5 cosmos1...

    # paginated list of all depositors with non-zero claims
    appd query provider consumer-fee-pool-claims 5 --page 1 --limit 100
