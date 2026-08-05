module github.com/adcontextprotocol/adcp-go/cmd/router

go 1.25.0

require (
	github.com/adcontextprotocol/adcp-go v0.0.0
	github.com/adcontextprotocol/adcp-go/registry v0.0.0-20260716182726-fdf3ef034a47
	github.com/adcontextprotocol/adcp-go/targeting v0.0.0
	github.com/adcontextprotocol/adcp-go/tmproto v0.1.0
	github.com/stretchr/testify v1.11.1
)

require (
	github.com/adcontextprotocol/adcp-go/urlcanon v0.1.0 // indirect
	github.com/davecgh/go-spew v1.1.1 // indirect
	github.com/hashicorp/golang-lru/v2 v2.0.7 // indirect
	github.com/klauspost/cpuid/v2 v2.2.10 // indirect
	github.com/kr/text v0.2.0 // indirect
	github.com/pmezard/go-difflib v1.0.0 // indirect
	golang.org/x/crypto v0.54.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
	lukechampine.com/blake3 v1.4.1 // indirect
)

replace github.com/adcontextprotocol/adcp-go => ../../

replace github.com/adcontextprotocol/adcp-go/registry => ../../registry

replace github.com/adcontextprotocol/adcp-go/targeting => ../../targeting
