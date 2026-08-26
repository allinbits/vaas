# Queries Reference

The full query surface of both VAAS modules: 18 provider queries and 2 consumer
queries, each with a CLI command. Every gRPC method has a CLI equivalent and
vice versa.

## Command roots

The CLI subcommand root is the **module name**, not the chain role:

```
providerd query vaasprovider <command>
providerd tx     vaasprovider <command>
consumerd query  vaasconsumer <command>
```

There is no consumer tx root -- the consumer module's only message
(`MsgUpdateParams`) is governance-authority-signed and has no CLI command.

Every query command accepts the standard query flags (`--node`, `--height`,
`--output/-o`, `--grpc-addr`). Commands marked *paginated* also take
`--page`, `--limit`, `--offset`, `--page-key`, `--count-total`, and `--reverse`.

---

## Provider queries

### Consumer lifecycle and identity

| Command | Args | Returns |
|---|---|---|
| `consumer-chain` | `<consumer-id>` | One consumer's `consumer_id`, `chain_id`, `owner_address`, `phase`, `metadata`, `init_params`, `client_id`, and `fee_pool_address`. For a consumer that has not launched, `client_id` is empty and `init_params` may be zero-valued rather than an error. For a `DELETED` consumer, `chain_id` is empty as well: deletion releases the chain ID for reuse (see [consumer-lifecycle.md](consumer-lifecycle.md)), so identify a deleted consumer by `consumer_id`. |
| `list-consumer-chains` | `[phase]`, *paginated* | Every consumer, or only those in the given phase. `phase` is numeric: `1` REGISTERED, `2` INITIALIZED, `3` LAUNCHED, `4` STOPPED, `5` DELETED, `6` PAUSED. Omit it (or pass `0`) for no filter. Each row carries `chain_id`, `client_id`, `phase`, `metadata`, `consumer_id`, and `fee_pool_address`; a `DELETED` row's `chain_id` is empty. |
| `consumer-genesis` | `<consumer-id>` | The `ConsumerGenesisState` the provider built at launch. This is the payload you inject into the consumer's `genesis.json` under `app_state.vaasconsumer`; the CLI prints the genesis object itself, not a response wrapper. Errors `NotFound` before launch. |
| `consumer-genesis-time` | `<consumer-id>` | The consumer's genesis timestamp, **derived** from the IBC consensus state at the consumer's `initial_height`. Errors if the consumer is unknown, has no client yet, or has no consensus state at that height. |
| `consumer-id-from-client-id` | `<client-id>` | The consumer id a provider-side client belongs to. The client id wanted here is the *provider's* client tracking the consumer -- the `client_id` field of `consumer-chain`, not the consumer-side `provider-info` output. |
| `blocks-until-next-epoch` | -- | Blocks remaining until the next epoch boundary. Purely computed; returns `0` exactly on a boundary. |
| `params` | -- | The provider module `Params`. Note the **fee denom is not a parameter** -- it is fixed at application wiring, so use `consumer-fees-per-block` to learn the denom. See [params-reference.md](params-reference.md). |

### Validator sets and key assignment

| Command | Args | Returns |
|---|---|---|
| `consumer-validators` | `<consumer-id>` | The consumer's validator set joined with live provider staking data: `provider_address`, `consumer_key`, `consumer_power`, `description`, `provider_operator_address`, `jailed`, `status`, `provider_tokens`, `provider_power`, `validates_current_epoch`. **Returns an empty list for any phase other than `LAUNCHED`**, including `PAUSED`. A validator missing from staking is skipped silently. |
| `validator-consumer-key` | `<consumer-id> <provider-valcons-addr>` | The validator's assigned consumer consensus address. **Returns an empty string with no error when no key is assigned** -- treat empty as "uses its provider key". |
| `validator-provider-key` | `<consumer-id> <consumer-valcons-addr>` | The reverse lookup. Same convention: empty, not an error, when unmapped. |
| `all-pairs-valconsensus-address` | `<consumer-id>` | Every `(provider_address, consumer_address, consumer_key)` triple for the consumer. Not paginated. Only validators that *explicitly assigned* a key appear; validators using their provider key do not. |

See [key-assignment.md](key-assignment.md) for the assignment rules.

### Fees and the fee pool

