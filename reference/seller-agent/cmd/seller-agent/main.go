// Reference seller agent using adcp.Register with handler functions.
// Each handler represents where you'd integrate your real ad server / OMS.
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/adcontextprotocol/adcp-go/adcp"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

var agentURL = sellerAgentURL()

func sellerAgentURL() string {
	if u := os.Getenv("ADCP_AGENT_URL"); u != "" {
		return u
	}

	port := 3001
	if p := os.Getenv("PORT"); p != "" {
		if n, err := strconv.Atoi(p); err == nil {
			port = n
		}
	}

	return fmt.Sprintf("http://localhost:%d/mcp", port)
}

// --- Your product catalog and creative formats ---

func baseProducts() map[string]*adcp.Product {
	items := []adcp.Product{
		newProduct("premium-display", "Premium Display", "High-impact display placements across our premium publisher network.", "guaranteed", []string{"display"}, []adcp.FormatRef{{AgentURL: agentURL, ID: "display_300x250"}}, []adcp.PricingOption{{PricingOptionID: "pd-cpm-15", PricingModel: "cpm", FixedPrice: adcp.Ptr(15.00), Currency: "USD"}}),
		newProduct("video-preroll", "Video Pre-Roll", "15 and 30 second pre-roll video ads.", "non_guaranteed", []string{"video"}, []adcp.FormatRef{{AgentURL: agentURL, ID: "video_30s"}, {AgentURL: agentURL, ID: "video-15s"}}, []adcp.PricingOption{
			{PricingOptionID: "vp-cpm-25", PricingModel: "cpm", FixedPrice: adcp.Ptr(25.00), Currency: "USD"},
			{PricingOptionID: "vp-cpcv-05", PricingModel: "cpcv", FixedPrice: adcp.Ptr(0.05), Currency: "USD"},
		}),
	}

	out := make(map[string]*adcp.Product, len(items))
	for i := range items {
		product := items[i]
		out[product.ProductID] = &product
	}
	return out
}

var formats = []adcp.CreativeFormat{
	{FormatID: adcp.FormatRef{AgentURL: agentURL, ID: "display_300x250"}, Name: "Medium Rectangle", Assets: []adcp.AssetSlot{{ItemType: "individual", AssetID: "image", AssetType: "image", Required: true, AcceptedMediaTypes: []string{"image/png", "image/jpeg"}}}},
	{FormatID: adcp.FormatRef{AgentURL: agentURL, ID: "banner-300x250"}, Name: "Medium Rectangle", Assets: []adcp.AssetSlot{{ItemType: "individual", AssetID: "image", AssetType: "image", Required: true, AcceptedMediaTypes: []string{"image/png", "image/jpeg"}}}},
	{FormatID: adcp.FormatRef{AgentURL: agentURL, ID: "banner-728x90"}, Name: "Leaderboard", Assets: []adcp.AssetSlot{{ItemType: "individual", AssetID: "image", AssetType: "image", Required: true, AcceptedMediaTypes: []string{"image/png", "image/jpeg"}}}},
	{FormatID: adcp.FormatRef{AgentURL: agentURL, ID: "video-15s"}, Name: "Video :15", Assets: []adcp.AssetSlot{{ItemType: "individual", AssetID: "video_file", AssetType: "video", Required: true, AcceptedMediaTypes: []string{"video/mp4"}}}},
	{FormatID: adcp.FormatRef{AgentURL: agentURL, ID: "video_15s"}, Name: "Video :15", Assets: []adcp.AssetSlot{{ItemType: "individual", AssetID: "video", AssetType: "video", Required: true, AcceptedMediaTypes: []string{"video/mp4"}}}},
	{FormatID: adcp.FormatRef{AgentURL: agentURL, ID: "video-30s"}, Name: "Video :30", Assets: []adcp.AssetSlot{{ItemType: "individual", AssetID: "video_file", AssetType: "video", Required: true, AcceptedMediaTypes: []string{"video/mp4"}}}},
	{FormatID: adcp.FormatRef{AgentURL: agentURL, ID: "video_30s"}, Name: "30-second Video", Assets: []adcp.AssetSlot{{ItemType: "individual", AssetID: "video", AssetType: "video", Required: true, AcceptedMediaTypes: []string{"video/mp4"}}}},
}

var customScenarios = []string{"seed_product", "seed_pricing_option", "force_create_media_buy_arm"}

