module github.com/adcontextprotocol/adcp-go/reference/identity-agent

go 1.25.0

require (
	github.com/adcontextprotocol/adcp-go v0.0.0
	github.com/adcontextprotocol/adcp-go/targeting/prommetrics v0.0.0
	github.com/valkey-io/valkey-glide/go/v2 v2.3.1
)

require google.golang.org/protobuf v1.34.2 // indirect

replace (
	github.com/adcontextprotocol/adcp-go => ../../
	github.com/adcontextprotocol/adcp-go/targeting/prommetrics => ../../targeting/prommetrics
)
