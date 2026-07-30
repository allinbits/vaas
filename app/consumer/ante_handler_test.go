package app_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	abci "github.com/cometbft/cometbft/abci/types"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"

	dbm "github.com/cosmos/cosmos-db"
	ibctransfertypes "github.com/cosmos/ibc-go/v10/modules/apps/transfer/types"
	clientv2types "github.com/cosmos/ibc-go/v10/modules/core/02-client/v2/types"

	errorsmod "cosmossdk.io/errors"
	"cosmossdk.io/log"

	"github.com/cosmos/cosmos-sdk/baseapp"
	clienttx "github.com/cosmos/cosmos-sdk/client/tx"
	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	"github.com/cosmos/cosmos-sdk/crypto/keys/ed25519"
	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	cryptotypes "github.com/cosmos/cosmos-sdk/crypto/types"
	simtestutil "github.com/cosmos/cosmos-sdk/testutil/sims"
	sdk "github.com/cosmos/cosmos-sdk/types"
	signingtypes "github.com/cosmos/cosmos-sdk/types/tx/signing"
	"github.com/cosmos/cosmos-sdk/x/auth/ante"
	authsigning "github.com/cosmos/cosmos-sdk/x/auth/signing"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"

	consumerapp "github.com/allinbits/vaas/app/consumer"
	consumerante "github.com/allinbits/vaas/x/vaas/consumer/ante"
	consumertypes "github.com/allinbits/vaas/x/vaas/consumer/types"
	vaastypes "github.com/allinbits/vaas/x/vaas/types"
)

const (
	anteTestChainID  = "vaas-consumer-ante-test"
	providerClientID = "07-tendermint-0"
)

type testAppOptions map[string]any

func (o testAppOptions) Get(key string) any { return o[key] }

// setupAnteTestApp builds the reference consumer app on an in-memory database
// and runs it through InitChain, one finalized block, and a commit, so the
// ante chain sees the same committed module state (auth params, bank send
// config, consumer params) a live node has. The consumer module is
// initialized from an enabled restart genesis carrying one validator and the
// pinned provider client, the state a launched consumer runs with, with the
// photon-only fee policy set as the chain would set it: through the
// photon_fees_enabled consumer param.
func setupAnteTestApp(t *testing.T, photonFees bool) *consumerapp.App {
	t.Helper()

	db := dbm.NewMemDB()
	appOpts := testAppOptions{"home": t.TempDir()}
	capp := consumerapp.New(log.NewNopLogger(), db, nil, true, appOpts, baseapp.SetChainID(anteTestChainID))

	genesisState := consumerapp.ModuleBasics.DefaultGenesis(capp.AppCodec())

	valPubKey, err := cryptocodec.ToCmtProtoPublicKey(ed25519.GenPrivKey().PubKey())
	require.NoError(t, err)
	params := vaastypes.DefaultConsumerParams()
	params.Enabled = true
	params.PhotonFeesEnabled = photonFees
	consumerGenesis := consumertypes.NewRestartGenesisState(
		providerClientID,
		[]abci.ValidatorUpdate{{PubKey: valPubKey, Power: 100}},
		[]consumertypes.HeightToValsetUpdateID{{ValsetUpdateId: 1, Height: 1}},
		params,
	)
	genesisState[consumertypes.ModuleName] = capp.AppCodec().MustMarshalJSON(consumerGenesis)

	stateBytes, err := json.Marshal(genesisState)
	require.NoError(t, err)

	genesisTime := time.Unix(1_850_000_000, 0).UTC()
	_, err = capp.InitChain(&abci.RequestInitChain{
		ChainId:         anteTestChainID,
		Time:            genesisTime,
		ConsensusParams: simtestutil.DefaultConsensusParams,
		AppStateBytes:   stateBytes,
		InitialHeight:   1,
	})
	require.NoError(t, err)

	_, err = capp.FinalizeBlock(&abci.RequestFinalizeBlock{Height: 1, Time: genesisTime})
	require.NoError(t, err)
	_, err = capp.Commit()
	require.NoError(t, err)

	return capp
}