func newProduct(id, name, description, deliveryType string, channels []string, formatIDs []adcp.FormatRef, pricing []adcp.PricingOption) adcp.Product {
	return adcp.Product{
		ProductID:                  id,
		Name:                       name,
		Description:                description,
		PublisherProperties:        []adcp.PublisherPropertySelector{{PublisherDomain: "example.com", SelectionType: "all"}},
		Channels:                   channels,
		FormatIDs:                  formatIDs,
		DeliveryType:               deliveryType,
		PricingOptions:             pricing,
		ReportingCapabilities:      reportingCapabilities(),
		PropertyTargetingAllowed:   boolPtr(true),
		CollectionTargetingAllowed: boolPtr(true),
		MeasurementTerms: &adcp.MeasurementTerms{
			BillingMeasurement: &adcp.BillingMeasurement{Vendor: &adcp.BrandReference{Domain: "videoamp.example"}, MeasurementWindow: "c7", MaxVariancePercent: 10},
			MakegoodPolicy:     &adcp.MakegoodPolicy{AvailableRemedies: []string{"additional_delivery", "credit"}},
		},
	}
}

func reportingCapabilities() adcp.ReportingCapabilities {
	return adcp.ReportingCapabilities{
		AvailableReportingFrequencies: []string{"daily"},
		ExpectedDelayMinutes:          60,
		Timezone:                      "UTC",
		SupportsWebhooks:              false,
		AvailableMetrics:              []string{"impressions", "spend", "clicks"},
		DateRangeSupport:              "date_range",
	}
}

func boolPtr(v bool) *bool { return &v }

// responseBusinessEntity removes write-only payment fields before storing or
// echoing business entities in seller responses.
func responseBusinessEntity(in *adcp.BusinessEntity) *adcp.BusinessEntity {
	if in == nil {
		return nil
	}
	out := *in
	out.Bank = nil
	return &out
}

// --- Your backend state (replace with your real DB / ad server client) ---

type backend struct {
	mu        sync.RWMutex
	accounts  map[string]*adcp.AccountResult
	products  map[string]*adcp.Product
	mediaBuys map[string]*adcp.MediaBuyData
	creatives map[string]*creativeRecord
	delivery  map[string]*deliveryState
	buySeq    atomic.Int64
	forced    *forceArm
}

type creativeRecord struct {
	ID       string
	Name     string
	FormatID *adcp.FormatRef
	Status   string
	Assets   map[string]any
}

type forceArm struct {
	TaskID  string
	Message string
}

type deliveryState struct {
	Impressions, Clicks int
	Spend               float64
}

func (b *backend) seedProduct(productID string, fixture map[string]any) {
	b.mu.Lock()
	defer b.mu.Unlock()

	channels := []string{"display"}
	if raw, ok := fixture["channels"].([]any); ok && len(raw) > 0 {
		channels = make([]string, 0, len(raw))
		for _, item := range raw {
			if s, ok := item.(string); ok {
				channels = append(channels, s)
			}
		}
	}
	deliveryType, _ := fixture["delivery_type"].(string)
	if deliveryType == "" {
		deliveryType = "guaranteed"
	}
	formatIDs := []adcp.FormatRef{{AgentURL: agentURL, ID: "display_300x250"}}
	if raw, ok := fixture["format_ids"].([]any); ok && len(raw) > 0 {
		formatIDs = make([]adcp.FormatRef, 0, len(raw))
		for _, item := range raw {
			if m, ok := item.(map[string]any); ok {
				id, _ := m["id"].(string)
				if id != "" {
					formatIDs = append(formatIDs, adcp.FormatRef{AgentURL: agentURL, ID: id})
				}
			}
		}
	}
	product := newProduct(productID, productID, "Seeded storyboard product.", deliveryType, channels, formatIDs, []adcp.PricingOption{{PricingOptionID: "default", PricingModel: "cpm", FixedPrice: adcp.Ptr(10.0), Currency: "USD"}})
	b.products[productID] = &product
}

func (b *backend) seedPricingOption(productID, pricingOptionID string, fixture map[string]any) {
	b.mu.Lock()
	defer b.mu.Unlock()

	product, ok := b.products[productID]
	if !ok {
		created := newProduct(productID, productID, "Seeded storyboard product.", "guaranteed", []string{"display"}, []adcp.FormatRef{{AgentURL: agentURL, ID: "display_300x250"}}, nil)
		product = &created
		b.products[productID] = product
	}
	model, _ := fixture["pricing_model"].(string)
	if model == "" {
		model = "cpm"
	}
	currency, _ := fixture["currency"].(string)
	if currency == "" {
		currency = "USD"
	}
	fixedPrice := 10.0
	if v, ok := fixture["fixed_price"].(float64); ok {
		fixedPrice = v
	}
	product.PricingOptions = append(product.PricingOptions, adcp.PricingOption{PricingOptionID: pricingOptionID, PricingModel: model, Currency: currency, FixedPrice: adcp.Ptr(fixedPrice)})
}

