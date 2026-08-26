# Consumer Launch Runbook

The end-to-end operator flow for bringing a consumer chain from registration to
a healthy, fee-paying `LAUNCHED` state. It is the operational companion to
[consumer-lifecycle.md](consumer-lifecycle.md) (which describes the phases and
their on-chain effects) and [consumer-fee-pool.md](consumer-fee-pool.md) (the
fee-pool mechanics).

**The single most important step is funding the fee pool (step 5). An unfunded
launched consumer is debt-gated: it stops accepting value-bearing user
transactions.** Do not skip it.

Roles in this flow:

- **Consumer owner** -- the account that submits `MsgCreateConsumer`; drives
  registration and funds the fee pool.
- **Relayer** -- creates the IBC v2 clients; the provider never creates them.
- **Provider governance** -- only needed for pause/resume/removal, not for a
  normal launch.

---

## 1. Register the consumer

The owner submits `MsgCreateConsumer` on the provider (any account may; the
submitter becomes the owner). Its `initialization_parameters` seed the consumer
genesis the provider will build.

```
providerd tx vaasprovider create-consumer <path/to/create_consumer.json> --from <owner>
```

```json
{
  "chain_id": "consumer-1",
  "metadata": { "name": "consumer", "description": "my consumer", "metadata": "{}" },
  "initialization_parameters": {
    "initial_height": { "revision_number": 1, "revision_height": 1 },
    "genesis_hash": "",
    "binary_hash": "",
    "spawn_time": "2026-01-01T00:00:00Z",
    "unbonding_period": 1728000000000000,
    "vaas_timeout_period": 3600000000000,
    "historical_entries": 10000,
    "safe_mode_threshold": 10800000000000
  }
}
```

Only `chain_id` and `metadata` are required; `initialization_parameters` is
optional and defaults as a whole block when omitted. Supply it and every field
in it is validated, so fill it in completely. Field notes (validated by
`ValidateInitializationParameters` in
[x/vaas/provider/types/msg.go](../x/vaas/provider/types/msg.go) and
`validateConsumerInitParams` in
[x/vaas/provider/keeper/msg_server.go](../x/vaas/provider/keeper/msg_server.go);
see [params-reference.md](params-reference.md) section 4 for the full bounds):

- Use `vaas_timeout_period`, in nanoseconds, `<= 24h` (`3600000000000` = 1h).
  The field is **not** `ccv_timeout_period`; a legacy name is silently dropped
  to zero and then rejected.
- `safe_mode_threshold` (nanoseconds) is required to be positive and strictly
  below the provider's liveness grace. `10800000000000` = 3h.
- `unbonding_period` (nanoseconds) must not exceed the provider's unbonding
  period.

On success the provider assigns a `consumer_id` (an auto-incremented integer,
`"0"` for the first) and records the owner. If `spawn_time` is set, the consumer
moves straight to `INITIALIZED` and is queued for launch. The owner can still
adjust parameters (including `spawn_time`) with `MsgUpdateConsumer` until launch.

**To abandon the registration** -- a mistyped `chain_id`, or a launch called off --
the owner retires it rather than leaving it parked:

```
providerd tx vaasprovider retire-consumer <consumer-id> --from <owner>
```

This works only before launch (`REGISTERED` or `INITIALIZED`). It erases the
consumer's provider state, pays any fee-pool balance back to its depositors, and
releases the `chain_id` for immediate re-registration. Governance can also submit
it, which is the way out if the owner key is lost. See
[consumer-lifecycle.md](consumer-lifecycle.md).

## 2. Launch at spawn time (provider side, automatic)

At the first provider `BeginBlock` where `block_time >= spawn_time`, the provider
runs `LaunchConsumer`: it snapshots the current bonded validator set, builds the
`ConsumerGenesisState` (seeding the provider chain id and the owner address the
consumer will validate its client declaration against), stores it, and sets the
phase to `LAUNCHED`. If launch fails, the consumer is reset to `REGISTERED` for
a retry.
See [consumer-lifecycle.md](consumer-lifecycle.md) phase 3.

## 3. Bootstrap and start the consumer node (operator side)

```
providerd query vaasprovider consumer-genesis <consumer-id> -o json > consumer_genesis.json
```

Inject the result into the consumer chain's `genesis.json` under
`app_state.vaasconsumer`, then start the consumer node. On its first block the
consumer's `InitGenesis` installs the initial validator set from the genesis.
It creates no IBC client: the clients come from the relayer and are declared by
the owner in the next step.

## 4. Create the IBC v2 path and declare the clients

A relayer creates an IBC v2 client on each chain pointing at the other and
registers the counterparties (`add-path`, `--ibc-version 2`). Neither chain
creates these clients itself, and creation alone grants them no authority: the
consumer's owner then declares them, one transaction per side.