// anteTestContext branches a fresh, isolated context off the app's committed
// state, so each test case sees genesis-initialized module state plus only its
// own writes.
func anteTestContext(capp *consumerapp.App) sdk.Context {
	header := cmtproto.Header{
		ChainID: anteTestChainID,
		Height:  2,
		Time:    time.Unix(1_850_000_100, 0).UTC(),
	}
	ctx, _ := capp.NewUncachedContext(false, header).CacheContext()
	return ctx
}

// fundedSigner creates an on-chain account for a fresh key and mints it funds
// through the transfer module account -- the same module that mints real IBC
// vouchers -- so fee deduction can succeed for accepted transactions.
func fundedSigner(t *testing.T, capp *consumerapp.App, ctx sdk.Context, funds sdk.Coins) (cryptotypes.PrivKey, sdk.AccAddress) {
	t.Helper()

	priv := secp256k1.GenPrivKey()
	addr := sdk.AccAddress(priv.PubKey().Address())
	acc := capp.AccountKeeper.NewAccountWithAddress(ctx, addr)
	capp.AccountKeeper.SetAccount(ctx, acc)

	if !funds.IsZero() {
		require.NoError(t, capp.BankKeeper.MintCoins(ctx, ibctransfertypes.ModuleName, funds))
		require.NoError(t, capp.BankKeeper.SendCoinsFromModuleToAccount(ctx, ibctransfertypes.ModuleName, addr, funds))
	}
	return priv, addr
}

// signedBankSendTx builds a properly signed bank-send transaction with the
// given fee, exercising the full ante chain including signature verification.
func signedBankSendTx(t *testing.T, capp *consumerapp.App, ctx sdk.Context, priv cryptotypes.PrivKey, addr sdk.AccAddress, fee sdk.Coins) sdk.Tx {
	t.Helper()

	txConfig := capp.TxConfig()
	txBuilder := txConfig.NewTxBuilder()
	msg := &banktypes.MsgSend{
		FromAddress: addr.String(),
		ToAddress:   addr.String(),
		Amount:      sdk.NewCoins(sdk.NewInt64Coin("stake", 1)),
	}
	require.NoError(t, txBuilder.SetMsgs(msg))
	txBuilder.SetFeeAmount(fee)
	txBuilder.SetGasLimit(300000)

	acc := capp.AccountKeeper.GetAccount(ctx, addr)
	require.NotNil(t, acc)

	// Round one populates the signer info the sign bytes depend on; round two
	// stores the actual signature.
	sigV2 := signingtypes.SignatureV2{
		PubKey: priv.PubKey(),
		Data: &signingtypes.SingleSignatureData{
			SignMode: signingtypes.SignMode_SIGN_MODE_DIRECT,
		},
		Sequence: acc.GetSequence(),
	}
	require.NoError(t, txBuilder.SetSignatures(sigV2))

	signerData := authsigning.SignerData{
		ChainID:       anteTestChainID,
		AccountNumber: acc.GetAccountNumber(),
		Sequence:      acc.GetSequence(),
		PubKey:        priv.PubKey(),
		Address:       addr.String(),
	}
	sigV2, err := clienttx.SignWithPrivKey(
		ctx, signingtypes.SignMode_SIGN_MODE_DIRECT, signerData, txBuilder, priv, txConfig, acc.GetSequence(),
	)
	require.NoError(t, err)
	require.NoError(t, txBuilder.SetSignatures(sigV2))

	return txBuilder.GetTx()
}

