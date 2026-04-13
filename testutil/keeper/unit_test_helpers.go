package keeper

import (
	"crypto/rand"
	"encoding/binary"
	"testing"

	consumerkeeper "github.com/allinbits/vaas/x/vaas/consumer/keeper"
	consumertypes "github.com/allinbits/vaas/x/vaas/consumer/types"
	providerkeeper "github.com/allinbits/vaas/x/vaas/provider/keeper"
	"github.com/allinbits/vaas/x/vaas/types"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	tmproto "github.com/cometbft/cometbft/proto/tendermint/types"

	dbm "github.com/cosmos/cosmos-db"
	clientv2types "github.com/cosmos/ibc-go/v10/modules/core/02-client/v2/types"

	"cosmossdk.io/log"
	"cosmossdk.io/store"
	"cosmossdk.io/store/metrics"
	storetypes "cosmossdk.io/store/types"

	"github.com/cosmos/cosmos-sdk/codec"
	"github.com/cosmos/cosmos-sdk/codec/address"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	"github.com/cosmos/cosmos-sdk/crypto/keys/ed25519"
	"github.com/cosmos/cosmos-sdk/runtime"
	sdk "github.com/cosmos/cosmos-sdk/types"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	govkeeper "github.com/cosmos/cosmos-sdk/x/gov/keeper"
	govtypes "github.com/cosmos/cosmos-sdk/x/gov/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
)

// InMemKeeperParams parameters needed to instantiate an in-memory keeper
type InMemKeeperParams struct {
	Cdc      *codec.ProtoCodec
	StoreKey *storetypes.KVStoreKey
	Ctx      sdk.Context
}

// NewInMemKeeperParams instantiates in-memory keeper params with default values
func NewInMemKeeperParams(tb testing.TB) InMemKeeperParams {
	tb.Helper()
	storeKey := storetypes.NewKVStoreKey(types.StoreKey)
	memStoreKey := storetypes.NewMemoryStoreKey(types.MemStoreKey)

	db := dbm.NewMemDB()
	stateStore := store.NewCommitMultiStore(db, log.NewNopLogger(), metrics.NewNoOpMetrics())
	stateStore.MountStoreWithDB(storeKey, storetypes.StoreTypeIAVL, db)
	stateStore.MountStoreWithDB(memStoreKey, storetypes.StoreTypeMemory, nil)
	require.NoError(tb, stateStore.LoadLatestVersion())

	registry := codectypes.NewInterfaceRegistry()
	cryptocodec.RegisterInterfaces(registry)
	cdc := codec.NewProtoCodec(registry)

	ctx := sdk.NewContext(stateStore, tmproto.Header{}, false, log.NewNopLogger())

	return InMemKeeperParams{
		Cdc:      cdc,
		StoreKey: storeKey,
		Ctx:      ctx,
	}
}

// A struct holding pointers to any mocked external keeper needed for provider/consumer keeper setup.
type MockedKeepers struct {
	*MockClientKeeper
	*MockClientV2Keeper
	*MockStakingKeeper
	*MockSlashingKeeper
	*MockAccountKeeper
	*MockBankKeeper
}

// NewMockedKeepers instantiates a struct with pointers to properly instantiated mocked keepers.
func NewMockedKeepers(ctrl *gomock.Controller) MockedKeepers {
	mocks := MockedKeepers{
		MockClientKeeper:   NewMockClientKeeper(ctrl),
		MockClientV2Keeper: NewMockClientV2Keeper(ctrl),
		MockStakingKeeper:  NewMockStakingKeeper(ctrl),
		MockSlashingKeeper: NewMockSlashingKeeper(ctrl),
		MockAccountKeeper:  NewMockAccountKeeper(ctrl),
		MockBankKeeper:     NewMockBankKeeper(ctrl),
	}
	mocks.MockClientV2Keeper.EXPECT().GetClientCounterparty(gomock.Any(), gomock.Any()).Return(clientv2types.CounterpartyInfo{}, false).AnyTimes()
	return mocks
}

