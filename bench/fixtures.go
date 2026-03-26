package bench

func ptr(f float64) *float64 { return &f }

// RealisticOpenRTBRequest returns a fully populated BidRequest (~5KB JSON).
// Represents a real web display impression with full device, user, geo, and EID data.
func RealisticOpenRTBRequest() *BidRequest {
	return &BidRequest{
		ID: "auction-8f2a3b7c-91d4-4e5f-b8c2-7a9d1e3f5b6a",
		Imp: []Imp{
			{
				ID: "imp-1",
				Banner: &Banner{
					W: 300, H: 250, Pos: 1,
					Format: []Format{{W: 300, H: 250}, {W: 300, H: 600}, {W: 728, H: 90}},
					API:    []int{3, 5},
				},
				BidFloor:    0.50,
				BidFloorCur: "USD",
				Secure:      1,
				PMP: &PMP{
					Private: 0,
					Deals: []Deal{
						{ID: "deal-acme-q1-2026", BidFloor: 2.50, BidFloorCur: "USD", AT: 1},
						{ID: "deal-nova-always-on", BidFloor: 1.00, BidFloorCur: "USD", AT: 2},
					},
				},
			},
			{
				ID: "imp-2",
				Banner: &Banner{
					W: 728, H: 90, Pos: 3,
					Format: []Format{{W: 728, H: 90}, {W: 970, H: 250}},
				},
				BidFloor:    0.25,
				BidFloorCur: "USD",
				Secure:      1,
			},
		},
		Site: &Site{
			ID:     "site-oakwood-1234",
			Domain: "www.oakwoodpublishing.example.com",
			Page:   "https://www.oakwoodpublishing.example.com/2026/03/sustainable-kitchen-trends",
			Cat:    []string{"IAB8", "IAB8-5", "IAB8-18"},
			SectionCat: []string{"IAB8-5"},
			Ref:    "https://www.google.com/search?q=sustainable+kitchen",
			Publisher: &Publisher{
				ID:   "pub-oakwood-5678",
				Name: "Oakwood Publishing Group",
				Cat:  []string{"IAB8"},
			},
		},
		Device: &Device{
			UA:             "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36",
			IP:             "72.134.89.201",
			Geo:            &Geo{Lat: 40.7128, Lon: -74.0060, Country: "USA", Region: "NY", Metro: "501", City: "New York", Zip: "10001", Type: 2},
			DeviceType:     2,
			Make:           "Apple",
			Model:          "Macintosh",
			OS:             "Mac OS X",
			OSV:            "10.15.7",
			ConnectionType: 2,
			IFA:            "",
			JS:             1,
			Language:        "en",
			W:              1440,
			H:              900,
			PPI:            144,
		},
		User: &User{
			ID:       "user-a1b2c3d4e5f6",
			BuyerUID: "buyer-uid-7890",
			YOB:      1988,
			Gender:   "M",
			Geo:      &Geo{Lat: 40.71, Lon: -74.01, Country: "USA", Region: "NY", Type: 2},
			Ext: &UserExt{
				Consent: "CPXxRfAPXxRfAAfKABENB-CgAAAAAAAAAAYgAAAAAAAA",
				EIDs: []EID{
					{Source: "uidapi.com", UIDs: []EUID{{ID: "AgAAAAVacu1uaxgAAAQ14AAAAABAAAAA", AType: 3}}},
					{Source: "liveramp.com", UIDs: []EUID{{ID: "Xi4IHi7T7II:vGOb8Z0bVl65", AType: 3}}},
					{Source: "id5-sync.com", UIDs: []EUID{{ID: "ID5-ZHMOcvSShIBZiIBwLz_EMqOePAz3aJpjbyMEbhrY", AType: 1}}},
					{Source: "criteo.com", UIDs: []EUID{{ID: "crt-abc123def456", AType: 1}}},
				},
			},
		},
		AT:   1,
		TMax: 300,
		Cur:  []string{"USD"},
		BCat: []string{"IAB25", "IAB26", "IAB7-39"},
		BAdv: []string{"competitor1.example.com", "competitor2.example.com"},
		Regs: &Regs{
			COPPA: 0,
			Ext: &RegsExt{
				GDPR:      0,
				USPrivacy: "1YNN",
				GPP:       "DBACNYA~CPXxRfAPXxRfAAfKABENB-CgAAAAAAAAAAYgAAAAAAAA~1YNN",
				GPPSID:    []int{6, 7},
			},
		},
		Source: &Source{
			FD:  1,
			TID: "txn-a1b2c3d4-e5f6-7890",
		},
	}
}

