package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kataras/neffos"
	"github.com/kataras/neffos/gobwas"

	"github.com/robfig/cron/v3"
)

/*
	$ go get github.com/robfig/cron/v3@v3.0.1
*/

const (
	addr      = "localhost:8080"
	endpoint  = "/live"
	namespace = "agent"

	pushNotificationsEvery     = "10s"
	generateNotificationsEvery = "14s"

	onNotification           = "OnNotification"
	removeNotificationOnSeen = true
)

var (
	// all these fields should located to your app's structure and designing
	// however, for the sake of the example we will declare them as package-level variables here.
	database        *dbMock
	websocketServer *neffos.Server
)

func startCron() {
	c := cron.New()
	_, err := c.AddFunc("@every "+pushNotificationsEvery, func() {
		pushNotifications()
	})

	c.AddFunc("@every "+generateNotificationsEvery, func() {
		src := rand.NewSource(time.Now().UnixNano())
		r := rand.New(src)

		generateNotifications(r)
	})

	if err != nil {
		panic(err)
	}

	c.Start()
}

// Let's assume that we can't do something like
// map[userID][]notifications
// and those should be separated.
type (
	userTable         map[string]user
	notificationTable map[string]notification

	user struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}

	notification struct {
		ID      string `json:"id"`
		UserID  string `json:"userID"`
		Message string `json:"text"`
	}

	dbMock struct {
		users         userTable
		notifications notificationTable
		mu            sync.RWMutex
	}
)

var totalNotifications = new(uint64)

func generateNotificationID() string {
	return fmt.Sprintf("notificationID_%d", atomic.AddUint64(totalNotifications, 1))
}

const (
	minGenerate = 1
	maxGenerate = 3
)

func generateNotifications(r *rand.Rand) {
	generateLength := rand.Intn(maxGenerate-minGenerate) + minGenerate

	for userID := range database.users {
		for i := 1; i <= generateLength; i++ {
			nfID := generateNotificationID()
			nfMsg := "notification message_" + nfID + " for userID_" + userID
			nf := notification{
				ID:      nfID,
				UserID:  userID,
				Message: nfMsg,
			}
			database.upsertNotification(nf)
		}
	}
}

func newDbMock() *dbMock {
	users := make(userTable)
	notifications := make(notificationTable)

	for i := 1; i <= 5; i++ {
		userID := "userID_" + strconv.Itoa(i)
		userName := "name_" + userID
		u := user{
			ID:   userID,
			Name: userName,
		}

		for j := 1; j <= 3; j++ {
			nfID := generateNotificationID()
			nfMsg := "notification message_" + nfID + " for userID_" + userID
			nf := notification{
				ID:      nfID,
				UserID:  userID,
				Message: nfMsg,
			}
			notifications[nfID] = nf
		}

		users[userID] = u
	}

	// log.Printf("Users:\n%#+v", users)
	// log.Printf("Notifications:\n%#+v", notifications)

	return &dbMock{
		users:         users,
		notifications: notifications,
	}
}

func (db *dbMock) upsertNotification(nf notification) {
	db.mu.Lock()
	if nf.ID == "" {
		nf.ID = generateNotificationID()
	}
	db.notifications[nf.ID] = nf
	db.mu.Unlock()
}

func (db *dbMock) removeNotification(nfID string) {
	if nfID == "" {
		return
	}

	db.mu.Lock()
	delete(db.notifications, nfID)
	db.mu.Unlock()
}

// Accept user ids and get all of their notifications as one notification slice.
// Why? For the sake of the example, see the comments on the `pushNotifications`.
func (db *dbMock) getNotificationList(userIDs []string) (list []notification) {
	db.mu.RLock()
	for _, userID := range userIDs {
		if _, exists := db.users[userID]; !exists {
			continue
		}

		for _, v := range db.notifications {
			if v.UserID == userID {
				list = append(list, v)
			}
		}
	}
	db.mu.RUnlock()

	return
}

func upsertNotificationHandler(db *dbMock) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var nf notification

		err := json.NewDecoder(r.Body).Decode(&nf)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		var missingFields []string
		if nf.UserID == "" {
			missingFields = append(missingFields, "userID")
		}

		if nf.Message == "" {
			missingFields = append(missingFields, "message")
		}

		if len(missingFields) > 0 {
			http.Error(w, strings.Join(missingFields, ", ")+" missing", http.StatusBadRequest)
			return
		}

		db.upsertNotification(nf)
	}
}

// Declare them on a custom struct of course,
// they are exposed like that for the sake of the example.
var (
	// write access on websocketServer.OnConnect and OnDisconnect callbacks.
	// read access on pushNotifications() function.
	connections      = make(map[string]*neffos.Conn)
	connectionIDs    []string
	addConnection    = make(chan *neffos.Conn)
	removeConnection = make(chan *neffos.Conn)
	notify           = make(chan notification)
)

