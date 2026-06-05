package types_test

import (
	"testing"
	"time"

	"cosmossdk.io/math"

	tmtypes "github.com/cometbft/cometbft/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	clienttypes "github.com/cosmos/ibc-go/v10/modules/core/02-client/types"
	commitmenttypes "github.com/cosmos/ibc-go/v10/modules/core/23-commitment/types"
	ibctmtypes "github.com/cosmos/ibc-go/v10/modules/light-clients/07-tendermint"
	"github.com/stretchr/testify/require"

	"github.com/allinbits/vaas/testutil/crypto"
	"github.com/allinbits/vaas/x/vaas/provider/types"
	vaastypes "github.com/allinbits/vaas/x/vaas/types"
)

func TestValidateGenesisState(t *testing.T) {
	// minimal init params required for LAUNCHED/STOPPED/DELETED consumer states
	testInitParams := &types.ConsumerInitializationParameters{
		InitialHeight:     clienttypes.Height{RevisionNumber: 1, RevisionHeight: 1},
		GenesisHash:       []byte("g"),
		BinaryHash:        []byte("b"),
		SpawnTime:         time.Unix(100, 0).UTC(),
		UnbondingPeriod:   time.Hour,
		VaasTimeoutPeriod: time.Hour,
		HistoricalEntries: 10,
	}
	launchedCS := func(consumerID uint64, chainID, clientID string, preVAAS bool) types.ConsumerState {
		return types.ConsumerState{
			ConsumerId:      consumerID,
			ChainId:         chainID,
			ClientId:        clientID,
			Phase:           types.CONSUMER_PHASE_LAUNCHED,
			OwnerAddress:    sdk.AccAddress([]byte("vaas-test-owner-1234")).String(),
			InitParams:      testInitParams,
			ConsumerGenesis: getInitialConsumerGenesis(t, chainID, preVAAS),
		}
	}

	testCases := []struct {
		name     string
		genState *types.GenesisState
		expPass  bool
	}{
		{
			"valid initializing provider genesis with nil updates",
			types.NewGenesisState(
				types.DefaultValsetUpdateID,
				nil,
				[]types.ConsumerState{launchedCS(0, "chainid-1", "client-id", false)},
				types.DefaultParams(),
				nil,
				nil,
				nil,
				nil,
				nil,
			),
			true,
		},
		{
			"valid multiple provider genesis with multiple consumer chains",
			types.NewGenesisState(
				types.DefaultValsetUpdateID,
				nil,
				[]types.ConsumerState{
					launchedCS(0, "chainid-1", "client-id", false),
					launchedCS(1, "chainid-2", "client-id", true),
					launchedCS(2, "chainid-3", "client-id", false),
					launchedCS(3, "chainid-4", "client-id", true),
				},
				types.DefaultParams(),
				nil,
				nil,
				nil,
				nil,
				nil,
			),
			true,
		},
		{
			"valid provider genesis with custom params",
			types.NewGenesisState(
				types.DefaultValsetUpdateID,
				nil,
				[]types.ConsumerState{launchedCS(0, "chainid-1", "client-id", false)},
				types.NewParams(
					types.DefaultTrustingPeriodFraction, time.Hour, 600, 180, math.NewInt(42), types.DefaultMinDepositBlocks),
				nil,
				nil,
				nil,
				nil,
				nil,
			),
			true,
		},
		{
			"invalid zero valset update ID",
			types.NewGenesisState(
				0,
				nil,
				nil,
				types.DefaultParams(),
				nil,
				nil,
				nil,
				nil,
				nil,
			),
			false,
		},
		{
			"invalid valset ID to block height mapping",
			types.NewGenesisState(
				types.DefaultValsetUpdateID,
				[]types.ValsetUpdateIdToHeight{{ValsetUpdateId: 0}},
				nil,
				types.DefaultParams(),
				nil,
				nil,
				nil,
				nil,
				nil,
			),
			false,
		},
		{
			"invalid params, zero trusting period fraction",
			types.NewGenesisState(
				types.DefaultValsetUpdateID,
				nil,
				[]types.ConsumerState{launchedCS(0, "chainid-1", "client-id", false)},
				types.NewParams(
					"0.0", // 0 trusting period fraction here
					vaastypes.DefaultVAASTimeoutPeriod, 600, 180, math.NewInt(42), types.DefaultMinDepositBlocks),
				nil,
				nil,
				nil,
				nil,
				nil,
			),
			false,
		},
		{
			"invalid params, zero VAAS timeout",
			types.NewGenesisState(
				types.DefaultValsetUpdateID,
				nil,
				[]types.ConsumerState{launchedCS(0, "chainid-1", "client-id", false)},
				types.NewParams(
					types.DefaultTrustingPeriodFraction,
					0, // 0 ccv timeout here
					600, 180, math.NewInt(42), types.DefaultMinDepositBlocks),
				nil,
				nil,
				nil,
				nil,
				nil,
			),
			false,
		},
		{
			"empty consumer state chain id",
			types.NewGenesisState(
				types.DefaultValsetUpdateID,
				nil,
				[]types.ConsumerState{{ChainId: "", ClientId: "client-id"}},
				types.DefaultParams(),
				nil,
				nil,
				nil,
				nil,
				nil,
			),
			false,
		},
		{
			"valid consumer state with client id",
			types.NewGenesisState(
				types.DefaultValsetUpdateID,
				nil,
				[]types.ConsumerState{launchedCS(0, "chainid", "abc", false)},
				types.DefaultParams(),
				nil,
				nil,
				nil,
				nil,
				nil,
			),
			true,
		},
		{
			"invalid consumer state pending VSC packets",
			types.NewGenesisState(
				types.DefaultValsetUpdateID,
				nil,
				[]types.ConsumerState{func() types.ConsumerState {
					cs := launchedCS(0, "chainid", "client-id", false)
					cs.PendingValsetChanges = []vaastypes.ValidatorSetChangePacketData{{}} // ValsetUpdateId=0
					return cs
				}()},
				types.DefaultParams(),
				nil,
				nil,
				nil,
				nil,
				nil,
			),
			false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.genState.Validate()

			if tc.expPass {
				require.NoError(t, err, "test case: %s must pass", tc.name)
			} else {
				require.Error(t, err, "test case: %s must fail", tc.name)
			}
		})
	}
}

