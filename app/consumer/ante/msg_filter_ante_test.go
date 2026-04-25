package ante

import (
	"bytes"
	"context"
	"testing"

	errorsmod "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/x/authz"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"
	govtypes "github.com/cosmos/cosmos-sdk/x/gov/types/v1"
	channeltypes "github.com/cosmos/ibc-go/v10/modules/core/04-channel/types"
	"github.com/stretchr/testify/require"
	protov2 "google.golang.org/protobuf/proto"

	consumertypes "github.com/allinbits/vaas/x/vaas/consumer/types"
)

type mockConsumerKeeper struct {
	providerClientFound bool
	inDebt              bool
}

func (m mockConsumerKeeper) GetProviderClientID(context.Context) (string, bool) {
	return "07-tendermint-0", m.providerClientFound
}

func (m mockConsumerKeeper) IsConsumerInDebt(context.Context) bool {
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

func runDecorator(t *testing.T, k mockConsumerKeeper, msgs []sdk.Msg) (bool, error) {
	t.Helper()
	decorator := NewMsgFilterDecorator(k)
	nextCalled := false
	_, err := decorator.AnteHandle(sdk.Context{}, mockTx{msgs: msgs}, false, func(ctx sdk.Context, tx sdk.Tx, simulate bool) (sdk.Context, error) {
		nextCalled = true
		return ctx, nil
	})
	return nextCalled, err
}

func bankSendMsg() sdk.Msg {
	return &banktypes.MsgSend{
		FromAddress: testAccAddress(1).String(),
		ToAddress:   testAccAddress(2).String(),
		Amount:      sdk.NewCoins(sdk.NewInt64Coin("uatone", 10)),
	}
}

// Pre-CCV: /ibc.* messages are accepted so the IBC stack can be set up.
func TestMsgFilterDecoratorPreCCVAllowsIBCMsgs(t *testing.T) {
	nextCalled, err := runDecorator(t,
		mockConsumerKeeper{providerClientFound: false},
		[]sdk.Msg{&channeltypes.MsgRecvPacket{}},
	)
	require.NoError(t, err)
	require.True(t, nextCalled)
}

// Pre-CCV: any non-/ibc.* message is rejected.
func TestMsgFilterDecoratorPreCCVRejectsNonIBCMsgs(t *testing.T) {
	nextCalled, err := runDecorator(t,
		mockConsumerKeeper{providerClientFound: false},
		[]sdk.Msg{bankSendMsg()},
	)
	require.Error(t, err)
	require.False(t, nextCalled)
}

// Post-CCV, not in debt: everything passes.
func TestMsgFilterDecoratorAllowsNonIBCTxWhenNotInDebt(t *testing.T) {
	nextCalled, err := runDecorator(t,
		mockConsumerKeeper{providerClientFound: true, inDebt: false},
		[]sdk.Msg{bankSendMsg()},
	)
	require.NoError(t, err)
	require.True(t, nextCalled)
}

// Post-CCV, in debt: non-IBC, non-gov msgs are rejected with ErrConsumerInDebt.
func TestMsgFilterDecoratorBlocksNonIBCTxWhenInDebt(t *testing.T) {
	nextCalled, err := runDecorator(t,
		mockConsumerKeeper{providerClientFound: true, inDebt: true},
		[]sdk.Msg{bankSendMsg()},
	)
	require.Error(t, err)
	require.True(t, errorsmod.IsOf(err, consumertypes.ErrConsumerInDebt))
	require.False(t, nextCalled)
}

// Post-CCV, in debt: /ibc.core.* msgs pass to keep CCV liveness.
func TestMsgFilterDecoratorAllowsIBCCoreTxWhenInDebt(t *testing.T) {
	nextCalled, err := runDecorator(t,
		mockConsumerKeeper{providerClientFound: true, inDebt: true},
		[]sdk.Msg{&channeltypes.MsgRecvPacket{}},
	)
	require.NoError(t, err)
	require.True(t, nextCalled)
}

// Post-CCV, in debt: a tx mixing /ibc.core.* with anything else is rejected.
func TestMsgFilterDecoratorBlocksMixedIBCCoreAndNonIBCMessagesWhenInDebt(t *testing.T) {
	nextCalled, err := runDecorator(t,
		mockConsumerKeeper{providerClientFound: true, inDebt: true},
		[]sdk.Msg{&channeltypes.MsgRecvPacket{}, bankSendMsg()},
	)
	require.Error(t, err)
	require.True(t, errorsmod.IsOf(err, consumertypes.ErrConsumerInDebt))
	require.False(t, nextCalled)
}

// Governance messages are allowed during debt so the community can vote on
// recovery (emergency funding, param changes) without off-chain coordination.
func TestMsgFilterDecoratorAllowsGovTxWhenInDebt(t *testing.T) {
	msg := &govtypes.MsgVote{
		ProposalId: 1,
		Voter:      testAccAddress(1).String(),
		Option:     govtypes.OptionYes,
	}
	nextCalled, err := runDecorator(t,
		mockConsumerKeeper{providerClientFound: true, inDebt: true},
		[]sdk.Msg{msg},
	)
	require.NoError(t, err)
	require.True(t, nextCalled)
}

// Authz-wrapped IBC core txs are treated as non-IBC and rejected while the
// consumer is in debt. Real relayers sign /ibc.core.* messages directly with
// their own keys, so this path is not needed for CCV liveness.
func TestMsgFilterDecoratorRejectsAuthzWrappedIBCCoreTxWhenInDebt(t *testing.T) {
	msgExec := authz.NewMsgExec(testAccAddress(3), []sdk.Msg{&channeltypes.MsgRecvPacket{}})
	nextCalled, err := runDecorator(t,
		mockConsumerKeeper{providerClientFound: true, inDebt: true},
		[]sdk.Msg{&msgExec},
	)
	require.Error(t, err)
	require.True(t, errorsmod.IsOf(err, consumertypes.ErrConsumerInDebt))
	require.False(t, nextCalled)
}

func testAccAddress(seed byte) sdk.AccAddress {
	return sdk.AccAddress(bytes.Repeat([]byte{seed}, 20))
}
