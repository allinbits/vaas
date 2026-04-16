package types_test

import (
	"testing"
	"time"

	clienttypes "github.com/cosmos/ibc-go/v10/modules/core/02-client/types"
	commitmenttypes "github.com/cosmos/ibc-go/v10/modules/core/23-commitment/types"
	ibctmtypes "github.com/cosmos/ibc-go/v10/modules/light-clients/07-tendermint"
	"github.com/stretchr/testify/require"

	tmtypes "github.com/cometbft/cometbft/types"

	"github.com/allinbits/vaas/testutil/crypto"
	"github.com/allinbits/vaas/x/vaas/consumer/types"
	vaastypes "github.com/allinbits/vaas/x/vaas/types"
)

const (
	chainID                      = "gaia"
	trustingPeriod time.Duration = time.Hour * 24 * 7 * 2
	ubdPeriod      time.Duration = time.Hour * 24 * 7 * 3
	maxClockDrift  time.Duration = time.Second * 10
)

var (
	height      = clienttypes.NewHeight(0, 4)
	upgradePath = []string{"upgrade", "upgradedIBCState"}
)

func TestValidateInitialGenesisState(t *testing.T) {
	cId := crypto.NewCryptoIdentityFromIntSeed(238934)
	pubKey := cId.TMCryptoPubKey()

	validator := tmtypes.NewValidator(pubKey, 1)
	valSet := tmtypes.NewValidatorSet([]*tmtypes.Validator{validator})
	valHash := valSet.Hash()
	valUpdates := tmtypes.TM2PB.ValidatorUpdates(valSet)

	cs := ibctmtypes.NewClientState(chainID, ibctmtypes.DefaultTrustLevel, trustingPeriod, ubdPeriod, maxClockDrift, height, commitmenttypes.GetSDKSpecs(), upgradePath)
	consensusState := ibctmtypes.NewConsensusState(time.Now(), commitmenttypes.NewMerkleRoot([]byte("apphash")), valHash)

	params := vaastypes.DefaultParams()
	params.Enabled = true

	cases := []struct {
		name     string
		gs       *types.GenesisState
		expError bool
	}{
		{
			"valid new consumer genesis state",
			types.NewInitialGenesisState(cs, consensusState, valUpdates, params),
			false,
		},
		{
			"invalid new consumer genesis state: nil client state",
			types.NewInitialGenesisState(nil, consensusState, valUpdates, params),
			true,
		},
		{
			"invalid new consumer genesis state: invalid client state",
			types.NewInitialGenesisState(&ibctmtypes.ClientState{ChainId: "badClientState"},
				consensusState, valUpdates, params),
			true,
		},
		{
			"invalid new consumer genesis state: nil consensus state",
			types.NewInitialGenesisState(cs, nil, valUpdates, params),
			true,
		},
		{
			"invalid new consumer genesis state: invalid consensus state",
			types.NewInitialGenesisState(cs, &ibctmtypes.ConsensusState{Timestamp: time.Now()},
				valUpdates, params),
			true,
		},
		{
			"invalid new consumer genesis state: provider client id not empty",
			&types.GenesisState{
				Params:   params,
				NewChain: true,
				Provider: vaastypes.ProviderInfo{
					ClientState:    cs,
					ConsensusState: consensusState,
					InitialValSet:  valUpdates,
				},
				ProviderClientId: "ccvclient",
			},
			true,
		},
		{
			"invalid new consumer genesis state: nil initial validator set",
			types.NewInitialGenesisState(cs, consensusState, nil, params),
			true,
		},
		{
			"invalid new consumer genesis state: invalid consensus state validator set hash",
			types.NewInitialGenesisState(
				cs, ibctmtypes.NewConsensusState(
					time.Now(), commitmenttypes.NewMerkleRoot([]byte("apphash")), []byte("wrong_length_hash")),
				valUpdates, params),
			true,
		},
		{
			"invalid new consumer genesis state: initial validator set does not match validator set hash",
			types.NewInitialGenesisState(
				cs, ibctmtypes.NewConsensusState(
					time.Now(), commitmenttypes.NewMerkleRoot([]byte("apphash")), []byte("9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08")),
				valUpdates, params),
			true,
		},
		{
			"invalid new consumer genesis state: initial validator set does not match validator set hash",
			types.NewInitialGenesisState(
				cs, ibctmtypes.NewConsensusState(
					time.Now(), commitmenttypes.NewMerkleRoot([]byte("apphash")), []byte("9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08")),
				valUpdates, params),
			true,
		},
		{
			"invalid new consumer genesis state: invalid params - ccvTimeoutPeriod",
			types.NewInitialGenesisState(cs, consensusState, valUpdates,
				vaastypes.NewParams(
					true,
					0,
					vaastypes.DefaultHistoricalEntries,
					vaastypes.DefaultConsumerUnbondingPeriod,
				)),
			true,
		},
	}

	for _, c := range cases {
		err := c.gs.Validate()
		if c.expError {
			require.Error(t, err, "%s did not return expected error", c.name)
		} else {
			require.NoError(t, err, "%s returned unexpected error", c.name)
		}
	}
}

