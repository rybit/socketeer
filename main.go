package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"time"

	"github.com/gorilla/websocket"
	gio "github.com/graarh/golang-socketio"
	"github.com/graarh/golang-socketio/transport"
	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
)

const defaultPort = 10101

var debugEnabled bool
var log = logrus.NewEntry(logrus.StandardLogger())

type args struct {
	authToken string
	host      string
	secure    bool
	port      int
}

func main() {
	a := new(args)
	tailDeploy := cobra.Command{
		Use: "deploy [deployID]",
		RunE: func(_ *cobra.Command, args []string) error {
			initLog()
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
			initLog()
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
	echoFunc := cobra.Command{
		Use: "serve",
		RunE: func(_ *cobra.Command, args []string) error {
			initLog()
			return serve(a)
		},
	}
	var path string
	sendFunc := cobra.Command{
		Use: "send",
		RunE: func(_ *cobra.Command, args []string) error {
			initLog()
			return request(a, path)
		},
	}
	sendFunc.Flags().StringVarP(&path, "path", "p", "/stream", "A path to connect to")
	root := new(cobra.Command)
	root.AddCommand(&tailDeploy, &tailFunc, &echoFunc, &sendFunc)

	root.PersistentFlags().BoolVarP(&debugEnabled, "debug", "D", false, "enable debug logging")
	root.PersistentFlags().BoolVarP(&a.secure, "secure", "S", false, "enable wss vs ws")
	root.PersistentFlags().StringVarP(&a.authToken, "auth", "A", "", "auth token to include")
	root.PersistentFlags().StringVarP(&a.host, "host", "H", "localhost", "the url to connect to")
	root.PersistentFlags().IntVarP(&a.port, "port", "P", defaultPort, "the port to use for the websocket connection")

	if c, err := root.ExecuteC(); err != nil {
		logrus.Fatalf("Failed to execute command %s - %s", c.Name(), err.Error())
	}
}

func setCloser(conn *websocket.Conn) {
	conn.SetCloseHandler(func(code int, text string) error {
		log.Infof("Got close connection: %d, %s", code, text)
		if err := conn.WriteControl(websocket.CloseMessage, nil, time.Now().Add(time.Second)); err != nil {
			log.WithError(err).Error("error while sending close message")
		}

		return nil
	})
}

// serve will start a server on the designated port, then it will echo anything sent to /echo
func serve(params *args) error {
	up := websocket.Upgrader{
		ReadBufferSize:  4096,
		WriteBufferSize: 4096,
		// consider: EnableCompression: true,
	}

	readMsg := func(w http.ResponseWriter, r *http.Request) (*websocket.Conn, int, []byte, error) {
		conn, err := up.Upgrade(w, r, nil)
		if err != nil {
			log.WithError(err).Error("Error upgrading")
			return nil, 0, nil, err
		}
		log.Debug("Upgraded connection")
		setCloser(conn)
		conn.SetReadDeadline(time.Now())
		msgType, data, err := conn.ReadMessage()
		if err != nil {
			log.WithError(err).Error("Error reading in message")
			return nil, 0, nil, err
		}

		return conn, msgType, data, nil
	}
	dumpMsg := func(w http.ResponseWriter, r *http.Request, msgType int, data []byte, log *logrus.Entry) {
		switch msgType {
		case websocket.BinaryMessage:
			log.Debugf("Got BINARY message")
			// check for JSON
			if r.Header.Get("Content-Type") == "application/json" {
				log.Debug("Discovered it is JSON: parsing")
				blank := make(map[string]interface{})
				if err := json.Unmarshal(data, &blank); err != nil {
					log.WithError(err).Errorf("Failed to parse data into json: %s", string(data))
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				log.Infof("%+v", blank)
			} else {
				log.Infof("%s", string(data))
			}
		case websocket.TextMessage:
			log.Debugf("Got TEXT message")
			log.Infof("%s", string(data))
		default:
			log.Infof("Got unknown message type: %d", msgType)
			return
		}
	}

	mux := http.NewServeMux()
	mux.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("hello"))
	}))
	var clientID int
	// this will print all the messages until the connection is closed
	mux.Handle("/stream", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		clientID++
		cid := clientID
		lr := log.WithField("id", cid)
		lr.Debug("Starting stream request")
		conn, err := up.Upgrade(w, r, nil)
		if err != nil {
			lr.WithError(err).Error("Failed to upgrade connection")
			return
		}
		setCloser(conn)
		defer conn.Close()

		for {
			msgType, data, err := conn.ReadMessage()
			if err != nil {
				if err != io.EOF {
					lr.WithError(err).Error("Unexpected error while reading a message")
				}
				lr.Info("Shutting down connection")
				return
			}
			dumpMsg(w, r, msgType, data, lr)

			// write it back out
			if err := conn.WriteMessage(msgType, data); err != nil {
				lr.WithError(err).Error("Failed to write message back to the client")
			}
		}
	}))

	// this will dump one message and close the connection
	mux.Handle("/dump", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.Debug("Starting dump request")
		conn, msgType, data, err := readMsg(w, r)
		if err != nil {
			return
		}
		defer conn.Close()
		dumpMsg(w, r, msgType, data, log)
	}))

	log.Infof("Starting server on %d", params.port)
	return http.ListenAndServe(fmt.Sprintf(":%d", params.port), mux)
}

