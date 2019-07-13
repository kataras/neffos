module github.com/kataras/neffos

go 1.12

replace (
	golang.org/x/build => github.com/golang/build v0.0.0-20190709001953-30c0e6b89ea0
	golang.org/x/crypto => github.com/golang/crypto v0.0.0-20190701094942-4def268fd1a4
	golang.org/x/debug => github.com/golang/debug v0.0.0-20190515041333-621e2d3f35da
	golang.org/x/lint => github.com/golang/lint v0.0.0-20190409202823-959b441ac422
	golang.org/x/net => github.com/golang/net v0.0.0-20190628185345-da137c7871d7
	golang.org/x/perf => github.com/golang/perf v0.0.0-20190620143337-7c3f2128ad9b
	golang.org/x/sync => github.com/golang/sync v0.0.0-20190423024810-112230192c58
	golang.org/x/sys => github.com/golang/sys v0.0.0-20190710143415-6ec70d6a5542
	golang.org/x/text => github.com/golang/text v0.3.2
	golang.org/x/time => github.com/golang/time v0.0.0-20190308202827-9d24e82272b4
	golang.org/x/tools => github.com/golang/tools v0.0.0-20190711191110-9a621aea19f8
)

require (
	github.com/RussellLuo/timingwheel v0.0.0-20190518031256-7b3d146a266a
	github.com/gobwas/httphead v0.0.0-20180130184737-2c6c146eadee // indirect
	github.com/gobwas/pool v0.2.0 // indirect
	github.com/gobwas/ws v1.0.1
	github.com/gorilla/websocket v1.4.0
	github.com/iris-contrib/go.uuid v2.0.0+incompatible
	github.com/mediocregopher/radix/v3 v3.3.0
	github.com/nats-io/nats.go v1.8.1
)
