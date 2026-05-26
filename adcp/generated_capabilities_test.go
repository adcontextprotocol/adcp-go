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
}