func (b *backend) updateMediaBuy(input adcp.UpdateMediaBuyRequest) (*mcp.CallToolResult, any, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	buy, ok := b.mediaBuys[input.MediaBuyID]
	if !ok {
		return errorResult("MEDIA_BUY_NOT_FOUND", "Media buy not found.", input.Context)
	}
	if input.Canceled != nil && *input.Canceled {
		if buy.Status == "canceled" {
			return errorResult("NOT_CANCELLABLE", "Media buy is already canceled.", input.Context)
		}
		if !hasValidAction(buy.Status, "cancel") {
			return errorResult("INVALID_TRANSITION", "Media buy cannot be changed from its current status.", input.Context)
		}
		buy.Status = "canceled"
		now := time.Now().UTC().Format(time.RFC3339)
		buy.Cancellation = map[string]any{"reason": input.CancellationReason, "canceled_by": "buyer", "canceled_at": now}
		buy.Revision++
		buy.UpdatedAt = now
		decorateMediaBuy(buy)
		return updateMediaBuyResult(buy, packagesForCreateSuccess(buy.Packages), input.Context)
	}
	if input.Paused != nil {
		action := "resume"
		if *input.Paused {
			action = "pause"
		}
		if !hasValidAction(buy.Status, action) {
			return errorResult("INVALID_TRANSITION", "Media buy cannot be changed from its current status.", input.Context)
		}
		if *input.Paused {
			buy.Status = "paused"
		} else {
			buy.Status = "active"
		}
		decorateMediaBuy(buy)
	}
	if input.InvoiceRecipient != nil {
		buy.InvoiceRecipient = responseBusinessEntity(input.InvoiceRecipient)
	}
	if (input.StartTime != "" || input.EndTime != "") && !hasValidAction(buy.Status, "update_dates") {
		return errorResult("INVALID_ACTION", "Date updates are not supported by this reference seller.", input.Context)
	}
	if len(input.NewPackages) > 0 && !hasValidAction(buy.Status, "add_packages") {
		return errorResult("INVALID_ACTION", "Package additions are not supported by this reference seller.", input.Context)
	}
	if len(input.Packages) > 0 && !hasValidAction(buy.Status, "update_packages") {
		return errorResult("INVALID_TRANSITION", "Media buy cannot be changed from its current status.", input.Context)
	}

	packageIndex := make(map[string]int, len(buy.Packages))
	for i := range buy.Packages {
		packageIndex[buy.Packages[i].PackageID] = i
	}
	for _, upd := range input.Packages {
		if _, ok := packageIndex[upd.PackageID]; !ok {
			return errorResult("PACKAGE_NOT_FOUND", "Package not found.", input.Context)
		}
	}

	affected := make([]adcp.Package, 0, len(input.Packages))
	for _, upd := range input.Packages {
		i := packageIndex[upd.PackageID]
		if upd.Paused != nil {
			buy.Packages[i].Paused = upd.Paused
		}
		if upd.TargetingOverlay != nil {
			buy.Packages[i].TargetingOverlay = upd.TargetingOverlay
		}
		if len(upd.CreativeAssignments) > 0 {
			buy.Packages[i].CreativeAssignments = upd.CreativeAssignments
			if buy.Status == "pending_creatives" {
				buy.Status = "active"
				decorateMediaBuy(buy)
			}
		}
		affected = append(affected, buy.Packages[i].Package)
	}
	if len(affected) == 0 {
		affected = packagesForCreateSuccess(buy.Packages)
	}
	buy.Revision++
	buy.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	return updateMediaBuyResult(buy, affected, input.Context)
}

func (b *backend) listCreatives(input adcp.ListCreativesRequest) (*mcp.CallToolResult, any, error) {
	b.mu.RLock()
	defer b.mu.RUnlock()

	creativeIDs := map[string]bool{}
	formatIDs := map[adcp.FormatRef]bool{}
	if input.Filters != nil {
		for _, id := range input.Filters.CreativeIDs {
			creativeIDs[id] = true
		}
		for _, ref := range input.Filters.FormatIDs {
			formatIDs[ref] = true
		}
	}
	creatives := make([]map[string]any, 0, len(b.creatives))
	for _, c := range b.creatives {
		if len(creativeIDs) > 0 && !creativeIDs[c.ID] {
			continue
		}
		if len(formatIDs) > 0 && (c.FormatID == nil || !formatIDs[*c.FormatID]) {
			continue
		}
		item := map[string]any{
			"creative_id":  c.ID,
			"name":         c.Name,
			"status":       c.Status,
			"created_date": time.Now().UTC().Format(time.RFC3339),
			"updated_date": time.Now().UTC().Format(time.RFC3339),
		}
		if c.FormatID != nil {
			item["format_id"] = c.FormatID
		}
		if c.Assets != nil {
			item["assets"] = c.Assets
		}
		creatives = append(creatives, item)
	}
	result, out, err := adcp.ListCreativesResponse(creatives)
	if m, ok := out.(map[string]any); ok && input.Context != nil {
		m["context"] = input.Context
		result.StructuredContent = m
	}
	return result, out, err
}

