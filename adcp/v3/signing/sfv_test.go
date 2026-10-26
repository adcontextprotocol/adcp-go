package signing

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSFDictionaryRoundTrip(t *testing.T) {
	cases := []struct{ in, out string }{
		{`sig1="https://buyer.example"`, `sig1="https://buyer.example"`},
		{`a=1, b=2;x=?0, c`, `a=1, b=2;x=?0, c`},
		{`a=1,b=2`, `a=1, b=2`},
		{`a=(1 2);p="v", b=tok/x:y`, `a=(1 2);p="v", b=tok/x:y`},
		{`sig1=:AAEC:`, `sig1=:AAEC:`},
		{`sig1=:AAE:`, `sig1=:AAE=:`}, // padding is optional on parse
		{`d=1.500, e=-0.25, f=2.0`, `d=1.5, e=-0.25, f=2.0`},
		{`s="a\"b\\c"`, `s="a\"b\\c"`},
		{`k=()`, `k=()`},
		{`k=( "a"  "b" );q`, `k=("a" "b");q`},
		{"  a=1  ", `a=1`},
		{`sig1=("@method" "signature-agent";key="sig1");created=1;nonce="n";tag="web-bot-auth"`,
			`sig1=("@method" "signature-agent";key="sig1");created=1;nonce="n";tag="web-bot-auth"`},
		{`k=?1;p=?1`, `k;p`},
	}
	for _, tc := range cases {
		d, err := parseSFDictionary(tc.in)
		require.NoError(t, err, tc.in)
		require.Equal(t, tc.out, d.serialize(), tc.in)
	}
}

func TestSFDictionaryRejects(t *testing.T) {
	for _, in := range []string{
		``, ` `, `A=1`, `a=1,`, `a=1,,b=2`, `a=1 b=2`, `a="unterminated`, `a="bad\escape"`,
		`a=:not base64!:`, `a=?2`, `a=1.`, `a=1.2345`, `a=1234567890123456`, `a=1234567890123.1`,
		`a=(1 2`, `a=(1,2)`, `a=1, a=2`, `a=1;p=1;p=2`, `a=(1;p;p)`, `a=@`, "a=\"\x01\"",
		"a=\"caf\xc3\xa9\"", `sig1=("@method";key="a";key="b")`, strings.Repeat("a", maxStructuredFieldLen+1),
	} {
		_, err := parseSFDictionary(in)
		require.Error(t, err, "%q", in)
	}
}

func TestSFDictionaryAccessors(t *testing.T) {
	d, err := parseSFDictionary(`sig1=("@authority";req "content-digest");created=10;keyid="k";alg="ed25519"`)
	require.NoError(t, err)
	m, ok := d.get("sig1")
	require.True(t, ok)
	require.True(t, m.inner)
	require.Len(t, m.list, 2)
	require.Equal(t, `"@authority";req`, m.list[0].serialize())
	created, ok := intParam(m.params, "created")
	require.True(t, ok)
	require.EqualValues(t, 10, created)
	_, ok = stringParam(m.params, "created")
	require.False(t, ok, "created is an integer, not a string")
	keyid, ok := stringParam(m.params, "keyid")
	require.True(t, ok)
	require.Equal(t, "k", keyid)
	require.Equal(t, []string{"sig1"}, d.keys())
	_, ok = d.get("sig2")
	require.False(t, ok)
}
