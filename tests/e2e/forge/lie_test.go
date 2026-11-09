package main

import (
	"testing"

	"github.com/stretchr/testify/require"

	abci "github.com/cometbft/cometbft/abci/types"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
)

var (
	addrA = []byte{0xAA, 0xAA, 0xAA, 0xAA, 0xAA, 0xAA, 0xAA, 0xAA, 0xAA, 0xAA, 0xAA, 0xAA, 0xAA, 0xAA, 0xAA, 0xAA, 0xAA, 0xAA, 0xAA, 0xAA}
	addrB = []byte{0xBB, 0xBB, 0xBB, 0xBB, 0xBB, 0xBB, 0xBB, 0xBB, 0xBB, 0xBB, 0xBB, 0xBB, 0xBB, 0xBB, 0xBB, 0xBB, 0xBB, 0xBB, 0xBB, 0xBB}
)

func requestAtHeight10() *abci.RequestFinalizeBlock {
	return &abci.RequestFinalizeBlock{
		Height: 10,
		DecidedLastCommit: abci.CommitInfo{Votes: []abci.VoteInfo{
			{Validator: abci.Validator{Address: addrA, Power: 1}, BlockIdFlag: cmtproto.BlockIDFlagCommit},
			{Validator: abci.Validator{Address: addrB, Power: 1}, BlockIdFlag: cmtproto.BlockIDFlagCommit},
		}},
	}
}

func TestLieMarksTheTargetAbsentInRange(t *testing.T) {
	req := requestAtHeight10()
	got := lie{absent: addrA, from: 5, until: 15}.apply(req)

	require.Equal(t, cmtproto.BlockIDFlagAbsent, got.DecidedLastCommit.Votes[0].BlockIdFlag, "the target must be reported absent")
	require.Equal(t, cmtproto.BlockIDFlagCommit, got.DecidedLastCommit.Votes[1].BlockIdFlag, "another validator must be left alone")
	require.Equal(t, cmtproto.BlockIDFlagCommit, req.DecidedLastCommit.Votes[0].BlockIdFlag, "the request the node holds must not be changed")
}

func TestLieOutsideTheRangeChangesNothing(t *testing.T) {
	req := requestAtHeight10()
	require.Same(t, req, lie{absent: addrA, from: 11, until: 20}.apply(req))
}

func TestNoTargetLiesAboutNothing(t *testing.T) {
	req := requestAtHeight10()
	require.Same(t, req, lie{}.apply(req))
}
