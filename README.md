# WIP

[![FOSSA Status](https://app.fossa.com/api/projects/git%2Bgithub.com%2Fkataras%2Fneffos.svg?type=shield)](https://app.fossa.com/projects/git%2Bgithub.com%2Fkataras%2Fneffos?ref=badge_shield)

## Go Client

Built'n with this package. Types like `Conn`, `NSConn`, `Room` and `ConnHandler[Events, Namespaces, WithTimeout]` are used by both sides(`New` for server, `Dial` for client).

The `neffos` package is "hybrid/isomorphic", same code can be used for both server-side and client-side connections. See [_examples](_examples) for more.

## Typescript/Javascript Client

The client package lives on its own repository for front-end developers: <https://github.com/kataras/neffos.js>.

`neffos.js` client can run through any modern **browser** and **nodejs**. 