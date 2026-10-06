// Package redis provides a neffos StackExchange that scales neffos servers out
// through redis publish and subscribe.
//
// Every neffos server that uses the same redis server and the same channel
// prefix shares its broadcasts with the others. The exchange subscribes with
// plain SUBSCRIBE on exact channel names, so a namespace may contain any
// character, including the glob characters *, ? and [ ].
//
// Channel names, for the prefix given to NewStackExchange:
//
//	<prefix>.<namespace>.   broadcasts to a namespace, and to its rooms
//	<prefix>.<connID>.      messages sent to one connection (Message.To)
//	<prefix>.ask.<token>    replies to a Server.Ask
//
// A message for a room goes to its namespace channel. Each server then writes
// it only to the connections that joined that room. The namespace and
// connection names are the ones earlier neffos versions used, so older and
// newer servers on the same redis still share broadcasts. Server.Ask needs
// every server on this version.
package redis

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/kataras/neffos"

	"github.com/redis/go-redis/v9"
)

// Config is used on the `StackExchange` package-level function.
// Can be used to customize the redis client dialer.
type Config struct {
	// Network to use.
	// Defaults to "tcp".
	Network string
	// Addr of a single redis server instance.
	// See "Clusters" field for clusters support.
	// Defaults to "127.0.0.1:6379".
	Addr string
	// Clusters a list of network addresses for clusters.
	// If not empty "Addr" is ignored.
	Clusters []string

	Password    string
	DialTimeout time.Duration

	// MaxActive defines the size connection pool.
	// Defaults to 10.
	MaxActive int
}

// StackExchange is a `neffos.StackExchange` for redis.
type StackExchange struct {
	channel string
	// dialTimeout bounds every call that waits on redis for a websocket
	// connection: OnConnect, Subscribe and Unsubscribe.
	dialTimeout time.Duration

	client redis.UniversalClient

	mu          sync.Mutex
	subscribers map[*neffos.Conn]*redis.PubSub
	closed      bool
	closeOnce   sync.Once
}

var (
	_ neffos.StackExchange       = (*StackExchange)(nil)
	_ neffos.StackExchangeCloser = (*StackExchange)(nil)
)

// errClosed is returned by OnConnect and Ask after Close.
var errClosed = errors.New("redis stackexchange: closed")

// NewStackExchange returns a new redis StackExchange.
// The "channel" input argument is the channel prefix for publish and subscribe.
//
// It connects to redis before it returns, so an unreachable address or a wrong
// password is reported here.
func NewStackExchange(cfg Config, channel string) (*StackExchange, error) {
	if cfg.Network == "" {
		cfg.Network = "tcp"
	}

	if cfg.Addr == "" && len(cfg.Clusters) == 0 {
		cfg.Addr = "127.0.0.1:6379"
	}

	if cfg.DialTimeout <= 0 {
		cfg.DialTimeout = 30 * time.Second
	}

	if cfg.MaxActive == 0 {
		cfg.MaxActive = 10
	}

	client := newClient(cfg)

	ctx, cancel := context.WithTimeout(context.Background(), cfg.DialTimeout)
	err := client.Ping(ctx).Err()
	cancel()
	if err != nil {
		client.Close()
		return nil, err
	}

	exc := &StackExchange{
		client: client,
		// If you are using one redis server for multiple nefos servers,
		// use a different channel for each neffos server.
		// Otherwise a message sent from one server to all of its own clients will go
		// to all clients of all nefos servers that use the redis server.
		// We could use multiple channels but overcomplicate things here.
		channel:     channel,
		dialTimeout: cfg.DialTimeout,
		subscribers: make(map[*neffos.Conn]*redis.PubSub),
	}

	return exc, nil
}

// newClient returns the redis client for cfg, whose defaults are already set.
// ContextTimeoutEnabled makes go-redis honour context deadlines; without it a
// command waits for its own read timeout whatever the context says.
func newClient(cfg Config) redis.UniversalClient {
	if len(cfg.Clusters) > 0 {
		return redis.NewClusterClient(&redis.ClusterOptions{
			Addrs:                 cfg.Clusters,
			Password:              cfg.Password,
			DialTimeout:           cfg.DialTimeout,
			PoolSize:              cfg.MaxActive,
			ContextTimeoutEnabled: true,
		})
	}

	return redis.NewClient(&redis.Options{
		Network:               cfg.Network,
		Addr:                  cfg.Addr,
		Password:              cfg.Password,
		DialTimeout:           cfg.DialTimeout,
		PoolSize:              cfg.MaxActive,
		ContextTimeoutEnabled: true,
	})
}

// Close closes the subscriber of every connection and then the redis client.
// It returns the error of closing the client. Calling Close again does
// nothing and returns nil.
func (exc *StackExchange) Close() error {
	var err error
	exc.closeOnce.Do(func() {
		exc.mu.Lock()
		exc.closed = true
		subscribers := exc.subscribers
		exc.subscribers = make(map[*neffos.Conn]*redis.PubSub)
		exc.mu.Unlock()

		for _, ps := range subscribers {
			ps.Close()
		}

		err = exc.client.Close()
	})

	return err
}

// getChannel returns the channel a message for namespace, room or connection
// connID is published to. A room shares the channel of its namespace, see the
// package documentation.
func (exc *StackExchange) getChannel(namespace, room, connID string) string {
	if connID != "" {
		// publish direct and let the server-side do the checks
		// of valid or invalid message to send on this particular client.
		return exc.channel + "." + connID + "."
	}

	return exc.channel + "." + namespace + "."
}

// askChannel returns the channel that carries the reply to the Server.Ask
// waiting on token.
func (exc *StackExchange) askChannel(token string) string {
	return exc.channel + ".ask." + token
}

