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

func TestLieRangeIsInclusive(t *testing.T) {
	for name, l := range map[string]lie{
		"from is the height":  {absent: addrA, from: 10, until: 15},
		"until is the height": {absent: addrA, from: 5, until: 10},
		"a one-height range":  {absent: addrA, from: 10, until: 10},
	} {
		t.Run(name, func(t *testing.T) {
			got := l.apply(requestAtHeight10())
			require.Equal(t, cmtproto.BlockIDFlagAbsent, got.DecidedLastCommit.Votes[0].BlockIdFlag, "the range's bounds must be inside the lie")
		})
	}

	req := requestAtHeight10()
	require.Same(t, req, lie{absent: addrA, from: 5, until: 9}.apply(req), "the height after until must be outside the lie")
	require.Same(t, req, lie{absent: addrA, from: 11, until: 15}.apply(req), "the height before from must be outside the lie")
}

func TestLieFromEnvRejectsAnAddressOfTheWrongLength(t *testing.T) {
	t.Setenv("FORGE_ABSENT_ADDRESS", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA") // 19 bytes
	t.Setenv("FORGE_FROM_HEIGHT", "5")
	t.Setenv("FORGE_UNTIL_HEIGHT", "15")
	_, err := lieFromEnv()
	require.Error(t, err, "an address that can match no vote must be refused, not silently lie about nothing")
}

func TestNoTargetLiesAboutNothing(t *testing.T) {
	req := requestAtHeight10()
	require.Same(t, req, lie{}.apply(req))
}
