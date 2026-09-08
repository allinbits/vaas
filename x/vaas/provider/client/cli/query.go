package cli

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/allinbits/vaas/x/vaas/provider/types"
	"github.com/spf13/cobra"

	"github.com/cosmos/cosmos-sdk/client"
	"github.com/cosmos/cosmos-sdk/client/flags"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/version"
)

// parseConsumerIdArg parses a CLI positional argument as a uint64 consumer id.
func parseConsumerIdArg(arg string) (uint64, error) {
	cid, err := strconv.ParseUint(arg, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid consumer id %q: %w", arg, err)
	}
	return cid, nil
}

// NewQueryCmd returns a root CLI command handler for all x/vaas/provider query commands.
func NewQueryCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:                        types.ModuleName,
		Short:                      "Querying commands for the VAAS provider module",
		DisableFlagParsing:         true,
		SuggestionsMinimumDistance: 2,
		RunE:                       client.ValidateCmd,
	}

	cmd.AddCommand(CmdConsumerGenesis())
	cmd.AddCommand(CmdConsumerChains())
	cmd.AddCommand(CmdConsumerValidatorKeyAssignment())
	cmd.AddCommand(CmdProviderValidatorKey())
	cmd.AddCommand(CmdAllPairsValConsAddrByConsumer())
	cmd.AddCommand(CmdProviderParameters())
	cmd.AddCommand(CmdConsumerValidators())
	cmd.AddCommand(CmdBlocksUntilNextEpoch())
	cmd.AddCommand(CmdConsumerIdFromClientId())
	cmd.AddCommand(CmdConsumerChain())
	cmd.AddCommand(CmdConsumerGenesisTime())
	cmd.AddCommand(CmdConsumerFeesPerBlock())
	cmd.AddCommand(CmdAllConsumerFeesPerBlockOverrides())
	cmd.AddCommand(CmdConsumerFeePoolClaim())
	cmd.AddCommand(CmdConsumerFeePoolClaims())
	cmd.AddCommand(CmdPendingDowntimeSlashes())
	cmd.AddCommand(CmdWithheldFeeRecords())
	return cmd
}

// CmdConsumerGenesis queries the consumer chain genesis by consumer id.
func CmdConsumerGenesis() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "consumer-genesis [consumer-id]",
		Short: "Query for consumer chain genesis state by consumer id",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			clientCtx, err := client.GetClientQueryContext(cmd)
			if err != nil {
				return err
			}
			queryClient := types.NewQueryClient(clientCtx)

			cid, err := parseConsumerIdArg(args[0])
			if err != nil {
				return err
			}
			req := types.QueryConsumerGenesisRequest{ConsumerId: cid}
			res, err := queryClient.QueryConsumerGenesis(cmd.Context(), &req)
			if err != nil {
				return err
			}

			return clientCtx.PrintProto(&res.GenesisState)
		},
	}

	flags.AddQueryFlagsToCmd(cmd)

	return cmd
}

func CmdConsumerChains() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list-consumer-chains [phase]",
		Short: "Query consumer chains for provider chain.",
		Long: `Query consumer chains for provider chain. An optional
		integer parameter can be passed for phase filtering of consumer chains,
		(Registered=1|Initialized=2|Launched=3|Stopped=4|Deleted=5).`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) (err error) {
			clientCtx, err := client.GetClientQueryContext(cmd)
			if err != nil {
				return err
			}
			queryClient := types.NewQueryClient(clientCtx)

			req := &types.QueryConsumerChainsRequest{}

			if len(args) >= 1 && args[0] != "" {
				phase, err := strconv.ParseInt(args[0], 10, 32)
				if err != nil {
					return err
				}
				req.Phase = types.ConsumerPhase(phase)
			}

			fs, err := client.FlagSetWithPageKeyDecoded(cmd.Flags())
			if err != nil {
				return err
			}

			req.Pagination, err = client.ReadPageRequest(fs)
			if err != nil {
				return err
			}

			res, err := queryClient.QueryConsumerChains(cmd.Context(), req)
			if err != nil {
				return err
			}

			return clientCtx.PrintProto(res)
		},
	}

	flags.AddQueryFlagsToCmd(cmd)
	flags.AddPaginationFlagsToCmd(cmd, "consumer chains")

	return cmd
}