func TestValidateGenesisState_FeePoolShares(t *testing.T) {
	alice := sdk.AccAddress([]byte("alice___________")).String()
	bob := sdk.AccAddress([]byte("bob_____________")).String()
	cs := types.ConsumerState{
		ConsumerId:   0,
		ChainId:      "chain-1",
		Phase:        types.CONSUMER_PHASE_REGISTERED,
		OwnerAddress: sdk.AccAddress([]byte("vaas-test-owner-1234")).String(),
	}

	build := func(shares ...types.ConsumerFeePoolShare) *types.GenesisState {
		gs := types.NewGenesisState(
			types.DefaultValsetUpdateID, nil,
			[]types.ConsumerState{cs},
			types.DefaultParams(),
			nil, nil, nil, nil,
			shares,
		)
		return gs
	}

	t.Run("valid share record", func(t *testing.T) {
		err := build(types.ConsumerFeePoolShare{
			ConsumerId: 0, Depositor: alice, Denom: "uphoton",
			Shares: math.NewInt(100),
		}).Validate()
		require.NoError(t, err)
	})

	t.Run("invalid depositor bech32", func(t *testing.T) {
		err := build(types.ConsumerFeePoolShare{
			ConsumerId: 0, Depositor: "not-a-bech32", Denom: "uphoton",
			Shares: math.NewInt(100),
		}).Validate()
		require.Error(t, err)
	})

	t.Run("invalid denom", func(t *testing.T) {
		err := build(types.ConsumerFeePoolShare{
			ConsumerId: 0, Depositor: alice, Denom: "",
			Shares: math.NewInt(100),
		}).Validate()
		require.Error(t, err)
	})

	t.Run("zero shares", func(t *testing.T) {
		err := build(types.ConsumerFeePoolShare{
			ConsumerId: 0, Depositor: alice, Denom: "uphoton",
			Shares: math.ZeroInt(),
		}).Validate()
		require.Error(t, err)
	})

	t.Run("negative shares", func(t *testing.T) {
		err := build(types.ConsumerFeePoolShare{
			ConsumerId: 0, Depositor: alice, Denom: "uphoton",
			Shares: math.NewInt(-1),
		}).Validate()
		require.Error(t, err)
	})

	t.Run("orphan consumer id", func(t *testing.T) {
		err := build(types.ConsumerFeePoolShare{
			ConsumerId: 99, Depositor: alice, Denom: "uphoton",
			Shares: math.NewInt(100),
		}).Validate()
		require.Error(t, err)
	})

	t.Run("duplicate triple", func(t *testing.T) {
		err := build(
			types.ConsumerFeePoolShare{
				ConsumerId: 0, Depositor: alice, Denom: "uphoton",
				Shares: math.NewInt(50),
			},
			types.ConsumerFeePoolShare{
				ConsumerId: 0, Depositor: alice, Denom: "uphoton",
				Shares: math.NewInt(50),
			},
		).Validate()
		require.Error(t, err)
	})

	t.Run("two depositors same denom is allowed", func(t *testing.T) {
		err := build(
			types.ConsumerFeePoolShare{
				ConsumerId: 0, Depositor: alice, Denom: "uphoton",
				Shares: math.NewInt(60),
			},
			types.ConsumerFeePoolShare{
				ConsumerId: 0, Depositor: bob, Denom: "uphoton",
				Shares: math.NewInt(40),
			},
		).Validate()
		require.NoError(t, err)
	})
}

