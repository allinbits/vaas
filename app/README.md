# VAAS Localnet Setup

This guide explains how to run a local VAAS (Validator-as-a-Service) testnet with a provider chain, consumer chain, and IBC relayer.

## Prerequisites

- Go 1.21+
- [Hermes IBC Relayer](https://hermes.informal.systems/) v1.10+

### Install Hermes

```bash
# macOS ARM64
curl -L https://github.com/informalsystems/hermes/releases/download/v1.10.4/hermes-v1.10.4-aarch64-apple-darwin.tar.gz | tar -xz -C ~/bin/

# Verify installation
~/bin/hermes version
```

## Quick Start

You'll need **3 terminal windows** to run the full setup.

### Terminal 1: Provider Chain

```bash
# From the repository root
make provider-start
```

This will:
- Build the provider and consumer binaries
- Initialize the provider chain with chain-id `provider-localnet`
- Create a validator and start the chain

Wait until you see blocks being produced before continuing.

### Terminal 2: Consumer Chain

```bash
# From the repository root (after provider is running)
make consumer-start
```

This will:
- Initialize the consumer chain with chain-id `consumer-localnet`
- Register the consumer on the provider
- Fetch the consumer genesis from the provider
- Copy the validator key from provider
- Start the consumer chain

Wait until you see blocks being produced before continuing.

### Terminal 3: Relayer

```bash
# From the repository root (after both chains are running)
make relayer-setup
make relayer-start
```

This will:
- Configure Hermes for both chains
- Set up relayer keys
- Create the IBC connection using genesis clients
- Create the VAAS channel
- Start relaying packets

## Network Configuration

| Chain | RPC | gRPC | P2P |
|-------|-----|------|-----|
| Provider | localhost:26657 | localhost:9090 | localhost:26656 |
| Consumer | localhost:26667 | localhost:9092 | localhost:26666 |

## Useful Commands

### Query Provider

```bash
# List consumer chains
./build/provider --home ~/.provider-localnet query provider list-consumer-chains

# Get consumer genesis
./build/provider --home ~/.provider-localnet query provider consumer-genesis 0
```

### Query Consumer

```bash
# Check provider info
./build/consumer --home ~/.consumer-localnet query vaasconsumer provider-info --node tcp://localhost:26667

# List IBC channels
./build/consumer --home ~/.consumer-localnet query ibc channel channels --node tcp://localhost:26667
```

### Check Relayer Status

```bash
# View Hermes logs
tail -f /tmp/hermes.log

# Query channel status
~/bin/hermes query channels --chain consumer-localnet
```

## Clean Up

To stop everything and clean up:

```bash
# Stop background processes
pkill -f "provider.*start"
pkill -f "consumer.*start"
pkill -f hermes

# Clean all data
make localnet-clean
```

## Troubleshooting

### Consumer fails to start with "consumer genesis not found"

The provider needs time to process the consumer creation transaction. The `consumer-start` target includes a retry loop, but if it still fails:

```bash
# Check if consumer is registered
./build/provider --home ~/.provider-localnet query provider list-consumer-chains

# Manually run genesis fetch
make consumer-genesis CONSUMER_ID=0
make consumer-run
```

### Relayer channel creation fails

The VAAS channel must use the genesis client (`07-tendermint-0`). If you see "invalid client" errors:

```bash
# Check which clients exist
./build/consumer --home ~/.consumer-localnet query ibc client states --node tcp://localhost:26667

# The genesis client should track provider-localnet
# Ensure you're using: --a-client 07-tendermint-0 --b-client 07-tendermint-0
```

### Port conflicts

If ports are already in use, clean up any existing processes:

```bash
lsof -i :26657  # Provider RPC
lsof -i :26667  # Consumer RPC
lsof -i :9090   # Provider gRPC
lsof -i :9092   # Consumer gRPC
```

## Architecture

```
┌─────────────────┐         IBC/VAAS          ┌─────────────────┐
│                 │◄────────Channel──────────►│                 │
│    Provider     │                           │    Consumer     │
│    Chain        │    Hermes Relayer         │    Chain        │
│                 │◄────────────────────────►│                 │
│  Port: provider │                           │  Port: consumer │
└─────────────────┘                           └─────────────────┘
     :26657                                        :26667
```

The provider chain manages consumer chain registration and validator sets. The consumer chain receives validator updates from the provider via the VAAS IBC channel. The Hermes relayer handles packet relay between the two chains.