| Command | Args | Returns |
|---|---|---|
| `consumer-fees-per-block` | `<consumer-id>` | The effective per-block fee as a `Coin` plus `is_override`. Resolved on the fly: the per-consumer override if one is set, else the global `fees_per_block_amount`, always at the wired fee denom. Rejected with `NotFound` for an unknown or `DELETED` consumer, so a bogus id cannot be answered with the global default. This is the only query that surfaces the fee denom. |
| `all-consumer-fees-per-block-overrides` | *paginated* | Every consumer that has an override, ordered by consumer id. The amount is a bare integer with no denom. |
| `consumer-fee-pool-claim` | `<consumer-id> <depositor>` | One depositor's claim, per denom. Pass the **gov authority address** to read the community pool's position: this query aliases it to the distribution module account, which is the depositor of record for community-pool funding. |
| `consumer-fee-pool-claims` | `<consumer-id>`, *paginated* | Every depositor with a non-zero claim, sorted by address. This one does **not** alias the gov authority -- the community pool appears under the raw distribution module account address. |
| `withheld-fee-records` | `<consumer-id>` | The amounts currently escrowed against open downtime challenge windows: `provider_cons_addr`, `amount`, `expires_at`. Not paginated (at most one row per validator). This escrow is what caps both distribution and withdrawals. |

See [consumer-fee-pool.md](consumer-fee-pool.md).

### Liveness and downtime

| Command | Args | Returns |
|---|---|---|
| `consumer-liveness` | `<consumer-id>` | `last_ack_time`, `grace_period`, `removal_eta`, `degraded`. `grace_period` is derived (provider unbonding times `liveness_grace_fraction`), `removal_eta` is `last_ack + grace`, and `degraded` trips at half the grace period as an early warning. **Caveat:** a consumer that has never acked reports the current block time as its last ack, so it looks freshly alive rather than overdue. |
| `pending-downtime-slashes` | `<consumer-id>` | Every accepted downtime window awaiting its challenge window: `provider_cons_addr`, `window_start_height`, `span`, `missed_count`, `missed_blocks_bitmap`, `slash_tokens`, `matures_at`, `consumer_cons_addr`. Not paginated, and deliberately so -- it is bounded by validator count times pending windows. A validator can appear more than once, one row per disjoint window. These are the rows `challenge-consumer-downtime` contests before `matures_at`. |

See [consumer-downtime.md](consumer-downtime.md) and
[consumer-liveness.md](consumer-liveness.md).

---

## Consumer queries

| Command | Args | Returns |
|---|---|---|
| `params` | -- | The consumer's `ConsumerParams`: `enabled`, `vaas_timeout_period`, `historical_entries`, `unbonding_period`, `safe_mode_threshold`, `signed_blocks_window`, `min_signed_per_window`. The last two are provider-owned, arrive via VSC packets, and cannot be changed locally. |
| `provider-info` | -- | `consumer` and `provider` `ChainInfo` pairs, derived from IBC state. This is the "is my consumer wired up yet" probe: it errors `NotFound` until the provider client exists. **Read the fields carefully:** `consumer.chainID` is the local chain id but `consumer.clientID` is the id of the client *on the consumer that tracks the provider*; `provider.chainID` comes from that client's state, and `provider.clientID` is never populated. |

---

## Provider transactions

For completeness, since several messages are governance-only and therefore have
no CLI command.

| Message | Signer | CLI command |
|---|---|---|
| `MsgCreateConsumer` | submitter | `create-consumer <path/to/params.json>` |
| `MsgUpdateConsumer` | owner | `update-consumer <path/to/params.json>` |
| `MsgAssignConsumerKey` | validator account | `assign-consensus-key <consumer-id> <consumer-pubkey>` |
| `MsgFundConsumerFeePool` | any | `fund-consumer-fee-pool <consumer-id> <amount>` |
| `MsgWithdrawConsumerFeePool` | depositor | `withdraw-consumer-fee-pool <consumer-id> <coins>` |
| `MsgSweepConsumerFeePool` | owner | `sweep-consumer-fee-pool <consumer-id> [--denoms]` |
| `MsgSubmitConsumerDoubleVoting` | any | `submit-consumer-double-voting <consumer-id> <evidence.json> <header.json>` |
| `MsgSubmitConsumerMisbehaviour` | any | `submit-consumer-misbehaviour <consumer-id> <misbehaviour.json>` |
| `MsgChallengeConsumerDowntime` | any | `challenge-consumer-downtime <consumer-id> <validator-cons-addr> <claimed-height> --consumer-rpc <url>` |
| `MsgRemoveConsumer` | owner **or** gov pre-launch; gov only after | `remove-consumer <consumer-id>` (pre-launch); governance proposal after launch |
| `MsgResumeConsumer` | gov authority | none -- governance proposal |
| `MsgSetConsumerFeesPerBlock` | gov authority | none -- governance proposal |
| `MsgUpdateParams` | gov authority | none -- governance proposal |

The consumer module exposes `MsgUpdateParams` only, governance-signed, with no
CLI command.
