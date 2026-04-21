package ante

import (
	"bytes"
	"context"
	"testing"

	errorsmod "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/x/authz"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"
	channeltypes "github.com/cosmos/ibc-go/v10/modules/core/04-channel/types"
	"github.com/stretchr/testify/require"
	protov2 "google.golang.org/protobuf/proto"

	consumertypes "github.com/allinbits/vaas/x/vaas/consumer/types"
)

type mockConsumerFundsKeeper struct {
	providerChannelFound bool
	inDebt               bool
}

func (m mockConsumerFundsKeeper) GetProviderChannel(context.Context) (string, bool) {
	return "channel-0", m.providerChannelFound
}

func (m mockConsumerFundsKeeper) IsConsumerInDebt(context.Context) bool {
	return m.inDebt
}

type mockTx struct {
	msgs []sdk.Msg
}

func (m mockTx) GetMsgs() []sdk.Msg {
	return m.msgs
}

func (m mockTx) GetMsgsV2() ([]protov2.Message, error) {
	return nil, nil
}

func TestConsumerFundsDecoratorSkipsGateBeforeProviderChannel(t *testing.T) {
	msg := &banktypes.MsgSend{
		FromAddress: testAccAddress(1).String(),
		ToAddress:   testAccAddress(2).String(),
		Amount:      sdk.NewCoins(sdk.NewInt64Coin("uatone", 10)),
	}

	decorator := NewConsumerFundsDecorator(mockConsumerFundsKeeper{
		providerChannelFound: false,
		inDebt:               true,
	})

	nextCalled := false
	_, err := decorator.AnteHandle(sdk.Context{}, mockTx{msgs: []sdk.Msg{msg}}, false, func(ctx sdk.Context, tx sdk.Tx, simulate bool) (sdk.Context, error) {
		nextCalled = true
		return ctx, nil
	})
	require.NoError(t, err)
	require.True(t, nextCalled)
}

func TestConsumerFundsDecoratorAllowsNonIBCTxWhenNotInDebt(t *testing.T) {
	msg := &banktypes.MsgSend{
		FromAddress: testAccAddress(1).String(),
		ToAddress:   testAccAddress(2).String(),
		Amount:      sdk.NewCoins(sdk.NewInt64Coin("uatone", 10)),
	}

	decorator := NewConsumerFundsDecorator(mockConsumerFundsKeeper{
		providerChannelFound: true,
		inDebt:               false,
	})

	nextCalled := false
	_, err := decorator.AnteHandle(sdk.Context{}, mockTx{msgs: []sdk.Msg{msg}}, false, func(ctx sdk.Context, tx sdk.Tx, simulate bool) (sdk.Context, error) {
		nextCalled = true
		return ctx, nil
	})
	require.NoError(t, err)
	require.True(t, nextCalled)
}

func TestConsumerFundsDecoratorBlocksNonIBCTxWhenInDebt(t *testing.T) {
	msg := &banktypes.MsgSend{
		FromAddress: testAccAddress(1).String(),
		ToAddress:   testAccAddress(2).String(),
		Amount:      sdk.NewCoins(sdk.NewInt64Coin("uatone", 10)),
	}

	decorator := NewConsumerFundsDecorator(mockConsumerFundsKeeper{
		providerChannelFound: true,
		inDebt:               true,
	})

	nextCalled := false
	_, err := decorator.AnteHandle(sdk.Context{}, mockTx{msgs: []sdk.Msg{msg}}, false, func(ctx sdk.Context, tx sdk.Tx, simulate bool) (sdk.Context, error) {
		nextCalled = true
		return ctx, nil
	})
	require.Error(t, err)
	require.True(t, errorsmod.IsOf(err, consumertypes.ErrConsumerInDebt))
	require.False(t, nextCalled)
}

func TestConsumerFundsDecoratorAllowsIBCCoreTxWhenInDebt(t *testing.T) {
	msg := &channeltypes.MsgRecvPacket{}

	decorator := NewConsumerFundsDecorator(mockConsumerFundsKeeper{
		providerChannelFound: true,
		inDebt:               true,
	})

	nextCalled := false
	_, err := decorator.AnteHandle(sdk.Context{}, mockTx{msgs: []sdk.Msg{msg}}, false, func(ctx sdk.Context, tx sdk.Tx, simulate bool) (sdk.Context, error) {
		nextCalled = true
		return ctx, nil
	})
	require.NoError(t, err)
	require.True(t, nextCalled)
}

func TestConsumerFundsDecoratorBlocksMixedIBCCoreAndNonIBCMessagesWhenInDebt(t *testing.T) {
	ibcMsg := &channeltypes.MsgRecvPacket{}
	bankMsg := &banktypes.MsgSend{
		FromAddress: testAccAddress(1).String(),
		ToAddress:   testAccAddress(2).String(),
		Amount:      sdk.NewCoins(sdk.NewInt64Coin("uatone", 10)),
	}

	decorator := NewConsumerFundsDecorator(mockConsumerFundsKeeper{
		providerChannelFound: true,
		inDebt:               true,
	})

	nextCalled := false
	_, err := decorator.AnteHandle(sdk.Context{}, mockTx{msgs: []sdk.Msg{ibcMsg, bankMsg}}, false, func(ctx sdk.Context, tx sdk.Tx, simulate bool) (sdk.Context, error) {
		nextCalled = true
		return ctx, nil
	})
	require.Error(t, err)
	require.True(t, errorsmod.IsOf(err, consumertypes.ErrConsumerInDebt))
	require.False(t, nextCalled)
}

// Authz-wrapped IBC core txs are treated as non-IBC and rejected while the
// consumer is in debt. Real relayers sign /ibc.core.* messages directly with
// their own keys, so this path is not needed for CCV liveness.
func TestConsumerFundsDecoratorRejectsAuthzWrappedIBCCoreTxWhenInDebt(t *testing.T) {
	ibcMsg := &channeltypes.MsgRecvPacket{}
	msgExec := authz.NewMsgExec(testAccAddress(3), []sdk.Msg{ibcMsg})

	decorator := NewConsumerFundsDecorator(mockConsumerFundsKeeper{
		providerChannelFound: true,
		inDebt:               true,
	})

	nextCalled := false
	_, err := decorator.AnteHandle(sdk.Context{}, mockTx{msgs: []sdk.Msg{&msgExec}}, false, func(ctx sdk.Context, tx sdk.Tx, simulate bool) (sdk.Context, error) {
		nextCalled = true
		return ctx, nil
	})
	require.Error(t, err)
	require.True(t, errorsmod.IsOf(err, consumertypes.ErrConsumerInDebt))
	require.False(t, nextCalled)
}

func testAccAddress(seed byte) sdk.AccAddress {
	return sdk.AccAddress(bytes.Repeat([]byte{seed}, 20))
}
