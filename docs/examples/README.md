# VAAS Example Configurations

This directory contains example configurations for deploying VAAS with IBC v2 (Eureka) support.

## Files

| File | Description |
|------|-------------|
| [provider_genesis.json](provider_genesis.json) | Example provider chain genesis state |
| [consumer_genesis.json](consumer_genesis.json) | Example consumer chain genesis state |
| [msg_create_consumer.json](msg_create_consumer.json) | Example MsgCreateConsumer transaction |

## IBC v2 vs v1 Configuration

VAAS supports both IBC v1 (channel-based) and IBC v2 (client-based) routing. The key differences in configuration:

| Aspect | IBC v1 | IBC v2 |
|--------|--------|--------|
| Consumer Identification | `channel_id` | `client_id` |
| Provider Tracking | `provider_channel_id` | `provider_client_id` |
| Routing | Port/Channel pairs | Client IDs with App IDs |
| Connection | Required | Not required |

## Usage

### New Chain Deployment (IBC v2)

For new deployments using IBC v2:

1. Use `provider_genesis.json` as a template for your provider chain
2. Create consumers using `msg_create_consumer.json` as reference
3. Consumer genesis is automatically generated when spawning

### Existing Chain Migration

IBC v2 migration from v1 is not supported for existing chains. Chains must be deployed fresh with IBC v2 configuration.
