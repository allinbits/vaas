# Parameters Reference

A consolidated reference for every configurable parameter in VAAS, across both
modules. For each parameter this gives its type, its validated bound, its
default, and where it is set.

Three parameter sets exist:

1. **Provider module `Params`** -- global provider settings, one set for the
   whole chain.
2. **Provider `InfractionParameters`** -- global slashing/jailing settings, one
   set applied to every consumer.
3. **Consumer module `ConsumerParams`** -- the per-consumer-chain settings that
   the provider ships to each consumer at launch.

A fourth, closely related set -- the per-consumer
`ConsumerInitializationParameters` the provider holds for each consumer -- is
listed at the end because it seeds the consumer params above.

The deep-dive tables for the downtime subset ([consumer-downtime.md](consumer-downtime.md)
section 8) and the liveness subset ([consumer-liveness.md](consumer-liveness.md)
section 5) are the authoritative narrative for those parameters; this document
cross-links them rather than repeating the reasoning.

To read the live values off a running chain rather than the defaults here, see
[queries-reference.md](queries-reference.md) -- `params` on either module, plus
`consumer-fees-per-block` for the per-consumer effective fee and
`consumer-liveness` for the derived grace period.

---

## 1. Provider module `Params`

One global set. Seeded from the provider genesis (`app_state.provider.params`)
and changed only by governance through the provider `MsgUpdateParams`
(authority-gated: the signer must be the governance authority). Source:
[x/vaas/provider/types/params.go](../x/vaas/provider/types/params.go) (defaults
lines 15-88, `Params.Validate` lines 234-265) and the `UpdateParams` handler in
[x/vaas/provider/keeper/msg_server.go](../x/vaas/provider/keeper/msg_server.go)
(lines 41-67).

| Parameter | Type | Bound | Default | Notes |
|---|---|---|---|---|
| `trusting_period_fraction` | string decimal | `(0, 1)` | `"0.66"` | provider IBC client trusting period = consumer unbonding * this fraction |
| `liveness_grace_fraction` | string decimal | `(0, 1)` | `"0.66"` | consumer removal grace = provider unbonding * this fraction; see [consumer-liveness.md](consumer-liveness.md) section 5 |
| `vaas_timeout_period` | duration | `(0, 24h]` | `1h` | IBC timeout on VSC packets; 24h is the ibc-go v2 `MaxTimeoutDelta` hard cap |
| `blocks_per_epoch` | int64 | `> 0` | `600` | VSC cadence and the fee-collection period (about 1h at 6s blocks) |
| `fees_per_block_amount` | Int | set and `> 0` | `1000` | amount only; the denom is not a parameter (see below) |
| `min_deposit_blocks` | uint64 | none (not checked in `Params.Validate`) | `14400` | fee-pool minimum-deposit floor multiplier; `0` disables the floor |
| `max_pause_duration` | duration | `> 0` | `720h` (30 days) | how long a consumer may stay `PAUSED` before auto-stop; see [consumer-downtime.md](consumer-downtime.md) section 7 |

The bounds come from `Params.Validate`: `trusting_period_fraction` and
`liveness_grace_fraction` go through `ValidateStringFractionNonZero`, which
rejects `0`, negatives, and any value `>= 1`
([x/vaas/types/shared_params.go](../x/vaas/types/shared_params.go) lines 64-79);
`vaas_timeout_period` through `ValidateVAASTimeoutPeriod` against
`channeltypesv2.MaxTimeoutDelta` (24h); `blocks_per_epoch` must be a positive
int64; `fees_per_block_amount` must be set and positive. `min_deposit_blocks` is
**not** validated in `Params.Validate` -- its only effect is as the fee-pool
floor multiplier, where `0` is a valid "disable the floor" value.

**The fee denom is not a parameter.** It is wired into the keeper at application
construction (`Keeper.feeDenom`) and cannot change without a binary upgrade; the
standalone provider app in this repository wires `uphoton`. Only the amount
(`fees_per_block_amount`) is governable. Source: params.go lines 37-44 and
`GetFeesPerBlock` in
[x/vaas/provider/keeper/params.go](../x/vaas/provider/keeper/params.go) lines
64-69. A per-consumer raise-only override to this amount can be set with
`MsgSetConsumerFeesPerBlock`; see [consumer-fee-pool.md](consumer-fee-pool.md).

---

## 2. Provider `InfractionParameters`

One global set applied to every consumer -- not per-consumer. It is read
everywhere through `GetInfractionParams(ctx)` with no consumer id
([x/vaas/provider/keeper/params.go](../x/vaas/provider/keeper/params.go) lines
154-161). Source of defaults and validation:
[x/vaas/provider/types/params.go](../x/vaas/provider/types/params.go)
(`DefaultInfractionParameters` lines 123-145, `InfractionParameters.Validate`
lines 163-199, `SlashJailParameters.Validate` lines 222-231).

| Parameter | Type | Bound | Default |
|---|---|---|---|
| `double_sign.slash_fraction` | LegacyDec | `[0, 1]` | `0.05` |
| `double_sign.jail_duration` | duration | `>= 0` | max int64 ns (effectively permanent; moot -- see below) |
| `double_sign.tombstone` | bool | -- | `true` |
| `downtime.slash_fraction` | LegacyDec | `[0, 1]` | `0.0001` (a per-window cap, not the slash itself) |
| `downtime.jail_duration` | duration | `>= 0` | `0` (a downtime slash never jails) |
| `downtime.tombstone` | bool | -- | `false` |
| `downtime_grace_period` | duration | `>= 0` | `7 days` |
| `signed_blocks_window` | int64 | `> 0` | `600` |
| `min_signed_per_window` | LegacyDec | `(0, 1)` | `0.5` |
| `downtime_challenge_window` | duration | `> 0` | `7 days` |
| `downtime_evidence_max_age` | duration | `(0, downtime_challenge_window]` | `3 days` |

