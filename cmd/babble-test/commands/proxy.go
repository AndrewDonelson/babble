package commands

import (
	"bufio"
	"bytes"
	"encoding/gob"
	"fmt"
	"net"
	"os"

	runtime "github.com/mosaicnetworks/babble/cmd/babble-test/lib"
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
	conn, err := net.Dial("tcp", "localhost:9000")

	if err != nil {
		fmt.Println("Error connect:", err.Error())

		os.Exit(1)
	}

	defer conn.Close()

	go func() {
		buf := make([]byte, 1024)

		for {
			reqLen, err := conn.Read(buf)

			if err != nil || reqLen == 0 {
				fmt.Println("Error reading:", err.Error())

				os.Exit(1)
			}

			fmt.Println(string(buf[:reqLen]))
		}
	}()

	scanner := bufio.NewScanner(os.Stdin)
	scanner.Split(bufio.ScanLines)

	if err := send(conn, []byte{}); err != nil {
		fmt.Println("Error")

		return err
	}

	for {
		if !scanner.Scan() {
			return nil
		}

		ln := scanner.Text()

		if err := send(conn, []byte(ln)); err != nil {
			fmt.Println("Error")

			return err
		}
	}

	return nil
}

func send(conn net.Conn, tx []byte) error {
	msg := runtime.Packet{
		NodeId:  config.Node,
		Message: tx,
	}

	var res bytes.Buffer

	enc := gob.NewEncoder(&res)

	if err := enc.Encode(&msg); err != nil {
		fmt.Println("Error reading:", err.Error())

		return err
	}

	conn.Write(res.Bytes())

	return nil
}

//AddRunFlags adds flags to the Run command
func AddProxyFlags(cmd *cobra.Command) {
	cmd.Flags().IntVar(&config.Node, "node", config.Node, "Node index to connect to (starts from 0)")
}
