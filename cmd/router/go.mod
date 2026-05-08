module github.com/adcontextprotocol/adcp-go/cmd/router

go 1.25.0

require (
	github.com/adcontextprotocol/adcp-go v0.0.0
	github.com/adcontextprotocol/adcp-go/targeting/prommetrics v0.0.0
)

require (
	golang.org/x/crypto v0.48.0 // indirect
	golang.org/x/sys v0.42.0 // indirect
)

replace (
	github.com/adcontextprotocol/adcp-go => ../../
	github.com/adcontextprotocol/adcp-go/targeting/prommetrics => ../../targeting/prommetrics
)
