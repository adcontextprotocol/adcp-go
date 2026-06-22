//go:generate sh -c "cd ../internal/generate && go run . -schema ../../adcp/schemas/tmp -enums ../../adcp/schemas/enums -merge-schemas ../../adcp/schemas/core -overlay go-overlays.json -out ../../tmproto/types_gen.go -pkg tmproto"

package tmproto