// TODO: fix naming
func CmdConsumerValidatorKeyAssignment() *cobra.Command {
	bech32PrefixConsAddr := sdk.GetConfig().GetBech32ConsensusAddrPrefix()
	cmd := &cobra.Command{
		Use:   "validator-consumer-key [consumerId] [provider-validator-address]",
		Short: "Query assigned validator consensus public key for a consumer chain",
		Long: strings.TrimSpace(
			fmt.Sprintf(`Returns the currently assigned validator consensus public key for a
consumer chain, if one has been assigned.
Example:
$ %s query provider validator-consumer-key 3 %s1gghjut3ccd8ay0zduzj64hwre2fxs9ldmqhffj
`,
				version.AppName, bech32PrefixConsAddr,
			),
		),
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) (err error) {
			clientCtx, err := client.GetClientQueryContext(cmd)
			if err != nil {
				return err
			}
			queryClient := types.NewQueryClient(clientCtx)

			consumerId, err := parseConsumerIdArg(args[0])
			if err != nil {
				return err
			}

			addr, err := sdk.ConsAddressFromBech32(args[1])
			if err != nil {
				return err
			}

			req := &types.QueryValidatorConsumerAddrRequest{
				ConsumerId:      consumerId,
				ProviderAddress: addr.String(),
			}
			res, err := queryClient.QueryValidatorConsumerAddr(cmd.Context(), req)
			if err != nil {
				return err
			}

			return clientCtx.PrintProto(res)
		},
	}

	flags.AddQueryFlagsToCmd(cmd)

	return cmd
}

// TODO: fix naming
func CmdProviderValidatorKey() *cobra.Command {
	bech32PrefixConsAddr := sdk.GetConfig().GetBech32ConsensusAddrPrefix()
	cmd := &cobra.Command{
		Use:   "validator-provider-key [consumer-id] [consumer-validator-address]",
		Short: "Query validator consensus public key for the provider chain",
		Long: strings.TrimSpace(
			fmt.Sprintf(`Returns the currently assigned validator consensus public key for the provider chain.
Example:
$ %s query provider validator-provider-key 333 %s1gghjut3ccd8ay0zduzj64hwre2fxs9ldmqhffj
`,
				version.AppName, bech32PrefixConsAddr,
			),
		),
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) (err error) {
			clientCtx, err := client.GetClientQueryContext(cmd)
			if err != nil {
				return err
			}
			queryClient := types.NewQueryClient(clientCtx)

			consumerID, err := parseConsumerIdArg(args[0])
			if err != nil {
				return err
			}

			addr, err := sdk.ConsAddressFromBech32(args[1])
			if err != nil {
				return err
			}

			req := &types.QueryValidatorProviderAddrRequest{
				ConsumerId:      consumerID,
				ConsumerAddress: addr.String(),
			}
			res, err := queryClient.QueryValidatorProviderAddr(cmd.Context(), req)
			if err != nil {
				return err
			}

			return clientCtx.PrintProto(res)
		},
	}

	flags.AddQueryFlagsToCmd(cmd)

	return cmd
}

func CmdAllPairsValConsAddrByConsumer() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "all-pairs-valconsensus-address [consumer-id]",
		Short: "Query all pairs of valconsensus address by consumer ID.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			clientCtx, err := client.GetClientQueryContext(cmd)
			if err != nil {
				return err
			}
			queryClient := types.NewQueryClient(clientCtx)

			cid, err := parseConsumerIdArg(args[0])
			if err != nil {
				return err
			}
			req := types.QueryAllPairsValConsAddrByConsumerRequest{ConsumerId: cid}
			res, err := queryClient.QueryAllPairsValConsAddrByConsumer(cmd.Context(), &req)
			if err != nil {
				return err
			}

			return clientCtx.PrintProto(res)
		},
	}

	flags.AddQueryFlagsToCmd(cmd)

	return cmd
}

// Command to query provider parameters
func CmdProviderParameters() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "params",
		Short: "Query provider parameters information",
		Long: strings.TrimSpace(
			fmt.Sprintf(`Query parameter values of provider.
Example:
$ %s query provider params
		`, version.AppName),
		),
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			clientCtx, err := client.GetClientQueryContext(cmd)
			if err != nil {
				return err
			}
			queryClient := types.NewQueryClient(clientCtx)

			res, err := queryClient.QueryParams(cmd.Context(),
				&types.QueryParamsRequest{})
			if err != nil {
				return err
			}

			return clientCtx.PrintProto(&res.Params)
		},
	}

	flags.AddQueryFlagsToCmd(cmd)

	return cmd
}

