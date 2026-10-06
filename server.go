package neffos

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
)

// Upgrader is the definition type of a protocol upgrader, gorilla or gobwas or custom.
// It is the first parameter of the `New` function which constructs a neffos server.
type Upgrader func(w http.ResponseWriter, r *http.Request) (Socket, error)

// IDGenerator is the type of function that it is used
// to generate unique identifiers for new connections.
//
// See `Server.IDGenerator`.
type IDGenerator func(w http.ResponseWriter, r *http.Request) string

// DefaultIDGenerator returns a universal unique identifier for a new connection.
// It's the default `IDGenerator` for `Server`.
var DefaultIDGenerator IDGenerator = func(http.ResponseWriter, *http.Request) string {
	id, err := uuid.NewRandom()
	if err != nil {
		return strconv.FormatInt(time.Now().Unix(), 10)
	}
	return id.String()
}

// Server is the neffos server.
// Keeps the `IDGenerator` which can be customized, by default it's the `DefaultIDGenerator`  which
// generates connections unique identifiers using the uuid/v4.
//
// Callers can optionally register callbacks for connection, disconnection and errored.
// Its most important methods are `ServeHTTP` which is used to register the server on a specific endpoint
// and `Broadcast` and `Close`.
// Use the `New` function to create a new server, server starts automatically, no further action is required.
type Server struct {
	uuid string

	upgrader      Upgrader
	IDGenerator   IDGenerator
	StackExchange StackExchange

	// If `StackExchange` is set then this field is ignored.
	//
	// It overrides the default behavior(when no StackExchange is not used)
	// which publishes a message independently.
	// In short the default behavior doesn't wait for a message to be published to all clients
	// before any next broadcast call.
	//
	// Therefore, if set to true,
	// each broadcast call will publish its own message(s) by order.
	SyncBroadcaster bool
	// FireDisconnectAlways will allow firing the `OnDisconnect` server's
	// event even if the connection wasimmediately closed from the `OnConnect` server's event
	// through `Close()` or non-nil error.
	// See https://github.com/kataras/neffos/issues/41
	//
	// Defaults to false.
	FireDisconnectAlways bool

	namespaces Namespaces

	// per-connection settings from the ConnHandler (timeouts, heartbeat,
	// message size cap).
	settings connSettings

	// mu guards connections and count. It is never held while user callbacks
	// run or while another lock is taken.
	mu          sync.RWMutex
	connections map[*Conn]struct{}
	count       atomic.Uint64
	// readers counts the goroutines that read from a registered connection
	// (startReader and the broadcaster waiter). Add runs under mu together
	// with the registration, so it happens before any Wait that follows a
	// snapshot taken under mu.
	readers sync.WaitGroup

	disconnect        chan *Conn
	actions           chan action
	broadcastMessages chan []Message

	broadcaster *broadcaster

	// messages that this server must waits
	// for a reply from one of its own connections(see `waitMessages`).
	waitingMessages      map[string]chan Message
	waitingMessagesMutex sync.RWMutex

	closed atomic.Uint32
	// done is closed by Close() to signal the start() loop to exit and to let
	// goroutines that send to s.disconnect/s.actions/s.broadcastMessages
	// drop their work cleanly during shutdown.
	done chan struct{}
	// exchangeCloseOnce closes the StackExchange at most once.
	exchangeCloseOnce sync.Once

	// OnUpgradeError can be optionally registered to catch upgrade errors.
	OnUpgradeError func(err error)
	// OnConnect can be optionally registered to be notified for any new neffos client connection,
	// it can be used to force-connect a client to a specific namespace(s) or to send data immediately or
	// even to cancel a client connection and dissalow its connection when its return error value is not nil.
	// Don't confuse it with the `OnNamespaceConnect`, this callback is for the entire client side connection.
	OnConnect func(c *Conn) error
	// OnDisconnect can be optionally registered to notify about a connection's disconnect.
	// Don't confuse it with the `OnNamespaceDisconnect`, this callback is for the entire client side connection.
	// During Close and Shutdown, OnDisconnect may run on the closing goroutine
	// concurrently with the dispatch loop; protect shared state in the callback.
	OnDisconnect func(c *Conn)
}

