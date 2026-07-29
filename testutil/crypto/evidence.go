package crypto

import (
	"fmt"
	"time"

	"github.com/cometbft/cometbft/crypto/tmhash"
	tmproto "github.com/cometbft/cometbft/proto/tendermint/types"
	tmtypes "github.com/cometbft/cometbft/types"
)

// MakeBlockID utility function duplicated from CometBFT
// see https://github.com/cometbft/cometbft/blob/main/evidence/verify_test.go#L554
func MakeBlockID(hash []byte, partSetSize uint32, partSetHash []byte) tmtypes.BlockID {
	var (
		h   = make([]byte, tmhash.Size)
		psH = make([]byte, tmhash.Size)
	)
	copy(h, hash)
	copy(psH, partSetHash)
	return tmtypes.BlockID{
		Hash: h,
		PartSetHeader: tmtypes.PartSetHeader{
			Total: partSetSize,
			Hash:  psH,
		},
	}
}

func MakeAndSignVote(
	blockID tmtypes.BlockID,
	blockHeight int64,
	blockTime time.Time,
	valSet *tmtypes.ValidatorSet,
	signer tmtypes.PrivValidator,
	chainID string,
) *tmtypes.Vote {
	pubKey, err := signer.GetPubKey()
	if err != nil {
		panic(fmt.Errorf("can't get pubkey: %w", err))
	}
	addr := pubKey.Address()
	idx, _ := valSet.GetByAddress(addr)
	vote := &tmtypes.Vote{
		ValidatorAddress: addr,
		ValidatorIndex:   idx,
		Height:           blockHeight,
		Round:            0,
		Type:             tmproto.PrecommitType,
		BlockID:          blockID,
		Timestamp:        blockTime,
	}
	_, err = tmtypes.SignAndCheckVote(vote, signer, chainID, false)
	if err != nil {
		panic(err)
	}

	v := vote.ToProto()
	err = signer.SignVote(chainID, v)
	if err != nil {
		panic(err)
	}

	vote.Signature = v.Signature
	return vote
}

// MakeAndSignVoteWithForgedValAddress makes and signs a vote using two different keys:
// one to derive the validator address in the vote and a second to sign it.
func MakeAndSignVoteWithForgedValAddress(
	blockID tmtypes.BlockID,
	blockHeight int64,
	blockTime time.Time,
	valSet *tmtypes.ValidatorSet,
	signer tmtypes.PrivValidator,
	valAddressSigner tmtypes.PrivValidator,
	chainID string,
) *tmtypes.Vote {
	pubKey, err := signer.GetPubKey()
	if err != nil {
		panic(fmt.Errorf("can't get pubkey: %w", err))
	}
	addr := pubKey.Address()
	idx, _ := valSet.GetByAddress(addr)

	// create the vote using a different key than the signing key
	vote, err := tmtypes.MakeVote(
		valAddressSigner,
		chainID,
		idx,
		blockHeight,
		0,
		tmproto.PrecommitType,
		blockID,
		blockTime,
	)
	if err != nil {
		panic(err)
	}

	// sign vote using the given private key
	v := vote.ToProto()
	err = signer.SignVote(chainID, v)
	if err != nil {
		panic(err)
	}

	vote.Signature = v.Signature
	return vote
}