// Command to query the consumer validators by consumer ID
func CmdConsumerValidators() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "consumer-validators [consumer-id]",
		Short: "Query the last set consumer-validator set for a given consumer chain",
		Long: strings.TrimSpace(
			fmt.Sprintf(`Query the last set consumer-validator set for a given consumer chain.
Note that this does not necessarily mean that the consumer chain is currently using this validator set because a VSCPacket could be delayed, etc.
Example:
$ %s consumer-validators 3
		`, version.AppName),
		),
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			clientCtx, err := client.GetClientQueryContext(cmd)
			if err != nil {
				return err
			}
			queryClient := types.NewQueryClient(clientCtx)

			cid, err := parseConsumerIdArg(args[0])
			if err != nil {
				return err
			}
			res, err := queryClient.QueryConsumerValidators(cmd.Context(),
				&types.QueryConsumerValidatorsRequest{ConsumerId: cid})
			if err != nil {
				return err
			}

			return clientCtx.PrintProto(res)
		},
	}

	flags.AddQueryFlagsToCmd(cmd)

	return cmd
}

func CmdBlocksUntilNextEpoch() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "blocks-until-next-epoch",
		Short: "Query the number of blocks until the next epoch begins and validator updates are sent to consumer chains",
		Args:  cobra.ExactArgs(0),
		RunE: func(cmd *cobra.Command, args []string) error {
			clientCtx, err := client.GetClientQueryContext(cmd)
			if err != nil {
				return err
			}
			queryClient := types.NewQueryClient(clientCtx)

			req := &types.QueryBlocksUntilNextEpochRequest{}
			res, err := queryClient.QueryBlocksUntilNextEpoch(cmd.Context(), req)
			if err != nil {
				return err
			}

			return clientCtx.PrintProto(res)
		},
	}

	flags.AddQueryFlagsToCmd(cmd)

	return cmd
}

func CmdConsumerIdFromClientId() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "consumer-id-from-client-id [client-id]",
		Short: "Query the consumer id of the chain associated with the provided client id",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			clientCtx, err := client.GetClientQueryContext(cmd)
			if err != nil {
				return err
			}
			queryClient := types.NewQueryClient(clientCtx)

			req := &types.QueryConsumerIdFromClientIdRequest{ClientId: args[0]}
			res, err := queryClient.QueryConsumerIdFromClientId(cmd.Context(), req)
			if err != nil {
				return err
			}

			return clientCtx.PrintProto(res)
		},
	}

	flags.AddQueryFlagsToCmd(cmd)

	return cmd
}

func CmdConsumerChain() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "consumer-chain [consumer-id]",
		Short: "Query the consumer chain associated with the consumer id",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			clientCtx, err := client.GetClientQueryContext(cmd)
			if err != nil {
				return err
			}
			queryClient := types.NewQueryClient(clientCtx)

			cid, err := parseConsumerIdArg(args[0])
			if err != nil {
				return err
			}
			req := &types.QueryConsumerChainRequest{ConsumerId: cid}
			res, err := queryClient.QueryConsumerChain(cmd.Context(), req)
			if err != nil {
				return err
			}

			return clientCtx.PrintProto(res)
		},
	}

	flags.AddQueryFlagsToCmd(cmd)

	return cmd
}

func CmdConsumerGenesisTime() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "consumer-genesis-time [consumer-id]",
		Short: "Query the genesis time of the consumer chain associated with the consumer id",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			clientCtx, err := client.GetClientQueryContext(cmd)
			if err != nil {
				return err
			}
			queryClient := types.NewQueryClient(clientCtx)

			cid, err := parseConsumerIdArg(args[0])
			if err != nil {
				return err
			}
			req := &types.QueryConsumerGenesisTimeRequest{ConsumerId: cid}
			res, err := queryClient.QueryConsumerGenesisTime(cmd.Context(), req)
			if err != nil {
				return err
			}

			return clientCtx.PrintProto(res)
		},
	}

	flags.AddQueryFlagsToCmd(cmd)

	return cmd
}

// CmdConsumerFeesPerBlock queries the effective per-block fee for a consumer
// (either the per-consumer override, or the module-wide default from params).
func CmdConsumerFeesPerBlock() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "consumer-fees-per-block [consumer-id]",
		Short: "Query the effective per-block fee for a consumer",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			clientCtx, err := client.GetClientQueryContext(cmd)
			if err != nil {
				return err
			}
			queryClient := types.NewQueryClient(clientCtx)

			cid, err := parseConsumerIdArg(args[0])
			if err != nil {
				return err
			}
			req := &types.QueryConsumerFeesPerBlockRequest{ConsumerId: cid}
			res, err := queryClient.QueryConsumerFeesPerBlock(cmd.Context(), req)
			if err != nil {
				return err
			}

			return clientCtx.PrintProto(res)
		},
	}

	flags.AddQueryFlagsToCmd(cmd)

	return cmd
}

