# VAAS Rewrite Plan

Rewrite `x/ccv/provider` and `x/ccv/consumer` in a new `vaas` folder as a **new Go module** with package path `github.com/allinbits/vaas`.

---

## Feature Decisions Summary

### Provider Module

| Feature | Decision | Notes |
|---------|----------|-------|
| Consumer Lifecycle Management | **KEEP** | Phases: REGISTERED → INITIALIZED → LAUNCHED → STOPPED → DELETED |
| Top N Chains | **REMOVE** | Part of PSS removal |
| Opt-In Chains | **REMOVE** | Part of PSS removal - all validators validate all consumers |
| Partial Set Security (PSS) | **REMOVE** | All validators validate all consumers (no opt-in/out) |
| Power Shaping | **REMOVE** | No caps, allowlists, denylists, etc. |
| Key Assignment | **KEEP** | Validators can use different keys per consumer |
| Per-Consumer Infraction Parameters | **KEEP** | Customizable slash/jail params per consumer |
| Slash Packet Throttling | **REMOVE** | Remove all slash-related functionality |
| Consumer Reward Distribution | **REMOVE** | No cross-chain rewards |
| Double Voting Evidence | **KEEP** | Handle double voting evidence from consumers |
| Light Client Misbehavior | **KEEP (detection only)** | Keep detection/logging, no slashing action |
| Epoch-Based VSC Packets | **KEEP** | Send VSC at epoch boundaries |
| Per-Consumer Commission Rates | **REMOVE** | Same commission as provider |
| Consumer Metadata | **KEEP** | Name, description, metadata for discovery |
| Inactive Provider Validators (ADR-017) | **REMOVE** | Only active provider validators can validate consumers |
| Minimum Validators for Launch | **KEEP** | Safety check - don't launch with zero validators |
| Chain ID Update Before Launch | **KEEP** | Allow fixing chain ID typos before launch |
| Existing Client/Connection Reuse | **KEEP** | Reuse existing IBC client/connection when creating consumer |

### Consumer Module

| Feature | Decision | Notes |
|---------|----------|-------|
| VSC Packet Handling | **KEEP** | Core functionality, without VSCMatured (already removed in v6.4.0) |
| Slash Packet Sending | **REMOVE** | All slash-related removed |
| Slash Packet Throttling/Retry | **REMOVE** | All slash-related removed |
| Reward Distribution to Provider | **REMOVE** | Consistent with provider removal |
| Standalone-to-Consumer Changeover | **REMOVE** | Only new chains as consumers |
| Outstanding Downtime Flag | **REMOVE** | Part of slash removal |
| Historical Info Storage | **KEEP** | Required for IBC evidence verification |

---

## Resulting Architecture

### Provider Module (`vaas/x/ccv/provider`)

**Core Components:**
1. **Consumer Lifecycle** - Create, update, remove consumer chains with phase management
2. **Validator Set Management** - Track which validators are opted-in per consumer
3. **Key Assignment** - Allow validators to use different keys per consumer
4. **VSC Packet Generation** - Generate and send VSC packets at epoch boundaries
5. **IBC Channel Management** - Handshake, packet handling
6. **Light Client Misbehavior Detection** - Log/emit events for misbehavior (no slashing)
7. **Consumer Metadata** - Store chain name, description, metadata

**State to Keep:**
- Consumer ID → Chain ID, Channel ID, Client ID, Owner, Phase, Metadata
- Consumer ID → Initialization Parameters (including connection_id for client reuse)
- Spawn/Removal time scheduling
- Validator consumer public keys (key assignment)
- VSC update ID counter
- Epoch tracking

**Validator Selection:** All active provider validators automatically validate all consumer chains (no opt-in/out).

**Messages:**
- `MsgCreateConsumer`
- `MsgUpdateConsumer`
- `MsgRemoveConsumer`
- `MsgAssignConsumerKey`
- `MsgSubmitConsumerMisbehaviour`
- `MsgSubmitConsumerDoubleVoting`

### Consumer Module (`vaas/x/ccv/consumer`)

**Core Components:**
1. **VSC Packet Handling** - Receive and apply validator set changes
2. **Cross-Chain Validator Storage** - Store current validator set
3. **Historical Info Tracking** - Store validator sets by height for IBC
4. **IBC Channel Management** - Handshake, packet handling
5. **Parameters** - Minimal config (unbonding period, timeouts, etc.)

**State to Keep:**
- Provider client/channel IDs
- Cross-chain validators (current set)
- Historical info (validator sets by height)
- Height → VSC ID mapping
- Init genesis height
- Module parameters

