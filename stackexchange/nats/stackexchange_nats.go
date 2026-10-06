// Package nats provides a neffos StackExchange that scales neffos servers out
// through nats publish and subscribe.
//
// Every neffos server that uses the same nats server and the same
// SubjectPrefix shares its broadcasts with the others. The exchange keeps two
// nats connections for its whole life, one to publish and one to subscribe,
// no matter how many websocket connections it serves.
//
// Subject names, for the SubjectPrefix (default "neffos"):
//
//	<prefix>.<namespace>    broadcasts to a namespace, and to its rooms
//	<prefix>.<connID>       messages sent to one connection (Message.To)
//	<prefix>.ask.<token>    replies to a Server.Ask
//
// A message for a room goes to its namespace subject. Each server then writes
// it only to the connections that joined that room. The namespace and
// connection names are the ones earlier neffos versions used, so older and
// newer servers on the same nats still share broadcasts for namespaces made
// of plain characters. Server.Ask needs every server on this version.
//
// The characters nats gives a meaning to in a subject (".", "*", ">" and
// whitespace) are replaced with "_" in the namespace, the connection ID and
// the ask token, and an empty namespace becomes "_". Two namespaces that only
// differ in those characters, such as "a.b" and "a_b", share a subject. That
// costs some extra traffic and nothing else, because each server drops a
// message for a namespace its connection did not join.
//
// A cross-server Server.Ask works like this: the asking server subscribes to
// <prefix>.ask.<token> and publishes the message to the target connection's
// subject. The server that holds the connection writes it to the client and
// receives the reply. The reply matches no local wait, so neffos hands it to
// NotifyAsk, which publishes it to <prefix>.ask.<token>.
package nats

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/kataras/neffos"

	"github.com/nats-io/nats.go"
)

// StackExchange is a `neffos.StackExchange` for nats
// based on https://nats-io.github.io/docs/developer/tutorials/pubsub.html.
type StackExchange struct {
	// options holds the nats options for clients.
	// Defaults to the `nats.GetDefaultOptions()` which
	// can be overridden by the `With` function on `NewStackExchange`.
	opts nats.Options
	// If you use the same nats server instance for multiple neffos apps,
	// set this to different values across your apps.
	SubjectPrefix string

	// timeout bounds every call that waits on nats for a websocket
	// connection: OnConnect, Subscribe and the subscribe step of Ask.
	// It is the nats connect timeout (nats.Timeout option).
	timeout time.Duration

	publisher  *nats.Conn
	subscriber *nats.Conn

	mu            sync.Mutex
	subscriptions map[*neffos.Conn]map[string]*nats.Subscription
	closed        bool
	closeOnce     sync.Once
}

var (
	_ neffos.StackExchange       = (*StackExchange)(nil)
	_ neffos.StackExchangeCloser = (*StackExchange)(nil)
)

// errClosed is returned by OnConnect after Close.
var errClosed = errors.New("nats stackexchange: closed")

// notifyAskFlushTimeout bounds the flush that sends an Ask reply.
const notifyAskFlushTimeout = 5 * time.Second

// With accepts a nats.Options structure
// which contains the whole configuration
// and returns a nats.Option which can be passed
// to the `NewStackExchange`'s second input variadic argument.
// Note that use this method only when you want to override the default options
// at once.
func With(options nats.Options) nats.Option {
	return func(opts *nats.Options) error {
		*opts = options
		return nil
	}
}

