module github.com/adcontextprotocol/adcp-go/adcp/v3/signing/awskms

go 1.26.2

require (
	github.com/adcontextprotocol/adcp-go/adcp/v3 v3.0.0
	github.com/aws/aws-sdk-go-v2/service/kms v1.58.0
	github.com/stretchr/testify v1.11.1
)

// TODO: remove this replace and bump the require above once an adcp/v3 tag
// including signing.SigningProvider is cut. SigningProvider is introduced in
// the same PR as this module, so no released adcp/v3 tag has it yet — the
// pinned v3.0.0 predates it, which otherwise leaves this module unbuildable
// (and untested by CI, which builds each module standalone rather than via
// go.work) from the moment it's merged until the next adcp/v3 release.
// Matches the same relative-path replace convention cmd/*, bench/*, e2e, and
// reference/context-agent already use for their own local adcp-go
// dependencies.
replace github.com/adcontextprotocol/adcp-go/adcp/v3 => ../..

require (
	github.com/adcontextprotocol/adcp-go/urlcanon v0.1.0 // indirect
	github.com/aws/aws-sdk-go-v2 v1.45.1 // indirect
	github.com/aws/aws-sdk-go-v2/internal/configsources v1.5.1 // indirect
	github.com/aws/aws-sdk-go-v2/internal/endpoints/v2 v2.8.1 // indirect
	github.com/aws/smithy-go v1.28.1 // indirect
	github.com/davecgh/go-spew v1.1.1 // indirect
	github.com/google/jsonschema-go v0.4.2 // indirect
	github.com/modelcontextprotocol/go-sdk v1.5.0 // indirect
	github.com/pmezard/go-difflib v1.0.0 // indirect
	github.com/segmentio/asm v1.1.3 // indirect
	github.com/segmentio/encoding v0.5.4 // indirect
	github.com/yosida95/uritemplate/v3 v3.0.2 // indirect
	golang.org/x/oauth2 v0.35.0 // indirect
	golang.org/x/sys v0.41.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)
