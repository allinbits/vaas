package types_test

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/cometbft/cometbft/crypto/ed25519"
	tmproto "github.com/cometbft/cometbft/proto/tendermint/types"

	clienttypes "github.com/cosmos/ibc-go/v10/modules/core/02-client/types"
	channeltypesv2 "github.com/cosmos/ibc-go/v10/modules/core/04-channel/v2/types"
	ibctmtypes "github.com/cosmos/ibc-go/v10/modules/light-clients/07-tendermint"

	"cosmossdk.io/math"

	sdk "github.com/cosmos/cosmos-sdk/types"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	govtypes "github.com/cosmos/cosmos-sdk/x/gov/types"

	cryptoutil "github.com/allinbits/vaas/testutil/crypto"
	"github.com/allinbits/vaas/x/vaas/provider/types"
	vaastypes "github.com/allinbits/vaas/x/vaas/types"
)

func TestValidateStringField(t *testing.T) {
	testCases := []struct {
		name      string
		field     string
		maxLength int
		valid     bool
	}{
		{
			name:      "invalid: empty",
			field:     "",
			maxLength: 5,
			valid:     false,
		},
		{
			name:      "invalid: too long",
			field:     "this field is too long",
			maxLength: 5,
			valid:     false,
		},
		{
			name:      "valid",
			field:     "valid",
			maxLength: 5,
			valid:     true,
		},
	}

	for _, tc := range testCases {
		err := types.ValidateStringField(tc.name, tc.field, tc.maxLength)
		if tc.valid {
			require.NoError(t, err, tc.name)
		} else {
			require.Error(t, err, tc.name)
		}
	}
}

func TestTruncateString(t *testing.T) {
	testCases := []struct {
		str       string
		maxLength int
		expStr    string
	}{
		{"drink", 3, "dri"},
		{"drink", 6, "drink"},
		{"drink", 0, ""},
		{"drink", -1, ""},
		{"drink", 100, "drink"},
		{"pub", 100, "pub"},
		{"こんにちは", 3, "こんに"},
	}

	for _, tc := range testCases {
		truncated := types.TruncateString(tc.str, tc.maxLength)
		require.Equal(t, tc.expStr, truncated)
	}
}

func TestValidateConsumerMetadata(t *testing.T) {
	generateLongString := func(length int) string {
		result := make([]byte, length)
		for i := range result {
			result[i] = byte('a')
		}
		return string(result)
	}

	testCases := []struct {
		name     string
		metadata types.ConsumerMetadata
		valid    bool
	}{
		{
			name: "valid",
			metadata: types.ConsumerMetadata{
				Name:        "name",
				Description: "description",
				Metadata:    "metadata",
			},
			valid: true,
		},
		{
			name: "valid with long strings",
			metadata: types.ConsumerMetadata{
				Name:        generateLongString(types.MaxNameLength),
				Description: generateLongString(types.MaxDescriptionLength),
				Metadata:    generateLongString(types.MaxMetadataLength),
			},
			valid: true,
		},
		{
			name: "invalid name",
			metadata: types.ConsumerMetadata{
				Name:        generateLongString(types.MaxNameLength + 1),
				Description: "description",
				Metadata:    "metadata",
			},
			valid: false,
		},
		{
			name: "invalid description",
			metadata: types.ConsumerMetadata{
				Name:        "name",
				Description: generateLongString(types.MaxDescriptionLength + 1),
				Metadata:    "metadata",
			},
			valid: false,
		},
		{
			name: "invalid metadata",
			metadata: types.ConsumerMetadata{
				Name:        "name",
				Description: "description",
				Metadata:    generateLongString(types.MaxMetadataLength + 1),
			},
			valid: false,
		},
	}

	for _, tc := range testCases {
		err := types.ValidateConsumerMetadata(tc.metadata)
		if tc.valid {
			require.NoError(t, err, tc.name)
		} else {
			require.Error(t, err, tc.name)
		}
	}
}