// RealisticOpenRTBResponse returns a BidResponse with one seat and one bid.
func RealisticOpenRTBResponse() *BidResponse {
	return &BidResponse{
		ID: "auction-8f2a3b7c-91d4-4e5f-b8c2-7a9d1e3f5b6a",
		SeatBid: []SeatBid{
			{
				Seat: "seat-acme-corp",
				Bid: []Bid{
					{
						ID:      "bid-1",
						ImpID:   "imp-1",
						Price:   3.25,
						AdID:    "ad-acme-kitchen-q1",
						NURL:    "https://win.acme.example.com/n?id=bid-1&price=${AUCTION_PRICE}",
						ADM:     `<div class="ad-container"><a href="https://click.acme.example.com/c?id=bid-1"><img src="https://cdn.acme.example.com/creatives/kitchen-300x250-v3.png" width="300" height="250" alt="Acme Kitchen Products"></a><img src="https://imp.acme.example.com/i?id=bid-1" width="1" height="1" style="display:none"></div>`,
						ADomain: []string{"acme.example.com"},
						CID:     "campaign-acme-kitchen-q1",
						CrID:    "creative-kitchen-300x250-v3",
						Cat:     []string{"IAB8-5"},
						W:       300,
						H:       250,
						DealID:  "deal-acme-q1-2026",
					},
				},
			},
		},
		Cur: "USD",
	}
}

// RealisticTMPContextRequest returns a ContextMatchRequest for the same impression.
func RealisticTMPContextRequest() *TMPContextRequest {
	return &TMPContextRequest{
		ProtocolVersion: "1.0",
		RequestID:       "ctx-8f2a-oakwood-91b3",
		PropertyID:      "oakwood-publishing-main",
		PropertyType:    "website",
		PlacementID:     "article-sidebar-300x250",
		Artifacts:       []string{"article:sustainable-kitchen-2026-03"},
		AvailablePkgs: []TMPPackage{
			{PackageID: "pkg-display-0041", MediaBuyID: "mb-acme-q1", FormatIDs: []string{"display_300x250", "display_728x90"}},
			{PackageID: "pkg-native-0078", MediaBuyID: "mb-nova-q1", FormatIDs: []string{"native_infeed"}},
			{PackageID: "pkg-display-0103", MediaBuyID: "mb-summit-q1", FormatIDs: []string{"display_300x250"}},
		},
	}
}

// RealisticTMPIdentityRequest returns an IdentityMatchRequest for the same user.
func RealisticTMPIdentityRequest() *TMPIdentityRequest {
	return &TMPIdentityRequest{
		ProtocolVersion: "1.0",
		RequestID:       "id-3k9p-oakwood-d4f1",
		UserToken:       "tok_uid2_AgAAAAVacu1uaxgAAAQ14AAAAABAAAAA",
		UIDType:         "uid2",
		PackageIDs: []string{
			"pkg-display-0041", "pkg-display-0042", "pkg-display-0043",
			"pkg-native-0078", "pkg-native-0079",
			"pkg-display-0103", "pkg-display-0104",
			"pkg-video-0201", "pkg-video-0202",
			"pkg-native-0301",
		},
	}
}

// RealisticTMPContextResponse returns a response with 2 offers and signals.
func RealisticTMPContextResponse() *TMPContextResponse {
	return &TMPContextResponse{
		RequestID: "ctx-8f2a-oakwood-91b3",
		Offers: []TMPOffer{
			{PackageID: "pkg-display-0041"},
			{PackageID: "pkg-native-0078", Summary: "Organic kitchen products — contextual fit"},
		},
		Signals: &TMPSignals{
			Segments: []string{"sustainability", "home_cooking", "kitchen"},
			TargetingKVs: []TMPKV{
				{Key: "adcp_seg", Value: "sustainability"},
				{Key: "adcp_seg", Value: "home_cooking"},
				{Key: "adcp_pkg", Value: "pkg-display-0041"},
				{Key: "adcp_pkg", Value: "pkg-native-0078"},
			},
		},
	}
}

// RealisticTMPIdentityResponse returns eligibility for 10 packages.
func RealisticTMPIdentityResponse() *TMPIdentityResponse {
	return &TMPIdentityResponse{
		RequestID: "id-3k9p-oakwood-d4f1",
		Eligibility: []TMPEligibility{
			{PackageID: "pkg-display-0041", Eligible: true, IntentScore: ptr(0.82)},
			{PackageID: "pkg-display-0042", Eligible: true},
			{PackageID: "pkg-display-0043", Eligible: false},
			{PackageID: "pkg-native-0078", Eligible: true, IntentScore: ptr(0.65)},
			{PackageID: "pkg-native-0079", Eligible: true},
			{PackageID: "pkg-display-0103", Eligible: false},
			{PackageID: "pkg-display-0104", Eligible: true, IntentScore: ptr(0.41)},
			{PackageID: "pkg-video-0201", Eligible: false},
			{PackageID: "pkg-video-0202", Eligible: true},
			{PackageID: "pkg-native-0301", Eligible: true, IntentScore: ptr(0.73)},
		},
	}
}
