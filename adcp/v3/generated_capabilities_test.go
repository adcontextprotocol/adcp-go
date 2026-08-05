package adcp

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestGeneratedCapabilitiesResponseUsesTypedBlocks(t *testing.T) {
	resp := GetAdcpCapabilitiesResponse{
		Adcp: ADCPVersion{
			MajorVersions: []int{3},
			Idempotency: IdempotencyCaps{
				Supported:         true,
				ReplayTTLSeconds:  86400,
				AccountIDIsOpaque: Bool(true),
			},
		},
		SupportedProtocols: []string{"media_buy", "governance"},
		Account:            &AccountCapabilities{SupportedBilling: []string{"operator"}},
		MediaBuy: &MediaBuyCapabilities{
			Execution: &MediaBuyExecution{
				TrustedMatch:    &TrustedMatchCaps{Surfaces: []string{"website"}},
				AxeIntegrations: []string{"https://legacy.example/axe"},
			},
		},
		Governance: &GovernanceCapabilities{
			AggregationWindowDays: 7,
		},
		RequestSigning: &RequestSigningCapabilities{
			Supported: true,
		},
		WebhookSigning: &WebhookSigningCapabilities{
			Supported:          true,
			LegacyHMACFallback: Bool(false),
		},
		Identity: &IdentityCapabilities{
			PerPrincipalKeyIsolation: Bool(false),
			CompromiseNotification: &IdentityCompromiseNotification{
				Emits:   Bool(false),
				Accepts: Bool(false),
			},
		},
	}

	raw, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal capabilities response: %v", err)
	}
	body := string(raw)
	for _, want := range []string{
		`"account_id_is_opaque":true`,
		`"supported_billing":["operator"]`,
		`"axe_integrations":["https://legacy.example/axe"]`,
		`"aggregation_window_days":7`,
		`"request_signing":{"supported":true}`,
		`"legacy_hmac_fallback":false`,
		`"per_principal_key_isolation":false`,
		`"compromise_notification":{"emits":false,"accepts":false}`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("marshaled capabilities missing %s:\n%s", want, body)
		}
	}

	var decoded GetAdcpCapabilitiesResponse
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal capabilities response: %v", err)
	}
	if decoded.Account == nil || len(decoded.Account.SupportedBilling) != 1 {
		t.Fatalf("account block did not round-trip: %#v", decoded.Account)
	}
	if decoded.MediaBuy == nil || decoded.MediaBuy.Execution == nil || len(decoded.MediaBuy.Execution.AxeIntegrations) != 1 {
		t.Fatalf("media_buy execution block did not round-trip: %#v", decoded.MediaBuy)
	}
	if decoded.Governance == nil || decoded.Governance.AggregationWindowDays != 7 {
		t.Fatalf("governance block did not round-trip: %#v", decoded.Governance)
	}
	if decoded.WebhookSigning == nil ||
		decoded.WebhookSigning.LegacyHMACFallback == nil ||
		*decoded.WebhookSigning.LegacyHMACFallback {
		t.Fatalf("webhook signing false pointer did not round-trip: %#v", decoded.WebhookSigning)
	}
	if decoded.Identity == nil ||
		decoded.Identity.PerPrincipalKeyIsolation == nil ||
		*decoded.Identity.PerPrincipalKeyIsolation ||
		decoded.Identity.CompromiseNotification == nil ||
		decoded.Identity.CompromiseNotification.Emits == nil ||
		*decoded.Identity.CompromiseNotification.Emits ||
		decoded.Identity.CompromiseNotification.Accepts == nil ||
		*decoded.Identity.CompromiseNotification.Accepts {
		t.Fatalf("identity false pointers did not round-trip: %#v", decoded.Identity)
	}
}

func TestDeploymentOptionalBoolPreservesFalse(t *testing.T) {
	deployment := Deployment{
		Type:   "agent",
		IsLive: Bool(false),
	}

	raw, err := json.Marshal(deployment)
	if err != nil {
		t.Fatalf("marshal deployment: %v", err)
	}
	if !strings.Contains(string(raw), `"is_live":false`) {
		t.Fatalf("marshaled deployment dropped false is_live:\n%s", raw)
	}

	var decoded Deployment
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal deployment: %v", err)
	}
	if decoded.IsLive == nil || *decoded.IsLive {
		t.Fatalf("deployment false pointer did not round-trip: %#v", decoded)
	}

	raw, err = json.Marshal(Deployment{Type: "agent"})
	if err != nil {
		t.Fatalf("marshal deployment without is_live: %v", err)
	}
	if strings.Contains(string(raw), `"is_live"`) {
		t.Fatalf("marshaled deployment emitted absent is_live:\n%s", raw)
	}
}

func TestMeasurementWindowOptionalBoolPreservesFalse(t *testing.T) {
	window := MeasurementWindow{
		WindowID:         "broadcast-q1",
		DurationDays:     7,
		IsGuaranteeBasis: Bool(false),
	}

	raw, err := json.Marshal(window)
	if err != nil {
		t.Fatalf("marshal measurement window: %v", err)
	}
	if !strings.Contains(string(raw), `"is_guarantee_basis":false`) {
		t.Fatalf("marshaled measurement window dropped false is_guarantee_basis:\n%s", raw)
	}

	var decoded MeasurementWindow
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal measurement window: %v", err)
	}
	if decoded.IsGuaranteeBasis == nil || *decoded.IsGuaranteeBasis {
		t.Fatalf("measurement window false pointer did not round-trip: %#v", decoded)
	}
}