func TestValidateInitializationParameters(t *testing.T) {
	now := time.Now().UTC()
	coolStr := "Cosmos Hub is the best place to launch a chain. Interchain Security is awesome."
	tooLongHash := []byte(coolStr)

	testCases := []struct {
		name   string
		params types.ConsumerInitializationParameters
		valid  bool
	}{
		{
			name: "valid",
			params: types.ConsumerInitializationParameters{
				InitialHeight:     clienttypes.NewHeight(3, 4),
				GenesisHash:       []byte{0x01},
				BinaryHash:        []byte{0x01},
				SpawnTime:         now,
				UnbondingPeriod:   time.Duration(100000000000),
				VaasTimeoutPeriod: 15 * time.Minute,
				HistoricalEntries: 10000,
				SafeModeThreshold: vaastypes.DefaultSafeModeThreshold,
			},
			valid: true,
		},
		{
			name: "invalid - zero height",
			params: types.ConsumerInitializationParameters{
				InitialHeight:     clienttypes.ZeroHeight(),
				GenesisHash:       []byte{0x01},
				BinaryHash:        []byte{0x01},
				SpawnTime:         now,
				UnbondingPeriod:   time.Duration(100000000000),
				VaasTimeoutPeriod: 15 * time.Minute,
				HistoricalEntries: 10000,
				SafeModeThreshold: vaastypes.DefaultSafeModeThreshold,
			},
			valid: false,
		},
		{
			name: "invalid - hash too long",
			params: types.ConsumerInitializationParameters{
				InitialHeight:     clienttypes.NewHeight(3, 4),
				GenesisHash:       tooLongHash,
				BinaryHash:        []byte{0x01},
				SpawnTime:         now,
				UnbondingPeriod:   time.Duration(100000000000),
				VaasTimeoutPeriod: 15 * time.Minute,
				HistoricalEntries: 10000,
				SafeModeThreshold: vaastypes.DefaultSafeModeThreshold,
			},
			valid: false,
		},
		{
			name: "invalid - zero spawn time",
			params: types.ConsumerInitializationParameters{
				InitialHeight:     clienttypes.NewHeight(3, 4),
				GenesisHash:       []byte{0x01},
				BinaryHash:        []byte{0x01},
				SpawnTime:         time.Time{},
				UnbondingPeriod:   time.Duration(100000000000),
				VaasTimeoutPeriod: 15 * time.Minute,
				HistoricalEntries: 10000,
				SafeModeThreshold: vaastypes.DefaultSafeModeThreshold,
			},
			valid: true,
		},
		{
			name: "invalid - zero duration",
			params: types.ConsumerInitializationParameters{
				InitialHeight:     clienttypes.NewHeight(3, 4),
				GenesisHash:       []byte{0x01},
				BinaryHash:        []byte{0x01},
				SpawnTime:         now,
				UnbondingPeriod:   0,
				VaasTimeoutPeriod: 15 * time.Minute,
				HistoricalEntries: 10000,
				SafeModeThreshold: vaastypes.DefaultSafeModeThreshold,
			},
			valid: false,
		},
		{
			name: "invalid - HistoricalEntries zero",
			params: types.ConsumerInitializationParameters{
				InitialHeight:     clienttypes.NewHeight(3, 4),
				GenesisHash:       []byte{0x01},
				BinaryHash:        []byte{0x01},
				SpawnTime:         now,
				UnbondingPeriod:   time.Duration(100000000000),
				VaasTimeoutPeriod: 15 * time.Minute,
				HistoricalEntries: 0,
				SafeModeThreshold: vaastypes.DefaultSafeModeThreshold,
			},
			valid: false,
		},
		{
			name: "invalid - zero safe mode threshold",
			params: types.ConsumerInitializationParameters{
				InitialHeight:     clienttypes.NewHeight(3, 4),
				GenesisHash:       []byte{0x01},
				BinaryHash:        []byte{0x01},
				SpawnTime:         now,
				UnbondingPeriod:   time.Duration(100000000000),
				VaasTimeoutPeriod: 15 * time.Minute,
				HistoricalEntries: 10000,
				SafeModeThreshold: 0,
			},
			valid: false,
		},
	}

	for _, tc := range testCases {
		err := types.ValidateInitializationParameters(tc.params)
		if tc.valid {
			require.NoError(t, err, tc.name)
		} else {
			require.Error(t, err, tc.name)
		}
	}
}

func TestValidateByteSlice(t *testing.T) {
	testCases := []struct {
		name      string
		slice     []byte
		maxLength int
		valid     bool
	}{
		{
			name:      "valid: empty",
			slice:     []byte{},
			maxLength: 5,
			valid:     true,
		},
		{
			name:      "invalid: too long",
			slice:     []byte{0x01, 0x02},
			maxLength: 1,
			valid:     false,
		},
		{
			name:      "valid",
			slice:     []byte{0x01, 0x02},
			maxLength: 5,
			valid:     true,
		},
	}

	for _, tc := range testCases {
		err := types.ValidateByteSlice(tc.slice, tc.maxLength)
		if tc.valid {
			require.NoError(t, err, tc.name)
		} else {
			require.Error(t, err, tc.name)
		}
	}
}

func TestMsgCreateConsumerValidateBasic(t *testing.T) {
	testCases := []struct {
		name    string
		chainId string
		expPass bool
	}{
		{
			"empty chain id",
			"",
			false,
		},
		{
			"empty chain id after trimming",
			"   	",
			false,
		},
		{
			"valid chain id",
			"somechain-1",
			true,
		},
	}

	for _, tc := range testCases {
		validConsumerMetadata := types.ConsumerMetadata{Name: "name", Description: "description", Metadata: "metadata"}
		msg, err := types.NewMsgCreateConsumer("submitter", tc.chainId, validConsumerMetadata, nil)
		require.NoError(t, err)
		err = msg.ValidateBasic()
		if tc.expPass {
			require.NoError(t, err, "valid case: %s should not return error. got %w", tc.name, err)
		} else {
			require.Error(t, err, "invalid case: '%s' must return error but got none", tc.name)
		}
	}
}

