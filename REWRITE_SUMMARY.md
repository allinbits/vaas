# VAAS Module Rewrite Summary

This document records how VAAS was ported from
[`github.com/cosmos/interchain-security/v7`](https://github.com/cosmos/interchain-security)
into the standalone Go module `github.com/allinbits/vaas`. Its purpose is to
explain the **diff against the ICS codebase VAAS was ported from** — what was
removed, what was kept, and why — for readers familiar with the original
Interchain Security code. VAAS is now an independent project; there is no
intent or path to upstream changes back to `interchain-security`.

> **NOTE:** this is a historical comparison. The function-level diffs in
> the "Code Changes Made" section below describe edits performed during the
> port; subsequent cleanup has removed some of the resulting no-op stubs
> entirely. For current behavior, consult the code, [AGENTS.md](AGENTS.md),
> and [`docs/consumer-lifecycle.md`](docs/consumer-lifecycle.md).

## Overview

The rewrite simplifies the interchain security modules by removing several features while keeping the core functionality for validator set synchronization between provider and consumer chains.

## Features Removed

### Provider Module
| Feature | Reason |
|---------|--------|
| **Partial Set Security (PSS)** | All validators now validate all consumers - no opt-in/out |
| **Top N Chains** | Part of PSS removal |
| **Opt-In Chains** | Part of PSS removal |
| **Power Shaping** | No caps, allowlists, denylists, priority lists |
| **Per-Consumer Commission Rates** | Validators use same commission as provider |
| **Slash Packet Throttling** | Removed all slash meter logic |
| **Consumer Reward Distribution** | No cross-chain rewards |
| **Inactive Provider Validators (ADR-017)** | Only active provider validators can validate consumers |

### Consumer Module
| Feature | Reason |
|---------|--------|
| **Slash Packet Sending** | All slash-related removed |
| **Slash Packet Throttling/Retry** | All slash-related removed |
| **Reward Distribution to Provider** | Consistent with provider removal |
| **Standalone-to-Consumer Changeover** | Not currently supported; the consumer-keeper wiring is preserved as a reference for future transition work (see [docs/consumer-transition.md](docs/consumer-transition.md)) |
| **Outstanding Downtime Flag** | Part of slash removal |

## Features Kept

### Provider Module
- Consumer Lifecycle Management (REGISTERED → INITIALIZED → LAUNCHED → STOPPED → DELETED)
- Key Assignment (validators can use different keys per consumer)
- Provider Slashing Parameters only (per-consumer customization removed)
- VSC Packet Generation at epoch boundaries
- IBC Channel Management
- Double Voting Evidence handling
- Light Client Misbehavior Detection (logging only)
- Consumer Metadata storage
- Minimum validators check for launch
- Chain ID update before launch
- Existing client/connection reuse

### Consumer Module
- VSC Packet Handling (validator set updates)
- Cross-Chain Validator Storage
- Historical Info Tracking (for IBC evidence)
- IBC Channel Management
- Module Parameters

## Code Changes Made

### Provider Keeper (`x/vaas/provider/keeper/`)

**msg_server.go:**
- `CreateConsumer`: Removed power shaping and reward denom setup (kept per-consumer infraction parameters)
- `UpdateConsumer`: Removed power shaping (kept per-consumer infraction parameters)
- `SetConsumerCommissionRate`: Returns error (feature removed)
- `OptIn`: Simplified to no-op with optional key assignment
- `OptOut`: Returns error (PSS removed)

**relay.go:**
- `BeginBlockCIS`: Now a no-op (slash throttling removed)
- `OnRecvSlashPacket`: Removed slash meter logic, packets handled directly
- `HandleSlashPacket`: Uses per-consumer infraction parameters via `GetInfractionParameters`
- `QueueVSCPackets`: Removed activeValidators parameter

**validator_set_update.go:**
- `ComputeNextValidators`: Simplified - all validators validate all consumers, no power shaping
- `ComputeConsumerNextValSet`: Removed PSS logic and power shaping parameters

**consumer_lifecycle.go:**
- `BeginBlockLaunchConsumers`: Removed activeValidators
- `LaunchConsumer`: Simplified signature, removed `HasActiveConsumerValidator` check
- `DeleteConsumerChain`: Removed PSS/power shaping cleanup calls

**genesis.go:**
- `InitGenesis`: Removed `InitializeSlashMeter` call

**consumer_equivocation.go:**
- Uses per-consumer infraction parameters via `GetInfractionParameters`

**grpc_query.go:**
- `QueryThrottleState`: Returns empty values
- `QueryRegisteredConsumerRewardDenoms`: Returns empty list
- `QueryConsumerChainOptedInValidators`: Returns all consumer validators
- `QueryConsumerValidators`: Simplified to remove power shaping
- `GetConsumerChain`: Removed power shaping (kept per-consumer infraction parameters)
- `hasToValidate`: Simplified - always returns true for active validators

**keeper.go:**
- Added `CreateProviderConsensusValidator` function

**validator_set_storage.go:**
- Added `SetLastProviderConsensusValSet` and `GetLastProviderConsensusValSet`

**ibc_module.go:**
- `ProviderFeePoolAddr` set to empty string (rewards removed)

### Consumer Keeper (`x/vaas/consumer/keeper/`)

**genesis.go:**
- Removed `SetOutstandingDowntime` and `SetLastTransmissionBlockHeight`
- Simplified export to not export removed features

**validators.go:**
- `IsValidatorJailed`: Always returns false (slash functionality removed)
- `SlashWithInfractionReason`: Logs but doesn't send slash packet, returns zero

**module.go:**
- Removed migrations (ConsensusVersion = 1)
- Simplified `EndBlock` to remove changeover and distribution

**ibc_module.go:**
- Removed distribution transfer channel initialization

### Consumer Types (`x/vaas/consumer/types/`)

**genesis.go:**
- `NewRestartGenesisState`: Removed `OutstandingDowntime` and `LastTransmissionBlockHeight` parameters

### Shared Types (`x/vaas/types/`)

**denom_helpers.go:**
- Added `ValidateIBCDenom` function

## File Structure

```text
vaas/
├── go.mod                      # module github.com/allinbits/vaas
├── go.sum
├── REWRITE_SUMMARY.md          # This document
├── app/                        # provider + consumer Cosmos SDK apps and CLIs
├── docs/                       # protocol documentation
├── proto/vaas/                 # protobuf definitions
├── tests/e2e/                  # Docker-based e2e suite (ts-relayer)
├── testutil/
│   ├── crypto/                 # test crypto utilities
│   └── keeper/                 # in-memory keeper + generated mocks
└── x/vaas/
    ├── provider/
    │   ├── keeper/             # provider keeper
    │   ├── types/              # provider types (includes .pb.go files)
    │   ├── client/cli/         # CLI commands
    │   ├── module.go           # module definition
    │   └── ibc_module.go       # IBC v2 callbacks (api.IBCModule)
    ├── consumer/
    │   ├── keeper/             # consumer keeper
    │   ├── types/              # consumer types (includes .pb.go files)
    │   ├── client/cli/         # CLI commands
    │   ├── module.go           # module definition
    │   └── ibc_module.go       # IBC v2 callbacks (api.IBCModule)
    ├── no_valupdates_staking/  # staking without valset exports (consumer)
    ├── no_valupdates_genutil/  # genutil without gentx valset init (consumer)
    └── types/                  # shared types
```

## Testing

Unit tests live alongside their source files (`*_test.go`) and rely on the
in-memory keeper helpers in `testutil/keeper/`. Mocks are regenerated from
`x/vaas/types/expected_keepers.go` via `make mocks-gen`. Docker-based e2e
tests are under `tests/e2e/`.

When tests for features that were removed entirely (e.g. slash throttling,
reward distribution, PSS, power shaping) were not ported.

## Build & Test Commands

```bash
make build      # go build ./...
make test       # unit tests (25m timeout, excludes e2e)
make lint       # golangci-lint
make test-e2e   # Docker-based e2e (requires Docker)
```

## Migration Notes

1. **No opt-in/out**: All validators on the provider automatically validate all consumer chains
2. **No power shaping**: Consumer chains get the full provider validator set (up to MaxProviderConsensusValidators)
3. **Per-consumer infraction params**: Each consumer can have custom slash/jail parameters
4. **No rewards**: Consumer chains don't distribute rewards to the provider
5. **No slash packets**: Consumer chains log infractions but don't send slash packets to provider
6. **Simplified genesis**: Consumer genesis doesn't include outstanding downtime or last transmission block height

## Dependencies

VAAS targets the same family of dependencies as interchain-security v7,
adjusted for the AtomOne ecosystem. See [`go.mod`](go.mod) for the exact
pinned versions; at the time of writing:

- Cosmos SDK v0.53.0, replaced via `go.mod` `replace` directive with the
  AtomOne fork (`github.com/atomone-hub/cosmos-sdk v0.50.14-atomone.x`).
- IBC-Go v10.2.0 (IBC v2 routing is used; see
  [`x/vaas/provider/ibc_module.go`](x/vaas/provider/ibc_module.go) and
  [`x/vaas/consumer/ibc_module.go`](x/vaas/consumer/ibc_module.go)).
- CometBFT v0.38.20.

All imports were updated from `github.com/cosmos/interchain-security/v7` to `github.com/allinbits/vaas`.
