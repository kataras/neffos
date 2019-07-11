package neffos

// StackExchange is an optional interface
// that can be used to change the way neffos
// sends messages to its clients, i.e
// communication between multiple neffos servers.
//
// See the "kataras/neffos/stackexchange" subpackage for more details.
// Real-World example and usage documentation
// can be found at: "kataras/neffos/_examples/redis".
type StackExchange interface {
	// OnConnect should prepare the connection's subscriber.
	// It's called automatically after the neffos server's OnConnect (if any)
	// on incoming client connections.
	OnConnect(c *Conn) error
	// OnDisconnect should close the connection's subscriber that
	// created on the `OnConnect` method.
	// It's called automatically when a connection goes offline,
	// manually by server or client or by network failure.
	OnDisconnect(c *Conn)

	// Publish should publish a message through a stackexchange.
	// It's called automatically on neffos broadcasting.
	Publish(msg Message) bool
	// Subscribe should subscribe to a specific namespace,
	// it's called automatically on neffos namespace connected.
	Subscribe(c *Conn, namespace string)
	// Unsubscribe should unsubscribe from a specific namespace,
	// it's called automatically on neffos namespace disconnect.
	Unsubscribe(c *Conn, namespace string) // should close the subscriber.
}
