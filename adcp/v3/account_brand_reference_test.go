package adcp

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

// The fields of AccountReference (core/account-ref.json) and
// BrandReference (core/brand-ref.json) that the hand-written types once
// dropped (adcontextprotocol/adcp-go#611). Each fixture is valid against the
// schema and sets every property it names, so a missing field or a lost false
// fails the round trip.

const (
	accountRefSchema = "core/account-ref.json"
	brandRefSchema   = "core/brand-ref.json"
)

const brandKitOverrideJSON = `{
	"logo":{"asset_type":"image","url":"https://cdn.nova-athletics.example/logo.png","width":512,"height":128},
	"colors":{"primary":"#112233","secondary":"#445566","accent":"#778899"},
	"voice":"warm and direct",
	"tagline":"Run further"
}`

func TestAccountReferenceRoundTrip(t *testing.T) {
	const operatorUnit = `"operator_unit":{"id":"234284238","name":"Nova EMEA"}`
	const brand = `"brand":{"domain":"nova-athletics.example","brand_id":"spark","countries":["DE","NL"]}`

	t.Run("every property with sandbox true", func(t *testing.T) {
		ref := requireRoundTrip[AccountReference](t, accountRefSchema,
			`{`+brand+`,"operator":"pinnacle-media.com",`+operatorUnit+`,"currency":"EUR","timezone":"Europe/Amsterdam","sandbox":true}`)
		require.NotNil(t, ref.OperatorUnit)
		require.Equal(t, OperatorUnit{ID: "234284238", Name: "Nova EMEA"}, *ref.OperatorUnit)
		require.Equal(t, "EUR", ref.Currency)
		require.Equal(t, "Europe/Amsterdam", ref.Timezone)
		require.NotNil(t, ref.Sandbox)
		require.True(t, *ref.Sandbox)
		require.Equal(t, []string{"DE", "NL"}, ref.Brand.Countries)
	})

	t.Run("explicit sandbox false is re-encoded as false", func(t *testing.T) {
		ref := requireRoundTrip[AccountReference](t, accountRefSchema,
			`{"brand":{"domain":"acme-corp.com"},"operator":"acme-corp.com","sandbox":false}`)
		require.NotNil(t, ref.Sandbox)
		require.False(t, *ref.Sandbox)
	})

	t.Run("absent sandbox stays absent", func(t *testing.T) {
		ref := requireRoundTrip[AccountReference](t, accountRefSchema,
			`{"brand":{"domain":"acme-corp.com"},"operator":"acme-corp.com"}`)
		require.Nil(t, ref.Sandbox)
	})

	t.Run("operator unit without a name", func(t *testing.T) {
		ref := requireRoundTrip[AccountReference](t, accountRefSchema,
			`{"brand":{"domain":"acme-corp.com"},"operator":"acme-corp.com","operator_unit":{"id":"seat-7"}}`)
		require.Equal(t, OperatorUnit{ID: "seat-7"}, *ref.OperatorUnit)
	})

	t.Run("account_id alone", func(t *testing.T) {
		ref := requireRoundTrip[AccountReference](t, accountRefSchema, `{"account_id":"acc_acme_001"}`)
		require.Equal(t, "acc_acme_001", ref.AccountID)
		require.Nil(t, ref.Sandbox)
		require.Nil(t, ref.OperatorUnit)
	})

	t.Run("a built reference encodes the same way", func(t *testing.T) {
		withFalse, err := json.Marshal(AccountReference{Operator: "acme-corp.com", Sandbox: Bool(false)})
		require.NoError(t, err)
		require.JSONEq(t, `{"operator":"acme-corp.com","sandbox":false}`, string(withFalse))

		unset, err := json.Marshal(AccountReference{Operator: "acme-corp.com"})
		require.NoError(t, err)
		require.JSONEq(t, `{"operator":"acme-corp.com"}`, string(unset))
	})
}