func (b *backend) createMediaBuy(input *adcp.CreateMediaBuyRequest) (*adcp.MediaBuyData, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	for _, p := range input.Packages {
		if p.MeasurementTerms != nil && p.MeasurementTerms.BillingMeasurement != nil {
			bm := p.MeasurementTerms.BillingMeasurement
			if bm.MeasurementWindow != "c7" || bm.MaxVariancePercent < 5 {
				return nil, adcp.NewError("TERMS_REJECTED", adcp.ErrorOptions{
					Message:    "Measurement terms must use c7 with at least 5 percent variance.",
					Recovery:   "revise",
					Field:      "packages[0].measurement_terms",
					Suggestion: "Use measurement_window c7 and max_variance_percent 10.",
				})
			}
		}
	}
	n := b.buySeq.Add(1)
	id := fmt.Sprintf("mb-%d", n)
	pkgs := make([]adcp.PackageStatus, 0, len(input.Packages))
	hasCreatives := false
	for i, p := range input.Packages {
		if len(p.CreativeAssignments) > 0 {
			hasCreatives = true
		}
		pkgs = append(pkgs, adcp.PackageStatus{
			Package: adcp.Package{
				PackageID: fmt.Sprintf("%s-pkg-%d", id, i+1), ProductID: p.ProductID,
				PricingOptionID: p.PricingOptionID, Budget: p.Budget,
				StartTime: p.StartTime, EndTime: p.EndTime,
				AgencyEstimateNumber: p.AgencyEstimateNumber,
				MeasurementTerms:     p.MeasurementTerms, PerformanceStandards: p.PerformanceStandards,
				TargetingOverlay:    p.TargetingOverlay,
				CreativeAssignments: p.CreativeAssignments,
			},
		})
	}
	var totalBudget float64
	for _, p := range input.Packages {
		totalBudget += p.Budget
	}
	status := "pending_creatives"
	if hasCreatives {
		status = "active"
	}
	now := time.Now().UTC().Format(time.RFC3339)
	buy := &adcp.MediaBuyData{
		MediaBuyID:       id,
		Status:           status,
		TotalBudget:      totalBudget,
		InvoiceRecipient: responseBusinessEntity(input.InvoiceRecipient),
		Packages:         pkgs,
		ConfirmedAt:      now,
		CreatedAt:        now,
		UpdatedAt:        now,
		Revision:         1,
	}
	decorateMediaBuy(buy)
	b.mediaBuys[id] = buy
	for _, pkg := range pkgs {
		b.delivery[pkg.PackageID] = &deliveryState{}
	}
	return buy, nil
}

func (b *backend) createMediaBuyResponse(input *adcp.CreateMediaBuyRequest) (adcp.CreateMediaBuyResult, error) {
	b.mu.Lock()
	if b.forced != nil {
		forced := b.forced
		b.forced = nil
		b.mu.Unlock()
		return &adcp.CreateMediaBuySubmitted{Status: "submitted", TaskID: forced.TaskID, Message: forced.Message}, nil
	}
	b.mu.Unlock()

	buy, err := b.createMediaBuy(input)
	if err != nil {
		return nil, err
	}
	return mediaBuyCreateSuccess(buy), nil
}

func (b *backend) syncCreatives(input *adcp.SyncCreativesRequest) ([]adcp.CreativeResult, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	results := make([]adcp.CreativeResult, 0, len(input.Creatives))
	for _, c := range input.Creatives {
		action := "created"
		if _, exists := b.creatives[c.CreativeID]; exists {
			action = "updated"
		}
		b.creatives[c.CreativeID] = &creativeRecord{ID: c.CreativeID, Name: c.Name, FormatID: c.FormatID, Status: "approved", Assets: c.Assets}
		results = append(results, adcp.CreativeResult{CreativeID: c.CreativeID, Action: action, Status: "approved"})
	}
	for _, assign := range input.Assignments {
		if assign.CreativeID == "" || assign.PackageID == "" {
			continue
		}
		for _, buy := range b.mediaBuys {
			for i := range buy.Packages {
				if buy.Packages[i].PackageID == assign.PackageID {
					buy.Packages[i].CreativeAssignments = append(buy.Packages[i].CreativeAssignments, adcp.CreativeAssignment{
						CreativeID:   assign.CreativeID,
						Weight:       assign.Weight,
						PlacementIDs: assign.PlacementIDs,
					})
					if buy.Status == "pending_creatives" {
						buy.Status = "active"
						decorateMediaBuy(buy)
						buy.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
						buy.Revision++
					}
				}
			}
		}
	}
	return results, nil
}