func TestMsgUpdateConsumerValidateBasic(t *testing.T) {
	testCases := []struct {
		name       string
		newChainId string
		expPass    bool
	}{
		{
			"success",
			"validchainid-0",
			true,
		},
	}

	for _, tc := range testCases {
		msg, _ := types.NewMsgUpdateConsumer("", 0, "cosmos1p3ucd3ptpw902fluyjzhq3ffgq4ntddac9sa3s", nil, nil, tc.newChainId, "")
		err := msg.ValidateBasic()
		if tc.expPass {
			require.NoError(t, err, "valid case: %s should not return error. got %w", tc.name, err)
		} else {
			require.Error(t, err, "invalid case: '%s' must return error but got none", tc.name)
		}
	}
}

func TestMsgAssignConsumerKeyValidateBasic(t *testing.T) {
	cId1 := cryptoutil.NewCryptoIdentityFromIntSeed(35443543534)
	cId2 := cryptoutil.NewCryptoIdentityFromIntSeed(65465464564)

	valOpAddr1 := cId1.SDKValOpAddress()
	acc1 := sdk.AccAddress(valOpAddr1.Bytes()).String()
	acc2 := sdk.AccAddress(cId2.SDKValOpAddress().Bytes()).String()

	longChainId := "abcdefghijklmnopqrstuvwxyz"
	for i := 0; i < 3; i++ {
		longChainId += longChainId
	}

	testCases := []struct {
		name         string
		providerAddr string
		signer       string
		consumerKey  string
		consumerId   uint64
		expErr       bool
	}{
		{
			name:       "invalid: provider address is empty",
			consumerId: 1,
			expErr:     true,
		},
		{
			name:         "invalid: provider address is invalid",
			consumerId:   1,
			providerAddr: "some address",
			expErr:       true,
		},
		{
			name:         "invalid: provider address != submitter address",
			consumerId:   1,
			providerAddr: valOpAddr1.String(),
			signer:       acc2,
			expErr:       true,
		},
		{
			name:         "invalid: consumer pubkey empty",
			consumerId:   1,
			providerAddr: valOpAddr1.String(),
			signer:       acc1,
			expErr:       true,
		},
		{
			name:         "valid",
			consumerId:   1,
			providerAddr: valOpAddr1.String(),
			signer:       acc1,
			consumerKey:  "{\"@type\": \"/cosmos.crypto.ed25519.PubKey\", \"key\": \"e3BehnEIlGUAnJYn9V8gBXuMh4tXO8xxlxyXD1APGyk=\"}",
			expErr:       false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			msg := types.MsgAssignConsumerKey{
				ConsumerId:   tc.consumerId,
				ConsumerKey:  tc.consumerKey,
				ProviderAddr: tc.providerAddr,
				Signer:       tc.signer,
			}

			err := msg.ValidateBasic()
			if tc.expErr {
				require.Error(t, err, tc.name)
			} else {
				require.NoError(t, err, tc.name)
			}
		})
	}
}

func TestValidateInitialHeight(t *testing.T) {
	testCases := []struct {
		name          string
		chainId       string
		initialHeight clienttypes.Height
		expPass       bool
	}{
		{
			name:    "valid with revision number",
			chainId: "chain-1",
			initialHeight: clienttypes.Height{
				RevisionNumber: 1,
				RevisionHeight: 0,
			},
			expPass: true,
		},
		{
			name:    "valid without revision number",
			chainId: "chain",
			initialHeight: clienttypes.Height{
				RevisionNumber: 0,
				RevisionHeight: 0,
			},
			expPass: true,
		},
		{
			name:    "invalid without revision number",
			chainId: "chain",
			initialHeight: clienttypes.Height{
				RevisionNumber: 1,
				RevisionHeight: 0,
			},
			expPass: false,
		},
		{
			name:    "invalid without revision number",
			chainId: "chain-1",
			initialHeight: clienttypes.Height{
				RevisionNumber: 0,
				RevisionHeight: 0,
			},
			expPass: false,
		},
		{
			name:    "valid: evmos-like chain IDs",
			chainId: "evmos_9001-2",
			initialHeight: clienttypes.Height{
				RevisionNumber: 2,
				RevisionHeight: 0,
			},
			expPass: true,
		},
	}
	for _, tc := range testCases {
		err := types.ValidateInitialHeight(tc.initialHeight, tc.chainId)
		if tc.expPass {
			require.NoError(t, err, "valid case: '%s' should not return error. got %w", tc.name, err)
		} else {
			require.Error(t, err, "invalid case: '%s' must return error but got none", tc.name)
		}
	}
}

