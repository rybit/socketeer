package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"

	"github.com/Sirupsen/logrus"
	io "github.com/graarh/golang-socketio"
	"github.com/graarh/golang-socketio/transport"
	"github.com/spf13/cobra"
)

func main() {
	rootCmd := cobra.Command{
		Use:  "socketeer endpoint deploy_id token",
		RunE: run,
	}

	rootCmd.Flags().BoolP("verbose", "v", false, "if we should log out the debug")

	if err := rootCmd.Execute(); err != nil {
		log.Fatal("failed to run: " + err.Error())
	}
}

func run(cmd *cobra.Command, args []string) error {
	if len(args) != 3 {
		return errors.New("require endpoint, deploy_id, endpoint")
	}

	endpoint := args[0]
	deployID := args[1]
	token := args[2]
	if verbose, _ := cmd.Flags().GetBool("verbose"); verbose {
		logrus.SetLevel(logrus.DebugLevel)
	}

	l := logrus.WithField("endpoint", endpoint).WithField("deploy_id", deployID)
	l.Debug("connecting")
	defaultTransport := transport.GetDefaultWebsocketTransport()
	client, err := io.Dial(endpoint, defaultTransport)
	if err != nil {
		l.WithError(err).Fatal("Failed to connect to endpoint")
	}
	l.Debug("connected")

	l.Debug("listening on deploylog:request")
	innie := make(chan map[string]interface{})
	client.On("deploylog:entry", func(sock *io.Channel, input map[string]interface{}) {
		innie <- input
	})

	req := map[string]string{
		"deploy_id":  deployID,
		"auth_token": token,
	}
	l.Debug("making request")
	client.Emit("deploylog:request", req)

	l.Debug("waiting for messages")
	for in := range innie {
		bs, _ := json.MarshalIndent(&in, "", "  ")
		fmt.Println(string(bs))
		if complete, ok := in["completed"]; ok && complete.(bool) {
			l.Debug("completed the log")
			close(innie)
		}
	}
	l.Debug("shut down")
	return nil
}
