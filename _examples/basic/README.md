# Basic Example

## Requirements

- [NPM](https://nodejs.org)
- [Go Programming Language](https://golang.org/dl)

## How to run

Open a terminal window instance and execute:

```sh
$ cd ./browser
# build the browser-side client: ./browser/bundle.js which ./browser/index.html imports.
$ npm install && npm run-script build 
$ cd ../ # go back to ./basic
$ go run main.go server # start the neffos websocket server.
```

Open some web browser windows and navigate to <http://localhost:8080>,
each window will ask for a username and a room to join, each window(client connection) and server get notified for namespace connected/disconnected, room joined/left and chat events.

To start the go client side just open a new terminal window and execute:
```sh
$ go run main.go client
```

It will ask you for username and a room to join as well, it acts exactly the same as the `./browser/app.js` browser-side application.
