module github.com/adcontextprotocol/adcp-go/reference/identity-agent

go 1.22

require (
	github.com/adcontextprotocol/adcp-go v0.0.0
	github.com/alicebob/miniredis/v2 v2.33.0
	github.com/redis/go-redis/v9 v9.7.0
)

require (
	github.com/alicebob/gopher-json v0.0.0-20200520072559-a9ecdc9d1d3a // indirect
	github.com/cespare/xxhash/v2 v2.2.0 // indirect
	github.com/dgryski/go-rendezvous v0.0.0-20200823014737-9f7001d12a5f // indirect
	github.com/yuin/gopher-lua v1.1.1 // indirect
)

replace github.com/adcontextprotocol/adcp-go => ../../
