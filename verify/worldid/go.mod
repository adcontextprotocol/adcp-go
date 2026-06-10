module github.com/adcontextprotocol/adcp-go/verify/worldid

go 1.25.0

require github.com/adcontextprotocol/adcp-go v0.0.0

require (
	github.com/adcontextprotocol/adcp-go/urlcanon v0.0.0 // indirect
	github.com/hashicorp/golang-lru/v2 v2.0.7 // indirect
	github.com/klauspost/cpuid/v2 v2.2.10 // indirect
	golang.org/x/crypto v0.51.0 // indirect
	golang.org/x/sys v0.44.0 // indirect
	lukechampine.com/blake3 v1.4.1 // indirect
)

replace github.com/adcontextprotocol/adcp-go => ../..

replace github.com/adcontextprotocol/adcp-go/urlcanon => ../../urlcanon