func TestConsumerStateValidatePerPhase(t *testing.T) {
	validMetadata := types.ConsumerMetadata{Name: "n", Description: "d", Metadata: "m"}
	validInit := &types.ConsumerInitializationParameters{
		InitialHeight:     clienttypes.Height{RevisionNumber: 1, RevisionHeight: 1},
		GenesisHash:       []byte("g"),
		BinaryHash:        []byte("b"),
		SpawnTime:         time.Unix(100, 0).UTC(),
		UnbondingPeriod:   time.Hour,
		VaasTimeoutPeriod: time.Hour,
		HistoricalEntries: 10,
	}
	rt := time.Unix(200, 0).UTC()

	base := func(phase types.ConsumerPhase) types.ConsumerState {
		return types.ConsumerState{
			ConsumerId:      0,
			ChainId:         "test-consumer",
			Phase:           phase,
			OwnerAddress:    sdk.AccAddress([]byte("vaas-test-owner-1234")).String(),
			ConsumerGenesis: *vaastypes.DefaultConsumerGenesisState(),
		}
	}

	cases := []struct {
		name    string
		mutate  func(*types.ConsumerState)
		wantErr string // "" means valid
	}{
		// REGISTERED: only chain_id + owner required.
		{"REGISTERED valid", func(cs *types.ConsumerState) { *cs = base(types.CONSUMER_PHASE_REGISTERED) }, ""},
		{"REGISTERED empty owner", func(cs *types.ConsumerState) {
			*cs = base(types.CONSUMER_PHASE_REGISTERED)
			cs.OwnerAddress = ""
		}, "owner address"},
		{"REGISTERED invalid owner bech32", func(cs *types.ConsumerState) {
			*cs = base(types.CONSUMER_PHASE_REGISTERED)
			cs.OwnerAddress = "cosmos1notavalidchecksum"
		}, "invalid owner address"},
		{"REGISTERED with stray client_id", func(cs *types.ConsumerState) {
			*cs = base(types.CONSUMER_PHASE_REGISTERED)
			cs.ClientId = "07-tendermint-0"
		}, "client id must be empty"},

		// INITIALIZED: requires init_params; client_id absent.
		{"INITIALIZED valid", func(cs *types.ConsumerState) {
			*cs = base(types.CONSUMER_PHASE_INITIALIZED)
			cs.InitParams = validInit
		}, ""},
		{"INITIALIZED missing init_params", func(cs *types.ConsumerState) {
			*cs = base(types.CONSUMER_PHASE_INITIALIZED)
		}, "init params required"},

		// LAUNCHED: requires init_params + client_id + non-default consumer_genesis.
		{"LAUNCHED valid", func(cs *types.ConsumerState) {
			*cs = base(types.CONSUMER_PHASE_LAUNCHED)
			cs.InitParams = validInit
			cs.ClientId = "07-tendermint-0"
			cs.ConsumerGenesis = nonDefaultConsumerGenesis()
		}, ""},
		{"LAUNCHED missing client_id", func(cs *types.ConsumerState) {
			*cs = base(types.CONSUMER_PHASE_LAUNCHED)
			cs.InitParams = validInit
			cs.ConsumerGenesis = nonDefaultConsumerGenesis()
		}, "client id"},

		// STOPPED: LAUNCHED requirements + removal_time.
		{"STOPPED valid", func(cs *types.ConsumerState) {
			*cs = base(types.CONSUMER_PHASE_STOPPED)
			cs.InitParams = validInit
			cs.ClientId = "07-tendermint-0"
			cs.ConsumerGenesis = nonDefaultConsumerGenesis()
			cs.RemovalTime = &rt
		}, ""},
		{"STOPPED missing removal_time", func(cs *types.ConsumerState) {
			*cs = base(types.CONSUMER_PHASE_STOPPED)
			cs.InitParams = validInit
			cs.ClientId = "07-tendermint-0"
			cs.ConsumerGenesis = nonDefaultConsumerGenesis()
		}, "removal time"},

		// DELETED: chain_id + owner + init_params + metadata preserved; everything else cleared.
		{"DELETED valid", func(cs *types.ConsumerState) {
			*cs = base(types.CONSUMER_PHASE_DELETED)
			cs.InitParams = validInit
			cs.Metadata = &validMetadata
			cs.ConsumerGenesis = vaastypes.ConsumerGenesisState{} // cleared
		}, ""},
		{"DELETED missing metadata", func(cs *types.ConsumerState) {
			*cs = base(types.CONSUMER_PHASE_DELETED)
			cs.InitParams = validInit
			cs.ConsumerGenesis = vaastypes.ConsumerGenesisState{}
		}, "metadata required"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cs := types.ConsumerState{}
			tc.mutate(&cs)
			err := cs.Validate()
			if tc.wantErr == "" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
				require.Contains(t, err.Error(), tc.wantErr)
			}
		})
	}
}

