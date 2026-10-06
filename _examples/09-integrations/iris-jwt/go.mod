module github.com/kataras/neffos/_examples/09-integrations/iris-jwt

go 1.27

require (
	github.com/kataras/iris/v14 v14.0.0-00010101000000-000000000000
	github.com/kataras/neffos v0.1.0
)

require (
	github.com/andybalholm/brotli v1.2.6 // indirect
	github.com/aymerick/douceur v0.2.0 // indirect
	github.com/blang/semver/v4 v4.0.0 // indirect
	github.com/golang/snappy v1.0.0 // indirect
	github.com/gomarkdown/markdown v0.0.0-20260923180740-94fc73f6b1a3 // indirect
	github.com/gorilla/css v1.0.1 // indirect
	github.com/gorilla/websocket v1.5.3 // indirect
	github.com/iris-contrib/schema v0.0.7-0.20250208085038-f68827b0bbfa // indirect
	github.com/jpillora/backoff v1.0.0 // indirect
	github.com/kataras/golog v0.2.0 // indirect
	github.com/kataras/httpclient v0.2.0 // indirect
	github.com/kataras/jwt v0.3.0 // indirect
	github.com/klauspost/compress v1.20.1 // indirect
	github.com/microcosm-cc/bluemonday v1.0.27 // indirect
	github.com/schollz/closestmatch v2.1.0+incompatible // indirect
	github.com/vmihailenco/msgpack/v5 v5.4.1 // indirect
	github.com/vmihailenco/tagparser/v2 v2.0.0 // indirect
	go.uber.org/multierr v1.11.0 // indirect
	golang.ngrok.com/muxado/v2 v2.0.2 // indirect
	golang.ngrok.com/ngrok/v2 v2.2.0 // indirect
	golang.org/x/crypto v0.57.0 // indirect
	golang.org/x/net v0.59.0 // indirect
	golang.org/x/oauth2 v0.37.0 // indirect
	golang.org/x/sys v0.48.0 // indirect
	golang.org/x/text v0.42.0 // indirect
	golang.org/x/time v0.16.0 // indirect
	google.golang.org/protobuf v1.36.12 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)

// Iris v14 is not published yet and neffos v0.1.0 is not tagged yet: build
// against the sibling checkouts. Remove both lines when they are.
replace (
	github.com/kataras/iris/v14 => ../../../../iris-private
	github.com/kataras/neffos => ../../..
)
