<img src="gh_logo.png" />

[![build status](https://img.shields.io/travis/kataras/neffos/master.svg?style=for-the-badge)](https://travis-ci.org/kataras/neffos) [![report card](https://img.shields.io/badge/report%20card-a%2B-ff3333.svg?style=for-the-badge)](https://goreportcard.com/report/github.com/kataras/neffos) [![godocs](https://img.shields.io/badge/go-%20docs-488AC7.svg?style=for-the-badge)](https://godoc.org/github.com/kataras/neffos) [![view examples](https://img.shields.io/badge/learn%20by-examples-0077b3.svg?style=for-the-badge)](https://github.com/kataras/neffos/tree/master/_examples) [![chat](https://img.shields.io/gitter/room/neffos-framework/community.svg?color=blue&logo=gitter&style=for-the-badge)](https://gitter.im/neffos-framework/community) [![frontend pkg](https://img.shields.io/badge/browser%20-module-BDB76B.svg?style=for-the-badge)](https://github.com/kataras/neffos.js)

## Installation

The only requirement is the [Go Programming Language](https://golang.org/dl/)

```sh
$ go get -u github.com/kataras/neffos
```

## Go Client

Built'n with this package. Types like `Conn`, `NSConn`, `Room` and `ConnHandler[Events, Namespaces, WithTimeout]` are used by both sides(`New` for server, `Dial` for client).

The `neffos` package is "hybrid/isomorphic", same code can be used for both server-side and client-side connections.

## Typescript/Javascript Client

The client package lives on its own repository for front-end developers: <https://github.com/kataras/neffos.js>.

`neffos.js` client can run through any modern **browser** and **nodejs**.

<!-- TODO: >
## Quick start
 
```sh
# assume the following code in example.go file
$ cat example.go
```

```go
package main

import (
    "github.com/kataras/neffos"
    "github.com/kataras/neffos/gobwas"

)

func main() {

}
```

```
# run example.go and visit http://localhost:8080 on browser
$ go run example.go
```
-->

## Getting Started

```go
package server

import (
    "log"
    "net/http"
    "time"

    "github.com/kataras/neffos"
    "github.com/kataras/neffos/gorilla"
)

func onNamespaceConnect(c *neffos.NSConn, msg neffos.Message) error {
    log.Printf("[%s] connecting to [%s]. To disallow return a non-nil error.",
        c.Conn.ID(), msg.Namespace)
    return nil
}

func onNamespaceConnected(c *neffos.NSConn, msg neffos.Message) error {
    log.Printf("[%s] connected to [%s].", c.Conn.ID(), msg.Namespace)
    return nil
}

func onNamespaceDisconnect(c *neffos.NSConn, msg neffos.Message) error {
    log.Printf("[%s] disconnected from [%s].", c.Conn.ID(), msg.Namespace)
    return nil
}

func onNotice(c *neffos.NSConn, msg neffos.Message) error {
    send := append([]byte("got"), msg.Body...)
    c.Emit("reply", send)
    return nil
}

func onWhatTimeIsIt(c *neffos.NSConn, msg neffos.Message) error {
    now := time.Now().Format(time.RFC3339)
    // `neffos.Reply` can be optionally used when publish the
    // same message but setting a different body,
    // mostly useful on acknowledgements requests(`Conn.Ask`)
    // when client(or server) is blocking until response received.
    return neffos.Reply([]byte(now))
}

func onChat(c *neffos.NSConn, msg neffos.Message) error {
    c.Conn.Server().Broadcast(c, msg)
    return nil
}

func main() {
    var events = neffos.Namespaces{
        "default": neffos.Events{
            neffos.OnNamespaceConnect:    onNamespaceConnect,
            neffos.OnNamespaceConnected:  onNamespaceConnected,
            neffos.OnNamespaceDisconnect: onNamespaceDisconnect,

            "notice":       onNotice,
            "whatTimeIsIt": onWhatTimeIsIt,
            "chat":         onChat,
        },
    }

    server := neffos.New(gorilla.DefaultUpgrader, events)
    server.OnConnect = func(c *neffos.Conn) error {
        log.Printf(`[%s] connected to the server.
        Return a non-nil error to dismiss the client with an error.`, c.ID())
        return nil
    }
    server.OnDisconnect = func(c *neffos.Conn) {
        log.Printf("[%s] disconnected from the server.", c.ID())
    }
    server.OnUpgradeError = func(err error) {
        log.Printf("error: %v", err)
    }

    http.Handle("/echo", server)
    http.Handle("/", http.FileServer(http.Dir("./public")))
    log.Fatal(http.ListenAndServe(":8080", nil))
}

```

```sh
$ go run server.go
```

Comprehensive **examples** can be found at [_examples](_examples).

[![](ascii_outline.png)](ascii_outline.txt)

Detailed documentation can be found at [godocs](https://godoc.org/github.com/kataras/neffos).

## License

`neffos` is licensed under the [MIT](https://tldrlegal.com/license/mit-license) [License](LICENSE).

[![FOSSA Status](https://app.fossa.io/api/projects/git%2Bgithub.com%2Fkataras%2Fneffos.svg?type=large)](https://app.fossa.io/projects/git%2Bgithub.com%2Fkataras%2Fneffos?ref=badge_large)