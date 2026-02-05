# VAAS IBC v2 Deployment Guide

This guide covers deploying VAAS provider and consumer chains with IBC v2 (Eureka) support.

## Overview

IBC v2 simplifies the cross-chain validation setup by eliminating channel handshakes. Instead of establishing channels between provider and consumer, IBC v2 uses direct client-based routing with application identifiers.

### Key Differences from IBC v1

| Aspect | IBC v1 | IBC v2 |
|--------|--------|--------|
| Setup Complexity | Channel handshake required | No handshake needed |
| Routing | Channel/Port IDs | Client IDs + App IDs |
| Packet Ordering | Strict ordered channels | Out-of-order handling |
| Consumer Launch | After channel established | After RegisterCounterparty |

## Prerequisites

- Go 1.21+
- ibc-go v10+
- Cosmos SDK v0.53+
- CometBFT v1.0+

## Provider Chain Setup

### 1. Initialize Provider

```bash
# Initialize the provider chain
providerd init provider-node --chain-id provider-1

# Add genesis accounts and configure as needed
providerd genesis add-genesis-account ...
```

### 2. Configure VAAS Module

Add the VAAS provider module to your app.go:

```go
import (
    "github.com/allinbits/vaas/x/vaas/provider"
    providerkeeper "github.com/allinbits/vaas/x/vaas/provider/keeper"
    vaastypes "github.com/allinbits/vaas/x/vaas/types"
)

// In NewApp:
app.ProviderKeeper = providerkeeper.NewKeeper(
    appCodec,
    keys[providertypes.StoreKey],
    app.StakingKeeper,
    app.SlashingKeeper,
    app.IBCKeeper.ClientKeeper,
    authtypes.NewModuleAddress(govtypes.ModuleName).String(),
)

// Register IBC v2 module
providerIBCV2 := provider.NewIBCModuleV2(&app.ProviderKeeper)
ibcRouterV2.AddRoute(vaastypes.ProviderAppID, providerIBCV2)

// Set the IBC packet handler for v2
app.ProviderKeeper.SetIBCPacketHandler(app.IBCKeeper)
```

### 3. Genesis Configuration

Set provider parameters in genesis:

```json
{
  "vaas": {
    "valset_update_id": "1",
    "params": {
      "trusting_period_fraction": "0.66",
      "vaas_timeout_period": "2419200s",
      "blocks_per_epoch": "600",
      "max_provider_consensus_validators": "180"
    }
  }
}
```

### 4. Start Provider

```bash
providerd start
```

## Consumer Chain Setup

### 1. Create Consumer on Provider

Submit a MsgCreateConsumer transaction on the provider:

```bash
providerd tx provider create-consumer \
  --chain-id consumer-1 \
  --name "Consumer Chain" \
  --spawn-time "2024-06-01T12:00:00Z" \
  --unbonding-period "1209600s" \
  --from validator
```

Or using JSON:

```bash
providerd tx provider create-consumer consumer_config.json --from validator
```

### 2. Export Consumer Genesis

After the consumer is created but before spawn time:

```bash
providerd query provider consumer-genesis <consumer-id> --output json > consumer_genesis.json
```

### 3. Initialize Consumer Chain

```bash
# Initialize consumer node
consumerd init consumer-node --chain-id consumer-1

# Import the consumer genesis from provider
cp consumer_genesis.json ~/.consumerd/config/genesis.json
```

### 4. Configure Consumer VAAS Module

Add the VAAS consumer module to your consumer app.go:

```go
import (
    "github.com/allinbits/vaas/x/vaas/consumer"
    consumerkeeper "github.com/allinbits/vaas/x/vaas/consumer/keeper"
    vaastypes "github.com/allinbits/vaas/x/vaas/types"
)

// In NewApp:
app.ConsumerKeeper = consumerkeeper.NewKeeper(
    appCodec,
    keys[consumertypes.StoreKey],
    app.IBCKeeper.ClientKeeper,
)

// Register IBC v2 module
consumerIBCV2 := consumer.NewIBCModuleV2(&app.ConsumerKeeper)
ibcRouterV2.AddRoute(vaastypes.ConsumerAppID, consumerIBCV2)
```

### 5. Start Consumer at Spawn Time

Start the consumer chain at or after the spawn time:

```bash
consumerd start
```

## Client Registration (IBC v2)

### On Provider

After both chains are running, register the counterparty:

```bash
# Create light client of consumer on provider (if not using existing)
providerd tx ibc client create <client-state.json> <consensus-state.json> --from validator

# The consumer ID to client ID mapping is set automatically
```

### On Consumer

The consumer automatically establishes the provider client on receiving the first VSC packet.

## Validator Set Updates

In IBC v2, validator set changes (VSC packets) are sent at epoch boundaries:

1. Provider detects validator set changes
2. At epoch end, provider queues VSC packets for each launched consumer
3. VSC packets are sent via IBC v2 client routing
4. Consumer receives and applies validator updates

### Out-of-Order Handling

IBC v2 supports out-of-order packet delivery. The consumer:
- Tracks the highest valset update ID received
- Ignores packets with lower IDs (late arrivals)
- Processes packets with higher IDs normally

## Monitoring

### Provider Queries

```bash
# List all consumers
providerd query provider list-consumer-chains

# Get specific consumer state
providerd query provider consumer <consumer-id>

# Check pending VSC packets
providerd query provider pending-vsc-packets <consumer-id>
```

### Consumer Queries

```bash
# Get provider info
consumerd query consumer provider-info

# Check validator set
consumerd query staking validators
```

## Troubleshooting

### Consumer Not Receiving Updates

1. Verify consumer is in LAUNCHED phase:
   ```bash
   providerd query provider consumer <consumer-id>
   ```

2. Check client mapping exists:
   ```bash
   providerd query provider consumer-client <consumer-id>
   ```

3. Verify IBC v2 handler is set on provider

### Packet Timeouts

If VSC packets timeout:
- Consumer will be moved to STOPPED phase
- Removal process is initiated
- Check timeout configuration in params

### Client Not Found

Ensure the IBC client exists and is active:
```bash
providerd query ibc client state <client-id>
```

## Security Considerations

### Key Assignment

Validators should assign separate consensus keys for consumer chains:

```bash
providerd tx provider assign-consumer-key <consumer-id> <consumer-pubkey> --from validator
```

### Infraction Handling

Configure appropriate slash parameters per consumer:
- `double_sign`: Severe slashing for equivocation
- `downtime`: Light slashing for liveness issues

## Migration Notes

### IBC v1 to v2 Migration

**Migration from IBC v1 to v2 is not supported for existing chains.**

IBC v2 requires fresh chain deployment because:
1. Channel-based state cannot be migrated to client-based routing
2. In-flight packets on channels cannot be migrated
3. Consumer tracking mechanisms are fundamentally different

For new deployments, always use IBC v2 configuration.