```
providerd tx vaasprovider update-consumer declare.json --from <owner>
    # declare.json: {"consumer_id": <consumer-id>, "client_id": "07-tendermint-..."}

consumerd tx vaasconsumer set-provider-client <client-id> --from <owner>
```

Each declaration is validated before it binds (an active tendermint client of
the right chain id with a registered counterparty; on the provider side also a
trusting period above the downtime challenge horizon) and is permanent once
accepted ([security-model.md](security-model.md)). VSC delivery starts at the
first epoch boundary after the provider-side declaration. In localnet and e2e
the relayer is the `ts-relayer` and the declarations are scripted; see
[consumer-lifecycle.md](consumer-lifecycle.md) phase 3 and the localnet
[app/README.md](../app/README.md).

## 5. Fund the fee pool (do not skip)

Each consumer has a dedicated provider-side fee pool, whose address is returned
as the `fee_pool_address` field of
`query vaasprovider consumer-chain <consumer-id>` -- read it from the chain
rather than deriving it (see [consumer-fee-pool.md](consumer-fee-pool.md)).

Once per epoch, for each `LAUNCHED` consumer, `DistributeConsumerFees`
([x/vaas/provider/keeper/fees.go](../x/vaas/provider/keeper/fees.go)) requires
the pool's *unreserved* balance -- the balance net of amounts escrowed against
open downtime challenge windows -- to cover a full epoch fee
(`fees_per_block_amount * blocks_per_epoch`, in the module fee denom,
`uphoton`) before it pays anyone. It then draws only the eligible validators'
shares, so shares of validators excluded for downtime stay in the pool.

**If the unreserved balance cannot cover the full epoch fee, the provider flags
the consumer as in-debt** (`UpdateConsumerDebtStatus` in
[debt.go](../x/vaas/provider/keeper/debt.go)) and skips distribution entirely --
no partial payment. The in-debt flag rides the next VSC packet to the consumer
as `ValidatorSetChangePacketData.ConsumerInDebt`, whose transaction admission
gate (`MsgFilterDecorator` in
[x/vaas/consumer/ante/msg_filter_ante.go](../x/vaas/consumer/ante/msg_filter_ante.go))
then restricts incoming transactions to `/ibc.core.*` and `/cosmos.gov.*` only,
rejecting value-bearing application transactions -- the same restriction used
for a stale set (see [consumer-liveness.md](consumer-liveness.md) section 4). So
an unfunded consumer launches and then, at its first epoch fee collection,
becomes debt-gated until funding is restored.

Fund it before that first post-launch collection:

```
providerd tx vaasprovider fund-consumer-fee-pool <consumer-id> <amount>uphoton --from <owner>
```

or, in localnet, the `provider-fund-consumer-fee-pool` target in the
[Makefile](../Makefile):

```
make provider-fund-consumer-fee-pool CONSUMER_ID=0 FEE_POOL_AMOUNT=100000000uphoton
```

Funding rules ([consumer-fee-pool.md](consumer-fee-pool.md)):

- **Must** go through `MsgFundConsumerFeePool`. Direct bank sends to the fee-pool
  address are rejected by a send restriction (they bounce or fail), so the
  share accounting stays consistent.
- The denom must be the module fee denom (`uphoton`).
- A minimum deposit applies: `effective_fees_per_block.Amount * min_deposit_blocks`
  (default `min_deposit_blocks` = 14400, about one day of fees). Deposits below
  the floor are rejected with `ErrDepositBelowMinimum`;
  `min_deposit_blocks = 0` disables the check.

Size the deposit to cover enough epochs for your operational comfort; anyone can
top it up, and the owner (or governance for community-pool subsidies) can settle
or withdraw under the locks described in
[consumer-fee-pool.md](consumer-fee-pool.md).

## 6. Steady state

Once the clients are declared and the pool is funded:

- Every epoch the provider queues and sends a VSC packet per launched consumer;
  the consumer applies the changes on its next `EndBlock`. Diffs by default, an
  absolute snapshot when the consumer has fallen behind
  ([consumer-liveness.md](consumer-liveness.md) section 2).
- Every epoch the provider collects the consumer's fee and distributes it to
  bonded validators; a solvent pool clears the in-debt flag.

Watch the consumer's liveness (last ack, removal ETA, degraded flag) as
described in [consumer-liveness.md](consumer-liveness.md) section 6, and keep a
relayer updating the IBC clients so they never expire.
[queries-reference.md](queries-reference.md) lists everything you can query, and
[events-reference.md](events-reference.md) the events worth indexing -- for a
launch, `vaas_create_consumer`, `vaas_consumer_fee_pool_fund`, and the
consumer-side `vaas_packet` and `vaas_client_established`.

---

## Local sandbox

For a full local provider + consumer + `ts-relayer` stack, see the localnet
targets in the [Makefile](../Makefile) and [app/README.md](../app/README.md).
When adapting the localnet `create-consumer` payload, apply the same field
corrections as step 1 (`vaas_timeout_period <= 24h`, include
`safe_mode_threshold`).
