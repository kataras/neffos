module github.com/kataras/neffos

go 1.13

replace github.com/mediocregopher/radix => github.com/neffos-contrib/radix/v3 v3.4.3

require (
	github.com/gobwas/httphead v0.0.0-20180130184737-2c6c146eadee // indirect
	github.com/gobwas/pool v0.2.0 // indirect
	github.com/gobwas/ws v1.0.2
	github.com/gorilla/websocket v1.4.1
	github.com/iris-contrib/go.uuid v2.0.0+incompatible
	github.com/mediocregopher/radix/v3 v3.4.2
	github.com/nats-io/nats.go v1.9.1
	golang.org/x/sync v0.0.0-20190911185100-cd5d95a43a6e
)
