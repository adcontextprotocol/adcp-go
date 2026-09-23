//go:generate sh -c "cd ../internal/generate && go run . -schema ../../adcp/v3/schemas/trusted-match -enums ../../adcp/v3/schemas/enums -merge-schemas ../../adcp/v3/schemas/core -overlay go-overlays.json -out ../../tmproto/types_gen.go -pkg tmproto"

package tmproto