**Removed State:**
- Slash record
- Outstanding downtime
- Pending data packets (slash/VSCMatured)
- Reward distribution tracking
- Standalone changeover flags

---

## Implementation Plan

### Phase 1: Create Folder Structure
```
vaas/
├── go.mod                   # module github.com/allinbits/vaas
├── go.sum
├── x/
│   └── ccv/
│       ├── provider/
│       │   ├── keeper/
│       │   ├── types/
│       │   ├── client/cli/
│       │   ├── module.go
│       │   └── ibc_module.go
│       ├── consumer/
│       │   ├── keeper/
│       │   ├── types/
│       │   ├── client/cli/
│       │   ├── module.go
│       │   └── ibc_module.go
│       └── types/           # Shared types
├── proto/                   # Protobuf definitions
│   └── vaas/
│       └── ccv/
│           ├── provider/v1/
│           └── consumer/v1/
└── ...                      # Other root files as needed
```

**Go Module:** `github.com/allinbits/vaas`

Import paths will be:
- `github.com/allinbits/vaas/x/ccv/provider`
- `github.com/allinbits/vaas/x/ccv/consumer`
- `github.com/allinbits/vaas/x/ccv/types`

### Phase 2: Copy Provider Module Files

**Copy all files EXCEPT these (entirely for removed features):**
- `keeper/throttle.go`, `keeper/throttle_test.go` - Slash throttling
- `keeper/distribution.go`, `keeper/distribution_test.go` - Consumer rewards
- `keeper/power_shaping.go`, `keeper/power_shaping_test.go` - Power shaping
- `keeper/infraction_parameters.go`, `keeper/infraction_parameters_test.go` - Per-consumer infraction params
- `keeper/partial_set_security.go`, `keeper/partial_set_security_test.go` - PSS / Top N (keep opt-in only)
- `keeper/provider_consensus.go`, `keeper/provider_consensus_test.go` - Inactive validators
- `migrations/` - Skip entire migrations folder (fresh start)

**Files to keep but clean (remove references to removed features):**
- `keeper/consumer_equivocation.go` - Keep double voting evidence & light client misbehavior
- `keeper/keeper.go` - Remove keeper fields for removed features
- `keeper/msg_server.go` - Remove handlers for removed features
- `keeper/relay.go` - Remove slash packet handling
- `types/*.go` - Remove types for removed features

### Phase 3: Copy Consumer Module Files

**Copy all files EXCEPT these (entirely for removed features):**
- `keeper/changeover.go`, `keeper/changeover_test.go` - Standalone changeover
- `keeper/distribution.go`, `keeper/distribution_test.go` - Reward distribution
- `keeper/throttle_retry.go`, `keeper/throttle_retry_test.go` - Slash throttling/retry
- `migrations/` - Skip entire migrations folder (fresh start)

**Files to keep but clean:**
- `keeper/relay.go` - Remove slash packet sending, keep VSC handling
- `keeper/validators.go` - Remove outstanding downtime handling
- `keeper/keeper.go` - Remove keeper fields for removed features
- `types/*.go` - Remove types for removed features

### Phase 4: Update Imports & Module Path

1. Update all import paths from `github.com/cosmos/interchain-security/v7` to `github.com/allinbits/vaas`
2. Update proto package paths
3. Create `go.mod` with `module github.com/allinbits/vaas`
4. Run `go mod tidy` to resolve dependencies

### Phase 5: Clean Removed Feature Code

For each copied file:
1. Remove imports for removed features
2. Remove function calls to removed features
3. Remove keeper fields for removed features
4. Remove message handlers for removed features
5. Remove state keys for removed features
6. Update genesis import/export

### Phase 6: Testing

1. **Build**: `go build ./vaas/...`
2. **Unit tests**: `go test ./vaas/...`
3. **Integration test**: Start provider + consumer chains

---

## Key Files to Reference

**Provider (current):**
- `x/ccv/provider/keeper/keeper.go` - Keeper structure
- `x/ccv/provider/keeper/consumer_lifecycle.go` - Lifecycle management
- `x/ccv/provider/keeper/key_assignment.go` - Key assignment
- `x/ccv/provider/keeper/relay.go` - VSC packet logic
- `x/ccv/provider/types/keys.go` - State keys
- `x/ccv/provider/ibc_module.go` - IBC callbacks

**Consumer (current):**
- `x/ccv/consumer/keeper/keeper.go` - Keeper structure
- `x/ccv/consumer/keeper/relay.go` - VSC handling
- `x/ccv/consumer/keeper/validators.go` - Validator + historical info
- `x/ccv/consumer/types/keys.go` - State keys
- `x/ccv/consumer/ibc_module.go` - IBC callbacks