func nonDefaultConsumerGenesis() vaastypes.ConsumerGenesisState {
	gs := vaastypes.DefaultConsumerGenesisState()
	gs.NewChain = true
	return *gs
}

func getInitialConsumerGenesis(t *testing.T, chainID string, preVAAS bool) vaastypes.ConsumerGenesisState {
	t.Helper()
	cId := crypto.NewCryptoIdentityFromIntSeed(239668)
	pubKey := cId.TMCryptoPubKey()

	validator := tmtypes.NewValidator(pubKey, 1)
	valSet := tmtypes.NewValidatorSet([]*tmtypes.Validator{validator})
	valHash := valSet.Hash()
	valUpdates := tmtypes.TM2PB.ValidatorUpdates(valSet)

	var clientState *ibctmtypes.ClientState = nil
	var consensusState *ibctmtypes.ConsensusState = nil

	if preVAAS {
		// no client state needed for pre-VAAS
	} else {
		clientState = ibctmtypes.NewClientState(
			chainID,
			ibctmtypes.DefaultTrustLevel,
			time.Duration(1),
			time.Duration(2),
			time.Duration(1),
			clienttypes.Height{RevisionNumber: clienttypes.ParseChainID(chainID), RevisionHeight: 1},
			commitmenttypes.GetSDKSpecs(),
			[]string{"upgrade", "upgradedIBCState"})
		consensusState = ibctmtypes.NewConsensusState(time.Now(), commitmenttypes.NewMerkleRoot([]byte("apphash")), valHash)
	}

	params := vaastypes.DefaultConsumerParams()
	params.Enabled = true

	return *vaastypes.NewInitialConsumerGenesisState(clientState, consensusState, valUpdates, preVAAS, params)
}