// New constructs and returns a new neffos server.
// Listens to incoming connections automatically, no further action is required from the caller.
// The second parameter is the "connHandler", it can be
// filled as `Namespaces`, `Events` or `WithTimeout`, same namespaces and events can be used on the client-side as well,
// Use the `Conn#IsClient` on any event callback to determinate if it's a client-side connection or a server-side one.
//
// See examples for more.
func New(upgrader Upgrader, connHandler ConnHandler) *Server {
	settings := getSettings(connHandler)
	namespaces := connHandler.GetNamespaces()
	s := &Server{
		uuid:              uuid.NewString(),
		upgrader:          upgrader,
		namespaces:        namespaces,
		settings:          settings,
		connections:       make(map[*Conn]struct{}),
		disconnect:        make(chan *Conn),
		actions:           make(chan action),
		broadcastMessages: make(chan []Message),
		broadcaster:       newBroadcaster(),
		waitingMessages:   make(map[string]chan Message),
		IDGenerator:       DefaultIDGenerator,
		done:              make(chan struct{}),
	}

	go s.start()

	return s
}

// UseStackExchange can be used to add one or more StackExchange
// to the server.
// Returns a non-nil error when "exc"
// completes the `StackExchangeInitializer` interface and its `Init` failed.
//
// Read more at the `StackExchange` type's docs.
func (s *Server) UseStackExchange(exc StackExchange) error {
	if exc == nil {
		return nil
	}

	if err := stackExchangeInit(exc, s.namespaces); err != nil {
		return err
	}

	if s.usesStackExchange() {
		s.StackExchange = wrapStackExchanges(s.StackExchange, exc)
	} else {
		s.StackExchange = exc
	}

	return nil
}

// usesStackExchange reports whether this server
// uses one or more `StackExchange`s.
func (s *Server) usesStackExchange() bool {
	return s.StackExchange != nil
}

func (s *Server) start() {
	for {
		select {
		case <-s.done:
			return
		case c := <-s.disconnect:
			s.removeConn(c)
		case msgs := <-s.broadcastMessages:
			for _, c := range s.snapshot() {
				publishMessages(c, msgs)
			}
		case act := <-s.actions:
			// a snapshot, so act.call may read the connection set itself.
			for _, c := range s.snapshot() {
				act.call(c)
			}

			if act.done != nil {
				// act.done is buffered to cap 1 (see Server.Do), so this never blocks.
				act.done <- struct{}{}
			}
		}
	}
}

// addConn registers c unless the server is closed and reports whether it did.
// readers is the number of reader goroutines the caller starts for c once
// addConn returns true.
func (s *Server) addConn(c *Conn, readers int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	// checked under mu: Close sets closed before it takes its snapshot under
	// mu, so a connection is either in that snapshot or never registered.
	if s.closed.Load() > 0 {
		return false
	}

	s.connections[c] = struct{}{}
	s.count.Add(1)
	s.readers.Add(readers)
	return true
}

// removeConn unregisters c and fires the server's `OnDisconnect` and the
// StackExchange's `OnDisconnect` outside the lock. It does nothing if c is not
// registered, so each connection is reported once.
func (s *Server) removeConn(c *Conn) {
	if s.detach(c) {
		s.fireDisconnect(c)
	}
}

// detach unregisters c and reports whether it was registered. The caller
// that gets true owns the disconnect callbacks for c.
func (s *Server) detach(c *Conn) bool {
	s.mu.Lock()
	_, ok := s.connections[c]
	if ok {
		delete(s.connections, c)
		s.count.Add(^uint64(0))
	}
	s.mu.Unlock()

	return ok
}

// fireDisconnect runs the server's `OnDisconnect` and the StackExchange's
// `OnDisconnect` for a connection that detach reported as registered.
func (s *Server) fireDisconnect(c *Conn) {
	// don't fire disconnect if was immediately closed on the `OnConnect` server event.
	if s.OnDisconnect != nil && (s.FireDisconnectAlways || (c.readiness.isReady() && c.readiness.err == nil)) {
		s.OnDisconnect(c)
	}

	if s.usesStackExchange() {
		s.StackExchange.OnDisconnect(c)
	}
}

// snapshot returns the registered connections at this point in time.
func (s *Server) snapshot() []*Conn {
	s.mu.RLock()
	conns := make([]*Conn, 0, len(s.connections))
	for c := range s.connections {
		conns = append(conns, c)
	}
	s.mu.RUnlock()

	return conns
}

// closeExchange closes the StackExchange once, when it implements StackExchangeCloser.
func (s *Server) closeExchange() {
	s.exchangeCloseOnce.Do(func() {
		if closer, ok := s.StackExchange.(StackExchangeCloser); ok {
			closer.Close()
		}
	})
}

