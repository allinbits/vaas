#!/bin/bash
# Add relayer key using fixed mnemonic
# Usage: ./add-relayer-key.sh <binary> <home>

BINARY=$1
HOME_DIR=$2
MNEMONIC="abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon art"

# Delete existing key if present
$BINARY --home "$HOME_DIR" keys delete relayer -y 2>/dev/null || true

# Add key from mnemonic
echo "$MNEMONIC" | $BINARY --home "$HOME_DIR" keys add relayer --recover

echo "Relayer key added. Address:"
$BINARY --home "$HOME_DIR" keys show relayer -a
