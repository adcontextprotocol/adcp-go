package urlutil_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/adcontextprotocol/adcp-go/urlutil"
)

func TestValidateIdentifier(t *testing.T) {
	cases := []struct {
		name    string
		idType  urlutil.IdentifierType
		value   string
		wantErr bool
	}{
		{"domain bare", urlutil.IdentifierDomain, "example.com", false},
		{"domain wildcard", urlutil.IdentifierDomain, "*.example.com", false},
		{"domain subdomain value", urlutil.IdentifierDomain, "edition.example.com", false},
		{"domain uppercase", urlutil.IdentifierDomain, "Example.COM", true},
		{"domain url", urlutil.IdentifierDomain, "https://www.example.com/path", true},
		{"domain port", urlutil.IdentifierDomain, "example.com:8080", true},
		{"domain trailing dot", urlutil.IdentifierDomain, "example.com.", true},
		{"domain single label", urlutil.IdentifierDomain, "example", true},
		{"domain punycode", urlutil.IdentifierDomain, "xn--bcher-kva.de", false},
		{"domain unicode label", urlutil.IdentifierDomain, "bücher.de", true},
		{"domain label starts hyphen", urlutil.IdentifierDomain, "-example.com", true},
		{"domain empty", urlutil.IdentifierDomain, "", true},
		{"domain too long", urlutil.IdentifierDomain, strings.Repeat("a", 250) + ".com", true},

		{"subdomain ok", urlutil.IdentifierSubdomain, "edition.example.com", false},
		{"subdomain wildcard refused", urlutil.IdentifierSubdomain, "*.example.com", true},
		{"subdomain uppercase", urlutil.IdentifierSubdomain, "Edition.example.com", true},

		{"ios bundle", urlutil.IdentifierIOSBundle, "com.example.app", false},
		{"ios bundle underscore", urlutil.IdentifierIOSBundle, "com.example.my_app", true},
		{"ios bundle single label", urlutil.IdentifierIOSBundle, "myapp", true},
		{"android package", urlutil.IdentifierAndroidPackage, "com.example.my_app", false},
		{"android label starts digit", urlutil.IdentifierAndroidPackage, "com.9example.app", true},
		{"google play id is package", urlutil.IdentifierGooglePlayID, "com.example.app", false},
		{"bundle id generic", urlutil.IdentifierBundleID, "com.example.my-app_2", false},

		{"app store id numeric", urlutil.IdentifierAppleAppStoreID, "123456789", false},
		{"app store id not numeric", urlutil.IdentifierAppleAppStoreID, "id123456789", true},
		{"podcast id numeric", urlutil.IdentifierApplePodcastID, "987654321", false},

		{"rss url https", urlutil.IdentifierRSSURL, "https://example.com/feed.xml", false},
		{"rss url schemeless", urlutil.IdentifierRSSURL, "example.com/feed.xml", true},

		{"network id passthrough", urlutil.IdentifierNetworkID, "net-42_X", false},
		{"venue id empty", urlutil.IdentifierVenueID, "", true},
		{"unknown type", urlutil.IdentifierType("bogus"), "example.com", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := urlutil.ValidateIdentifier(tc.idType, tc.value)
			if tc.wantErr {
				require.Error(t, err)
				assert.ErrorIs(t, err, urlutil.ErrInvalid)
				var ierr *urlutil.IdentifierError
				require.True(t, errors.As(err, &ierr))
				assert.Equal(t, tc.idType, ierr.Type)
				assert.Equal(t, tc.value, ierr.Value)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestNormalizeIdentifier(t *testing.T) {
	cases := []struct {
		name          string
		idType        urlutil.IdentifierType
		value         string
		want          string
		wantRewritten bool
		wantErr       bool
	}{
		{"domain already canonical", urlutil.IdentifierDomain, "example.com", "example.com", false, false},
		{"domain pasted url", urlutil.IdentifierDomain, "https://www.example.com/path", "www.example.com", true, false},
		{"domain pasted url with port", urlutil.IdentifierDomain, "https://example.com:8443/x?y=1", "example.com", true, false},
		{"domain schemeless path", urlutil.IdentifierDomain, "example.com/path", "example.com", true, false},
		{"domain uppercase and trailing dot", urlutil.IdentifierDomain, "WWW.Example.COM.", "www.example.com", true, false},
		{"domain wildcard url", urlutil.IdentifierDomain, "*.Example.com", "*.example.com", true, false},
		{"domain idn to punycode", urlutil.IdentifierDomain, "bücher.de", "xn--bcher-kva.de", true, false},
		{"domain garbage", urlutil.IdentifierDomain, "not a host!!", "", false, true},
		{"domain empty", urlutil.IdentifierDomain, "", "", false, true},

		{"subdomain pasted url", urlutil.IdentifierSubdomain, "https://edition.example.com/path", "edition.example.com", true, false},
		{"subdomain wildcard refused", urlutil.IdentifierSubdomain, "*.example.com", "", false, true},

		{"bundle trim only", urlutil.IdentifierIOSBundle, "  com.example.app ", "com.example.app", true, false},
		{"bundle case preserved", urlutil.IdentifierAndroidPackage, "com.Example.App", "com.Example.App", false, false},
		{"numeric trim", urlutil.IdentifierAppleAppStoreID, " 123456789 ", "123456789", true, false},

		{"passthrough untouched", urlutil.IdentifierNetworkID, " net-42 ", " net-42 ", false, false},
		{"passthrough empty refused", urlutil.IdentifierVenueID, "", "", false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, rewritten, err := urlutil.NormalizeIdentifier(tc.idType, tc.value)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
			assert.Equal(t, tc.wantRewritten, rewritten)
			// The canonical form must validate strictly.
			assert.NoError(t, urlutil.ValidateIdentifier(tc.idType, got))
		})
	}
}

func TestValidatePublisherDomainValue(t *testing.T) {
	cases := []struct {
		name    string
		domain  string
		wantErr bool
	}{
		{"bare domain", "example.com", false},
		{"subdomain", "edition.example.com", false},
		{"single label", "pub1", false},
		{"url", "https://www.example.com/path", true},
		{"wildcard", "*.example.com", true},
		{"uppercase", "Example.com", true},
		{"port", "example.com:8080", true},
		{"trailing dot", "example.com.", true},
		{"underscore", "ex_ample.com", true},
		{"empty", "", true},
		{"label too long", strings.Repeat("a", 64) + ".com", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := urlutil.ValidatePublisherDomain(tc.domain)
			if tc.wantErr {
				require.Error(t, err)
				assert.ErrorIs(t, err, urlutil.ErrInvalid)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tc.domain, got)
			}
		})
	}
}