// request will simply take stdin and write each line to the socket
func request(a *args, path string) error {
	url := url(a) + path
	log.Debugf("Connecting to %s", url)
	conn, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		return err
	}
	setCloser(conn)
	defer conn.Close()
	log.Infof("Connected to %s", url)

	shutdown := make(chan os.Signal, 1)
	signal.Notify(shutdown, os.Interrupt)

	// start something that will read from the socket
	go func() {
		for {
			_, data, err := conn.ReadMessage()
			if err != nil {
				log.WithError(err).Info("Got error while listening - shutting down.")
				shutdown <- os.Interrupt
				return
			}
			fmt.Println("\nREAD:", string(data))
		}
	}()

	// read from stdin and send to the connection
	reader := bufio.NewReader(os.Stdin)
	for {
		select {
		case <-shutdown:
			conn.Close()
			return nil
		default:
			fmt.Print("Enter text: ")
			data, err := reader.ReadBytes('\n')
			if err != nil {
				log.WithError(err).Error("Failed to read from stdin")
				return err
			}
			data = data[:len(data)-1]
			log.Debugf("Writing '%s'", string(data))
			if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
				log.WithError(err).Errorf("Failed to write message to server: '%s'", string(data))
				return err
			}
		}
	}
}

func tailFunc(a *args, siteID, funcName, host string) error {
	client, err := setup(a)
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
	client.On("funclog:entry", func(c *gio.Channel, something interface{}) {
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
	client, err := setup(a)
	if err != nil {
		return err
	}
	deployID = stringOr(deployID, "Enter deploy ID: ")
	a.authToken = stringOr(a.authToken, "Enter auth token: ")
	log = log.WithField("deploy_id", deployID)

	log.Infof("listeneing for messages for messages: deploylog:entry")
	count := 0
	client.On("deploylog:entry", func(c *gio.Channel, something interface{}) {
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

func initLog() {
	if debugEnabled {
		logrus.SetLevel(logrus.DebugLevel)
	}

	logrus.SetFormatter(&logrus.TextFormatter{
		FullTimestamp: true,
	})
}

func setup(a *args) (*gio.Client, error) {
	// tail := "socket.io/?EIO=3&transport=websocket"
	// if !strings.HasSuffix(a.url, tail) {
	// 	if strings.HasSuffix(a.url, "/") {
	// 		a.url += tail
	// 	} else {
	// 		a.url += "/" + tail
	// 	}
	// }

	url := url(a)
	log.WithFields(logrus.Fields{
		"host": a.host,
		"port": a.port,
		"url":  url,
	}).Info("Starting to connect to URL")
	client, err := gio.Dial(url, transport.GetDefaultWebsocketTransport())
	if err != nil {
		return nil, err
	}
	log.Debug("Connected")
	return client, nil
}

func url(a *args) string {
	scheme := "ws"
	if a.secure {
		scheme = "wss"
	}
	return fmt.Sprintf("%s://%s:%d", scheme, a.host, a.port)
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
