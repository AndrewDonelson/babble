package commands

import (
	"github.com/spf13/cobra"
)

var tx string

// ProxyCmd displays the version of babble being used
func NewProxyCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "proxy",
		Short: "Connect to a proxy",
		RunE:  connectProxy,
	}

	AddProxyFlags(cmd)

	return cmd
}

func connectProxy(cmd *cobra.Command, args []string) error {
	// _, err := ConnectProxy()

	return nil
}

//AddRunFlags adds flags to the Run command
func AddProxyFlags(cmd *cobra.Command) {
	cmd.Flags().IntVar(&config.Node, "node", config.Node, "Node index to connect to (starts from 0)")
	cmd.Flags().BoolVar(&config.Stdin, "stdin", config.Stdin, "Send some transactions from stdin")
	cmd.Flags().StringVar(&tx, "submit", tx, "Tx to submit and quit")
}
