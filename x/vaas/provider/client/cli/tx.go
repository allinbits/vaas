package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/allinbits/vaas/x/vaas/provider/types"
	"github.com/spf13/cobra"

	"github.com/cometbft/cometbft/crypto"
	"github.com/cometbft/cometbft/crypto/ed25519"
	tmproto "github.com/cometbft/cometbft/proto/tendermint/types"
	rpchttp "github.com/cometbft/cometbft/rpc/client/http"
	tmtypes "github.com/cometbft/cometbft/types"

	clienttypes "github.com/cosmos/ibc-go/v10/modules/core/02-client/types"
	ibcexported "github.com/cosmos/ibc-go/v10/modules/core/exported"
	ibctmtypes "github.com/cosmos/ibc-go/v10/modules/light-clients/07-tendermint"

	"github.com/cosmos/cosmos-sdk/client"
	"github.com/cosmos/cosmos-sdk/client/flags"
	"github.com/cosmos/cosmos-sdk/client/tx"
	"github.com/cosmos/cosmos-sdk/codec"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/version"
)

// FlagConsumerRPC and FlagTrustedHeight configure
// NewChallengeConsumerDowntimeCmd's assembly of a MsgChallengeConsumerDowntime
// from a live consumer chain RPC endpoint.
const (
	FlagConsumerRPC   = "consumer-rpc"
	FlagTrustedHeight = "trusted-height"
)

// GetTxCmd returns the transaction commands for this module
func GetTxCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:                        types.ModuleName,
		Short:                      fmt.Sprintf("%s transactions subcommands", types.ModuleName),
		DisableFlagParsing:         true,
		SuggestionsMinimumDistance: 2,
		RunE:                       client.ValidateCmd,
	}

	cmd.AddCommand(NewAssignConsumerKeyCmd())
	cmd.AddCommand(NewSubmitConsumerMisbehaviourCmd())
	cmd.AddCommand(NewSubmitConsumerDoubleVotingCmd())
	cmd.AddCommand(NewCreateConsumerCmd())
	cmd.AddCommand(NewUpdateConsumerCmd())
	cmd.AddCommand(NewFundConsumerFeePoolCmd())
	cmd.AddCommand(NewWithdrawConsumerFeePoolCmd())
	cmd.AddCommand(NewSweepConsumerFeePoolCmd())
	cmd.AddCommand(NewChallengeConsumerDowntimeCmd())

	return cmd
}

func NewAssignConsumerKeyCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "assign-consensus-key [consumer-id] [consumer-pubkey]",
		Short: "assign a consensus public key to use for a consumer chain",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			clientCtx, err := client.GetClientTxContext(cmd)
			if err != nil {
				return err
			}

			submitter := clientCtx.GetFromAddress().String()
			txf, err := tx.NewFactoryCLI(clientCtx, cmd.Flags())
			if err != nil {
				return err
			}
			txf = txf.WithTxConfig(clientCtx.TxConfig).WithAccountRetriever(clientCtx.AccountRetriever)

			providerValAddr := clientCtx.GetFromAddress()

			cid, err := parseConsumerIdArg(args[0])
			if err != nil {
				return err
			}
			msg, err := types.NewMsgAssignConsumerKey(cid, sdk.ValAddress(providerValAddr), args[1], submitter)
			if err != nil {
				return err
			}
			if err := msg.ValidateBasic(); err != nil {
				return err
			}

			return tx.GenerateOrBroadcastTxWithFactory(clientCtx, txf, msg)
		},
	}

	flags.AddTxFlagsToCmd(cmd)

	_ = cmd.MarkFlagRequired(flags.FlagFrom)

	return cmd
}

func NewSubmitConsumerMisbehaviourCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "submit-consumer-misbehaviour [consumer-id] [misbehaviour]",
		Short: "submit an IBC misbehaviour for a consumer chain",
		Long: strings.TrimSpace(
			fmt.Sprintf(`Submit an IBC misbehaviour detected on a consumer chain.
An IBC misbehaviour contains two conflicting IBC client headers, which are used to form a light client attack evidence.
The misbehaviour type definition can be found in the IBC client messages, see ibc-go/proto/ibc/core/client/v1/tx.proto.

Example:
%s tx provider submit-consumer-misbehaviour [consumer-id] [path/to/misbehaviour.json]
			`, version.AppName)),
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			clientCtx, err := client.GetClientTxContext(cmd)
			if err != nil {
				return err
			}

			txf, err := tx.NewFactoryCLI(clientCtx, cmd.Flags())
			if err != nil {
				return err
			}
			txf = txf.WithTxConfig(clientCtx.TxConfig).WithAccountRetriever(clientCtx.AccountRetriever)

			submitter := clientCtx.GetFromAddress()
			misbJson, err := os.ReadFile(args[1])
			if err != nil {
				return err
			}

			cdc := codec.NewProtoCodec(clientCtx.InterfaceRegistry)

			misbehaviour := ibctmtypes.Misbehaviour{}
			if err := cdc.UnmarshalJSON(misbJson, &misbehaviour); err != nil {
				return fmt.Errorf("misbehaviour unmarshalling failed: %s", err)
			}

			cid, err := parseConsumerIdArg(args[0])
			if err != nil {
				return err
			}
			msg, err := types.NewMsgSubmitConsumerMisbehaviour(cid, submitter, &misbehaviour)
			if err != nil {
				return err
			}
			if err := msg.ValidateBasic(); err != nil {
				return err
			}

			return tx.GenerateOrBroadcastTxWithFactory(clientCtx, txf, msg)
		},
	}

	flags.AddTxFlagsToCmd(cmd)

	_ = cmd.MarkFlagRequired(flags.FlagFrom)

	return cmd
}

func NewSubmitConsumerDoubleVotingCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "submit-consumer-double-voting [consumer-id] [evidence] [infraction_header]",
		Short: "submit a double voting evidence for a consumer chain",
		Long: strings.TrimSpace(
			fmt.Sprintf(`Submit a Tendermint duplicate vote evidence detected on a consumer chain with
 the IBC light client header for the infraction height.
 The DuplicateVoteEvidence type definition can be found in the Tendermint messages,
 see cometbft/proto/tendermint/types/evidence.proto and the IBC header
 definition can be found in the IBC messages, see ibc-go/proto/ibc/lightclients/tendermint/v1/tendermint.proto.

Example:
%s tx provider submit-consumer-double-voting [consumer-id] [path/to/evidence.json] [path/to/infraction_header.json]
`, version.AppName)),
		Args: cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			clientCtx, err := client.GetClientTxContext(cmd)
			if err != nil {
				return err
			}

			txf, err := tx.NewFactoryCLI(clientCtx, cmd.Flags())
			if err != nil {
				return err
			}
			txf = txf.WithTxConfig(clientCtx.TxConfig).WithAccountRetriever(clientCtx.AccountRetriever)

			submitter := clientCtx.GetFromAddress()
			cdc := codec.NewProtoCodec(clientCtx.InterfaceRegistry)

			evidenceJson, err := os.ReadFile(args[1])
			if err != nil {
				return err
			}

			ev := tmproto.DuplicateVoteEvidence{}
			if err := cdc.UnmarshalJSON(evidenceJson, &ev); err != nil {
				return fmt.Errorf("duplicate vote evidence unmarshalling failed: %s", err)
			}

			headerJson, err := os.ReadFile(args[2])
			if err != nil {
				return err
			}

			header := ibctmtypes.Header{}
			if err := cdc.UnmarshalJSON(headerJson, &header); err != nil {
				return fmt.Errorf("infraction IBC header unmarshalling failed: %s", err)
			}

			cid, err := parseConsumerIdArg(args[0])
			if err != nil {
				return err
			}
			msg, err := types.NewMsgSubmitConsumerDoubleVoting(cid, submitter, &ev, &header)
			if err != nil {
				return err
			}
			if err := msg.ValidateBasic(); err != nil {
				return err
			}

			return tx.GenerateOrBroadcastTxWithFactory(clientCtx, txf, msg)
		},
	}

	flags.AddTxFlagsToCmd(cmd)

	_ = cmd.MarkFlagRequired(flags.FlagFrom)

	return cmd
}

func NewCreateConsumerCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "create-consumer [consumer-parameters]",
		Short: "create a consumer chain",
		Long: strings.TrimSpace(
			fmt.Sprintf(`Create a consumer chain and get the assigned consumer id of this chain.
Note that the one that signs this message is the owner of this consumer chain. The owner can be later
changed by updating the consumer chain.

Example:
%s tx provider create-consumer [path/to/create_consumer.json]

where create_consumer.json has the following structure:
{
  "chain_id": "consumer-1",
  "metadata": {
    "name": "chain consumer",
    "description": "description",
    "metadata": "{\"forge_json_url\": \"...\", \"stage\": \"mainnet\"}"
  },
  "initialization_parameters": {
    "initial_height": {
     "revision_number": 1,
     "revision_height": 1
    },
    "genesis_hash": "",
    "binary_hash": "",
    "spawn_time": "2024-08-29T12:26:16.529913Z",
    "unbonding_period": 1728000000000000,
    "ccv_timeout_period": 2419200000000000,
    "historical_entries": 10000
  }
}

Note that both 'chain_id' and 'metadata' are mandatory;
and 'initialization_parameters' is optional.
The parameters not provided are set to their zero value.
`, version.AppName)),
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			clientCtx, err := client.GetClientTxContext(cmd)
			if err != nil {
				return err
			}

			txf, err := tx.NewFactoryCLI(clientCtx, cmd.Flags())
			if err != nil {
				return err
			}
			txf = txf.WithTxConfig(clientCtx.TxConfig).WithAccountRetriever(clientCtx.AccountRetriever)

			submitter := clientCtx.GetFromAddress().String()

			consCreateJson, err := os.ReadFile(args[0])
			if err != nil {
				return err
			}
			consCreate := types.MsgCreateConsumer{}
			if err = json.Unmarshal(consCreateJson, &consCreate); err != nil {
				return fmt.Errorf("consumer data unmarshalling failed: %w", err)
			}

			msg, err := types.NewMsgCreateConsumer(submitter, consCreate.ChainId, consCreate.Metadata, consCreate.InitializationParameters)
			if err != nil {
				return err
			}
			if err = msg.ValidateBasic(); err != nil {
				return err
			}

			return tx.GenerateOrBroadcastTxWithFactory(clientCtx, txf, msg)
		},
	}

	flags.AddTxFlagsToCmd(cmd)

	_ = cmd.MarkFlagRequired(flags.FlagFrom)

	return cmd
}

func NewUpdateConsumerCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "update-consumer [consumer-parameters]",
		Short: "update a consumer chain",
		Long: strings.TrimSpace(
			fmt.Sprintf(`Update a consumer chain to change its parameters (e.g., spawn time, infraction parameters, etc.).
Note that only the owner of the chain can initialize it.

Example:
%s tx provider update-consumer [path/to/update_consumer.json]

where update_consumer.json has the following structure:
{
   "consumer_id": "0",
   "new_owner_address": "cosmos10d07y265gmmuvt4z0w9aw880jnsr700j6zn9kn",
   "metadata": {
    "name": "chain consumer",
    "description": "description",
    "metadata": "{\"forge_json_url\": \"...\", \"stage\": \"mainnet\"}"
   },
   "initialization_parameters": {
    "initial_height": {
     "revision_number": 1,
     "revision_height": 1
    },
    "genesis_hash": "",
    "binary_hash": "",
    "spawn_time": "2024-08-29T12:26:16.529913Z",
    "unbonding_period": 1728000000000000,
    "ccv_timeout_period": 2419200000000000,
    "historical_entries": 10000
   },
   "new_chain_id": "newConsumer-1" // is optional and can be empty (i.e., "new_chain_id": "")
}

Note that only 'consumer_id' is mandatory. The others are optional.
Not providing one of them will leave the existing values unchanged.
Providing one of 'metadata' or 'initialization_parameters'
will update all the containing fields.
If one of the fields is missing, it will be set to its zero value.
`, version.AppName)),
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			clientCtx, err := client.GetClientTxContext(cmd)
			if err != nil {
				return err
			}

			txf, err := tx.NewFactoryCLI(clientCtx, cmd.Flags())
			if err != nil {
				return err
			}
			txf = txf.WithTxConfig(clientCtx.TxConfig).WithAccountRetriever(clientCtx.AccountRetriever)

			owner := clientCtx.GetFromAddress().String()

			consUpdateJson, err := os.ReadFile(args[0])
			if err != nil {
				return err
			}

			consUpdate := types.MsgUpdateConsumer{}
			if err = json.Unmarshal(consUpdateJson, &consUpdate); err != nil {
				return fmt.Errorf("consumer data unmarshalling failed: %w", err)
			}

			msg, err := types.NewMsgUpdateConsumer(owner, consUpdate.ConsumerId, consUpdate.NewOwnerAddress, consUpdate.Metadata,
				consUpdate.InitializationParameters, consUpdate.NewChainId)
			if err != nil {
				return err
			}
			if err := msg.ValidateBasic(); err != nil {
				return err
			}

			return tx.GenerateOrBroadcastTxWithFactory(clientCtx, txf, msg)
		},
	}

	flags.AddTxFlagsToCmd(cmd)

	_ = cmd.MarkFlagRequired(flags.FlagFrom)

	return cmd
}

func NewFundConsumerFeePoolCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "fund-consumer-fee-pool [consumer-id] [amount]",
		Short: "Deposit funds into a consumer's fee pool, crediting the signer with shares",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			clientCtx, err := client.GetClientTxContext(cmd)
			if err != nil {
				return err
			}
			consumerId, err := parseConsumerIdArg(args[0])
			if err != nil {
				return err
			}
			amount, err := sdk.ParseCoinNormalized(args[1])
			if err != nil {
				return err
			}
			msg := &types.MsgFundConsumerFeePool{
				Signer:     clientCtx.GetFromAddress().String(),
				ConsumerId: consumerId,
				Amount:     amount,
			}
			if err := msg.ValidateBasic(); err != nil {
				return err
			}
			return tx.GenerateOrBroadcastTxCLI(clientCtx, cmd.Flags(), msg)
		},
	}
	flags.AddTxFlagsToCmd(cmd)
	_ = cmd.MarkFlagRequired(flags.FlagFrom)
	return cmd
}

func NewWithdrawConsumerFeePoolCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "withdraw-consumer-fee-pool [consumer-id] [coins]",
		Short: "Withdraw tokens from your share in a consumer's fee pool (multi-denom)",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			clientCtx, err := client.GetClientTxContext(cmd)
			if err != nil {
				return err
			}
			consumerId, err := parseConsumerIdArg(args[0])
			if err != nil {
				return err
			}
			coins, err := sdk.ParseCoinsNormalized(args[1])
			if err != nil {
				return err
			}
			msg := &types.MsgWithdrawConsumerFeePool{
				Signer:     clientCtx.GetFromAddress().String(),
				ConsumerId: consumerId,
				Amount:     coins,
			}
			if err := msg.ValidateBasic(); err != nil {
				return err
			}
			return tx.GenerateOrBroadcastTxCLI(clientCtx, cmd.Flags(), msg)
		},
	}
	flags.AddTxFlagsToCmd(cmd)
	_ = cmd.MarkFlagRequired(flags.FlagFrom)
	return cmd
}

func NewSweepConsumerFeePoolCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sweep-consumer-fee-pool [consumer-id]",
		Short: "Owner-triggered pro-rata distribution of a consumer's fee pool",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			clientCtx, err := client.GetClientTxContext(cmd)
			if err != nil {
				return err
			}
			consumerId, err := parseConsumerIdArg(args[0])
			if err != nil {
				return err
			}
			denoms, _ := cmd.Flags().GetStringSlice("denoms")
			msg := &types.MsgSweepConsumerFeePool{
				Signer:     clientCtx.GetFromAddress().String(),
				ConsumerId: consumerId,
				Denoms:     denoms,
			}
			if err := msg.ValidateBasic(); err != nil {
				return err
			}
			return tx.GenerateOrBroadcastTxCLI(clientCtx, cmd.Flags(), msg)
		},
	}
	cmd.Flags().StringSlice("denoms", nil,
		"denoms to sweep; repeat flag or pass comma-separated (default: all denoms with shares or balance)")
	flags.AddTxFlagsToCmd(cmd)
	_ = cmd.MarkFlagRequired(flags.FlagFrom)
	return cmd
}