func TestBrandReferenceRoundTrip(t *testing.T) {
	t.Run("every property", func(t *testing.T) {
		b := requireRoundTrip[BrandReference](t, brandRefSchema, `{
			"domain":"nova-athletics.example",
			"brand_id":"spark",
			"countries":["DE","NL"],
			"industries":["sporting_goods"],
			"data_subject_contestation":{"email":"privacy@nova-athletics.example","languages":["en","nl"]},
			"brand_kit_override":`+brandKitOverrideJSON+`
		}`)
		require.Equal(t, []string{"DE", "NL"}, b.Countries)
		require.NotNil(t, b.BrandKitOverride)
		require.NotNil(t, b.BrandKitOverride.Logo)
		require.Equal(t, 512, b.BrandKitOverride.Logo.Width)
		require.Equal(t, BrandKitOverrideColors{Primary: "#112233", Secondary: "#445566", Accent: "#778899"}, *b.BrandKitOverride.Colors)
		require.Equal(t, "warm and direct", b.BrandKitOverride.Voice)
		require.Equal(t, "Run further", b.BrandKitOverride.Tagline)
	})

	t.Run("a partial override keeps the absent slots absent", func(t *testing.T) {
		b := requireRoundTrip[BrandReference](t, brandRefSchema,
			`{"domain":"acme-corp.com","brand_kit_override":{"colors":{"primary":"#ABCDEF"}}}`)
		require.Nil(t, b.BrandKitOverride.Logo)
		require.Equal(t, "#ABCDEF", b.BrandKitOverride.Colors.Primary)
		require.Empty(t, b.BrandKitOverride.Colors.Secondary)
	})

	t.Run("countries and the override stay absent", func(t *testing.T) {
		b := requireRoundTrip[BrandReference](t, brandRefSchema, `{"domain":"acme-corp.com"}`)
		require.Nil(t, b.Countries)
		require.Nil(t, b.BrandKitOverride)
	})
}

// A request that embeds AccountReference, through its own request schema.
func TestAccountReferenceThroughGetMediaBuyDeliveryRequest(t *testing.T) {
	const ref = "media-buy/get-media-buy-delivery-request.json"

	req := requireRoundTrip[GetMediaBuyDeliveryRequest](t, ref, `{
		"account":{
			"brand":{"domain":"nova-athletics.example","countries":["NL"],"brand_kit_override":{"tagline":"Run further"}},
			"operator":"nova-athletics.example",
			"operator_unit":{"id":"234284238","name":"Nova EMEA"},
			"currency":"EUR",
			"timezone":"Europe/Amsterdam",
			"sandbox":false
		},
		"media_buy_ids":["mb_1"]
	}`)
	require.NotNil(t, req.Account)
	require.NotNil(t, req.Account.Sandbox)
	require.False(t, *req.Account.Sandbox)
	require.Equal(t, "EUR", req.Account.Currency)
	require.Equal(t, "Run further", req.Account.Brand.BrandKitOverride.Tagline)

	absent := requireRoundTrip[GetMediaBuyDeliveryRequest](t, ref,
		`{"account":{"brand":{"domain":"acme-corp.com"},"operator":"acme-corp.com"}}`)
	require.Nil(t, absent.Account.Sandbox)
}

// AdCP 3.3 opens these objects (additionalProperties: true), so a seller on
// 3.3 may send keys this SDK does not know. The types follow the repo's
// convention for open objects, plain structs that ignore unknown keys on
// decode: the decode succeeds and every known field is kept. The payload is not
// valid against the 3.2.2 schemas, which close the objects, so it does not go
// through assertSchemaValid.
func TestAccountReferenceIgnoresUnknownKeys(t *testing.T) {
	var ref AccountReference
	require.NoError(t, json.Unmarshal([]byte(`{
		"brand":{
			"domain":"nova-athletics.example",
			"brand_id":"spark",
			"countries":["NL"],
			"x_brand_future":{"a":1},
			"data_subject_contestation":{"email":"privacy@nova-athletics.example","x_contestation_future":true},
			"brand_kit_override":{
				"tagline":"Run further",
				"colors":{"primary":"#112233","x_slot_future":"#000000"},
				"x_override_future":["a"]
			}
		},
		"operator":"nova-athletics.example",
		"operator_unit":{"id":"234284238","name":"Nova EMEA","x_unit_future":"u"},
		"currency":"EUR",
		"timezone":"Europe/Amsterdam",
		"sandbox":true,
		"x_account_future":1
	}`), &ref))

	require.Equal(t, "nova-athletics.example", ref.Operator)
	require.Equal(t, OperatorUnit{ID: "234284238", Name: "Nova EMEA"}, *ref.OperatorUnit)
	require.Equal(t, "EUR", ref.Currency)
	require.Equal(t, "Europe/Amsterdam", ref.Timezone)
	require.NotNil(t, ref.Sandbox)
	require.True(t, *ref.Sandbox)
	require.Equal(t, "nova-athletics.example", ref.Brand.Domain)
	require.Equal(t, "spark", ref.Brand.BrandID)
	require.Equal(t, []string{"NL"}, ref.Brand.Countries)
	require.Equal(t, "privacy@nova-athletics.example", ref.Brand.DataSubjectContestation.Email)
	require.Equal(t, "Run further", ref.Brand.BrandKitOverride.Tagline)
	require.Equal(t, "#112233", ref.Brand.BrandKitOverride.Colors.Primary)
}
