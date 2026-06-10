module github.com/adcontextprotocol/adcp-go/reference/context-agent

go 1.26.2

require (
	github.com/adcontextprotocol/adcp-go v0.0.0
	github.com/adcontextprotocol/adcp-go/targeting/prommetrics v0.0.0
	github.com/stretchr/testify v1.11.1
)

require (
	github.com/adcontextprotocol/adcp-go/adcp v0.0.0 // indirect
	github.com/davecgh/go-spew v1.1.1 // indirect
	github.com/hashicorp/golang-lru/v2 v2.0.7 // indirect
	github.com/klauspost/cpuid/v2 v2.2.10 // indirect
	github.com/pmezard/go-difflib v1.0.0 // indirect
	golang.org/x/crypto v0.51.0 // indirect
	golang.org/x/sys v0.44.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
	lukechampine.com/blake3 v1.4.1 // indirect
)

replace (
	github.com/adcontextprotocol/adcp-go => ../../
	github.com/adcontextprotocol/adcp-go/targeting/prommetrics => ../../targeting/prommetrics
)

replace github.com/adcontextprotocol/adcp-go/adcp => ../../adcp
