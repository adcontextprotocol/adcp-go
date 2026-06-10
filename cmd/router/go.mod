module github.com/adcontextprotocol/adcp-go/cmd/router

go 1.26.2

require (
	github.com/adcontextprotocol/adcp-go v0.0.0
	github.com/adcontextprotocol/adcp-go/targeting/prommetrics v0.0.0
)

require (
	github.com/adcontextprotocol/adcp-go/adcp v0.0.0 // indirect
	github.com/hashicorp/golang-lru/v2 v2.0.7 // indirect
	github.com/klauspost/cpuid/v2 v2.2.10 // indirect
	golang.org/x/crypto v0.51.0 // indirect
	golang.org/x/sys v0.44.0 // indirect
	lukechampine.com/blake3 v1.4.1 // indirect
)

replace (
	github.com/adcontextprotocol/adcp-go => ../../
	github.com/adcontextprotocol/adcp-go/targeting/prommetrics => ../../targeting/prommetrics
)

replace github.com/adcontextprotocol/adcp-go/adcp => ../../adcp
