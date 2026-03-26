module github.com/adcontextprotocol/adcp-go/bench

go 1.22

require capnproto.org/go/capnp/v3 v3.1.0-alpha.2

require (
	github.com/colega/zeropool v0.0.0-20230505084239-6fb4a4f75381 // indirect
	golang.org/x/sync v0.7.0 // indirect
)

replace github.com/adcontextprotocol/adcp-go => ../