// Close terminates the server and all of its connections, client connections are getting notified.
// Close is idempotent: subsequent calls are no-ops.
//
// Close closes every connection with CloseGoingAway and the reason "server
// closed", all of them at the same time, and waits for them: each
// connection's OnNamespaceDisconnect / OnRoomLeave callbacks and the server's
// OnDisconnect run once per connection before this method returns. The
// OnNamespaceDisconnect / OnRoomLeave callbacks of different connections may
// run concurrently, as they already can in normal operation. OnDisconnect
// may run on the closing goroutine concurrently with the dispatch loop;
// protect shared state in the callback.
//
// It then signals the internal dispatch loop to exit and (when the configured
// StackExchange implements StackExchangeCloser) releases its broker
// connections. Close does not wait for event callbacks that are still
// running; use Shutdown for that.
// After Close, Upgrade and Ask return ErrServerClosed, and Do and
// Broadcast return without doing anything.
func (s *Server) Close() {
	if s.terminateAll("server closed") {
		s.closeExchange()
	}
}

// Shutdown closes the server like Close, with CloseGoingAway and the reason
// "server shutting down", and then waits until every connection's reader
// goroutine has returned, which includes any event callback still running.
// It gives up when ctx is done and returns ctx.Err(); otherwise it returns
// nil. The StackExchange is closed after the wait, or when ctx gives up.
//
// Do not call Shutdown from inside an event callback or OnConnect /
// OnDisconnect: it would wait for the callback that called it, until ctx is
// done.
func (s *Server) Shutdown(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}

	s.terminateAll("server shutting down")
	defer s.closeExchange()

	// terminateAll has set closed and taken its snapshot under mu, so no
	// registration can call readers.Add after this point.
	done := make(chan struct{})
	go func() {
		s.readers.Wait()
		close(done)
	}()

	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// terminateAll marks the server closed, terminates every connection with
// CloseGoingAway and reason (concurrently, one goroutine per connection),
// unregisters each one, and stops the dispatch loop. It
// reports false, doing nothing, when the server was already closed.
func (s *Server) terminateAll(reason string) bool {
	if !s.closed.CompareAndSwap(0, 1) {
		return false
	}

	// Detach every connection first, so the dispatch loop's own disconnect
	// path finds nothing to report and this goroutine owns every
	// OnDisconnect call: they all run, one at a time, before Close returns.
	// Each Terminate may wait up to the write timeout for its close frame,
	// so the connections close in parallel.
	var conns []*Conn
	for _, c := range s.snapshot() {
		if s.detach(c) {
			conns = append(conns, c)
		}
	}

	var wg sync.WaitGroup
	for _, c := range conns {
		wg.Go(func() { c.Terminate(CloseGoingAway, reason) })
	}
	wg.Wait()

	for _, c := range conns {
		s.fireDisconnect(c)
	}

	close(s.done)
	return true
}

// ErrServerClosed is returned by `Server#Upgrade` and `Server#Ask`
// after `Server#Close` was called.
var ErrServerClosed = errors.New("server closed")

var errInvalidMethod = errors.New("no valid request method")

// URLParamAsHeaderPrefix is the prefix that server parses the url parameters as request headers.
// The client's `URLParamAsHeaderPrefix` must match.
// Note that this is mostly useful for javascript browser-side clients, nodejs and go client support custom headers by default.
// No action required from end-developer, exported only for chance to a custom parsing.
const URLParamAsHeaderPrefix = "X-Websocket-Header-"

func tryParseURLParamsToHeaders(r *http.Request) {
	q := r.URL.Query()
	for k, values := range q {
		if len(k) <= len(URLParamAsHeaderPrefix) {
			continue
		}

		k = http.CanonicalHeaderKey(k) // canonical, so no X-WebSocket thing.

		idx := strings.Index(k, URLParamAsHeaderPrefix)
		if idx != 0 { // must be prefix.
			continue
		}

		if r.Header == nil {
			r.Header = make(http.Header)
		}

		k = k[len(URLParamAsHeaderPrefix):]

		for _, v := range values {
			r.Header.Add(k, v)
		}
	}
}

var errUpgradeOnRetry = errors.New("check status")

