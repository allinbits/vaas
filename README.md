# VAAS - Validator-as-a-Service

**vaas** is a simplified implementation of the Interchain Security (ICS) protocol, derived from [interchain-security](https://github.com/cosmos/interchain-security). It provides core cross-chain validation functionality while removing complex features not needed for simpler deployments.

## Overview

VAAS allows Cosmos blockchains to lease their proof-of-stake security to consumer chains. All active validators on the provider chain automatically validate all consumer chains - there is no opt-in/opt-out mechanism.

## Features

### IBC v2 (Eureka) Support

VAAS supports both IBC v1 (channel-based) and IBC v2 (client-based) routing:

| Feature | IBC v1 | IBC v2 |
|---------|--------|--------|
| Routing | Channel/Port IDs | Client IDs |
| Packet Order | Strict (ordered channels) | Flexible (out-of-order handling) |
| Consumer Tracking | Channel ID mapping | Client ID mapping |
| Application IDs | `provider`/`consumer` ports | `vaas/provider`/`vaas/consumer` |

IBC v2 benefits:
- **Simpler setup**: No channel handshake required
- **Out-of-order packets**: Gracefully handles late VSC packets
- **Client-based routing**: Direct routing via light client IDs

### Kept from ICS

| Feature | Description |
|---------|-------------|
| Consumer Lifecycle | Full lifecycle management (REGISTERED → INITIALIZED → LAUNCHED → STOPPED → DELETED) |
| Key Assignment | Validators can use different consensus keys per consumer chain |
| Per-Consumer Infraction Parameters | Customizable slash/jail parameters per consumer |
| VSC Packets | Validator set updates sent at epoch boundaries |
| Double Voting Evidence | Handle double voting evidence from consumers |
| Light Client Misbehavior | Detection and logging of misbehavior |
| Consumer Metadata | Name, description, metadata for chain discovery |
| Client/Connection Reuse | Reuse existing IBC client/connection when creating consumer |

### Removed from ICS

| Feature | Reason |
|---------|--------|
| Partial Set Security (PSS) | All validators validate all consumers |
| Top N / Opt-In Chains | No validator selection per consumer |
| Power Shaping | No caps, allowlists, denylists, priority lists |
| Consumer Reward Distribution | No cross-chain rewards |
| Slash Packet Throttling | Simplified slash handling |
| Per-Consumer Commission Rates | Validators use same commission as provider |
| Standalone-to-Consumer Changeover | Only new chains as consumers |

## IBC v2 Integration

To enable IBC v2 support in your application:

```go
import (
    "github.com/allinbits/vaas/x/vaas/provider"
    "github.com/allinbits/vaas/x/vaas/consumer"
    vaastypes "github.com/allinbits/vaas/x/vaas/types"
)

// Provider chain setup
providerIBCV2 := provider.NewIBCModuleV2(&providerKeeper)
ibcRouterV2.AddRoute(vaastypes.ProviderAppID, providerIBCV2)
providerKeeper.SetIBCPacketHandler(ibcCoreHandler)

// Consumer chain setup
consumerIBCV2 := consumer.NewIBCModuleV2(&consumerKeeper)
ibcRouterV2.AddRoute(vaastypes.ConsumerAppID, consumerIBCV2)
```

The IBC v2 modules implement the `api.IBCModule` interface from ibc-go v10.

## Learn More

- [ICS Documentation](https://cosmos.github.io/interchain-security/)
- [ICS Technical Specification](https://github.com/cosmos/ibc/blob/main/spec/app/ics-028-cross-chain-validation/README.md)
- [Cosmos SDK Documentation](https://docs.cosmos.network)
- [IBC Protocol](https://ibc.cosmos.network/)