// NewInMemProviderKeeper instantiates an in-mem provider keeper from params and mocked keepers
func NewInMemProviderKeeper(params InMemKeeperParams, mocks MockedKeepers) providerkeeper.Keeper {
	storeService := runtime.NewKVStoreService(params.StoreKey)
	return providerkeeper.NewKeeper(
		params.Cdc,
		storeService,
		mocks.MockClientKeeper,
		mocks.MockClientV2Keeper,
		mocks.MockStakingKeeper,
		mocks.MockSlashingKeeper,
		mocks.MockAccountKeeper,
		mocks.MockBankKeeper,
		govkeeper.Keeper{}, // HACK: to make parts of the test work
		authtypes.NewModuleAddress(govtypes.ModuleName).String(),
		address.NewBech32Codec("cosmosvaloper"),
		address.NewBech32Codec("cosmosvalcons"),
		authtypes.FeeCollectorName,
	)
}

// NewInMemConsumerKeeper instantiates an in-mem consumer keeper from params and mocked keepers
func NewInMemConsumerKeeper(params InMemKeeperParams, mocks MockedKeepers) consumerkeeper.Keeper {
	storeService := runtime.NewKVStoreService(params.StoreKey)

	return consumerkeeper.NewKeeper(
		params.Cdc,
		storeService,
		mocks.MockClientKeeper,
		mocks.MockClientV2Keeper,
		mocks.MockSlashingKeeper,
		mocks.MockBankKeeper,
		mocks.MockAccountKeeper,
		authtypes.FeeCollectorName,
		authtypes.NewModuleAddress(govtypes.ModuleName).String(),
		address.NewBech32Codec("cosmosvaloper"),
		address.NewBech32Codec("cosmosvalcons"),
	)
}

// Returns an in-memory provider keeper, context, controller, and mocks, given a test instance and parameters.
//
// Note: Calling ctrl.Finish() at the end of a test function ensures that
// no unexpected calls to external keepers are made.
func GetProviderKeeperAndCtx(t *testing.T, params InMemKeeperParams) (
	providerkeeper.Keeper, sdk.Context, *gomock.Controller, MockedKeepers,
) {
	t.Helper()
	ctrl := gomock.NewController(t)
	mocks := NewMockedKeepers(ctrl)
	return NewInMemProviderKeeper(params, mocks), params.Ctx, ctrl, mocks
}

// Return an in-memory consumer keeper, context, controller, and mocks, given a test instance and parameters.
//
// Note: Calling ctrl.Finish() at the end of a test function ensures that
// no unexpected calls to external keepers are made.
func GetConsumerKeeperAndCtx(t *testing.T, params InMemKeeperParams) (
	consumerkeeper.Keeper, sdk.Context, *gomock.Controller, MockedKeepers,
) {
	t.Helper()
	ctrl := gomock.NewController(t)
	mocks := NewMockedKeepers(ctrl)
	return NewInMemConsumerKeeper(params, mocks), params.Ctx, ctrl, mocks
}

func ExpectCreateClientMock(ctx sdk.Context, mocks MockedKeepers, clientType, clientID string,
	clientState, consState []byte,
) *gomock.Call {
	return mocks.MockClientKeeper.EXPECT().CreateClient(ctx, clientType, clientState, consState).Return(clientID,
		nil).Times(1)
}

func SetupMocksForLastBondedValidatorsExpectation(mockStakingKeeper *MockStakingKeeper, maxValidators uint32, vals []stakingtypes.Validator, times int) {
	validatorsCall := mockStakingKeeper.EXPECT().GetBondedValidatorsByPower(gomock.Any()).Return(vals, nil)
	maxValidatorsCall := mockStakingKeeper.EXPECT().MaxValidators(gomock.Any()).Return(maxValidators, nil)

	if times == -1 {
		validatorsCall.AnyTimes()
		maxValidatorsCall.AnyTimes()
	} else {
		validatorsCall.Times(times)
		maxValidatorsCall.Times(times)
	}
}

// Obtains a CrossChainValidator with a newly generated key, and randomized field values
func GetNewCrossChainValidator(t *testing.T) consumertypes.CrossChainValidator {
	t.Helper()
	b1 := make([]byte, 8)
	_, _ = rand.Read(b1)
	power := int64(binary.BigEndian.Uint64(b1))
	privKey := ed25519.GenPrivKey()
	validator, err := consumertypes.NewCCValidator(privKey.PubKey().Address(), power, privKey.PubKey())
	require.NoError(t, err)
	return validator
}