// NewStackExchange returns a new nats StackExchange.
// The required field is "url" which should be in the form
// of nats connection string, e.g. nats://username:pass@localhost:4222.
// Other option is to leave the url with localhost:4222 and pass
// authentication options such as `nats.UserInfo(username, pass)` or
// nats.UserCredentials("./userCredsFile") at the second variadic input argument.
//
// Options can be used to register nats error and close handlers too.
// The exchange also reports nats errors, disconnects and reconnects through
// neffos.Debugf, after calling the handlers given here.
//
// Alternatively, use the `With(nats.Options)` function to
// customize the client through struct fields.
//
// It opens two nats connections, one to publish and one to subscribe, and
// fails if either cannot connect. The nats.Timeout option (2s by default)
// also bounds how long OnConnect and Subscribe wait for the nats server.
func NewStackExchange(url string, options ...nats.Option) (*StackExchange, error) {
	// Cache the options to be used on every client and
	// respect any customization by caller.
	opts := nats.GetDefaultOptions()
	if url == "" {
		url = nats.DefaultURL
	}
	opts.Url = url
	opts.NoEcho = true

	for _, opt := range options {
		if opt == nil {
			continue
		}
		if err := opt(&opts); err != nil {
			return nil, err
		}
	}

	// opts.Url may change from caller, use the struct's field to respect it.
	servers := strings.Split(opts.Url, ",")
	for i, s := range servers {
		servers[i] = strings.TrimSpace(s)
	}
	// append to make sure that any custom servers from caller
	// are respected, no check for duplications.
	opts.Servers = append(opts.Servers, servers...)

	withDebugHandlers(&opts)

	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = nats.DefaultTimeout
	}

	publisher, err := opts.Connect()
	if err != nil {
		return nil, err
	}

	subscriber, err := opts.Connect()
	if err != nil {
		publisher.Close()
		return nil, err
	}

	exc := &StackExchange{
		opts:          opts,
		SubjectPrefix: "neffos",
		timeout:       timeout,
		publisher:     publisher,
		subscriber:    subscriber,
		subscriptions: make(map[*neffos.Conn]map[string]*nats.Subscription),
	}

	return exc, nil
}

// withDebugHandlers chains neffos.Debugf after the caller's nats error,
// disconnect and reconnect handlers. neffos.Debugf prints nothing unless
// neffos.EnableDebug was called.
func withDebugHandlers(opts *nats.Options) {
	asyncErr := opts.AsyncErrorCB
	opts.AsyncErrorCB = func(nc *nats.Conn, sub *nats.Subscription, err error) {
		if asyncErr != nil {
			asyncErr(nc, sub, err)
		}
		subject := ""
		if sub != nil {
			subject = sub.Subject
		}
		neffos.Debugf("nats stackexchange: error on subject %q: %v", subject, err)
	}

	disconnected := opts.DisconnectedErrCB
	opts.DisconnectedErrCB = func(nc *nats.Conn, err error) {
		if disconnected != nil {
			disconnected(nc, err)
		}
		if err != nil {
			neffos.Debugf("nats stackexchange: disconnected: %v", err)
		}
	}

	reconnected := opts.ReconnectedCB
	opts.ReconnectedCB = func(nc *nats.Conn) {
		if reconnected != nil {
			reconnected(nc)
		}
		neffos.Debugf("nats stackexchange: reconnected to %s", nc.ConnectedUrl())
	}
}

// subjectReplacer turns the characters that have a meaning in a nats subject
// into "_", so a namespace, connection ID or ask token is always one plain token.
var subjectReplacer = strings.NewReplacer(
	".", "_",
	"*", "_",
	">", "_",
	" ", "_",
	"\t", "_",
	"\r", "_",
	"\n", "_",
)

// subjectToken returns s as a single valid subject token. An empty s becomes "_".
// Different inputs can map to the same token, for example "a.b" and "a_b".
func subjectToken(s string) string {
	if s == "" {
		return "_"
	}
	return subjectReplacer.Replace(s)
}

// getSubject returns the subject for a message. A message with a connID goes
// to that connection's subject; any other message, room messages included,
// goes to its namespace subject. It never panics: an empty namespace becomes "_".
func (exc *StackExchange) getSubject(namespace, room, connID string) string {
	if connID != "" {
		// publish direct and let the server-side do the checks
		// of valid or invalid message to send on this particular client.
		return exc.SubjectPrefix + "." + subjectToken(connID)
	}

	// Rooms share their namespace subject: the interface has no room
	// subscribe, and each server filters room messages per connection.
	return exc.SubjectPrefix + "." + subjectToken(namespace)
}

// askSubject returns the subject a Server.Ask waits on for its reply.
func (exc *StackExchange) askSubject(token string) string {
	return exc.SubjectPrefix + ".ask." + subjectToken(token)
}

