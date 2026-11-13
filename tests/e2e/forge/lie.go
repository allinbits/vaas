package main

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"os"
	"strconv"

	abci "github.com/cometbft/cometbft/abci/types"
	"github.com/cometbft/cometbft/crypto"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
)

// lie is the one thing the forge falsifies: in every block finalized at a
// height within [from, until], the validator at address absent is reported
// to the app as not having signed the previous block, whatever it did. The
// blocks and their commits stay real; only the app's view of who signed is
// false, which is what a lying consumer binary would control.
type lie struct {
	absent []byte
	from   int64
	until  int64
}

// lieFromEnv reads the lie from FORGE_ABSENT_ADDRESS (the target's
// CometBFT validator address, hex), FORGE_FROM_HEIGHT and
// FORGE_UNTIL_HEIGHT (inclusive). With no address set the forge lies about
// nothing.
func lieFromEnv() (lie, error) {
	addr := os.Getenv("FORGE_ABSENT_ADDRESS")
	if addr == "" {
		return lie{}, nil
	}
	absent, err := hex.DecodeString(addr)
	if err != nil {
		return lie{}, fmt.Errorf("FORGE_ABSENT_ADDRESS %q is not hex: %w", addr, err)
	}
	if len(absent) != crypto.AddressSize {
		return lie{}, fmt.Errorf("FORGE_ABSENT_ADDRESS %q is %d bytes, not a %d-byte validator address", addr, len(absent), crypto.AddressSize)
	}
	from, err := strconv.ParseInt(os.Getenv("FORGE_FROM_HEIGHT"), 10, 64)
	if err != nil {
		return lie{}, fmt.Errorf("FORGE_FROM_HEIGHT: %w", err)
	}
	until, err := strconv.ParseInt(os.Getenv("FORGE_UNTIL_HEIGHT"), 10, 64)
	if err != nil {
		return lie{}, fmt.Errorf("FORGE_UNTIL_HEIGHT: %w", err)
	}
	if until < from {
		return lie{}, fmt.Errorf("FORGE_UNTIL_HEIGHT %d is below FORGE_FROM_HEIGHT %d", until, from)
	}
	return lie{absent: absent, from: from, until: until}, nil
}

// apply returns the request the app is shown. Outside the lie it is req
// itself; inside, a copy whose decided last commit reports the target
// absent. req is never modified: the node keeps the true one.
func (l lie) apply(req *abci.RequestFinalizeBlock) *abci.RequestFinalizeBlock {
	if len(l.absent) == 0 || req.Height < l.from || req.Height > l.until {
		return req
	}
	forged := *req
	forged.DecidedLastCommit.Votes = make([]abci.VoteInfo, len(req.DecidedLastCommit.Votes))
	copy(forged.DecidedLastCommit.Votes, req.DecidedLastCommit.Votes)
	for i, vote := range forged.DecidedLastCommit.Votes {
		if bytes.Equal(vote.Validator.Address, l.absent) {
			forged.DecidedLastCommit.Votes[i].BlockIdFlag = cmtproto.BlockIDFlagAbsent
		}
	}
	return &forged
}
