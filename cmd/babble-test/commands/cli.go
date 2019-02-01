package commands

import (
	"os"
	"os/signal"

	runtime "github.com/mosaicnetworks/babble/cmd/babble-test/lib"
	"github.com/spf13/cobra"
)

//NewCliCmd starts a prompt to interact with running nodes
func NewCliCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "cli",
		Short:   "Interactive prompt",
		PreRunE: loadConfig,
		RunE: func(cmd *cobra.Command, args []string) error {
			run := runtime.New(config.Babble, config.NbNodes, config.SendTxs)

			signalChan := make(chan os.Signal, 1)

			signal.Notify(signalChan, os.Interrupt)

			return run.Start()
		},
	}

	AddCliFlags(cmd)

	return cmd
}

/*******************************************************************************
* CONFIG
*******************************************************************************/

//AddCliFlags adds flags to the Run command
func AddCliFlags(cmd *cobra.Command) {
	cmd.Flags().Int("nodes", config.NbNodes, "Amount of nodes to spawn")
	cmd.Flags().String("datadir", config.Babble.DataDir, "Top-level directory for configuration and data")
	cmd.Flags().String("log", config.Babble.LogLevel, "debug, info, warn, error, fatal, panic")
	cmd.Flags().Duration("heartbeat", config.Babble.NodeConfig.HeartbeatTimeout, "Time between gossips")

	cmd.Flags().Int("sync-limit", config.Babble.NodeConfig.SyncLimit, "Max number of events for sync")
	cmd.Flags().Int("send-txs", config.SendTxs, "Send some random transactions")
}
