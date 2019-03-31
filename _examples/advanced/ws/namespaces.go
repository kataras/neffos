package ws

type connHandler interface {
	getNamespaces() Namespaces
}

var (
	_ connHandler = (Events)(nil)
	_ connHandler = (Namespaces)(nil)
)

var (
	OnNamespaceConnect    = "_OnNamespaceConnect"
	OnNamespaceDisconnect = "_OnNamespaceDisconnect"
)

type Events map[string]MessageHandlerFunc

func (e Events) getNamespaces() Namespaces {
	return Namespaces{"": e}
}

func (e Events) fireOnNamespaceConnect(c NSConn, msg Message) error {
	if h, ok := e[OnNamespaceConnect]; ok {
		return h(c, msg)
	}

	return nil
}

func (e Events) fireOnNamespaceDisconnect(c NSConn, msg Message) error {
	if h, ok := e[OnNamespaceDisconnect]; ok {
		return h(c, msg)
	}

	return nil
}

func (e Events) fireEvent(c NSConn, msg Message) error {
	if h, ok := e[msg.Event]; ok {
		return h(c, msg)
	}

	return nil
}

type Namespaces map[string]Events

func (nss Namespaces) getNamespaces() Namespaces { return nss }