func startConnectionManager(ctx context.Context) {
	if ctx == nil {
		ctx = context.TODO()
	}

	go func() {
		for {
			select {
			case c := <-addConnection:
				connections[c.ID()] = c
				connectionIDs = append(connectionIDs, c.ID())
			case c := <-removeConnection:
				delete(connections, c.ID())

				if len(connectionIDs) == 1 {
					connectionIDs = connectionIDs[0:0]
				} else {
					for i, n := 0, len(connectionIDs); i < n; i++ {
						if connectionIDs[i] == c.ID() {
							connectionIDs = append(connectionIDs[0:i], connectionIDs[i+1:]...)
							break
						}
					}
				}
			case nf := <-notify:
				c, ok := connections[nf.UserID]
				if !ok {
					continue
				}

				ok = c.Write(neffos.Message{
					Namespace: namespace,
					Event:     onNotification,
					Body:      neffos.Marshal(nf),
				})

				if ok && removeNotificationOnSeen {
					database.removeNotification(nf.ID)
				}

			case <-ctx.Done():
				return
			}
		}
	}()
}

// This is designed like that becaues we assume,
// that you are told that you must use a database call that will
// accept a list of user ids and returns a list of notifications,
// And you are not allowed to get a list of available notifications for each connection/user
// on every cron job run through a database call and
// you are NOT allowed to Server.Broadcast to a subcollection of connectios i.e rooms
// or namespaces.
// See `dbMock.getNotificationList`.
func pushNotifications() {
	notifications := database.getNotificationList(connectionIDs)

	if n := len(notifications); n == 0 {
		log.Println("no new notification(s) to send...")
		return
	} else {
		log.Printf("sending [%d] notifications...", n)
	}

	for _, nf := range notifications {
		notify <- nf
	}
}

var (
	serverEvents = neffos.Namespaces{
		namespace: neffos.Events{
			neffos.OnNamespaceConnected: func(c *neffos.NSConn, msg neffos.Message) error {
				// Note that we could send notifications pending
				// for this user here but see the comments above
				// to understand why we don't do it on this example,
				// at short: we assume that we are not allowed to get notifications per user
				// only database and cron managers can retrieve and push notifications.

				log.Printf("[%s] connected to namespace [%s].", c, msg.Namespace)
				return nil
			},
			neffos.OnNamespaceDisconnect: func(c *neffos.NSConn, msg neffos.Message) error {
				log.Printf("[%s] disconnected from namespace [%s].", c, msg.Namespace)
				return nil
			},
		},
	}

	clientEvents = neffos.JoinConnHandlers(serverEvents, neffos.Namespaces{
		namespace: neffos.Events{
			onNotification: func(c *neffos.NSConn, msg neffos.Message) error {
				var nf notification
				err := msg.Unmarshal(&nf)
				if err != nil {
					return err
				}

				log.Println(nf.Message)
				return nil
			},
		},
	})
)

func main() {
	args := os.Args[1:]
	if len(args) == 0 {
		log.Fatalf("expected program to start with 'server' or 'client' argument")
	}
	side := args[0]

	switch side {
	case "server":
		startServer()
	case "client":
		startClient()
	default:
		log.Fatalf("unexpected argument, expected 'server' or 'client' but got '%s'", side)
	}
}

func startServer() {
	database = newDbMock()
	websocketServer = neffos.New(gobwas.DefaultUpgrader, serverEvents)
	websocketServer.IDGenerator = func(w http.ResponseWriter, r *http.Request) string {
		// [You can add your own auth here...]
		if userID := r.Header.Get("X-UserID"); userID != "" {
			return userID
		}

		return neffos.DefaultIDGenerator(w, r)
	}

	websocketServer.OnUpgradeError = func(err error) {
		log.Printf("ERROR: %v", err)
	}
	websocketServer.OnConnect = func(c *neffos.Conn) error {
		addConnection <- c

		if c.WasReconnected() {
			log.Printf("[%s] connection is a result of a client-side re-connection, with tries: %d", c.ID(), c.ReconnectTries)
		}

		log.Printf("[%s] connected to the server.", c)
		// if returns non-nil error then it refuses the client to connect to the server.
		return nil
	}
	websocketServer.OnDisconnect = func(c *neffos.Conn) {
		removeConnection <- c
		log.Printf("[%s] disconnected from the server.", c)
	}

	log.Printf("Listening on: %s\nPress CTRL/CMD+C to interrupt.", addr)
	http.Handle("/", upsertNotificationHandler(database))
	http.Handle(endpoint, websocketServer)

	startConnectionManager(context.TODO())
	startCron()

	log.Fatal(http.ListenAndServe(addr, nil))
}

func startClient() {
	var userID string
	if len(os.Args) > 2 {
		userID = os.Args[2]
	} else {
		fmt.Print("Please specify a User ID: ")
		fmt.Scanf("%s", &userID)
	}

	client, err := neffos.Dial(
		nil,
		gobwas.Dialer(gobwas.Options{Header: gobwas.Header{"X-UserID": []string{userID}}}),
		// The endpoint, e.g. ws://localhost:8080/live.
		addr+endpoint,
		// The namespaces and events, can be optionally shared with the server's.
		clientEvents)

	if err != nil {
		log.Fatal(err)
	}

	// connect to the "agent" namespace.
	_, err = client.Connect(nil, namespace)
	if err != nil {
		log.Fatal(err)
	}

	// Block the program termination until client closed,
	// in this case we don't call client.Close manually
	// therefore it will wait until server shutdown.
	<-client.NotifyClose
}

/*
$ go run main.go server
$ go run main.go client userID_1
$ go run main.go client userID_not_exist_no_receive
# ...
$ go run main.go client userID_2
$ go run main.go client userID_3
*/
