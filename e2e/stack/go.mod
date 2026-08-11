module github.com/adcontextprotocol/adcp-go/e2e/stack

go 1.25.0

require (
	github.com/adcontextprotocol/adcp-go v0.1.0
	github.com/adcontextprotocol/adcp-go/targeting v0.1.0
	github.com/adcontextprotocol/adcp-go/tmproto v0.1.0
	github.com/adcontextprotocol/adcp-go/urlcanon v0.1.0
	github.com/redis/go-redis/v9 v9.22.0
)

require (
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/hashicorp/golang-lru/v2 v2.0.7 // indirect
	github.com/klauspost/cpuid/v2 v2.2.10 // indirect
	github.com/kr/text v0.2.0 // indirect
	go.uber.org/atomic v1.11.0 // indirect
	golang.org/x/crypto v0.54.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
	lukechampine.com/blake3 v1.4.1 // indirect
)

replace github.com/adcontextprotocol/adcp-go => ../../