// IsTryingToReconnect reports whether the returning "err" from the `Server#Upgrade`
// is from a client that was trying to reconnect to the websocket server.
//
// Look the `Conn#WasReconnected` and `Conn#ReconnectTries` too.
func IsTryingToReconnect(err error) (ok bool) {
	return errors.Is(err, errUpgradeOnRetry)
}

// This header key should match with that browser-client's `whenResourceOnline->re-dial` uses.
const websocketReconectHeaderKey = "X-Websocket-Reconnect"

func isServerConnID(s string) bool {
	return strings.HasPrefix(s, "neffos(0x")
}

func genServerConnID(s *Server, c *Conn) string {
	return fmt.Sprintf("neffos(0x%s(%s%p))", s.uuid, c.id, c)
}

// Upgrade handles the connection, same as `ServeHTTP` but it can accept
// a socket wrapper and a "customIDGen" that overrides the server's IDGenerator
// and it does return the connection or any errors.
func (s *Server) Upgrade(
	w http.ResponseWriter,
	r *http.Request,
	socketWrapper func(Socket) Socket,
	customIDGen IDGenerator,
) (*Conn, error) {
	if s.closed.Load() > 0 {
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return nil, ErrServerClosed
	}

	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusFound)
		return nil, errUpgradeOnRetry
	}

	if r.Method != http.MethodGet {
		// RCF rfc2616 https://www.w3.org/Protocols/rfc2616/rfc2616-sec10.html
		// The response MUST include an Allow header containing a list of valid methods for the requested resource.
		//
		// https://developer.mozilla.org/en-US/docs/Web/HTTP/Headers/Allow#Examples
		w.Header().Set("Allow", http.MethodGet)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.WriteHeader(http.StatusMethodNotAllowed)
		fmt.Fprintln(w, http.StatusText(http.StatusMethodNotAllowed))
		return nil, errInvalidMethod
	}

	tryParseURLParamsToHeaders(r)

	socket, err := s.upgrader(w, r)
	if err != nil {
		if s.OnUpgradeError != nil {
			s.OnUpgradeError(err)
		}
		return nil, err
	}

	if socketWrapper != nil {
		socket = socketWrapper(socket)
	}

	c := newConn(socket, s.namespaces)
	if customIDGen != nil {
		c.id = customIDGen(w, r)
	} else {
		c.id = s.IDGenerator(w, r)
	}
	c.serverConnID = genServerConnID(s, c)

	c.applySettings(s.settings)
	c.server = s

	retriesHeaderValue := r.Header.Get(websocketReconectHeaderKey)
	if retriesHeaderValue != "" {
		c.ReconnectTries, _ = strconv.Atoi(retriesHeaderValue)
	}

	waitBroadcasts := !s.usesStackExchange() && !s.SyncBroadcaster
	readers := 1
	if waitBroadcasts {
		readers++
	}

	if !s.addConn(c, readers) {
		// the response is already hijacked, so no status can be written;
		// send a close frame when the socket can, else drop the connection.
		if closer, ok := socket.(SocketCloser); !ok || closer.Close(CloseGoingAway, "server closed", time.Second) != nil {
			socket.NetConn().Close()
		}
		return nil, ErrServerClosed
	}

	if waitBroadcasts {
		// take the head before any goroutine starts, so every broadcast made
		// from here on (OnConnect included) reaches this connection.
		entry := s.broadcaster.head()
		go func() {
			defer s.readers.Done()
			for entry != nil {
				entry = s.waitMessages(c, entry)
			}
		}()
	}

	go func() {
		defer s.readers.Done()
		c.startReader()
	}()

	// it waits for the ack, so it never pings a connection OnConnect rejects.
	c.startHeartbeat()

	// Before `OnConnect` in order to be able
	// to Broadcast inside the `OnConnect` custom func.
	if s.usesStackExchange() {
		if err := s.StackExchange.OnConnect(c); err != nil {
			c.readiness.unwait(err)
			return nil, err
		}

		// Close may have run while the exchange was connecting: its
		// StackExchange.OnDisconnect then came before this OnConnect
		// finished. Tell the exchange again (unsubscribing is idempotent)
		// and skip the user's OnConnect.
		if s.closed.Load() > 0 {
			s.StackExchange.OnDisconnect(c)
			c.readiness.unwait(ErrServerClosed)
			return nil, ErrServerClosed
		}
	}

	// Start the reader before `OnConnect`: clients may remotely connect to a namespace before
	// `Server#OnConnect` returns, so any `Server:NSConn#OnNamespaceConnected` can write
	// immediately back to the client. The server's `OnConnect` can also write, but any Message
	// targeting a not-yet-locally-connected namespace would (correctly) fail the write checks.
	//
	// To honor the contract that a non-nil error from `OnConnect` must abort the connection
	// before any namespace connect, we accept messages into the queue while `OnConnect` runs
	// (same path used for messages arriving before ack). The ack ready signal also waits on
	// `OnConnect`'s outcome; on error, no events fire.
	if s.OnConnect != nil {
		if err = s.OnConnect(c); err != nil {
			c.readiness.unwait(err)
			// No need to disconnect here, connection's .Close will be called on readiness ch errored.
			return nil, err
		}
	}

	c.readiness.unwait(nil)

	return c, nil
}

