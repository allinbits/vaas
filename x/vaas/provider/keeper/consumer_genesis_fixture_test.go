package keeper_test

import (
	"time"

	tmtypes "github.com/cometbft/cometbft/types"

	clienttypes "github.com/cosmos/ibc-go/v10/modules/core/02-client/types"
	commitmenttypes "github.com/cosmos/ibc-go/v10/modules/core/23-commitment/types"
	ibctmtypes "github.com/cosmos/ibc-go/v10/modules/light-clients/07-tendermint"

	cryptotestutil "github.com/allinbits/vaas/testutil/crypto"
	vaastypes "github.com/allinbits/vaas/x/vaas/types"
)

// launchedConsumerGenesis is the consumer genesis a launched consumer stores
// on the provider: the one the provider authored at launch, complete enough
// for the consumer module to accept it, with one validator and the provider
// client and consensus states a new chain starts from.
func launchedConsumerGenesis() vaastypes.ConsumerGenesisState {
	pubKey := cryptotestutil.NewCryptoIdentityFromIntSeed(239668).TMCryptoPubKey()
	valSet := tmtypes.NewValidatorSet([]*tmtypes.Validator{tmtypes.NewValidator(pubKey, 1)})
	clientState := ibctmtypes.NewClientState(
		"provider", ibctmtypes.DefaultTrustLevel,
		time.Duration(1), time.Duration(2), time.Duration(1),
		clienttypes.Height{RevisionNumber: 0, RevisionHeight: 1},
		commitmenttypes.GetSDKSpecs(), []string{"upgrade", "upgradedIBCState"},
	)
	consensusState := ibctmtypes.NewConsensusState(
		time.Unix(1_700_000_000, 0).UTC(), commitmenttypes.NewMerkleRoot([]byte("apphash")), valSet.Hash(),
	)
	return *vaastypes.NewInitialConsumerGenesisState(
		clientState, consensusState, tmtypes.TM2PB.ValidatorUpdates(valSet), vaastypes.DefaultConsumerParams(),
	)
}
