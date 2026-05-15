package identityagent

import (
	"strings"
	"testing"

	"github.com/adcontextprotocol/adcp-go/tmproto"
)

func TestParseTmpxPriority(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		want    []tmproto.UIDType
		wantErr string
	}{
		{name: "empty", in: "", want: nil},
		{name: "whitespace only", in: "  ,  ", want: []tmproto.UIDType{}},
		{
			name: "two entries",
			in:   "uid2, id5",
			want: []tmproto.UIDType{tmproto.UIDTypeUID2, tmproto.UIDTypeID5},
		},
		{
			name:    "duplicate rejected",
			in:      "uid2,uid2",
			wantErr: "appears more than once",
		},
		{
			name:    "unknown type rejected",
			in:      "uid2,bogus",
			wantErr: "not a TMPX-encodable",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseTmpxPriority(tc.in)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("expected error containing %q, got %v", tc.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("len got %v want %v", got, tc.want)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Fatalf("index %d: got %v want %v", i, got[i], tc.want[i])
				}
			}
		})
	}
}