// ServeHTTP completes the `http.Handler` interface, it should be passed on a http server's router
// to serve this neffos server on a specific endpoint.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.Upgrade(w, r, nil, nil)
}

// GetTotalConnections returns the total amount of the connected connections to the server, it's fast
// and can be used as frequently as needed.
func (s *Server) GetTotalConnections() uint64 {
	return s.count.Load()
}

type action struct {
	call func(*Conn)
	done chan struct{}
}

// Do loops through all connected connections and fires "fn" once per connection.
// Callers can use this to manipulate connections outside an event callback.
// fn may call `GetConnections` and `GetConnectionsByNamespace`.
//
// Do not perform long work inside fn: it runs on the server's dispatch goroutine
// and delays other Do calls, disconnect notifications and SyncBroadcaster
// broadcasts until it returns.
//
// If async is true Do returns immediately; otherwise it blocks until every
// connection has been processed. After `Close`, Do returns without calling fn.
func (s *Server) Do(fn func(*Conn), async bool) {
	if s.closed.Load() > 0 {
		return
	}

	act := action{call: fn}
	if !async {
		// Buffered to cap 1 so the dispatch loop never blocks on a caller that
		// already abandoned the wait.
		act.done = make(chan struct{}, 1)
	}

	select {
	case s.actions <- act:
	case <-s.done:
		return
	}

	if !async {
		// the loop runs an accepted action to the end before it can see done.
		<-act.done
	}
}

func publishMessages(c *Conn, msgs []Message) bool {
	for _, msg := range msgs {
		if msg.from == c.ID() {
			// if the message is not supposed to return back to any connection with this ID.
			continue
		}

		// if "To" field is given then send to a specific connection.
		if msg.To != "" && msg.To != c.ID() {
			continue
		}

		// c.Write may fail if the message is not supposed to end to this client
		// but the connection should be still open in order to continue.
		if !c.Write(msg) && c.IsClosed() {
			return false
		}
	}

	return true
}

// waitMessages waits for entry to be published, writes its messages to c and
// returns the entry to wait on next. It returns nil when c is closed.
func (s *Server) waitMessages(c *Conn, entry *broadcastEntry) *broadcastEntry {
	msgs, next, ok := s.broadcaster.waitUntilClosed(entry, c.closeCh)
	if !ok {
		return nil
	}

	if !publishMessages(c, msgs) {
		return nil
	}

	return next
}

type stringerValue struct{ v string }

func (s stringerValue) String() string { return s.v }

// Exclude can be passed on `Server#Broadcast` when
// caller does not have access to the `Conn`, `NSConn` or a `Room` value but
// has access to a string variable which is a connection's ID instead.
//
// Example Code:
// nsConn.Conn.Server().Broadcast(
//
//		neffos.Exclude("connection_id_here"),
//	 neffos.Message{Namespace: "default", Room: "roomName or empty", Event: "chat", Body: [...]})
func Exclude(connID string) fmt.Stringer { return stringerValue{connID} }