func makeMsgHandler(c *neffos.Conn) nats.MsgHandler {
	return func(m *nats.Msg) {
		msg := c.DeserializeMessage(neffos.TextMessage, m.Data)
		msg.FromStackExchange = true

		c.Write(msg)
	}
}

// OnConnect subscribes the connection to its own subject for direct neffos
// messages. It's called automatically after the neffos server's OnConnect
// (if any) on incoming client connections.
// It waits for the nats server to confirm the subscription for at most the
// nats connect timeout and returns an error if it does not.
func (exc *StackExchange) OnConnect(c *neffos.Conn) error {
	exc.mu.Lock()
	closed := exc.closed
	exc.mu.Unlock()
	if closed {
		return errClosed
	}

	subject := exc.getSubject("", "", c.ID())
	sub, err := exc.subscriber.Subscribe(subject, makeMsgHandler(c))
	if err != nil {
		neffos.Debugf("[%s] nats stackexchange: subscribe to %q: %v", c.ID(), subject, err)
		return err
	}

	if err = exc.subscriber.FlushTimeout(exc.timeout); err != nil {
		neffos.Debugf("[%s] nats stackexchange: confirm subscription to %q: %v", c.ID(), subject, err)
		sub.Unsubscribe()
		return err
	}

	exc.mu.Lock()
	if exc.closed {
		exc.mu.Unlock()
		sub.Unsubscribe()
		return errClosed
	}
	exc.subscriptions[c] = map[string]*nats.Subscription{subject: sub}
	exc.mu.Unlock()

	return nil
}

// Publish publishes messages through nats.
// It's called automatically on neffos broadcasting.
// It returns false on the first message nats refuses.
func (exc *StackExchange) Publish(msgs []neffos.Message) bool {
	for _, msg := range msgs {
		if err := exc.publish(msg); err != nil {
			return false
		}
	}

	return true
}

func (exc *StackExchange) publish(msg neffos.Message) error {
	subject := exc.getSubject(msg.Namespace, msg.Room, msg.To)
	if err := exc.publisher.Publish(subject, msg.Serialize()); err != nil {
		neffos.Debugf("nats stackexchange: publish to %q: %v", subject, err)
		return err
	}
	return nil
}

// Ask implements server Ask for nats. It blocks until the reply arrives or
// ctx is done. It subscribes to the reply subject, waits for the nats server
// to confirm that (bounded by ctx and the nats connect timeout), publishes
// msg and then waits for one reply. It starts no goroutine.
func (exc *StackExchange) Ask(ctx context.Context, msg neffos.Message, token string) (response neffos.Message, err error) {
	sub, err := exc.subscriber.SubscribeSync(exc.askSubject(token))
	if err != nil {
		return response, err
	}
	defer sub.Unsubscribe()

	flushCtx, cancel := context.WithTimeout(ctx, exc.timeout)
	err = exc.subscriber.FlushWithContext(flushCtx)
	cancel()
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return response, ctxErr
		}
		return response, err
	}

	if err = exc.publish(msg); err != nil {
		return response, fmt.Errorf("%w: %v", neffos.ErrWrite, err)
	}

	m, err := sub.NextMsgWithContext(ctx)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return response, ctxErr
		}
		return response, err
	}

	response = neffos.DeserializeMessage(neffos.TextMessage, m.Data, false, false)
	return response, response.Err
}

// NotifyAsk notifies and unblocks a "msg" subscriber, called on a server connection's read when expects a result.
// It publishes the reply to the ask subject of token and waits up to 5s for
// nats to take it.
func (exc *StackExchange) NotifyAsk(msg neffos.Message, token string) error {
	msg.ClearWait()
	if err := exc.publisher.Publish(exc.askSubject(token), msg.Serialize()); err != nil {
		return err
	}
	return exc.publisher.FlushTimeout(notifyAskFlushTimeout)
}

