# Consumer Fee Pool

Every consumer chain on VAAS has a dedicated fee pool on the provider chain,
held at a deterministic account address derived from the consumer ID:

    fee_pool_address = NewModuleAddress("vaas-consumer-fee-pool-<consumer_id>")

This account funds the per-block service charge that the provider drains
from the pool every block (`fees_per_block`) while the consumer is in
`CONSUMER_PHASE_LAUNCHED`. If the pool is short, the consumer is flagged as
in-debt and its ante gate blocks user transactions until funding is restored.

## Funding

Funding the pool MUST go through `MsgFundConsumerFeePool`. Direct bank sends
to the fee pool address are rejected by a `bank.SendRestriction` registered
on the provider chain — funds sent that way will either bounce (IBC) or fail
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

## Withdrawing

Each depositor controls their own shares and can withdraw at any time via
`MsgWithdrawConsumerFeePool`. The message accepts multi-denom `Coins` and
is atomic — if any denom in the request fails its share check, the whole
transaction reverts.

### Share math (TL;DR)

- Shares are minted when you deposit. Initial deposit mints
  `shares = amount`; subsequent deposits mint
  `amount × total_shares / pool_balance` (balance BEFORE this deposit).
- Your claim at any time is
  `your_shares × pool_balance / total_shares`.
- A withdraw of `amount ≥ claim` burns all your shares and delivers your
  exact claim. Partial withdraws (`amount < claim`) burn proportional
  shares and may deliver marginally less than requested due to integer
  truncation.

This is the same accounting pattern used by ERC-4626 vaults and liquid
staking modules: per-block fee consumption reduces share value, not share
count, so consumption is borne pro-rata by current share-holders.

## Sweeping

The consumer owner can trigger a full settlement via
`MsgSweepConsumerFeePool`, distributing the pool pro-rata to all
share-holders. The message takes an optional list of denoms; if empty, all
denoms with shares or balance are swept. Any truncation residue per denom
is forwarded to the community pool.

The same sweep runs automatically when a consumer is deleted (auto-sweep
on `DeleteConsumerChain`). If the auto-sweep fails for any reason, the
delete aborts and the consumer stays in `STOPPED` — funds are never
silently lost.

## Trust model

- Producer governance has **no** unilateral authority over consumer-owned
  funds. Gov interacts as a single depositor (via the community pool path)
  using the same messages as everyone else.
- The consumer owner can trigger settlement but cannot redirect funds to
  arbitrary recipients — pro-rata distribution to known depositors is the
  only outcome.
- Each depositor controls their own shares independently.

## Queries

- `vaas query consumer-fee-pool-claim <consumer-id> <depositor>` — one
  depositor's claim across all denoms. Pass the gov authority address to
  query the community pool's holdings.
- `vaas query consumer-fee-pool-claims <consumer-id>` — paginated list of
  all depositors with non-zero claims.

## CLI examples

    # fund a pool with 1000uphoton from your key
    vaas tx fund-consumer-fee-pool 5 1000uphoton --from operator

    # withdraw a mix of denoms from your share in pool 5
    vaas tx withdraw-consumer-fee-pool 5 250uphoton,30uatone --from operator

    # owner sweeps all denoms with shares or balance
    vaas tx sweep-consumer-fee-pool 5 --from owner

    # owner sweeps only the listed denoms (comma-separated or repeated flag)
    vaas tx sweep-consumer-fee-pool 5 --denoms=uphoton,uatone --from owner
    vaas tx sweep-consumer-fee-pool 5 --denoms=uphoton --denoms=uatone --from owner

    # query a single depositor's claim
    vaas query consumer-fee-pool-claim 5 cosmos1...

    # paginated list of all depositors with non-zero claims
    vaas query consumer-fee-pool-claims 5 --page 1 --limit 100