// CmdAllConsumerFeesPerBlockOverrides queries all per-consumer fees-per-block overrides.
func CmdAllConsumerFeesPerBlockOverrides() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "all-consumer-fees-per-block-overrides",
		Short: "Query all per-consumer fees-per-block overrides",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			clientCtx, err := client.GetClientQueryContext(cmd)
			if err != nil {
				return err
			}
			queryClient := types.NewQueryClient(clientCtx)

			fs, err := client.FlagSetWithPageKeyDecoded(cmd.Flags())
			if err != nil {
				return err
			}

			pageReq, err := client.ReadPageRequest(fs)
			if err != nil {
				return err
			}

			req := &types.QueryAllConsumerFeesPerBlockOverridesRequest{Pagination: pageReq}
			res, err := queryClient.QueryAllConsumerFeesPerBlockOverrides(cmd.Context(), req)
			if err != nil {
				return err
			}

			return clientCtx.PrintProto(res)
		},
	}

	flags.AddQueryFlagsToCmd(cmd)
	flags.AddPaginationFlagsToCmd(cmd, "consumer fees-per-block overrides")

	return cmd
}

func CmdConsumerFeePoolClaim() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "consumer-fee-pool-claim [consumer-id] [depositor]",
		Short: "Query a depositor's claimable balance on a consumer fee pool",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			clientCtx, err := client.GetClientQueryContext(cmd)
			if err != nil {
				return err
			}
			consumerId, err := parseConsumerIdArg(args[0])
			if err != nil {
				return err
			}
			qc := types.NewQueryClient(clientCtx)
			res, err := qc.ConsumerFeePoolClaim(cmd.Context(), &types.QueryConsumerFeePoolClaimRequest{
				ConsumerId: consumerId,
				Depositor:  args[1],
			})
			if err != nil {
				return err
			}
			return clientCtx.PrintProto(res)
		},
	}
	flags.AddQueryFlagsToCmd(cmd)
	return cmd
}

func CmdConsumerFeePoolClaims() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "consumer-fee-pool-claims [consumer-id]",
		Short: "Query all depositor claims on a consumer fee pool (paginated)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			clientCtx, err := client.GetClientQueryContext(cmd)
			if err != nil {
				return err
			}
			consumerId, err := parseConsumerIdArg(args[0])
			if err != nil {
				return err
			}
			pageReq, err := client.ReadPageRequest(cmd.Flags())
			if err != nil {
				return err
			}
			qc := types.NewQueryClient(clientCtx)
			res, err := qc.ConsumerFeePoolClaims(cmd.Context(), &types.QueryConsumerFeePoolClaimsRequest{
				ConsumerId: consumerId,
				Pagination: pageReq,
			})
			if err != nil {
				return err
			}
			return clientCtx.PrintProto(res)
		},
	}
	flags.AddPaginationFlagsToCmd(cmd, "consumer-fee-pool-claims")
	flags.AddQueryFlagsToCmd(cmd)
	return cmd
}

// CmdPendingDowntimeSlashes queries the pending downtime slashes queued for a
// consumer, awaiting the challenge window before execution.
func CmdPendingDowntimeSlashes() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "pending-downtime-slashes [consumer-id]",
		Short: "Query the pending downtime slashes queued for a consumer",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			clientCtx, err := client.GetClientQueryContext(cmd)
			if err != nil {
				return err
			}
			queryClient := types.NewQueryClient(clientCtx)

			consumerId, err := parseConsumerIdArg(args[0])
			if err != nil {
				return err
			}
			req := &types.QueryPendingDowntimeSlashesRequest{ConsumerId: consumerId}
			res, err := queryClient.QueryPendingDowntimeSlashes(cmd.Context(), req)
			if err != nil {
				return err
			}

			return clientCtx.PrintProto(res)
		},
	}

	flags.AddQueryFlagsToCmd(cmd)

	return cmd
}

// CmdWithheldFeeRecords queries the fee shares currently withheld from
// validators for a consumer due to a pending or executed downtime slash.
func CmdWithheldFeeRecords() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "withheld-fee-records [consumer-id]",
		Short: "Query the fee shares withheld from validators for a consumer",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			clientCtx, err := client.GetClientQueryContext(cmd)
			if err != nil {
				return err
			}
			queryClient := types.NewQueryClient(clientCtx)

			consumerId, err := parseConsumerIdArg(args[0])
			if err != nil {
				return err
			}
			req := &types.QueryWithheldFeeRecordsRequest{ConsumerId: consumerId}
			res, err := queryClient.QueryWithheldFeeRecords(cmd.Context(), req)
			if err != nil {
				return err
			}

			return clientCtx.PrintProto(res)
		},
	}

	flags.AddQueryFlagsToCmd(cmd)

	return cmd
}