// Subscribe subscribes to a specific namespace,
// it's called automatically on neffos namespace connected.
// It waits for the nats server to confirm the subscription for at most the
// nats connect timeout. Errors go to neffos.Debugf.
func (exc *StackExchange) Subscribe(c *neffos.Conn, namespace string) {
	subject := exc.getSubject(namespace, "", "")

	exc.mu.Lock()
	subs, ok := exc.subscriptions[c]
	if !ok {
		// not connected through OnConnect, or already disconnected or closed.
		exc.mu.Unlock()
		return
	}
	if _, ok := subs[subject]; ok {
		exc.mu.Unlock()
		return
	}
	sub, err := exc.subscriber.Subscribe(subject, makeMsgHandler(c))
	if err != nil {
		exc.mu.Unlock()
		neffos.Debugf("[%s] nats stackexchange: subscribe to %q: %v", c.ID(), subject, err)
		return
	}
	subs[subject] = sub
	exc.mu.Unlock()

	if err = exc.subscriber.FlushTimeout(exc.timeout); err != nil {
		// The subscription stays; nats registers it again once it reconnects.
		neffos.Debugf("[%s] nats stackexchange: confirm subscription to %q: %v", c.ID(), subject, err)
	}
}

// Unsubscribe unsubscribes from a specific namespace,
// it's called automatically on neffos namespace disconnect.
// It does not wait for the nats server. Errors go to neffos.Debugf.
func (exc *StackExchange) Unsubscribe(c *neffos.Conn, namespace string) {
	subject := exc.getSubject(namespace, "", "")

	exc.mu.Lock()
	sub, ok := exc.subscriptions[c][subject]
	if ok {
		delete(exc.subscriptions[c], subject)
	}
	exc.mu.Unlock()

	if !ok {
		return
	}
	if err := sub.Unsubscribe(); err != nil {
		neffos.Debugf("[%s] nats stackexchange: unsubscribe from %q: %v", c.ID(), subject, err)
	}
}

// OnDisconnect removes every subscription the connection holds, its own
// subject and its namespaces.
// It's called automatically when a connection goes offline,
// manually by server or client or by network failure.
func (exc *StackExchange) OnDisconnect(c *neffos.Conn) {
	exc.mu.Lock()
	subs := exc.subscriptions[c]
	delete(exc.subscriptions, c)
	exc.mu.Unlock()

	for subject, sub := range subs {
		if err := sub.Unsubscribe(); err != nil {
			neffos.Debugf("[%s] nats stackexchange: unsubscribe from %q: %v", c.ID(), subject, err)
		}
	}
}

// Close drains the subscriber connection, so messages already received are
// still delivered, and then closes the publisher connection. It waits for the
// drain for at most the nats drain timeout plus 5s, and falls back to closing
// the subscriber when draining fails.
// It returns the first error met. It is safe to call Close more than once;
// later calls return nil.
func (exc *StackExchange) Close() error {
	var err error
	exc.closeOnce.Do(func() {
		exc.mu.Lock()
		exc.closed = true
		exc.subscriptions = make(map[*neffos.Conn]map[string]*nats.Subscription)
		exc.mu.Unlock()

		err = exc.drainSubscriber()
		exc.publisher.Close()
	})
	return err
}

func (exc *StackExchange) drainSubscriber() error {
	closed := exc.subscriber.StatusChanged(nats.CLOSED)
	defer exc.subscriber.RemoveStatusListener(closed)

	if err := exc.subscriber.Drain(); err != nil {
		exc.subscriber.Close()
		if errors.Is(err, nats.ErrConnectionClosed) {
			// already closed, for example by the caller's own handlers.
			return nil
		}
		return err
	}

	drainTimeout := exc.opts.DrainTimeout
	if drainTimeout <= 0 {
		drainTimeout = nats.DefaultDrainTimeout
	}
	// nats adds a flush of up to 5s after draining the subscriptions.
	timer := time.NewTimer(drainTimeout + 5*time.Second + time.Second)
	defer timer.Stop()

	select {
	case <-closed:
	case <-timer.C:
		exc.subscriber.Close()
		return nats.ErrDrainTimeout
	}

	// Drain reports its own failures (a timeout, a failed flush) as the
	// connection's last error.
	return exc.subscriber.LastError()
}