// newAppAnteHandler builds the ante chain exactly as the app does. The photon
// fee decorator is always part of that chain; whether it enforces is read from
// the consumer params by the decorator itself.
func newAppAnteHandler(t *testing.T, capp *consumerapp.App) sdk.AnteHandler {
	t.Helper()
	handler, err := consumerapp.NewAnteHandler(consumerapp.HandlerOptions{
		HandlerOptions: ante.HandlerOptions{
			AccountKeeper:   capp.AccountKeeper,
			BankKeeper:      capp.BankKeeper,
			SignModeHandler: capp.TxConfig().SignModeHandler(),
			SigGasConsumer:  ante.DefaultSigVerificationGasConsumer,
		},
		IBCKeeper:      capp.IBCKeeper,
		ConsumerKeeper: capp.ConsumerKeeper,
	})
	require.NoError(t, err)
	return handler
}

// TestConsumerAnteHandlerPhotonFees runs signed transactions through the
// reference consumer app's full ante chain against real keepers, covering the
// photon fee policy in both param settings and both decorator phases.
func TestConsumerAnteHandlerPhotonFees(t *testing.T) {
	photonDenom := consumerante.ExpectedPhotonDenom(providerClientID)

	testCases := []struct {
		name       string
		photonFees bool
		routable   bool
		fee        sdk.Coins
		funds      sdk.Coins
		expectErr  bool
	}{
		{
			name:       "param on: photon voucher fee is accepted and deducted",
			photonFees: true,
			routable:   true,
			fee:        sdk.NewCoins(sdk.NewInt64Coin(photonDenom, 500)),
			funds:      sdk.NewCoins(sdk.NewInt64Coin(photonDenom, 1000)),
		},
		{
			name:       "param on: non-photon fee is rejected by the fee policy, not by fund checks",
			photonFees: true,
			routable:   true,
			fee:        sdk.NewCoins(sdk.NewInt64Coin("uatone", 500)),
			expectErr:  true,
		},
		{
			name:       "param on: empty fee is rejected",
			photonFees: true,
			routable:   true,
			fee:        sdk.NewCoins(),
			expectErr:  true,
		},
		{
			name:       "param on but provider client not yet routable: bootstrap fees pass",
			photonFees: true,
			routable:   false,
			fee:        sdk.NewCoins(sdk.NewInt64Coin("uatone", 500)),
			funds:      sdk.NewCoins(sdk.NewInt64Coin("uatone", 1000)),
		},
		{
			name:       "param off: any fee denom is accepted",
			photonFees: false,
			routable:   true,
			fee:        sdk.NewCoins(sdk.NewInt64Coin("uatone", 500)),
			funds:      sdk.NewCoins(sdk.NewInt64Coin("uatone", 1000)),
		},
		{
			name:       "param off: empty fee is accepted",
			photonFees: false,
			routable:   true,
			fee:        sdk.NewCoins(),
		},
	}

	// One app per param setting, each initialized from a genesis carrying that
	// setting, so the enforcing decision comes from committed module state.
	apps := map[bool]*consumerapp.App{
		false: setupAnteTestApp(t, false),
		true:  setupAnteTestApp(t, true),
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			capp := apps[tc.photonFees]
			ctx := anteTestContext(capp)

			// The provider client is pinned from genesis; a registered
			// counterparty marks it routable, i.e. adopted after the first
			// VSC delivery.
			if tc.routable {
				capp.IBCKeeper.ClientV2Keeper.SetClientCounterparty(ctx, providerClientID, clientv2types.CounterpartyInfo{
					ClientId: "07-tendermint-9",
				})
			}

			priv, addr := fundedSigner(t, capp, ctx, tc.funds)
			tx := signedBankSendTx(t, capp, ctx, priv, addr, tc.fee)

			anteHandler := newAppAnteHandler(t, capp)
			_, err := anteHandler(ctx, tx, false)

			if tc.expectErr {
				require.Error(t, err)
				require.True(t, errorsmod.IsOf(err, consumertypes.ErrInvalidFeeDenom),
					"rejection must come from the photon fee policy, got: %v", err)
				return
			}
			require.NoError(t, err)
		})
	}
}
