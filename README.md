<img src="gh_logo.png" />

[![build status](https://img.shields.io/travis/kataras/neffos/master.svg?style=for-the-badge)](https://travis-ci.org/kataras/neffos) [![report card](https://img.shields.io/badge/report%20card-a%2B-ff3333.svg?style=for-the-badge)](https://goreportcard.com/report/github.com/kataras/neffos) [![godocs](https://img.shields.io/badge/go-%20docs-488AC7.svg?style=for-the-badge)](https://godoc.org/github.com/kataras/neffos) [![view examples](https://img.shields.io/badge/learn%20by-examples-0077b3.svg?style=for-the-badge)](https://github.com/kataras/neffos/tree/master/_examples) [![chat](https://img.shields.io/gitter/room/neffos-framework/community.svg?color=blue&logo=gitter&style=for-the-badge)](https://gitter.im/neffos-framework/community) [![frontend pkg](https://img.shields.io/badge/browser%20-module-BDB76B.svg?style=for-the-badge)](https://github.com/kataras/neffos.js)

## Installation

The only requirement is the [Go Programming Language](https://golang.org/dl/)

```sh
$ go get -u github.com/kataras/neffos
```

## Go Client

Built'n with this package. Types like `Conn`, `NSConn`, `Room` and `ConnHandler[Events, Namespaces, WithTimeout]` are used by both sides(`New` for server, `Dial` for client).

The `neffos` package is "hybrid/isomorphic", same code can be used for both server-side and client-side connections. See [_examples](_examples) for more.

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

## Documentation

Detailed documentation can be found at [godocs](https://godoc.org/github.com/kataras/neffos).

<!--
<pre>
</pre>
-->

[![](ascii_outline.png)](ascii_outline.txt)

## License

`neffos` is licensed under the [MIT](https://tldrlegal.com/license/mit-license) [License](LICENSE).

[![FOSSA Status](https://app.fossa.io/api/projects/git%2Bgithub.com%2Fkataras%2Fneffos.svg?type=large)](https://app.fossa.io/projects/git%2Bgithub.com%2Fkataras%2Fneffos?ref=badge_large)