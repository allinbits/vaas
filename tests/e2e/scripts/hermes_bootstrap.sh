#!/bin/sh
# Hermes relayer bootstrap script for VAAS e2e tests.
# This script is used as a reference — the test suite generates
# the actual bootstrap script dynamically with correct chain IDs
# and mnemonic.
#
# Usage: This script is mounted into the Hermes container and executed
# as the entrypoint. It imports relayer keys and starts Hermes.

set -e

PROVIDER_CHAIN_ID="${PROVIDER_CHAIN_ID:-provider-e2e}"
CONSUMER_CHAIN_ID="${CONSUMER_CHAIN_ID:-consumer-e2e}"
RELAYER_MNEMONIC="${RELAYER_MNEMONIC:-abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon art}"

echo "Waiting for chains to be ready..."
sleep 5

# Import relayer keys for both chains
echo "$RELAYER_MNEMONIC" > /tmp/mnemonic.txt
hermes keys add --chain "$PROVIDER_CHAIN_ID" --mnemonic-file /tmp/mnemonic.txt --key-name relayer 2>/dev/null || true
hermes keys add --chain "$CONSUMER_CHAIN_ID" --mnemonic-file /tmp/mnemonic.txt --key-name relayer 2>/dev/null || true
rm -f /tmp/mnemonic.txt

echo "Hermes keys configured, starting relayer..."
hermes start