func (b *backend) getDelivery(input *adcp.GetMediaBuyDeliveryRequest) (*adcp.DeliveryData, error) {
	b.mu.RLock()
	defer b.mu.RUnlock()

	now := time.Now().UTC()
	ids := input.MediaBuyIDs
	if len(ids) == 0 {
		for id := range b.mediaBuys {
			ids = append(ids, id)
		}
	}
	deliveries := make([]adcp.MediaBuyDelivery, 0, len(ids))
	for _, mbID := range ids {
		buy, ok := b.mediaBuys[mbID]
		if !ok {
			continue
		}
		pkgDel := make([]adcp.PackageDelivery, 0)
		var totImps, totClicks int
		var totSpend float64
		for _, pkg := range buy.Packages {
			ds := b.delivery[pkg.PackageID]
			if ds == nil {
				ds = &deliveryState{}
			}
			pkgDel = append(pkgDel, adcp.PackageDelivery{
				PackageID:    pkg.PackageID,
				Impressions:  float64(ds.Impressions),
				Clicks:       float64(ds.Clicks),
				Spend:        ds.Spend,
				PricingModel: "cpm",
				Rate:         10,
				Currency:     "USD",
			})
			totImps += ds.Impressions
			totClicks += ds.Clicks
			totSpend += ds.Spend
		}
		deliveries = append(deliveries, adcp.MediaBuyDelivery{MediaBuyID: mbID, Status: buy.Status, Totals: adcp.MediaBuyDeliveryTotals{Impressions: float64(totImps), Clicks: float64(totClicks), Spend: totSpend}, ByPackage: pkgDel})
	}
	return &adcp.DeliveryData{ReportingPeriod: adcp.ReportingPeriod{Start: now.Add(-24 * time.Hour).Format(time.RFC3339), End: now.Format(time.RFC3339)}, Currency: "USD", MediaBuyDeliveries: deliveries}, nil
}

func (b *backend) forceAccountStatus(accountID, status string) (*adcp.StateTransition, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	acct, ok := b.accounts[accountID]
	if !ok {
		return nil, fmt.Errorf("NOT_FOUND")
	}
	prev := acct.Status
	acct.Status = status
	return &adcp.StateTransition{Success: true, PreviousState: prev, CurrentState: status}, nil
}

func (b *backend) forceMediaBuyStatus(mediaBuyID, status, _ string) (*adcp.StateTransition, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	buy, ok := b.mediaBuys[mediaBuyID]
	if !ok {
		return nil, fmt.Errorf("NOT_FOUND")
	}
	prev := buy.Status
	if prev == "completed" || prev == "rejected" || prev == "canceled" {
		return nil, fmt.Errorf("INVALID_TRANSITION")
	}
	buy.Status = status
	return &adcp.StateTransition{Success: true, PreviousState: prev, CurrentState: status}, nil
}

func (b *backend) forceCreativeStatus(creativeID, status, _ string) (*adcp.StateTransition, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	creative, ok := b.creatives[creativeID]
	if !ok {
		return nil, fmt.Errorf("NOT_FOUND")
	}
	prev := creative.Status
	creative.Status = status
	return &adcp.StateTransition{Success: true, PreviousState: prev, CurrentState: status}, nil
}

func (b *backend) simulateDelivery(mediaBuyID string, p adcp.SimulateDeliveryParams) (*adcp.SimulationResult, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	buy, ok := b.mediaBuys[mediaBuyID]
	if !ok {
		return nil, fmt.Errorf("NOT_FOUND")
	}
	var spend float64
	if p.ReportedSpend != nil {
		spend = p.ReportedSpend.Amount
	}
	count := len(buy.Packages)
	if count == 0 {
		return &adcp.SimulationResult{Success: true, Simulated: map[string]any{"impressions": p.Impressions, "clicks": p.Clicks, "spend": spend}}, nil
	}
	baseImps, extraImps := p.Impressions/count, p.Impressions%count
	baseClicks, extraClicks := p.Clicks/count, p.Clicks%count
	totalBudget := mediaBuyBudget(buy)
	for i, pkg := range buy.Packages {
		ds := b.delivery[pkg.PackageID]
		if ds == nil {
			ds = &deliveryState{}
			b.delivery[pkg.PackageID] = ds
		}
		imps := baseImps
		if i < extraImps {
			imps++
		}
		clicks := baseClicks
		if i < extraClicks {
			clicks++
		}
		ds.Impressions += imps
		ds.Clicks += clicks
		ds.Spend += weightedSpend(spend, pkg.Budget, totalBudget, count)
	}
	return &adcp.SimulationResult{Success: true, Simulated: map[string]any{"impressions": p.Impressions, "clicks": p.Clicks, "spend": spend}}, nil
}

