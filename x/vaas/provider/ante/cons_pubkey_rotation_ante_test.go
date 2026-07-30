package ante

import (
	"bytes"
	"testing"

	errorsmod "cosmossdk.io/errors"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	cryptotypes "github.com/cosmos/cosmos-sdk/crypto/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/x/authz"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
	"github.com/stretchr/testify/require"
	protov2 "google.golang.org/protobuf/proto"

	cryptotestutil "github.com/allinbits/vaas/testutil/crypto"
	providertypes "github.com/allinbits/vaas/x/vaas/provider/types"
)

// mockProviderKeeper answers the collision predicate from a fixed set of
// consensus addresses. all=true treats every address as taken, which is how the
// tests show that messages other than a rotation are never asked about.
type mockProviderKeeper struct {
	inUse map[string]bool
	all   bool
}

func (m mockProviderKeeper) IsConsumerConsAddrInUse(_ sdk.Context, consAddr sdk.ConsAddress) bool {
	return m.all || m.inUse[consAddr.String()]
}

type mockTx struct {
	msgs []sdk.Msg
}

func (m mockTx) GetMsgs() []sdk.Msg                    { return m.msgs }
func (m mockTx) GetMsgsV2() ([]protov2.Message, error) { return nil, nil }

func runDecorator(t *testing.T, k mockProviderKeeper, msgs []sdk.Msg) (bool, error) {
	t.Helper()
	decorator := NewConsPubKeyRotationDecorator(k)
	nextCalled := false
	_, err := decorator.AnteHandle(sdk.Context{}, mockTx{msgs: msgs}, false, func(ctx sdk.Context, tx sdk.Tx, simulate bool) (sdk.Context, error) {
		nextCalled = true
		return ctx, nil
	})
	return nextCalled, err
}

// rotationMsg builds a MsgRotateConsPubKey declaring newPubKey as the
// validator's next provider consensus key.
func rotationMsg(t *testing.T, newPubKey cryptotypes.PubKey) sdk.Msg {
	t.Helper()
	pkAny, err := codectypes.NewAnyWithValue(newPubKey)
	require.NoError(t, err)
	return &stakingtypes.MsgRotateConsPubKey{
		ValidatorAddress: cryptotestutil.NewCryptoIdentityFromIntSeed(1).SDKValOpAddressString(),
		NewPubkey:        pkAny,
	}
}

func bankSendMsg() sdk.Msg {
	return &banktypes.MsgSend{
		FromAddress: testAccAddress(1).String(),
		ToAddress:   testAccAddress(2).String(),
		Amount:      sdk.NewCoins(sdk.NewInt64Coin("uatone", 10)),
	}
}

func testAccAddress(seed byte) sdk.AccAddress {
	return sdk.AccAddress(bytes.Repeat([]byte{seed}, 20))
}

func TestConsPubKeyRotationDecorator(t *testing.T) {
	// takenKey is assigned as some validator's consumer key; freshKey is not.
	takenKey := cryptotestutil.NewCryptoIdentityFromIntSeed(2).ConsensusSDKPubKey()
	freshKey := cryptotestutil.NewCryptoIdentityFromIntSeed(3).ConsensusSDKPubKey()
	keeper := mockProviderKeeper{
		inUse: map[string]bool{sdk.ConsAddress(takenKey.Address()).String(): true},
	}
	grantee := testAccAddress(3)

	// A rotation whose declared key is not a public key at all: nothing to check,
	// x/staking's own handler rejects the message.
	undecodableAny, err := codectypes.NewAnyWithValue(bankSendMsg())
	require.NoError(t, err)
	undecodableRotation := &stakingtypes.MsgRotateConsPubKey{
		ValidatorAddress: cryptotestutil.NewCryptoIdentityFromIntSeed(1).SDKValOpAddressString(),
		NewPubkey:        undecodableAny,
	}

	authzWrap := func(msgs ...sdk.Msg) sdk.Msg {
		msgExec := authz.NewMsgExec(grantee, msgs)
		return &msgExec
	}

	testCases := []struct {
		name      string
		keeper    mockProviderKeeper
		msgs      []sdk.Msg
		expectErr bool
	}{
		{
			name:      "rotation onto a key already assigned as a consumer key is rejected",
			keeper:    keeper,
			msgs:      []sdk.Msg{rotationMsg(t, takenKey)},
			expectErr: true,
		},
		{
			name:   "rotation onto a key no consumer holds passes",
			keeper: keeper,
			msgs:   []sdk.Msg{rotationMsg(t, freshKey)},
		},
		{
			name:      "a colliding rotation alongside unrelated messages is rejected",
			keeper:    keeper,
			msgs:      []sdk.Msg{bankSendMsg(), rotationMsg(t, takenKey)},
			expectErr: true,
		},
		{
			name:   "messages other than a rotation are never inspected",
			keeper: mockProviderKeeper{all: true},
			msgs:   []sdk.Msg{bankSendMsg()},
		},
		{
			name:      "authz-wrapped colliding rotation is rejected",
			keeper:    keeper,
			msgs:      []sdk.Msg{authzWrap(rotationMsg(t, takenKey))},
			expectErr: true,
		},
		{
			name:      "nested authz-wrapped colliding rotation is rejected",
			keeper:    keeper,
			msgs:      []sdk.Msg{authzWrap(authzWrap(rotationMsg(t, takenKey)))},
			expectErr: true,
		},
		{
			name:   "authz-wrapped non-colliding rotation passes",
			keeper: keeper,
			msgs:   []sdk.Msg{authzWrap(rotationMsg(t, freshKey))},
		},
		{
			name:   "authz-wrapped unrelated message passes",
			keeper: mockProviderKeeper{all: true},
			msgs:   []sdk.Msg{authzWrap(bankSendMsg())},
		},
		{
			name:   "rotation declaring no key passes (x/staking rejects it)",
			keeper: mockProviderKeeper{all: true},
			msgs:   []sdk.Msg{&stakingtypes.MsgRotateConsPubKey{ValidatorAddress: "cosmosvaloper1"}},
		},
		{
			name:   "rotation declaring a non-key passes (x/staking rejects it)",
			keeper: mockProviderKeeper{all: true},
			msgs:   []sdk.Msg{undecodableRotation},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			nextCalled, err := runDecorator(t, tc.keeper, tc.msgs)
			if tc.expectErr {
				require.Error(t, err)
				require.True(t, errorsmod.IsOf(err, providertypes.ErrConsumerKeyInUse))
				require.False(t, nextCalled)
				return
			}
			require.NoError(t, err)
			require.True(t, nextCalled)
		})
	}
}
