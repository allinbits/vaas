# VAAS Module Rewrite Summary

This document summarizes the rewrite of the CCV (Cross-Chain Validation) provider and consumer modules from `github.com/cosmos/interchain-security/v7` into a new `vaas` folder as a standalone Go module with package path `github.com/allinbits/vaas`.

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
| **Standalone-to-Consumer Changeover** | Only new chains as consumers |
| **Outstanding Downtime Flag** | Part of slash removal |

## Features Kept

### Provider Module
- Consumer Lifecycle Management (REGISTERED → INITIALIZED → LAUNCHED → STOPPED → DELETED)
- Key Assignment (validators can use different keys per consumer)
- Per-Consumer Infraction Parameters (custom slash/jail params per consumer)
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

### Provider Keeper (`x/ccv/provider/keeper/`)

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

### Consumer Keeper (`x/ccv/consumer/keeper/`)

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

### Consumer Types (`x/ccv/consumer/types/`)

**genesis.go:**
- `NewRestartGenesisState`: Removed `OutstandingDowntime` and `LastTransmissionBlockHeight` parameters

### Shared Types (`x/ccv/types/`)

**denom_helpers.go:**
- Added `ValidateIBCDenom` function

## File Structure

```
vaas/
├── go.mod                      # module github.com/allinbits/vaas
├── go.sum
├── REWRITE_SUMMARY.md          # This document
├── testutil/
│   ├── crypto/                 # Test crypto utilities
│   └── keeper/                 # Test keeper utilities
└── x/
    └── ccv/
        ├── provider/
        │   ├── keeper/         # Provider keeper implementation
        │   ├── types/          # Provider types (includes .pb.go files)
        │   ├── client/cli/     # CLI commands
        │   ├── module.go       # Module definition
        │   └── ibc_module.go   # IBC callbacks
        ├── consumer/
        │   ├── keeper/         # Consumer keeper implementation
        │   ├── types/          # Consumer types (includes .pb.go files)
        │   ├── client/cli/     # CLI commands
        │   ├── module.go       # Module definition
        │   └── ibc_module.go   # IBC callbacks
        └── types/              # Shared types
```

## Test Status

### Tests Passing
- `x/ccv/consumer/keeper` - Genesis, keeper, params, validators tests
- `x/ccv/consumer/types` - Genesis, keys, params tests
- `x/ccv/provider/keeper` - Consumer equivocation, hooks, keeper, key assignment, msg server, params, permissionless tests
- `x/ccv/provider/types` - Genesis, keys, msg, params tests
- `x/ccv/types` - Shared params, utils, wire tests

### Tests Removed (tested removed features)
- Provider: relay, staking keeper interface, validator set update, consumer lifecycle, genesis, grpc query
- Consumer: relay (slash packet sending)

## Build & Test Commands

```bash
cd vaas
go build ./...
go test ./...
```

## Migration Notes

1. **No opt-in/out**: All validators on the provider automatically validate all consumer chains
2. **No power shaping**: Consumer chains get the full provider validator set (up to MaxProviderConsensusValidators)
3. **Per-consumer infraction params**: Each consumer can have custom slash/jail parameters
4. **No rewards**: Consumer chains don't distribute rewards to the provider
5. **No slash packets**: Consumer chains log infractions but don't send slash packets to provider
6. **Simplified genesis**: Consumer genesis doesn't include outstanding downtime or last transmission block height

## Dependencies

The module uses the same dependencies as the original interchain-security v7:
- Cosmos SDK v0.53
- IBC-Go v10.1.1
- CometBFT

All imports were updated from `github.com/cosmos/interchain-security/v7` to `github.com/allinbits/vaas`.