func TestMsgFundConsumerFeePool_ValidateBasic(t *testing.T) {
	validSigner := sdk.AccAddress([]byte("alice___________")).String()
	tests := []struct {
		name    string
		msg     types.MsgFundConsumerFeePool
		wantErr bool
	}{
		{"valid", types.MsgFundConsumerFeePool{
			Signer:     validSigner,
			ConsumerId: 0,
			Amount:     sdk.NewInt64Coin("uphoton", 100),
		}, false},
		{"invalid signer", types.MsgFundConsumerFeePool{
			Signer:     "not-bech32",
			ConsumerId: 0,
			Amount:     sdk.NewInt64Coin("uphoton", 100),
		}, true},
		{"zero amount", types.MsgFundConsumerFeePool{
			Signer:     validSigner,
			ConsumerId: 0,
			Amount:     sdk.NewInt64Coin("uphoton", 0),
		}, true},
		{"invalid denom", types.MsgFundConsumerFeePool{
			Signer:     validSigner,
			ConsumerId: 0,
			Amount:     sdk.Coin{Denom: "", Amount: math.NewInt(100)},
		}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.msg.ValidateBasic()
			if tc.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestMsgWithdrawConsumerFeePool_ValidateBasic(t *testing.T) {
	validSigner := sdk.AccAddress([]byte("alice___________")).String()
	tests := []struct {
		name    string
		msg     types.MsgWithdrawConsumerFeePool
		wantErr bool
	}{
		{"valid", types.MsgWithdrawConsumerFeePool{
			Signer:     validSigner,
			ConsumerId: 0,
			Amount:     sdk.NewCoins(sdk.NewInt64Coin("uphoton", 100)),
		}, false},
		{"invalid signer", types.MsgWithdrawConsumerFeePool{
			Signer:     "not-bech32",
			ConsumerId: 0,
			Amount:     sdk.NewCoins(sdk.NewInt64Coin("uphoton", 100)),
		}, true},
		{"empty coins", types.MsgWithdrawConsumerFeePool{
			Signer:     validSigner,
			ConsumerId: 0,
			Amount:     sdk.Coins{},
		}, true},
		{"coins with zero amount", types.MsgWithdrawConsumerFeePool{
			Signer:     validSigner,
			ConsumerId: 0,
			Amount:     sdk.Coins{sdk.NewInt64Coin("uphoton", 0)},
		}, true},
		{"coins with duplicate denom", types.MsgWithdrawConsumerFeePool{
			Signer:     validSigner,
			ConsumerId: 0,
			Amount:     sdk.Coins{sdk.NewInt64Coin("uphoton", 50), sdk.NewInt64Coin("uphoton", 50)},
		}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.msg.ValidateBasic()
			if tc.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestMsgSweepConsumerFeePool_ValidateBasic(t *testing.T) {
	validSigner := sdk.AccAddress([]byte("alice___________")).String()
	tests := []struct {
		name    string
		msg     types.MsgSweepConsumerFeePool
		wantErr bool
	}{
		{"valid with explicit denoms", types.MsgSweepConsumerFeePool{
			Signer:     validSigner,
			ConsumerId: 0,
			Denoms:     []string{"uphoton", "uatom"},
		}, false},
		{"valid with empty denoms", types.MsgSweepConsumerFeePool{
			Signer:     validSigner,
			ConsumerId: 0,
			Denoms:     []string{},
		}, false},
		{"invalid signer", types.MsgSweepConsumerFeePool{
			Signer:     "not-bech32",
			ConsumerId: 0,
			Denoms:     []string{"uphoton"},
		}, true},
		{"invalid denom string", types.MsgSweepConsumerFeePool{
			Signer:     validSigner,
			ConsumerId: 0,
			Denoms:     []string{"INVALID DENOM WITH SPACES"},
		}, true},
		{"duplicate denom", types.MsgSweepConsumerFeePool{
			Signer:     validSigner,
			ConsumerId: 0,
			Denoms:     []string{"uphoton", "uphoton"},
		}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.msg.ValidateBasic()
			if tc.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestMsgRemoveConsumer_ValidateBasic(t *testing.T) {
	tests := []struct {
		name    string
		msg     types.MsgRemoveConsumer
		wantErr bool
	}{
		{"valid", types.MsgRemoveConsumer{
			Signer:     sdk.AccAddress([]byte("alice___________")).String(),
			ConsumerId: 0,
		}, false},
		{"invalid signer", types.MsgRemoveConsumer{
			Signer:     "not-bech32",
			ConsumerId: 0,
		}, true},
		{"empty signer", types.MsgRemoveConsumer{
			ConsumerId: 0,
		}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.msg.ValidateBasic()
			if tc.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestValidateChainId(t *testing.T) {
	testCases := []struct {
		name    string
		chainId string
		expPass bool
	}{
		{
			name:    "valid chain id",
			chainId: "chain-1",
			expPass: true,
		},
		{
			name:    "valid chain id with no revision",
			chainId: "chainId",
			expPass: true,
		},
		{
			name:    "invalid (too long) chain id",
			chainId: strings.Repeat("thisIsAnExtremelyLongChainId", 2),
			expPass: false,
		},
		{
			name:    "empty chain id",
			chainId: "    ",
			expPass: false,
		},
	}
	for _, tc := range testCases {
		err := types.ValidateChainId("ChainId", tc.chainId)
		if tc.expPass {
			require.NoError(t, err, "valid case: '%s' should not return error. got %w", tc.name, err)
		} else {
			require.Error(t, err, "invalid case: '%s' must return error but got none", tc.name)
		}
	}
}

func TestMsgSetConsumerFeesPerBlock_ValidateBasic(t *testing.T) {
	validAuthority := authtypes.NewModuleAddress(govtypes.ModuleName).String()
	cases := []struct {
		name      string
		msg       types.MsgSetConsumerFeesPerBlock
		wantError bool
	}{
		{
			name: "empty amount (clear) is valid",
			msg: types.MsgSetConsumerFeesPerBlock{
				Authority:  validAuthority,
				ConsumerId: 1,
				Amount:     "",
			},
			wantError: false,
		},
		{
			name: "positive amount is valid",
			msg: types.MsgSetConsumerFeesPerBlock{
				Authority:  validAuthority,
				ConsumerId: 1,
				Amount:     "2500",
			},
			wantError: false,
		},
		{
			name: "zero amount rejected",
			msg: types.MsgSetConsumerFeesPerBlock{
				Authority:  validAuthority,
				ConsumerId: 1,
				Amount:     "0",
			},
			wantError: true,
		},
		{
			name: "negative amount rejected",
			msg: types.MsgSetConsumerFeesPerBlock{
				Authority:  validAuthority,
				ConsumerId: 1,
				Amount:     "-1",
			},
			wantError: true,
		},
		{
			name: "garbage amount rejected",
			msg: types.MsgSetConsumerFeesPerBlock{
				Authority:  validAuthority,
				ConsumerId: 1,
				Amount:     "twopointfive",
			},
			wantError: true,
		},
		{
			name: "empty authority rejected",
			msg: types.MsgSetConsumerFeesPerBlock{
				Authority:  "",
				ConsumerId: 1,
				Amount:     "100",
			},
			wantError: true,
		},
		{
			name: "malformed bech32 authority rejected",
			msg: types.MsgSetConsumerFeesPerBlock{
				Authority:  "not-a-bech32",
				ConsumerId: 1,
				Amount:     "100",
			},
			wantError: true,
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			err := tc.msg.ValidateBasic()
			if tc.wantError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

// TestValidateInitParams_VaasTimeoutBounds table-drives ValidateInitializationParameters
// focusing on the VaasTimeoutPeriod bound (0, MaxTimeoutDelta]: positive with no
// lower floor, capped at the ibc-go v2 hard cap. All other fields are valid so
// only the timeout is under test.
func TestValidateInitParams_VaasTimeoutBounds(t *testing.T) {
	base := types.ConsumerInitializationParameters{
		InitialHeight:     clienttypes.NewHeight(1, 1),
		GenesisHash:       []byte{0x01},
		BinaryHash:        []byte{0x01},
		SpawnTime:         time.Now().UTC(),
		UnbondingPeriod:   21 * 24 * time.Hour,
		HistoricalEntries: 1000,
		SafeModeThreshold: vaastypes.DefaultSafeModeThreshold,
	}

	tests := []struct {
		name    string
		timeout time.Duration
		wantErr bool
	}{
		{
			name:    "small positive value: ok (no lower floor)",
			timeout: time.Second,
			wantErr: false,
		},
		{
			name:    "zero: error",
			timeout: 0,
			wantErr: true,
		},
		{
			name:    "negative: error",
			timeout: -time.Second,
			wantErr: true,
		},
		{
			name:    "exactly MaxTimeoutDelta: ok",
			timeout: channeltypesv2.MaxTimeoutDelta,
			wantErr: false,
		},
		{
			name:    "above MaxTimeoutDelta: error",
			timeout: channeltypesv2.MaxTimeoutDelta + time.Hour,
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			params := base
			params.VaasTimeoutPeriod = tc.timeout
			err := types.ValidateInitializationParameters(params)
			if tc.wantErr {
				require.Error(t, err)
				require.ErrorIs(t, err, types.ErrInvalidConsumerInitializationParameters)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestMsgChallengeConsumerDowntime_ValidateBasic(t *testing.T) {
	validSigner := sdk.AccAddress([]byte("alice___________")).String()
	validHeader := &ibctmtypes.Header{
		SignedHeader: &tmproto.SignedHeader{
			Header: &tmproto.Header{},
		},
	}
	validCommit := &tmproto.Commit{}

	base := func() types.MsgChallengeConsumerDowntime {
		return types.MsgChallengeConsumerDowntime{
			Signer:          validSigner,
			ConsumerId:      1,
			ValidatorAddr:   []byte{0x01},
			ClaimedHeight:   10,
			Header:          validHeader,
			LastCommit:      validCommit,
			ValidatorPubkey: make([]byte, ed25519.PubKeySize),
		}
	}

	tests := []struct {
		name    string
		mutate  func(msg *types.MsgChallengeConsumerDowntime)
		wantErr bool
	}{
		{"valid", func(msg *types.MsgChallengeConsumerDowntime) {}, false},
		{"invalid signer", func(msg *types.MsgChallengeConsumerDowntime) {
			msg.Signer = "not-bech32"
		}, true},
		{"empty validator_addr", func(msg *types.MsgChallengeConsumerDowntime) {
			msg.ValidatorAddr = nil
		}, true},
		{"zero claimed_height", func(msg *types.MsgChallengeConsumerDowntime) {
			msg.ClaimedHeight = 0
		}, true},
		{"negative claimed_height", func(msg *types.MsgChallengeConsumerDowntime) {
			msg.ClaimedHeight = -1
		}, true},
		{"nil header", func(msg *types.MsgChallengeConsumerDowntime) {
			msg.Header = nil
		}, true},
		{"empty header (nil signed_header)", func(msg *types.MsgChallengeConsumerDowntime) {
			// Crafted input: &ibctmtypes.Header{} with no SignedHeader at all.
			// The handler dereferences msg.Header.Header.ChainID (the
			// SignedHeader's promoted field), which would nil-pointer panic
			// on this shape if ValidateBasic let it through.
			msg.Header = &ibctmtypes.Header{}
		}, true},
		{"header with nil signed_header.header", func(msg *types.MsgChallengeConsumerDowntime) {
			// SignedHeader is present but its own Header is nil -- the same
			// panic-on-dereference shape, one level deeper.
			msg.Header = &ibctmtypes.Header{
				SignedHeader: &tmproto.SignedHeader{},
			}
		}, true},
		{"nil last_commit", func(msg *types.MsgChallengeConsumerDowntime) {
			msg.LastCommit = nil
		}, true},
		{"empty validator_pubkey", func(msg *types.MsgChallengeConsumerDowntime) {
			msg.ValidatorPubkey = nil
		}, true},
		{"31-byte validator_pubkey", func(msg *types.MsgChallengeConsumerDowntime) {
			// One byte short of ed25519.PubKeySize; the handler's
			// ed25519.PubKey(...).Address() call panics on this length.
			msg.ValidatorPubkey = make([]byte, ed25519.PubKeySize-1)
		}, true},
		{"33-byte validator_pubkey", func(msg *types.MsgChallengeConsumerDowntime) {
			// One byte past ed25519.PubKeySize; same panic shape as above.
			msg.ValidatorPubkey = make([]byte, ed25519.PubKeySize+1)
		}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			msg := base()
			tc.mutate(&msg)
			err := msg.ValidateBasic()
			if tc.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestMsgResumeConsumer_ValidateBasic(t *testing.T) {
	validAuthority := authtypes.NewModuleAddress(govtypes.ModuleName).String()
	tests := []struct {
		name    string
		msg     types.MsgResumeConsumer
		wantErr bool
	}{
		{"valid", types.MsgResumeConsumer{
			Authority:  validAuthority,
			ConsumerId: 1,
		}, false},
		{"invalid authority", types.MsgResumeConsumer{
			Authority:  "not-bech32",
			ConsumerId: 1,
		}, true},
		{"empty authority", types.MsgResumeConsumer{
			Authority:  "",
			ConsumerId: 1,
		}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.msg.ValidateBasic()
			if tc.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

// TestMsgUpdateParams_ValidateBasic: the message is a diff, so it must name
// at least one parameter to change and a bech32 authority; the merged set is
// validated by the handler against the stored parameters.
func TestMsgUpdateParams_ValidateBasic(t *testing.T) {
	validAuthority := authtypes.NewModuleAddress(govtypes.ModuleName).String()
	blocks := int64(900)
	tombstone := false
	threshold := "0.5"
	delay := time.Hour

	tests := []struct {
		name    string
		msg     types.MsgUpdateParams
		wantErr bool
	}{
		{"one top-level field", types.MsgUpdateParams{Authority: validAuthority, BlocksPerEpoch: &blocks}, false},
		{"one infraction field", types.MsgUpdateParams{
			Authority:            validAuthority,
			InfractionParameters: &types.InfractionParametersUpdate{DoubleSign: &types.SlashJailParametersUpdate{Tombstone: &tombstone}},
		}, false},
		{"the refusal pause threshold alone", types.MsgUpdateParams{Authority: validAuthority, RefusalPauseThreshold: &threshold}, false},
		{"the equivocation execution delay alone", types.MsgUpdateParams{Authority: validAuthority, EquivocationExecutionDelay: &delay}, false},
		{"the removal vote deferral margin alone", types.MsgUpdateParams{Authority: validAuthority, RemovalVoteDeferralMargin: &delay}, false},
		{"invalid authority", types.MsgUpdateParams{Authority: "not-bech32", BlocksPerEpoch: &blocks}, true},
		{"nothing to change", types.MsgUpdateParams{Authority: validAuthority}, true},
		{"empty infraction diff changes nothing", types.MsgUpdateParams{
			Authority:            validAuthority,
			InfractionParameters: &types.InfractionParametersUpdate{Downtime: &types.SlashJailParametersUpdate{}},
		}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.msg.ValidateBasic()
			if tc.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

// TestMsgSetConsumerRefusalValidateBasic: the refusal must name a valid
// validator and be signed by that validator's operator account.
func TestMsgSetConsumerRefusalValidateBasic(t *testing.T) {
	valAddr := sdk.ValAddress([]byte("refusing-validator-1"))
	operator := sdk.AccAddress(valAddr).String()
	other := sdk.AccAddress([]byte("someone-else-entirely")).String()

	require.NoError(t, types.NewMsgSetConsumerRefusal(operator, 0, valAddr.String(), true).ValidateBasic())
	require.NoError(t, types.NewMsgSetConsumerRefusal(operator, 0, valAddr.String(), false).ValidateBasic())
	require.Error(t, types.NewMsgSetConsumerRefusal(operator, 0, "not-a-valoper", true).ValidateBasic())
	require.Error(t, types.NewMsgSetConsumerRefusal("not-an-account", 0, valAddr.String(), true).ValidateBasic())
	require.Error(t, types.NewMsgSetConsumerRefusal(other, 0, valAddr.String(), true).ValidateBasic(),
		"only the operator account may sign")
}

func ref[T any](v T) *T {
	return &v
}

// clonedParams returns a copy of p that shares no slash policy with it, so a
// write through either policy pointer shows up as a difference.
func clonedParams(p types.Params) types.Params {
	if p.InfractionParameters.DoubleSign != nil {
		doubleSign := *p.InfractionParameters.DoubleSign
		p.InfractionParameters.DoubleSign = &doubleSign
	}
	if p.InfractionParameters.Downtime != nil {
		downtime := *p.InfractionParameters.Downtime
		p.InfractionParameters.Downtime = &downtime
	}
	return p
}

// TestMsgUpdateParamsApplyToReplacesExactlyTheFieldSet: for every field of
// the diff, a message setting that field alone yields the stored parameters
// with that one field replaced and nothing else touched, down to one field
// of one slash policy, and the stored parameters themselves are left as they
// were.
func TestMsgUpdateParamsApplyToReplacesExactlyTheFieldSet(t *testing.T) {
	infraction := func(u types.InfractionParametersUpdate) types.MsgUpdateParams {
		return types.MsgUpdateParams{InfractionParameters: &u}
	}
	doubleSign := func(u types.SlashJailParametersUpdate) types.MsgUpdateParams {
		return infraction(types.InfractionParametersUpdate{DoubleSign: &u})
	}
	downtime := func(u types.SlashJailParametersUpdate) types.MsgUpdateParams {
		return infraction(types.InfractionParametersUpdate{Downtime: &u})
	}

	cases := []struct {
		name string
		msg  types.MsgUpdateParams
		want func(p *types.Params)
	}{
		{
			"trusting_period_fraction",
			types.MsgUpdateParams{TrustingPeriodFraction: ref("0.8")},
			func(p *types.Params) { p.TrustingPeriodFraction = "0.8" },
		},
		{
			"liveness_grace_fraction",
			types.MsgUpdateParams{LivenessGraceFraction: ref("0.75")},
			func(p *types.Params) { p.LivenessGraceFraction = "0.75" },
		},
		{
			"vaas_timeout_period",
			types.MsgUpdateParams{VaasTimeoutPeriod: ref(2 * time.Hour)},
			func(p *types.Params) { p.VaasTimeoutPeriod = 2 * time.Hour },
		},
		{
			"blocks_per_epoch",
			types.MsgUpdateParams{BlocksPerEpoch: ref(int64(300))},
			func(p *types.Params) { p.BlocksPerEpoch = 300 },
		},
		{
			"fees_per_block_amount",
			types.MsgUpdateParams{FeesPerBlockAmount: ref(math.NewInt(2500))},
			func(p *types.Params) { p.FeesPerBlockAmount = math.NewInt(2500) },
		},
		{
			"min_deposit_blocks",
			types.MsgUpdateParams{MinDepositBlocks: ref(uint64(7))},
			func(p *types.Params) { p.MinDepositBlocks = 7 },
		},
		{
			"max_pause_duration",
			types.MsgUpdateParams{MaxPauseDuration: ref(48 * time.Hour)},
			func(p *types.Params) { p.MaxPauseDuration = 48 * time.Hour },
		},
		{
			"refusal_pause_threshold",
			types.MsgUpdateParams{RefusalPauseThreshold: ref("0.25")},
			func(p *types.Params) { p.RefusalPauseThreshold = "0.25" },
		},
		{
			"equivocation_execution_delay",
			types.MsgUpdateParams{EquivocationExecutionDelay: ref(48 * time.Hour)},
			func(p *types.Params) { p.EquivocationExecutionDelay = 48 * time.Hour },
		},
		{
			"removal_vote_deferral_margin",
			types.MsgUpdateParams{RemovalVoteDeferralMargin: ref(2 * time.Hour)},
			func(p *types.Params) { p.RemovalVoteDeferralMargin = 2 * time.Hour },
		},
		{
			"downtime_grace_period", infraction(types.InfractionParametersUpdate{DowntimeGracePeriod: ref(2 * time.Hour)}),
			func(p *types.Params) { p.InfractionParameters.DowntimeGracePeriod = 2 * time.Hour },
		},
		{
			"signed_blocks_window", infraction(types.InfractionParametersUpdate{SignedBlocksWindow: ref(int64(100))}),
			func(p *types.Params) { p.InfractionParameters.SignedBlocksWindow = 100 },
		},
		{
			"min_signed_per_window", infraction(types.InfractionParametersUpdate{MinSignedPerWindow: ref(math.LegacyMustNewDecFromStr("0.75"))}),
			func(p *types.Params) {
				p.InfractionParameters.MinSignedPerWindow = math.LegacyMustNewDecFromStr("0.75")
			},
		},
		{
			"downtime_challenge_window", infraction(types.InfractionParametersUpdate{DowntimeChallengeWindow: ref(96 * time.Hour)}),
			func(p *types.Params) { p.InfractionParameters.DowntimeChallengeWindow = 96 * time.Hour },
		},
		{
			"downtime_evidence_max_age", infraction(types.InfractionParametersUpdate{DowntimeEvidenceMaxAge: ref(24 * time.Hour)}),
			func(p *types.Params) { p.InfractionParameters.DowntimeEvidenceMaxAge = 24 * time.Hour },
		},
		{
			"double_sign.slash_fraction", doubleSign(types.SlashJailParametersUpdate{SlashFraction: ref(math.LegacyMustNewDecFromStr("0.1"))}),
			func(p *types.Params) {
				p.InfractionParameters.DoubleSign.SlashFraction = math.LegacyMustNewDecFromStr("0.1")
			},
		},
		{
			"double_sign.jail_duration", doubleSign(types.SlashJailParametersUpdate{JailDuration: ref(time.Hour)}),
			func(p *types.Params) { p.InfractionParameters.DoubleSign.JailDuration = time.Hour },
		},
		{
			"double_sign.tombstone", doubleSign(types.SlashJailParametersUpdate{Tombstone: ref(false)}),
			func(p *types.Params) { p.InfractionParameters.DoubleSign.Tombstone = false },
		},
		{
			"downtime.slash_fraction", downtime(types.SlashJailParametersUpdate{SlashFraction: ref(math.LegacyMustNewDecFromStr("0.002"))}),
			func(p *types.Params) {
				p.InfractionParameters.Downtime.SlashFraction = math.LegacyMustNewDecFromStr("0.002")
			},
		},
		{
			"downtime.jail_duration", downtime(types.SlashJailParametersUpdate{JailDuration: ref(10 * time.Minute)}),
			func(p *types.Params) { p.InfractionParameters.Downtime.JailDuration = 10 * time.Minute },
		},
		{
			"downtime.tombstone", downtime(types.SlashJailParametersUpdate{Tombstone: ref(true)}),
			func(p *types.Params) { p.InfractionParameters.Downtime.Tombstone = true },
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stored := types.DefaultParams()
			untouched := clonedParams(stored)
			want := clonedParams(stored)
			tc.want(&want)
			require.NotEqual(t, untouched, want, "the case must change the field it names")

			require.Equal(t, want, tc.msg.ApplyTo(stored))
			require.Equal(t, untouched, stored, "the stored parameters are copied, never written through")
		})
	}
}

// TestMsgUpdateParamsApplyToStartsAnAbsentPolicyFromEmpty: a slash policy the
// stored parameters do not carry is built from the diff alone, and the empty
// diff changes nothing.
func TestMsgUpdateParamsApplyToStartsAnAbsentPolicyFromEmpty(t *testing.T) {
	stored := types.DefaultParams()
	stored.InfractionParameters.Downtime = nil

	got := types.MsgUpdateParams{InfractionParameters: &types.InfractionParametersUpdate{
		Downtime: &types.SlashJailParametersUpdate{JailDuration: ref(10 * time.Minute)},
	}}.ApplyTo(stored)
	require.Equal(t, &types.SlashJailParameters{JailDuration: 10 * time.Minute}, got.InfractionParameters.Downtime)
	require.Nil(t, stored.InfractionParameters.Downtime)

	require.Equal(t, stored, types.MsgUpdateParams{}.ApplyTo(stored))
}
