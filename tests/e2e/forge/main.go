// Command consumer-forge is a deliberately lying VAAS consumer node, for the
// byzantine-consumer e2e suite only. It runs the reference consumer app
// unchanged and falsifies one input, who signed each block (see lie), so
// the suite can observe the provider's answer to a consumer that accuses a
// live validator of downtime. It is built only into the forge e2e image and
// offers only `start`; it is not a reference for anything.
package main

import (
	"fmt"
	"io"
	"os"

	abci "github.com/cometbft/cometbft/abci/types"

	dbm "github.com/cosmos/cosmos-db"

	"cosmossdk.io/log"

	"github.com/cosmos/cosmos-sdk/server"
	svrcmd "github.com/cosmos/cosmos-sdk/server/cmd"
	servertypes "github.com/cosmos/cosmos-sdk/server/types"

	"github.com/allinbits/vaas/app/cmd/consumer/cmd"
	consumerapp "github.com/allinbits/vaas/app/consumer"
	appparams "github.com/allinbits/vaas/app/consumer/params"
)

// forgedApp is the reference consumer app with one lie in front of it.
type forgedApp struct {
	*consumerapp.App
	lie lie
}

// FinalizeBlock hands the app the forged view of the block.
func (a forgedApp) FinalizeBlock(req *abci.RequestFinalizeBlock) (*abci.ResponseFinalizeBlock, error) {
	return a.App.FinalizeBlock(a.lie.apply(req))
}

func main() {
	appparams.SetAddressPrefixes("cosmos")

	l, err := lieFromEnv()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	creator := func(logger log.Logger, db dbm.DB, traceStore io.Writer, appOpts servertypes.AppOptions) servertypes.Application {
		app := consumerapp.New(logger, db, traceStore, true, appOpts, server.DefaultBaseappOptions(appOpts)...)
		return forgedApp{App: app, lie: l}
	}

	// The reference root command carries the configuration setup `start`
	// needs; everything but `start` is removed, and `start` runs the forged
	// app.
	rootCmd := cmd.NewRootCmd()
	rootCmd.Use = "consumer-forge"
	for _, sub := range rootCmd.Commands() {
		rootCmd.RemoveCommand(sub)
	}
	rootCmd.AddCommand(server.StartCmd(creator, consumerapp.DefaultNodeHome))

	if err := svrcmd.Execute(rootCmd, "", consumerapp.DefaultNodeHome); err != nil {
		fmt.Fprintln(rootCmd.OutOrStderr(), err)
		os.Exit(1)
	}
}