The last five rows are the downtime-detection subset; their meaning, the
per-window slash pricing, and the challenge window are documented in full in
[consumer-downtime.md](consumer-downtime.md) section 8. `signed_blocks_window`
and `min_signed_per_window` are provider-owned: the provider mirrors them into
each consumer's genesis and every VSC packet (`CurrentDowntimeParams`,
[params.go](../x/vaas/provider/keeper/params.go) lines 189-198).

`double_sign.jail_duration` defaulting to the maximum int64 is moot: `tombstone`
is `true`, so a double-signer is permanently removed regardless of the jail
timer. A downtime slash uses `jail_duration = 0` and `tombstone = false`
because it never jails.

Two cross-parameter constraints are enforced when infraction params are
validated:

- `downtime_evidence_max_age <= downtime_challenge_window` (`Validate`, lines
  192-197).
- `downtime_evidence_max_age + downtime_challenge_window < trusting_period_fraction
  * default_consumer_unbonding` (`ValidateInfractionParamsAgainst`, lines
  207-220), so the oldest challengeable header stays light-client verifiable
  through the end of its challenge window.

**Where set:** the provider genesis field `infraction_parameters` only,
defaulting to `DefaultInfractionParameters()` when omitted (`InitGenesis` in
[x/vaas/provider/keeper/genesis.go](../x/vaas/provider/keeper/genesis.go)). The
current provider `Msg` service exposes no transaction that changes them on a
running chain: `MsgUpdateParams` carries only `Params`
([proto/vaas/provider/v1/tx.proto](../proto/vaas/provider/v1/tx.proto)), and
`MsgCreateConsumer` neither carries nor stores per-consumer infraction
parameters -- every consumer is validated under the single global set
(`msgServer.CreateConsumer` in
[msg_server.go](../x/vaas/provider/keeper/msg_server.go)).

---

## 3. Consumer module `ConsumerParams`

The per-consumer-chain settings, held on the consumer. Source:
[x/vaas/types/params.go](../x/vaas/types/params.go) (`DefaultConsumerParams`
lines 56-65, `ConsumerParams.Validate` lines 67-88).

| Parameter | Type | Bound | Default |
|---|---|---|---|
| `enabled` | bool | -- | `false` |
| `vaas_timeout_period` | duration | `> 0` | `1h` |
| `historical_entries` | int64 | `> 0` | `10000` (staking default) |
| `unbonding_period` | duration | `> 0` | provider default unbonding minus 1 day (about 20 days) |
| `safe_mode_threshold` | duration | `> 0` | `3h` |
| `signed_blocks_window` | int64 | `> 0` | `600` (provider-owned) |
| `min_signed_per_window` | LegacyDec | `(0, 1)` | `0.5` (provider-owned) |

**Where set:** these values arrive in the consumer genesis that the provider
builds at launch (`MakeConsumerGenesis`), seeded from the per-consumer
`ConsumerInitializationParameters` in section 4. `signed_blocks_window` and
`min_signed_per_window` are provider-owned and kept in sync by every VSC packet;
the consumer's own `MsgUpdateParams` preserves the stored values and cannot
change them locally ([x/vaas/types/params.go](../x/vaas/types/params.go) lines
35-38; [consumer-downtime.md](consumer-downtime.md) section 2). The
liveness-facing subset (`vaas_timeout_period`, `unbonding_period`,
`safe_mode_threshold`) is documented with its cross-chain bounds in
[consumer-liveness.md](consumer-liveness.md) section 5.

---

## 4. Per-consumer `ConsumerInitializationParameters` (provider-side)

Held per consumer on the provider and supplied through `MsgCreateConsumer` /
`MsgUpdateConsumer`. They seed the consumer genesis (and therefore section 3's
consumer params). Source: `DefaultConsumerInitializationParameters`
([x/vaas/provider/types/params.go](../x/vaas/provider/types/params.go) lines
147-161), `ValidateInitializationParameters`
([x/vaas/provider/types/msg.go](../x/vaas/provider/types/msg.go) lines 419-450),
and the additional cross-chain bounds enforced in `validateConsumerInitParams`
([msg_server.go](../x/vaas/provider/keeper/msg_server.go) lines 233-255).

| Parameter | Type | Bound | Default |
|---|---|---|---|
| `initial_height` | Height | non-zero | `1-1` |
| `genesis_hash` | bytes | length `<= MaxHashLength` | empty |
| `binary_hash` | bytes | length `<= MaxHashLength` | empty |
| `spawn_time` | timestamp | zero means "not scheduled" | zero |
| `unbonding_period` | duration | `> 0` and `<= provider unbonding` | provider default minus 1 day |
| `vaas_timeout_period` | duration | `(0, 24h]` | `1h` |
| `historical_entries` | int64 | `> 0` | `10000` |
| `safe_mode_threshold` | duration | `> 0` and `< provider liveness grace` | `3h` |

The two cross-chain bounds are provider-enforced at create/update time so the
relationships hold without operator discipline: a consumer cannot be configured
to outlive the provider's slashable window (`unbonding_period <= provider
unbonding`), and its own safe mode always engages before the provider's liveness
sweep would remove it (`safe_mode_threshold < provider liveness grace`). See
[consumer-liveness.md](consumer-liveness.md) section 5.

`vaas_timeout_period` here is the timeout the consumer applies to the evidence
packets it sends the provider; the provider's own `Params.vaas_timeout_period`
(section 1) is the timeout on the VSC packets it sends the consumer.
