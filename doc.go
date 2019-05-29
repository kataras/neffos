/*
Source code and other details for the project are available at GitHub:
   https://github.com/kataras/neffos

Current Version
	0.1.6

Installation

The only requirement is the Go Programming Language
	$ go get -u github.com/kataras/neffos

Go Client
	Built'n with this package.
	Types like `Conn`, `NSConn`, `Room` and `ConnHandler[Events, Namespaces, WithTimeout]` are used by both sides(`New` for server, `Dial` for client).
	The neffos package is "hybrid/isomorphic", same code can be used for both server-side and client-side connections.

Typescript/Javascript Client
	The client package lives on its own repository for front-end developers: <https://github.com/kataras/neffos.js>.
	The neffos.js client can run through any modern **browser** and **nodejs**.

Examples
   https://github.com/kataras/neffos/tree/master/_examples
*/

package neffos
