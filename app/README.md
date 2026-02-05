# VAAS Localnet Setup

This guide explains how to run a local VAAS (Validator-as-a-Service) testnet with a provider chain, consumer chain, and IBC relayer.

## Prerequisites

- Go 1.21+
- [Hermes IBC Relayer](https://hermes.informal.systems/) v1.10+

### Install Hermes

```bash
# macOS ARM64
curl -L https://github.com/informalsystems/hermes/releases/download/v1.13.3/hermes-v1.13.3-aarch64-apple-darwin.tar.gz | tar -xz -C ~/.local/bin/


# linux amd64
curl -L https://github.com/informalsystems/hermes/releases/download/v1.13.3/hermes-v1.13.3-x86_64-unknown-linux-gnu.tar.gz | tar -xz -C ~/.local/bin/

# Verify installation (assuming binary is in your $PATH)
hermes version
```

Make sure that `~/.local/bin` is in your $PATH, or use `/usr/local/bin` instead if it applies to your system.

## Quick Start

A single command starts the entire localnet (provider, consumer, and relayer):

```bash
make localnet-start
```

This will:
1. Build the provider and consumer binaries
2. Start the provider chain (`provider-localnet`) in the background
3. Wait for the provider RPC to be ready
4. Initialize and start the consumer chain (`consumer-localnet`) in the background
5. Wait for the consumer RPC to be ready
6. Configure and set up the Hermes relayer (keys, connection, channel)
7. Start the relayer in the background

Log files:
- Provider: `/tmp/vaas-provider.log`
- Consumer: `/tmp/vaas-consumer.log`
- Relayer: `/tmp/vaas-hermes.log`

## Manual Setup (3-Terminal)

For debugging or development, you can run each component in a separate terminal.

### Terminal 1: Provider Chain

```bash
make provider-start
```

Wait until you see blocks being produced before continuing.

### Terminal 2: Consumer Chain

```bash
# After provider is running
make consumer-start
```

Wait until you see blocks being produced before continuing.

### Terminal 3: Relayer

```bash
# After both chains are running
make relayer-setup
make relayer-start
```

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
tail -f /tmp/vaas-hermes.log

# Query channel status
hermes --config ~/.vaas-hermes/config.toml query channels --chain consumer-localnet
```

## Clean Up

Stop all processes and remove all localnet data:

```bash
make localnet-clean
```

This will kill running provider/consumer/hermes processes, remove chain data directories (`~/.provider-localnet`, `~/.consumer-localnet`), Hermes config (`~/.vaas-hermes`), log files, and temporary files.

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
│                 │◄─────────────────────────►│                 │
│  Port: provider │                           │  Port: consumer │
└─────────────────┘                           └─────────────────┘
     :26657                                        :26667
```

The provider chain manages consumer chain registration and validator sets. The consumer chain receives validator updates from the provider via the VAAS IBC channel. The Hermes relayer handles packet relay between the two chains.