// Broadcast publishes msgs to every connection that is allowed to receive them.
// It does not block: the message is queued for the connection's writer, so this
// method is safe to call as frequently as needed.
//
// At-most-once semantics: when a connection's outbox is saturated or the
// connection is mid-close, that connection silently drops the message. There is
// no per-message delivery acknowledgement; if delivery confirmation is required,
// use Ask instead.
//
// If exceptSender is non-nil, msgs are not sent back to that connection. The
// argument may be a *Conn, *NSConn, *Room, or the result of Exclude(connID).
//
// When `StackExchange` is configured, msgs are published through it. When
// `SyncBroadcaster` is true, the call enqueues to the dispatch loop so broadcasts
// preserve a strict order at the cost of throughput.
func (s *Server) Broadcast(exceptSender fmt.Stringer, msgs ...Message) {

	if exceptSender != nil {
		var fromExplicit, from string

		switch c := exceptSender.(type) {
		case *Conn:
			fromExplicit = c.serverConnID
		case *NSConn:
			fromExplicit = c.Conn.serverConnID
		default:
			from = exceptSender.String()
		}

		for i := range msgs {
			if from != "" {
				msgs[i].from = from
			} else {
				msgs[i].FromExplicit = fromExplicit
			}
		}
	}

	if s.usesStackExchange() {
		s.StackExchange.Publish(msgs)
		return
	}

	if s.SyncBroadcaster {
		if s.closed.Load() > 0 {
			return
		}

		select {
		case s.broadcastMessages <- msgs:
		case <-s.done:
		}
		return
	}

	s.broadcaster.broadcast(msgs)
}

// Ask broadcasts msg and blocks until a matching reply arrives or ctx is canceled.
//
// If msg.To is set, the reply comes from that specific connection. Otherwise Ask
// returns the first reply received from any connection that handles the event.
//
// Always call Ask with a context that has a deadline (context.WithTimeout). A
// connection that goes silent will otherwise pin this goroutine forever.
//
// When `StackExchange` is configured, the wait is routed through it so the reply
// can come from a different neffos instance.
func (s *Server) Ask(ctx context.Context, msg Message) (Message, error) {
	if ctx == nil {
		ctx = context.TODO()
	}

	if s.closed.Load() > 0 {
		return Message{}, ErrServerClosed
	}

	msg.wait = genWait(false) + "-" + s.uuid[:8]

	if s.usesStackExchange() {
		// the exchange waits on the clean token; the wire carries the marked one.
		token := msg.wait
		msg.wait = genWaitStackExchange(token)
		return s.StackExchange.Ask(ctx, msg, token)
	}

	// Buffered to cap 1 so a replier never blocks if ctx fires before we read.
	// The deferred cleanup ensures the entry is removed on both the receive and
	// the cancel paths.
	ch := make(chan Message, 1)
	s.waitingMessagesMutex.Lock()
	s.waitingMessages[msg.wait] = ch
	s.waitingMessagesMutex.Unlock()

	defer func() {
		s.waitingMessagesMutex.Lock()
		delete(s.waitingMessages, msg.wait)
		s.waitingMessagesMutex.Unlock()
	}()

	s.Broadcast(nil, msg)

	select {
	case <-ctx.Done():
		return Message{}, ctx.Err()
	case <-s.done:
		return Message{}, ErrServerClosed
	case receive := <-ch:
		return receive, receive.Err
	}
}

// GetConnectionsByNamespace can be used as an alternative way to retrieve
// all connected connections to a specific "namespace" on a specific time point.
// Do not use this function frequently, it is not designed to be fast or cheap, use it for debugging or logging every 'x' time.
//
// It is safe for concurrent use, including from inside a `Do` callback.
func (s *Server) GetConnectionsByNamespace(namespace string) map[string]*NSConn {
	conns := make(map[string]*NSConn)

	// the namespace lookups run on a snapshot, so mu is never held together
	// with a connection's own locks.
	for _, c := range s.snapshot() {
		if ns := c.Namespace(namespace); ns != nil {
			conns[ns.Conn.ID()] = ns
		}
	}

	return conns
}

// GetConnections can be used as an alternative way to retrieve
// all connected connections to the server on a specific time point.
// Do not use this function frequently, it is not designed to be fast or cheap, use it for debugging or logging every 'x' time.
//
// It is safe for concurrent use, including from inside a `Do` callback.
func (s *Server) GetConnections() map[string]*Conn {
	snapshot := s.snapshot()
	conns := make(map[string]*Conn, len(snapshot))
	for _, c := range snapshot {
		conns[c.ID()] = c
	}

	return conns
}

var (
	// ErrBadNamespace may return from a `Conn#Connect` method when the remote side does not declare the given namespace.
	ErrBadNamespace = errors.New("bad namespace")
	// ErrBadRoom may return from a `Room#Leave` method when trying to leave from a not joined room.
	ErrBadRoom = errors.New("bad room")
	// ErrWrite may return from any connection's method when the underline connection is closed (unexpectedly).
	ErrWrite = errors.New("write closed")
)
