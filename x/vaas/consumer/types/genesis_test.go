package types_test

import (
	"testing"
	"time"

	clienttypes "github.com/cosmos/ibc-go/v10/modules/core/02-client/types"
	commitmenttypes "github.com/cosmos/ibc-go/v10/modules/core/23-commitment/types"
	ibctmtypes "github.com/cosmos/ibc-go/v10/modules/light-clients/07-tendermint"
	"github.com/stretchr/testify/require"

	tmtypes "github.com/cometbft/cometbft/types"

	"cosmossdk.io/math"

	sdk "github.com/cosmos/cosmos-sdk/types"

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

	params := vaastypes.DefaultConsumerParams()
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
				vaastypes.NewConsumerParams(
					true,
					0,
					vaastypes.DefaultHistoricalEntries,
					vaastypes.DefaultConsumerUnbondingPeriod,
					vaastypes.DefaultSafeModeThreshold,
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

// TestValidateMissedBlockBitmapLength verifies that GenesisState.Validate
// rejects an imported MissedBlockBitmapEntry whose bitmap length does not
// match ceil(params.SignedBlocksWindow/8). TrackMissedBlocks
// (x/vaas/consumer/keeper/downtime.go) indexes into this bitmap based on the
// current window; an unguarded mismatched length imported straight from
// genesis would otherwise only be caught at BeginBlock, potentially halting
// the chain.
func TestValidateMissedBlockBitmapLength(t *testing.T) {
	cId := crypto.NewCryptoIdentityFromIntSeed(238934)
	pubKey := cId.TMCryptoPubKey()

	validator := tmtypes.NewValidator(pubKey, 1)
	valSet := tmtypes.NewValidatorSet([]*tmtypes.Validator{validator})
	valHash := valSet.Hash()
	valUpdates := tmtypes.TM2PB.ValidatorUpdates(valSet)

	cs := ibctmtypes.NewClientState(chainID, ibctmtypes.DefaultTrustLevel, trustingPeriod, ubdPeriod, maxClockDrift, height, commitmenttypes.GetSDKSpecs(), upgradePath)
	consensusState := ibctmtypes.NewConsensusState(time.Now(), commitmenttypes.NewMerkleRoot([]byte("apphash")), valHash)

	params := vaastypes.DefaultConsumerParams()
	params.Enabled = true
	wantLen := int((params.SignedBlocksWindow + 7) / 8)

	base := func() *types.GenesisState {
		return types.NewInitialGenesisState(cs, consensusState, valUpdates, params)
	}

	t.Run("correct length is valid", func(t *testing.T) {
		gs := base()
		gs.MissedBlockBitmaps = []types.MissedBlockBitmapEntry{
			{Addr: []byte("validator-addr-one"), Bitmap: make([]byte, wantLen)},
		}
		require.NoError(t, gs.Validate())
	})

	t.Run("undersized bitmap is rejected", func(t *testing.T) {
		gs := base()
		gs.MissedBlockBitmaps = []types.MissedBlockBitmapEntry{
			{Addr: []byte("validator-addr-one"), Bitmap: make([]byte, wantLen-1)},
		}
		err := gs.Validate()
		require.Error(t, err)
		require.Contains(t, err.Error(), "missed block bitmap")
	})

	t.Run("oversized bitmap is rejected", func(t *testing.T) {
		gs := base()
		gs.MissedBlockBitmaps = []types.MissedBlockBitmapEntry{
			{Addr: []byte("validator-addr-one"), Bitmap: make([]byte, wantLen+1)},
		}
		err := gs.Validate()
		require.Error(t, err)
		require.Contains(t, err.Error(), "missed block bitmap")
	})

	t.Run("zero-length bitmap is rejected", func(t *testing.T) {
		gs := base()
		gs.MissedBlockBitmaps = []types.MissedBlockBitmapEntry{
			{Addr: []byte("validator-addr-one"), Bitmap: nil},
		}
		err := gs.Validate()
		require.Error(t, err)
		require.Contains(t, err.Error(), "missed block bitmap")
	})
}

// TestValidateStagedDowntimeParams verifies that GenesisState.Validate
// rejects a StagedDowntimeParams entry that would otherwise bypass the
// keeper's validDowntimeParams filter (InitGenesis sets it directly into the
// collection) and later panic applyStagedDowntimeParams's
// minSigned.MulInt64 call at the next window close.
func TestValidateStagedDowntimeParams(t *testing.T) {
	cId := crypto.NewCryptoIdentityFromIntSeed(238934)
	pubKey := cId.TMCryptoPubKey()

	validator := tmtypes.NewValidator(pubKey, 1)
	valSet := tmtypes.NewValidatorSet([]*tmtypes.Validator{validator})
	valHash := valSet.Hash()
	valUpdates := tmtypes.TM2PB.ValidatorUpdates(valSet)

	cs := ibctmtypes.NewClientState(chainID, ibctmtypes.DefaultTrustLevel, trustingPeriod, ubdPeriod, maxClockDrift, height, commitmenttypes.GetSDKSpecs(), upgradePath)
	consensusState := ibctmtypes.NewConsensusState(time.Now(), commitmenttypes.NewMerkleRoot([]byte("apphash")), valHash)

	params := vaastypes.DefaultConsumerParams()
	params.Enabled = true

	base := func() *types.GenesisState {
		return types.NewInitialGenesisState(cs, consensusState, valUpdates, params)
	}

	t.Run("valid staged downtime params", func(t *testing.T) {
		gs := base()
		gs.StagedDowntimeParams = &vaastypes.DowntimeParams{
			SignedBlocksWindow: 200,
			MinSignedPerWindow: math.LegacyNewDecWithPrec(6, 1),
		}
		require.NoError(t, gs.Validate())
	})

	t.Run("nil MinSignedPerWindow is rejected", func(t *testing.T) {
		gs := base()
		gs.StagedDowntimeParams = &vaastypes.DowntimeParams{
			SignedBlocksWindow: 200,
		}
		err := gs.Validate()
		require.Error(t, err)
		require.Contains(t, err.Error(), "staged downtime params")
	})

	t.Run("non-positive signed blocks window is rejected", func(t *testing.T) {
		gs := base()
		gs.StagedDowntimeParams = &vaastypes.DowntimeParams{
			SignedBlocksWindow: 0,
			MinSignedPerWindow: math.LegacyNewDecWithPrec(6, 1),
		}
		err := gs.Validate()
		require.Error(t, err)
		require.Contains(t, err.Error(), "staged downtime params")
	})

	t.Run("min signed per window out of range is rejected", func(t *testing.T) {
		gs := base()
		gs.StagedDowntimeParams = &vaastypes.DowntimeParams{
			SignedBlocksWindow: 200,
			MinSignedPerWindow: math.LegacyOneDec(),
		}
		err := gs.Validate()
		require.Error(t, err)
		require.Contains(t, err.Error(), "staged downtime params")
	})
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

	params := vaastypes.DefaultConsumerParams()
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
				vaastypes.NewConsumerParams(
					true,
					0,
					vaastypes.DefaultHistoricalEntries,
					vaastypes.DefaultConsumerUnbondingPeriod,
					vaastypes.DefaultSafeModeThreshold,
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

// TestValidatePendingEvidencePackets verifies that GenesisState.Validate
// rejects a PendingEvidencePacketEntry whose bytes would not survive the
// keeper: SendEvidencePackets silently drops any stored entry it cannot
// unmarshal, and the queue is the only remaining copy of the evidence once
// the source window closes.
func TestValidatePendingEvidencePackets(t *testing.T) {
	cId := crypto.NewCryptoIdentityFromIntSeed(238934)
	pubKey := cId.TMCryptoPubKey()

	validator := tmtypes.NewValidator(pubKey, 1)
	valSet := tmtypes.NewValidatorSet([]*tmtypes.Validator{validator})
	valHash := valSet.Hash()
	valUpdates := tmtypes.TM2PB.ValidatorUpdates(valSet)

	cs := ibctmtypes.NewClientState(chainID, ibctmtypes.DefaultTrustLevel, trustingPeriod, ubdPeriod, maxClockDrift, height, commitmenttypes.GetSDKSpecs(), upgradePath)
	consensusState := ibctmtypes.NewConsensusState(time.Now(), commitmenttypes.NewMerkleRoot([]byte("apphash")), valHash)

	params := vaastypes.DefaultConsumerParams()
	params.Enabled = true

	addr := sdk.ConsAddress("validator-addr-evidence-one")
	packet := vaastypes.NewEvidencePacketData(addr, 1, []byte{0xFF, 0x03}, 10, 100, math.LegacyNewDecWithPrec(5, 1))

	base := func() *types.GenesisState {
		return types.NewInitialGenesisState(cs, consensusState, valUpdates, params)
	}

	t.Run("valid pending evidence packet", func(t *testing.T) {
		gs := base()
		gs.PendingEvidencePackets = []types.PendingEvidencePacketEntry{
			{Addr: addr, Packet: packet.GetBytes()},
		}
		require.NoError(t, gs.Validate())
	})

	t.Run("empty addr is rejected", func(t *testing.T) {
		gs := base()
		gs.PendingEvidencePackets = []types.PendingEvidencePacketEntry{
			{Addr: nil, Packet: packet.GetBytes()},
		}
		err := gs.Validate()
		require.Error(t, err)
		require.Contains(t, err.Error(), "pending evidence packet")
	})

	t.Run("non-JSON packet bytes are rejected", func(t *testing.T) {
		gs := base()
		gs.PendingEvidencePackets = []types.PendingEvidencePacketEntry{
			{Addr: addr, Packet: []byte("{not json")},
		}
		err := gs.Validate()
		require.Error(t, err)
		require.Contains(t, err.Error(), "pending evidence packet")
	})

	t.Run("packet failing EvidencePacketData.Validate is rejected", func(t *testing.T) {
		gs := base()
		bad := packet
		bad.WindowStartHeight = 0
		gs.PendingEvidencePackets = []types.PendingEvidencePacketEntry{
			{Addr: addr, Packet: bad.GetBytes()},
		}
		err := gs.Validate()
		require.Error(t, err)
		require.Contains(t, err.Error(), "pending evidence packet")
	})

	t.Run("addr mismatching the packet's validator addr is rejected", func(t *testing.T) {
		gs := base()
		gs.PendingEvidencePackets = []types.PendingEvidencePacketEntry{
			{Addr: sdk.ConsAddress("validator-addr-evidence-two"), Packet: packet.GetBytes()},
		}
		err := gs.Validate()
		require.Error(t, err)
		require.Contains(t, err.Error(), "does not match packet validator addr")
	})
}