// timeoutContext returns a context that ends after the configured DialTimeout.
func (exc *StackExchange) timeoutContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), exc.dialTimeout)
}

func (exc *StackExchange) subscriber(c *neffos.Conn) *redis.PubSub {
	exc.mu.Lock()
	ps := exc.subscribers[c]
	exc.mu.Unlock()
	return ps
}

// OnConnect prepares the connection redis subscriber
// and subscribes to itself for direct neffos messages.
// It's called automatically after the neffos server's OnConnect (if any)
// on incoming client connections.
func (exc *StackExchange) OnConnect(c *neffos.Conn) error {
	// A redis that accepts the connection but never answers must not hold
	// the websocket handshake forever.
	ctx, cancel := exc.timeoutContext()
	defer cancel()

	ps := exc.client.Subscribe(ctx, exc.getChannel("", "", c.ID()))
	// Receive waits for the subscription to be confirmed, or for its error.
	if _, err := ps.Receive(ctx); err != nil {
		ps.Close()
		return err
	}

	exc.mu.Lock()
	if exc.closed {
		exc.mu.Unlock()
		ps.Close()
		return errClosed
	}
	exc.subscribers[c] = ps
	exc.mu.Unlock()

	// The channel is closed when ps is closed, which ends this goroutine.
	go func() {
		for m := range ps.Channel() {
			msg := c.DeserializeMessage(neffos.TextMessage, []byte(m.Payload))
			msg.FromStackExchange = true

			c.Write(msg)
		}
	}()

	return nil
}

// Publish publishes messages through redis.
// It's called automatically on neffos broadcasting.
func (exc *StackExchange) Publish(msgs []neffos.Message) bool {
	for _, msg := range msgs {
		if err := exc.publish(context.Background(), msg); err != nil {
			neffos.Debugf("redis stackexchange: publish: %v", err)
			return false
		}
	}

	return true
}

func (exc *StackExchange) publish(ctx context.Context, msg neffos.Message) error {
	channel := exc.getChannel(msg.Namespace, msg.Room, msg.To)
	return exc.client.Publish(ctx, channel, msg.Serialize()).Err()
}

// Ask implements the server Ask feature for redis. It blocks until the reply
// arrives on the ask channel of token, or ctx is done.
func (exc *StackExchange) Ask(ctx context.Context, msg neffos.Message, token string) (response neffos.Message, err error) {
	exc.mu.Lock()
	closed := exc.closed
	exc.mu.Unlock()
	if closed {
		return response, errClosed
	}

	ps := exc.client.Subscribe(ctx, exc.askChannel(token))
	defer ps.Close()

	// Subscribe before publishing, so the reply cannot be missed.
	if _, err = ps.Receive(ctx); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			err = ctxErr
		}
		return
	}

	if err = exc.publish(ctx, msg); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return response, ctxErr
		}
		neffos.Debugf("redis stackexchange: ask publish: %v", err)
		return response, fmt.Errorf("%w: %v", neffos.ErrWrite, err)
	}

	// ReceiveMessage only stops on a context deadline, not on cancellation,
	// so it runs on its own goroutine. Closing ps on return ends it.
	type result struct {
		m   *redis.Message
		err error
	}
	resCh := make(chan result, 1)
	go func() {
		m, err := ps.ReceiveMessage(ctx)
		resCh <- result{m, err}
	}()

	select {
	case <-ctx.Done():
		return response, ctx.Err()
	case res := <-resCh:
		if res.err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return response, ctxErr
			}
			return response, res.err
		}

		response = neffos.DeserializeMessage(neffos.TextMessage, []byte(res.m.Payload), false, false)
		return response, response.Err
	}
}

// NotifyAsk publishes msg, the reply to the Server.Ask waiting on token, to
// that Ask's channel. It's called on a server connection's read when a
// message answers an Ask.
func (exc *StackExchange) NotifyAsk(msg neffos.Message, token string) error {
	msg.ClearWait()
	return exc.client.Publish(context.Background(), exc.askChannel(token), msg.Serialize()).Err()
}

// Subscribe subscribes to a specific namespace,
// it's called automatically on neffos namespace connected.
// It sends SUBSCRIBE and returns without waiting for the confirmation.
// A failure is reported through neffos.Debugf.
func (exc *StackExchange) Subscribe(c *neffos.Conn, namespace string) {
	ps := exc.subscriber(c)
	if ps == nil {
		return
	}

	ctx, cancel := exc.timeoutContext()
	defer cancel()

	if err := ps.Subscribe(ctx, exc.getChannel(namespace, "", "")); err != nil {
		neffos.Debugf("redis stackexchange: [%s] subscribe to namespace %q: %v", c.ID(), namespace, err)
	}
}

// Unsubscribe unsubscribes from a specific namespace,
// it's called automatically on neffos namespace disconnect.
// A failure is reported through neffos.Debugf.
func (exc *StackExchange) Unsubscribe(c *neffos.Conn, namespace string) {
	ps := exc.subscriber(c)
	if ps == nil {
		return
	}

	ctx, cancel := exc.timeoutContext()
	defer cancel()

	if err := ps.Unsubscribe(ctx, exc.getChannel(namespace, "", "")); err != nil {
		neffos.Debugf("redis stackexchange: [%s] unsubscribe from namespace %q: %v", c.ID(), namespace, err)
	}
}

// OnDisconnect closes the connection's subscriber that OnConnect created.
// That unsubscribes it from every channel and ends its read goroutine.
// It's called automatically when a connection goes offline,
// manually by server or client or by network failure.
func (exc *StackExchange) OnDisconnect(c *neffos.Conn) {
	exc.mu.Lock()
	ps := exc.subscribers[c]
	delete(exc.subscribers, c)
	exc.mu.Unlock()

	if ps != nil {
		ps.Close()
	}
}
