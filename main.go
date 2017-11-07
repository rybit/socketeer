package main

import (
	"bufio"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/Sirupsen/logrus"
	io "github.com/graarh/golang-socketio"
	"github.com/graarh/golang-socketio/transport"
	"github.com/spf13/cobra"
)

type args struct {
	debug     bool
	url       string
	authToken string
}

func main() {
	a := new(args)
	tailDeploy := cobra.Command{
		Use: "deploy [deployID]",
		RunE: func(_ *cobra.Command, args []string) error {
			var deployID string
			if len(args) > 0 {
				deployID = args[0]
			}
			return tailDeploy(a, deployID)
		},
	}

	tailFunc := cobra.Command{
		Use: "func [siteID] [funcname] [host]",
		RunE: func(_ *cobra.Command, args []string) error {
			var siteID, host, funcName string
			switch len(args) {
			case 3:
				host = args[2]
				fallthrough
			case 2:
				funcName = args[1]
				fallthrough
			case 1:
				siteID = args[0]
			}
			return tailFunc(a, siteID, funcName, host)
		},
	}

	root := new(cobra.Command)
	root.AddCommand(&tailDeploy, &tailFunc)

	root.PersistentFlags().BoolVarP(&a.debug, "debug", "D", false, "enable debug logging")
	root.PersistentFlags().StringVarP(&a.authToken, "auth", "A", "", "auth token to include")
	root.PersistentFlags().StringVarP(&a.url, "url", "U", "ws://localhost:9090", "the url to connect to")

	if c, err := root.ExecuteC(); err != nil {
		log.Fatalf("Failed to execute command %s - %s", c.Name(), err.Error())
	}
}

func tailFunc(a *args, siteID, funcName, host string) error {
	log, client, err := setup(a)
	if err != nil {
		return err
	}

	funcName = stringOr(funcName, "Enter function name: ")
	siteID = stringOr(siteID, "Enter site ID: ")
	host = stringOr(host, "Enter host: ")
	a.authToken = stringOr(a.authToken, "Enter auth token: ")
	log = log.WithFields(logrus.Fields{
		"func": funcName,
		"site": siteID,
		"host": host,
	})

	count := 0
	log.Infof("listening for messages for messages: funclog:entry")
	client.On("funclog:entry", func(c *io.Channel, something interface{}) {
		log.Infof("%d - %+v", count, something)
		count++
	})

	// send a command for a deploy log
	log.Info("Sending request to deploylog:request")
	req := map[string]string{
		"auth_token": a.authToken,
		"site_id":    siteID,
		"func_name":  funcName,
		"host":       host,
	}
	if err := client.Emit("funclog:request", req); err != nil {
		log.WithError(err).Fatal("Failed to request log")
	}

	select {}
}

func tailDeploy(a *args, deployID string) error {
	log, client, err := setup(a)
	if err != nil {
		return err
	}
	deployID = stringOr(deployID, "Enter deploy ID: ")
	a.authToken = stringOr(a.authToken, "Enter auth token: ")
	log = log.WithField("deploy_id", deployID)

	log.Infof("listeneing for messages for messages: deploylog:entry")
	count := 0
	client.On("deploylog:entry", func(c *io.Channel, something interface{}) {
		log.Infof("%d - %+v", count, something)
		count++
	})

	// send a command for a deploy log
	log.Info("Sending request to deploylog:request")
	req := map[string]string{
		"deploy_id":  deployID,
		"auth_token": a.authToken,
	}
	if err := client.Emit("deploylog:request", req); err != nil {
		log.WithError(err).Fatal("Failed to request log")
	}

	select {}
}

func setup(a *args) (*logrus.Entry, *io.Client, error) {
	if a.debug {
		logrus.SetLevel(logrus.DebugLevel)
	}

	logrus.SetFormatter(&logrus.TextFormatter{
		FullTimestamp: true,
	})

	tail := "socket.io/?EIO=3&transport=websocket"
	if !strings.HasPrefix(a.url, tail) {
		if strings.HasPrefix(a.url, "/") {
			a.url += "/" + tail
		} else {
			a.url += tail
		}
	}
	log := logrus.WithField("url", a.url)
	log.Info("Starting to connect to URL")
	client, err := io.Dial(a.url, transport.GetDefaultWebsocketTransport())
	if err != nil {
		return nil, nil, err
	}
	log.Debug("Connected")
	return log, client, nil
}

func stringOr(val, message string) string {
	if val != "" {
		return val
	}
	reader := bufio.NewReader(os.Stdin)
	for {
		fmt.Print(message)
		str, err := reader.ReadString('\n')
		if err != nil {
			fmt.Println("--- error input", err)
			continue
		}

		if str == "" {
			fmt.Println("--- must provide an input")
		} else {
			return str
		}
	}
}
