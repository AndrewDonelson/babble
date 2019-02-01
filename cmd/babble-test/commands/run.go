package commands

import (
	runtime "github.com/mosaicnetworks/babble/cmd/babble-test/lib"
	"github.com/mosaicnetworks/babble/src/babble"
	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

//NewRunCmd returns the command that starts a Babble node
func NewRunCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "run",
		Short:   "Run a network of nodes",
		PreRunE: loadConfig,
		RunE: func(cmd *cobra.Command, args []string) error {
			run := runtime.New(config.Babble, config.NbNodes, config.SendTxs)

			// signalChan := make(chan os.Signal, 1)

			// signal.Notify(signalChan, os.Interrupt)

			if err := run.RunBabbles(); err != nil {
				return nil
			}

			return run.Wait()
		},
	}

	AddRunFlags(cmd)

	return cmd
}

/*******************************************************************************
* CONFIG
*******************************************************************************/

//AddRunFlags adds flags to the Run command
func AddRunFlags(cmd *cobra.Command) {
	cmd.Flags().Int("nodes", config.NbNodes, "Amount of nodes to spawn")
	cmd.Flags().String("datadir", config.Babble.DataDir, "Top-level directory for configuration and data")
	cmd.Flags().String("log", config.Babble.LogLevel, "debug, info, warn, error, fatal, panic")
	cmd.Flags().Duration("heartbeat", config.Babble.NodeConfig.HeartbeatTimeout, "Time between gossips")

	cmd.Flags().Int("sync-limit", config.Babble.NodeConfig.SyncLimit, "Max number of events for sync")
	cmd.Flags().Int("send-txs", config.SendTxs, "Send some random transactions")
}

func loadConfig(cmd *cobra.Command, args []string) error {

	err := bindFlagsLoadViper(cmd)
	if err != nil {
		return err
	}

	config, err = parseConfig()
	if err != nil {
		return err
	}

	config.Babble.Logger.Level = babble.LogLevel(config.Babble.LogLevel)
	config.Babble.NodeConfig.Logger = config.Babble.Logger

	return nil
}

//Bind all flags and read the config into viper
func bindFlagsLoadViper(cmd *cobra.Command) error {
	// cmd.Flags() includes flags from this command and all persistent flags from the parent
	if err := viper.BindPFlags(cmd.Flags()); err != nil {
		return err
	}

	viper.SetConfigName("babble")              // name of config file (without extension)
	viper.AddConfigPath(config.Babble.DataDir) // search root directory
	// viper.AddConfigPath(filepath.Join(config.Babble.DataDir, "babble")) // search root directory /config

	// If a config file is found, read it in.
	if err := viper.ReadInConfig(); err == nil {
		config.Babble.Logger.Debugf("Using config file: %s", viper.ConfigFileUsed())
	} else if _, ok := err.(viper.ConfigFileNotFoundError); ok {
		config.Babble.Logger.Debugf("No config file found in: %s", config.Babble.DataDir)
	} else {
		return err
	}

	return nil
}

//Retrieve the default environment configuration.
func parseConfig() (*CLIConfig, error) {
	conf := NewDefaultCLIConfig()
	err := viper.Unmarshal(conf)
	if err != nil {
		return nil, err
	}
	return conf, err
}

func logLevel(l string) logrus.Level {
	switch l {
	case "debug":
		return logrus.DebugLevel
	case "info":
		return logrus.InfoLevel
	case "warn":
		return logrus.WarnLevel
	case "error":
		return logrus.ErrorLevel
	case "fatal":
		return logrus.FatalLevel
	case "panic":
		return logrus.PanicLevel
	default:
		return logrus.DebugLevel
	}
}