func (b *backend) simulateBudgetSpend(p adcp.SimulateBudgetParams) (*adcp.SimulationResult, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	buy, ok := b.mediaBuys[p.MediaBuyID]
	if !ok {
		return nil, fmt.Errorf("NOT_FOUND")
	}
	total := mediaBuyBudget(buy)
	spend := total * p.SpendPercentage
	// Budget-spend simulation advances financial pacing only; use
	// simulateDelivery when impressions or clicks should move too.
	for _, pkg := range buy.Packages {
		if total == 0 {
			continue
		}
		ds := b.delivery[pkg.PackageID]
		if ds == nil {
			ds = &deliveryState{}
			b.delivery[pkg.PackageID] = ds
		}
		ds.Spend += weightedSpend(spend, pkg.Budget, total, len(buy.Packages))
	}
	return &adcp.SimulationResult{Success: true, Simulated: map[string]any{"spend": spend, "percentage": p.SpendPercentage}}, nil
}

func mediaBuyBudget(buy *adcp.MediaBuyData) float64 {
	var total float64
	for _, pkg := range buy.Packages {
		total += pkg.Budget
	}
	return total
}

func weightedSpend(spend, packageBudget, totalBudget float64, packageCount int) float64 {
	if spend == 0 || packageCount == 0 {
		return 0
	}
	if totalBudget == 0 {
		return spend / float64(packageCount)
	}
	return spend * (packageBudget / totalBudget)
}

func (b *backend) handleCustomScenario(scenario string, params map[string]any) (any, error) {
	switch scenario {
	case "seed_product":
		productID, _ := params["product_id"].(string)
		if productID == "" {
			return nil, &adcp.TestControllerError{Code: "INVALID_PARAMS", Message: "seed_product requires params.product_id"}
		}
		fixture, _ := params["fixture"].(map[string]any)
		b.seedProduct(productID, fixture)
		return map[string]any{"success": true, "message": "seeded"}, nil
	case "seed_pricing_option":
		productID, _ := params["product_id"].(string)
		pricingOptionID, _ := params["pricing_option_id"].(string)
		if productID == "" || pricingOptionID == "" {
			return nil, &adcp.TestControllerError{Code: "INVALID_PARAMS", Message: "seed_pricing_option requires params.product_id and params.pricing_option_id"}
		}
		fixture, _ := params["fixture"].(map[string]any)
		b.seedPricingOption(productID, pricingOptionID, fixture)
		return map[string]any{"success": true, "message": "seeded"}, nil
	case "force_create_media_buy_arm":
		arm, _ := params["arm"].(string)
		if arm != "submitted" {
			return nil, &adcp.TestControllerError{Code: "UNKNOWN_SCENARIO", Message: "Scenario not supported: force_create_media_buy_arm"}
		}
		taskID, _ := params["task_id"].(string)
		message, _ := params["message"].(string)
		if taskID == "" {
			return nil, &adcp.TestControllerError{Code: "INVALID_PARAMS", Message: "force_create_media_buy_arm requires params.task_id"}
		}
		b.mu.Lock()
		b.forced = &forceArm{TaskID: taskID, Message: message}
		b.mu.Unlock()
		return map[string]any{"success": true, "forced": map[string]any{"arm": arm, "task_id": taskID, "message": message}}, nil
	default:
		return nil, &adcp.TestControllerError{Code: "UNKNOWN_SCENARIO", Message: "Unrecognized scenario name"}
	}
}

func updateMediaBuyResult(buy *adcp.MediaBuyData, affected []adcp.Package, context any) (*mcp.CallToolResult, any, error) {
	out := map[string]any{
		"media_buy_id":        buy.MediaBuyID,
		"status":              buy.Status,
		"revision":            buy.Revision,
		"implementation_date": time.Now().UTC().Format(time.RFC3339),
		"affected_packages":   affected,
		"valid_actions":       validActions(buy.Status),
		"sandbox":             true,
		"context":             context,
	}
	if buy.Cancellation != nil {
		out["cancellation"] = buy.Cancellation
	}
	return adcp.Result(out, "Media buy updated")
}

func mediaBuyCreateSuccess(buy *adcp.MediaBuyData) *adcp.CreateMediaBuySuccess {
	return &adcp.CreateMediaBuySuccess{
		MediaBuyID:       buy.MediaBuyID,
		Account:          buy.Account,
		InvoiceRecipient: responseBusinessEntity(buy.InvoiceRecipient),
		Status:           buy.Status,
		ConfirmedAt:      buy.ConfirmedAt,
		CreativeDeadline: buy.CreativeDeadline,
		Revision:         buy.Revision,
		ValidActions:     buy.ValidActions,
		Packages:         packagesForCreateSuccess(buy.Packages),
		Sandbox:          adcp.Bool(true),
		Ext:              buy.Ext,
	}
}

func packagesForCreateSuccess(statuses []adcp.PackageStatus) []adcp.Package {
	pkgs := make([]adcp.Package, 0, len(statuses))
	for _, status := range statuses {
		pkgs = append(pkgs, status.Package)
	}
	return pkgs
}

