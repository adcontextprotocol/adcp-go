module github.com/adcontextprotocol/adcp-go/e2e

go 1.25.0

require github.com/adcontextprotocol/adcp-go v0.0.0

require (
	golang.org/x/crypto v0.48.0 // indirect
	golang.org/x/sys v0.42.0 // indirect
)

require (
	github.com/davecgh/go-spew v1.1.1 // indirect
	github.com/pmezard/go-difflib v1.0.0 // indirect
	github.com/stretchr/testify v1.11.1
	gopkg.in/yaml.v3 v3.0.1 // indirect
)

replace github.com/adcontextprotocol/adcp-go => ../
