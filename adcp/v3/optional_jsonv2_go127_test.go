//go:build goexperiment.jsonv2 && go1.27

package adcp

import jsonv2 "encoding/json/v2"

func jsonv2Marshal(v any) ([]byte, error) { return jsonv2.Marshal(v) }

func jsonv2Unmarshal(data []byte, v any) error { return jsonv2.Unmarshal(data, v) }