func decorateMediaBuy(buy *adcp.MediaBuyData) {
	buy.Currency = "USD"
	buy.ValidActions = validActions(buy.Status)
	buy.InvoiceRecipient = responseBusinessEntity(buy.InvoiceRecipient)
	// Response envelope fields are typed; do not smuggle them through ext.
	buy.Ext = nil
}

func validActions(status string) []string {
	switch adcp.MediaBuyStatus(status) {
	case adcp.MediaBuyStatusCanceled, adcp.MediaBuyStatusCompleted, adcp.MediaBuyStatusRejected:
		return []string{}
	case adcp.MediaBuyStatusPaused:
		return mediaBuyActions(adcp.MediaBuyValidActionResume, adcp.MediaBuyValidActionCancel, adcp.MediaBuyValidActionSyncCreatives, adcp.MediaBuyValidActionUpdatePackages)
	case adcp.MediaBuyStatusPendingCreatives, adcp.MediaBuyStatusPendingStart:
		return mediaBuyActions(adcp.MediaBuyValidActionCancel, adcp.MediaBuyValidActionSyncCreatives, adcp.MediaBuyValidActionUpdatePackages)
	case adcp.MediaBuyStatusActive:
		return mediaBuyActions(adcp.MediaBuyValidActionPause, adcp.MediaBuyValidActionCancel, adcp.MediaBuyValidActionSyncCreatives, adcp.MediaBuyValidActionUpdatePackages)
	default:
		return []string{}
	}
}

func mediaBuyActions(actions ...adcp.MediaBuyValidAction) []string {
	out := make([]string, len(actions))
	for i, action := range actions {
		out[i] = string(action)
	}
	return out
}

func hasValidAction(status, action string) bool {
	for _, valid := range validActions(status) {
		if valid == action {
			return true
		}
	}
	return false
}

func errorResult(code, message string, context any) (*mcp.CallToolResult, any, error) {
	out := map[string]any{
		"adcp_error": map[string]any{
			"code":     code,
			"message":  message,
			"recovery": "correctable",
		},
		"errors": []map[string]any{{
			"code":     code,
			"message":  message,
			"recovery": "correctable",
		}},
		"context": context,
	}
	result, outAny, err := adcp.Result(out, message)
	result.IsError = true
	return result, outAny, err
}