// NewChallengeConsumerDowntimeCmd builds and broadcasts a
// MsgChallengeConsumerDowntime by fetching, from the consumer chain's own
// CometBFT RPC endpoint, everything HandleChallengeConsumerDowntime needs to
// verify a challenge (see docs/consumer-downtime.md, "The challenge"): the
// canonical commit for the claimed height H (/commit?height=H), the signed
// header for H+1 (/commit?height=H+1), the validator set matching that header
// (/validators?height=H+1), and the accused validator's self-authenticating
// pubkey (/validators?height=H). This is client-side tooling: it trusts the
// consumer RPC endpoint to answer honestly, exactly as any light client or
// relayer does -- the assembled message is still independently verified
// on-chain by HandleChallengeConsumerDowntime, so a dishonest RPC endpoint
// can at worst waste the challenger's gas, never forge a false challenge.
func NewChallengeConsumerDowntimeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "challenge-consumer-downtime [consumer-id] [validator-cons-addr] [claimed-height]",
		Short: "Challenge a pending downtime slash by proving the validator signed the claimed height",
		Long: strings.TrimSpace(fmt.Sprintf(`Challenge a pending downtime slash by proving the accused validator
signed the claimed height H: this command fetches the canonical commit for
H, the light-client header for H+1, and the relevant validator sets from
the consumer chain's own RPC endpoint (--%s), then assembles and broadcasts
MsgChallengeConsumerDowntime.

The H+1 header's trusted height defaults to the provider's currently
tracked latest height for this consumer's IBC client (queried on-chain);
pass --%s to override it, e.g. if that client's light-client module trails
behind the consumer chain tip.

Example:
%s tx provider challenge-consumer-downtime 0 cosmosvalcons1... 12345 --%s http://consumer-rpc:26657
`, FlagConsumerRPC, FlagTrustedHeight, version.AppName, FlagConsumerRPC)),
		Args: cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			clientCtx, err := client.GetClientTxContext(cmd)
			if err != nil {
				return err
			}

			consumerId, err := parseConsumerIdArg(args[0])
			if err != nil {
				return err
			}
			consAddr, err := sdk.ConsAddressFromBech32(args[1])
			if err != nil {
				return fmt.Errorf("invalid validator-cons-addr %q: %w", args[1], err)
			}
			claimedHeight, err := strconv.ParseInt(args[2], 10, 64)
			if err != nil {
				return fmt.Errorf("invalid claimed-height %q: %w", args[2], err)
			}

			consumerRPC, err := cmd.Flags().GetString(FlagConsumerRPC)
			if err != nil {
				return err
			}
			if consumerRPC == "" {
				return fmt.Errorf("--%s is required", FlagConsumerRPC)
			}

			rpcClient, err := rpchttp.New(consumerRPC, "/websocket")
			if err != nil {
				return fmt.Errorf("connecting to consumer rpc %s: %w", consumerRPC, err)
			}

			ctx := cmd.Context()
			nextHeight := claimedHeight + 1

			// The canonical commit for H: /commit?height=H's SignedHeader.Commit
			// -- this is the commit sealed into H+1's header, not H's own
			// self-reported, malleable SignedHeader.Commit.
			commitH, err := rpcClient.Commit(ctx, &claimedHeight)
			if err != nil {
				return fmt.Errorf("fetching commit at height %d: %w", claimedHeight, err)
			}

			// The signed header for H+1, becoming the light-client header's
			// SignedHeader.
			commitH1, err := rpcClient.Commit(ctx, &nextHeight)
			if err != nil {
				return fmt.Errorf("fetching commit at height %d: %w", nextHeight, err)
			}

			// The validator set at H+1, matching the header being submitted.
			valsH1, err := fetchAllValidators(ctx, rpcClient, nextHeight)
			if err != nil {
				return fmt.Errorf("fetching validators at height %d: %w", nextHeight, err)
			}
			valSetH1, err := tmtypes.NewValidatorSet(valsH1).ToProto()
			if err != nil {
				return fmt.Errorf("converting validator set at height %d: %w", nextHeight, err)
			}

			// The accused validator's self-authenticating pubkey, read from the
			// validator set at H (matching consAddr).
			valsH, err := fetchAllValidators(ctx, rpcClient, claimedHeight)
			if err != nil {
				return fmt.Errorf("fetching validators at height %d: %w", claimedHeight, err)
			}
			var validatorPubKey crypto.PubKey
			for _, v := range valsH {
				if bytes.Equal(v.Address, consAddr.Bytes()) {
					validatorPubKey = v.PubKey
					break
				}
			}
			if validatorPubKey == nil {
				return fmt.Errorf("validator %s not found in the validator set at height %d", consAddr, claimedHeight)
			}
			if _, ok := validatorPubKey.(ed25519.PubKey); !ok {
				return fmt.Errorf("validator %s's pubkey is not ed25519, the only key type HandleChallengeConsumerDowntime accepts", consAddr)
			}

			// The trusted height for the H+1 header: --trusted-height if given,
			// else the provider's currently tracked latest height for this
			// consumer's IBC client. The client state is queried regardless, to
			// source the revision number for the trusted height.
			chainResp, err := types.NewQueryClient(clientCtx).QueryConsumerChain(ctx, &types.QueryConsumerChainRequest{ConsumerId: consumerId})
			if err != nil {
				return fmt.Errorf("looking up client id for consumer %d: %w", consumerId, err)
			}
			if chainResp.ClientId == "" {
				return fmt.Errorf("consumer %d has no registered IBC client", consumerId)
			}
			clientStateResp, err := clienttypes.NewQueryClient(clientCtx).ClientState(ctx, &clienttypes.QueryClientStateRequest{ClientId: chainResp.ClientId})
			if err != nil {
				return fmt.Errorf("querying client state for client %s: %w", chainResp.ClientId, err)
			}
			var clientStateI ibcexported.ClientState
			if err := clientCtx.InterfaceRegistry.UnpackAny(clientStateResp.ClientState, &clientStateI); err != nil {
				return fmt.Errorf("unpacking client state for client %s: %w", chainResp.ClientId, err)
			}
			tmClientState, ok := clientStateI.(*ibctmtypes.ClientState)
			if !ok {
				return fmt.Errorf("client %s is not a tendermint client", chainResp.ClientId)
			}

			trustedHeight := tmClientState.LatestHeight
			if h, err := cmd.Flags().GetUint64(FlagTrustedHeight); err == nil && h != 0 {
				trustedHeight = clienttypes.NewHeight(tmClientState.LatestHeight.RevisionNumber, h)
			}

			trustedVals, err := fetchAllValidators(ctx, rpcClient, int64(trustedHeight.RevisionHeight))
			if err != nil {
				return fmt.Errorf("fetching validators at trusted height %d: %w", trustedHeight.RevisionHeight, err)
			}
			trustedValSet, err := tmtypes.NewValidatorSet(trustedVals).ToProto()
			if err != nil {
				return fmt.Errorf("converting validator set at trusted height %d: %w", trustedHeight.RevisionHeight, err)
			}

			header := &ibctmtypes.Header{
				SignedHeader:      commitH1.SignedHeader.ToProto(),
				ValidatorSet:      valSetH1,
				TrustedHeight:     trustedHeight,
				TrustedValidators: trustedValSet,
			}

			msg := &types.MsgChallengeConsumerDowntime{
				Signer:          clientCtx.GetFromAddress().String(),
				ConsumerId:      consumerId,
				ValidatorAddr:   consAddr.Bytes(),
				ClaimedHeight:   claimedHeight,
				Header:          header,
				LastCommit:      commitH.Commit.ToProto(),
				ValidatorPubkey: validatorPubKey.Bytes(),
			}
			if err := msg.ValidateBasic(); err != nil {
				return err
			}

			return tx.GenerateOrBroadcastTxCLI(clientCtx, cmd.Flags(), msg)
		},
	}

	cmd.Flags().String(FlagConsumerRPC, "", "CometBFT RPC endpoint of the consumer chain (required)")
	cmd.Flags().Uint64(FlagTrustedHeight, 0,
		"trusted height for the H+1 light-client header (default: the provider's tracked latest height for this consumer's client)")
	flags.AddTxFlagsToCmd(cmd)
	_ = cmd.MarkFlagRequired(flags.FlagFrom)
	_ = cmd.MarkFlagRequired(FlagConsumerRPC)

	return cmd
}

// fetchAllValidators pages through the consumer RPC's /validators endpoint
// to return the full validator set at height, rather than just its first
// (bounded-size) page.
func fetchAllValidators(ctx context.Context, rpcClient *rpchttp.HTTP, height int64) ([]*tmtypes.Validator, error) {
	var vals []*tmtypes.Validator
	page := 1
	const perPage = 100
	for {
		p, pp := page, perPage
		res, err := rpcClient.Validators(ctx, &height, &p, &pp)
		if err != nil {
			return nil, err
		}
		vals = append(vals, res.Validators...)
		if len(vals) >= res.Total {
			return vals, nil
		}
		page++
	}
}
