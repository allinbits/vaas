#!/bin/sh
# Adds the localnet "owner" key: HD index 1 of the shared relayer mnemonic, so
# the same address bytes exist on both chains while staying off the relayer's
# busy account. Used to register the consumer and declare/pin the IBC clients.
set -e

BINARY="$1"
HOME_DIR="$2"
MNEMONIC="abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon art"

echo "$MNEMONIC" | $BINARY keys add owner --recover --index 1 --home "$HOME_DIR" --keyring-backend test