func main() {
	b := &backend{
		accounts:  make(map[string]*adcp.AccountResult),
		products:  baseProducts(),
		mediaBuys: make(map[string]*adcp.MediaBuyData),
		creatives: make(map[string]*creativeRecord),
		delivery:  make(map[string]*deliveryState),
	}

	log.Fatal(adcp.Serve(func() *mcp.Server {
		server := mcp.NewServer(&mcp.Implementation{Name: "reference-seller", Version: "1.0.0"}, nil)

		adcp.Register(server, adcp.Config{
			Sandbox:              true,
			IdempotencyReplayTTL: 24 * time.Hour,
			Capabilities: &adcp.CapabilitiesData{
				Account: &adcp.AccountCapabilities{SupportedBilling: []string{"operator", "agent"}, Sandbox: boolPtr(true)},
				MediaBuy: &adcp.MediaBuyCapabilities{
					SupportedPricingModels: []string{"cpm", "cpcv"},
					Portfolio:              &adcp.PortfolioCaps{PublisherDomains: []string{"example.com"}, PrimaryChannels: []string{"display", "olv"}},
				},
				Creative: &adcp.CreativeCapabilities{HasCreativeLibrary: boolPtr(true), SupportsCompliance: boolPtr(true)},
				ComplianceTesting: &adcp.ComplianceTestingCapabilities{Scenarios: []string{
					"force_account_status", "force_media_buy_status", "force_creative_status",
					"simulate_delivery", "simulate_budget_spend",
				}},
			},
			ResolveAccount: func(_ context.Context, ref adcp.AccountReference) (any, error) {
				b.mu.RLock()
				defer b.mu.RUnlock()
				domain := ""
				if ref.Brand != nil {
					domain = ref.Brand.Domain
				}
				id := fmt.Sprintf("acct-%s-%s", domain, ref.Operator)
				if acct, ok := b.accounts[id]; ok {
					return acct, nil
				}
				return &adcp.AccountResult{AccountID: id, Brand: ref.Brand, Operator: ref.Operator, Action: "existing", Status: "active"}, nil
			},
			SyncAccounts: func(_ context.Context, input *adcp.SyncAccountsRequest) ([]adcp.AccountResult, error) {
				b.mu.Lock()
				defer b.mu.Unlock()
				results := make([]adcp.AccountResult, 0, len(input.Accounts))
				for _, acct := range input.Accounts {
					domain := "unknown"
					if acct.Brand != nil {
						domain = acct.Brand.Domain
					}
					id := fmt.Sprintf("acct-%s-%s", domain, acct.Operator)
					result := adcp.AccountResult{AccountID: id, Brand: acct.Brand, Operator: acct.Operator, Action: "created", Status: "active"}
					if existing, ok := b.accounts[id]; ok {
						result.Action = "updated"
						result.Status = existing.Status
					}
					b.accounts[id] = &result
					results = append(results, result)
				}
				return results, nil
			},
			SyncGovernance: func(_ context.Context, input *adcp.SyncGovernanceRequest) ([]adcp.GovernanceResult, error) {
				results := make([]adcp.GovernanceResult, 0, len(input.Accounts))
				for _, acct := range input.Accounts {
					govAcct := acct.Account
					if govAcct == nil {
						govAcct = &adcp.GovernanceAccount{Brand: acct.Brand, Operator: acct.Operator}
					}
					results = append(results, adcp.GovernanceResult{Account: govAcct, Status: "synced", GovernanceAgents: acct.GovernanceAgents})
				}
				return results, nil
			},
			GetProducts: func(_ context.Context, _ any, input *adcp.GetProductsRequest) (*adcp.ProductsData, error) {
				b.mu.RLock()
				defer b.mu.RUnlock()
				products := make([]adcp.Product, 0, len(b.products))
				for _, product := range b.products {
					products = append(products, *product)
				}
				data := &adcp.ProductsData{Products: products}
				if input.BuyingMode == "refine" && len(input.Refine) > 0 {
					applied := make([]map[string]any, 0, len(input.Refine))
					for _, ref := range input.Refine {
						item := map[string]any{"scope": ref["scope"], "status": "applied"}
						if productID, ok := ref["product_id"].(string); ok {
							item["product_id"] = productID
						}
						applied = append(applied, item)
					}
					return &adcp.ProductsData{Products: products, RefinementApplied: applied}, nil
				}
				return data, nil
			},
			CreateMediaBuy: func(_ context.Context, _ any, input *adcp.CreateMediaBuyRequest) (adcp.CreateMediaBuyResult, error) {
				return b.createMediaBuyResponse(input)
			},
			GetMediaBuys: func(_ context.Context, _ any, input *adcp.GetMediaBuysRequest) (*adcp.GetMediaBuysResponse, error) {
				b.mu.RLock()
				defer b.mu.RUnlock()
				buys := make([]adcp.MediaBuyData, 0)
				if len(input.MediaBuyIDs) > 0 {
					for _, id := range input.MediaBuyIDs {
						if buy, ok := b.mediaBuys[id]; ok {
							item := *buy
							decorateMediaBuy(&item)
							buys = append(buys, item)
						}
					}
				} else {
					for _, buy := range b.mediaBuys {
						item := *buy
						decorateMediaBuy(&item)
						buys = append(buys, item)
					}
				}
				return &adcp.GetMediaBuysResponse{MediaBuys: buys}, nil
			},
			ListCreativeFormats: func(_ context.Context, input *adcp.ListCreativeFormatsRequest) ([]adcp.CreativeFormat, error) {
				if len(input.FormatIDs) > 0 {
					filtered := make([]adcp.CreativeFormat, 0, len(input.FormatIDs))
					for _, want := range input.FormatIDs {
						for _, format := range formats {
							if format.FormatID.AgentURL == want.AgentURL && format.FormatID.ID == want.ID {
								filtered = append(filtered, format)
							}
						}
					}
					return filtered, nil
				}
				return formats, nil
			},
			SyncCreatives: func(_ context.Context, input *adcp.SyncCreativesRequest) ([]adcp.CreativeResult, error) {
				return b.syncCreatives(input)
			},
			GetDelivery: func(_ context.Context, _ any, input *adcp.GetMediaBuyDeliveryRequest) (*adcp.DeliveryData, error) {
				return b.getDelivery(input)
			},
		})

		adcp.AddTool(server, "update_media_buy", "Update a media buy",
			func(ctx context.Context, req *mcp.CallToolRequest, input adcp.UpdateMediaBuyRequest) (*mcp.CallToolResult, any, error) {
				return b.updateMediaBuy(input)
			})

		adcp.AddTool(server, "list_creatives", "List synced creatives",
			func(ctx context.Context, req *mcp.CallToolRequest, input adcp.ListCreativesRequest) (*mcp.CallToolResult, any, error) {
				return b.listCreatives(input)
			})

		// Test controller — sandbox only. Do not register in production.
		if os.Getenv("ADCP_SANDBOX") != "false" {
			adcp.RegisterTestController(server, &adcp.TestControllerStore{
				CustomScenarios:     customScenarios,
				ForceAccountStatus:  b.forceAccountStatus,
				ForceMediaBuyStatus: b.forceMediaBuyStatus,
				ForceCreativeStatus: b.forceCreativeStatus,
				SimulateDelivery:    b.simulateDelivery,
				SimulateBudgetSpend: b.simulateBudgetSpend,
				CustomScenario:      b.handleCustomScenario,
			})
		}

		return server
	}))
}