func TestValidateRestartConsumerGenesisState(t *testing.T) {
	cId := crypto.NewCryptoIdentityFromIntSeed(234234)
	pubKey := cId.TMCryptoPubKey()

	validator := tmtypes.NewValidator(pubKey, 1)
	valSet := tmtypes.NewValidatorSet([]*tmtypes.Validator{validator})
	valHash := valSet.Hash()
	valUpdates := tmtypes.TM2PB.ValidatorUpdates(valSet)

	heightToValsetUpdateID := []types.HeightToValsetUpdateID{
		{Height: 0, ValsetUpdateId: 0},
	}

	cs := ibctmtypes.NewClientState(chainID, ibctmtypes.DefaultTrustLevel, trustingPeriod, ubdPeriod, maxClockDrift, height, commitmenttypes.GetSDKSpecs(), upgradePath)
	consensusState := ibctmtypes.NewConsensusState(time.Now(), commitmenttypes.NewMerkleRoot([]byte("apphash")), valHash)

	params := vaastypes.DefaultParams()
	params.Enabled = true

	cases := []struct {
		name     string
		gs       *types.GenesisState
		expError bool
	}{
		{
			"valid restart consumer genesis state: handshake in progress",
			types.NewRestartGenesisState("ccvclient", valUpdates, heightToValsetUpdateID, params),
			false,
		},
		{
			"invalid restart consumer genesis state: provider id is empty",
			types.NewRestartGenesisState("", valUpdates, heightToValsetUpdateID, params),
			true,
		},
		{
			"invalid restart consumer genesis: client state defined",
			&types.GenesisState{
				Params:   params,
				NewChain: false,
				Provider: vaastypes.ProviderInfo{
					ClientState:    cs,
					ConsensusState: nil,
					InitialValSet:  valUpdates,
				},
				ProviderClientId: "ccvclient",
			},
			true,
		},
		{
			"invalid restart consumer genesis: consensus state defined",
			&types.GenesisState{
				Params:   params,
				NewChain: false,
				Provider: vaastypes.ProviderInfo{
					ClientState:    nil,
					ConsensusState: consensusState,
					InitialValSet:  valUpdates,
				},
				ProviderClientId: "ccvclient",
			},
			true,
		},
		{
			"invalid restart consumer genesis state: nil initial validator set",
			types.NewRestartGenesisState("ccvclient", nil, nil, params),
			true,
		},
		{
			"invalid restart consumer genesis state: invalid params",
			types.NewRestartGenesisState("ccvclient", valUpdates, nil,
				vaastypes.NewParams(
					true,
					0,
					vaastypes.DefaultHistoricalEntries,
					vaastypes.DefaultConsumerUnbondingPeriod,
				)),
			true,
		},
	}

	for _, c := range cases {
		err := c.gs.Validate()
		if c.expError {
			require.Error(t, err, "%s did not return expected error", c.name)
		} else {
			require.NoError(t, err, "%s returned unexpected error", c.name)
		}
	}
}
