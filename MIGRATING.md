# Migrating adcp-go

## Next: schema-backed typed SDK fields

This release tightens buyer, seller, and governance SDK surfaces around AdCP
3.0.12 schemas. Most wire payloads are unchanged, but several public Go structs
are more typed. Code that built these fields with `map[string]any` should move
to the generated structs below.

Optional object references are pointers: nil omits the field, and `&T{}` or
`adcp.Ptr(T{})` emits it. Required fields inside the nested struct still need to
be populated.

- Two inline object fields that previously used `any` are now typed:
  `ListCreativesRequest.Sort` is `*adcp.ListCreativesSort`, and
  `ArtifactWebhookPayload.Pagination` is `*adcp.ArtifactWebhookPagination`.
  Use nil to omit them.
- Two more generated inline object fields that previously used `any` are now
  typed: `PerformanceFeedback.MeasurementPeriod` is
  `adcp.DatetimeRange`, and `PlannedDelivery.Geo` is `*adcp.PlannedDeliveryGeo`.
  Populate `DatetimeRange.Start` and `DatetimeRange.End`; zero-value
  `DatetimeRange{}` is not a valid wire payload. Use nil to omit
  `PlannedDelivery.Geo`.
- Optional numeric fields where explicit zero is meaningful are now pointers:
  `PricingOption.FixedPrice`, `AudienceSelector.MinValue`,
  `AudienceSelector.MaxValue`, `ForecastPoint.Budget`,
  `CreativeAsset.Weight`, `KeywordTarget.BidPrice`, `PackageInput.BidPrice`,
  `PackageInput.Impressions`, `PackageUpdate.Budget`,
  `PackageUpdate.BidPrice`, `PackageUpdate.Impressions`, and
  `KeywordTargetUpdate.BidPrice`. Use nil to omit the field, and
  `adcp.Ptr(0.0)` or `adcp.Float64(0)` when the wire payload must include an
  explicit zero.
  `PackageInput.Budget` remains `float64` because it is required by the create
  package schema; `PackageUpdate.Budget` is `*float64` because package updates
  can omit budget or explicitly set it to zero.
- `UpdateMediaBuyRequest.Canceled` and `PackageUpdate.Canceled` are `*bool`.
  Use nil when the field is absent and `adcp.Bool(true)` when requesting
  cancellation. The AdCP schema constrains `canceled` to true; do not send
  `adcp.Bool(false)` to mean resume. Use `Paused: adcp.Bool(false)` for resume.
- `CreativeAssignments` is now `[]adcp.CreativeAssignment`. Use
  `adcp.Float64(0)` for an explicit paused creative weight; omitted weight
  still means equal rotation. Seller-specific assignment fields round-trip via
  `CreativeAssignment.Extra`.
- `PackageInput` is now generated from `media-buy/package-request.json`. The
  non-protocol `BuyerRef` field is gone; use `Ext` for seller-specific
  correlation metadata. The generated type also exposes schema fields that were
  previously missing, including `FormatIDs`, `Pacing`, `Impressions`, `Paused`,
  `Catalogs`, `OptimizationGoals`, `Creatives`, `Context`, and `Ext`.
- `UpdateMediaBuyRequest` is now generated from
  `media-buy/update-media-buy-request.json`. `StartTime` is a `string` instead
  of `any`, matching the current schema's `start-timing` alias.
- `CreateMediaBuyRequest.StartTime` is also now a `string` instead of `any`.
  The schema's `start-timing` alias resolves to string in Go; `"asap"` remains
  valid wire data.
- `GetProductsRequest.PropertyList` is now `*adcp.PropertyListRef`, and
  `GetProductsRequest.TimeBudget` is now `*adcp.Duration`. Use nil when these
  filters are absent.
- Schema-referenced core objects now use generated Go types instead of `any`:

| Field | New Go type |
| --- | --- |
| `Account.BillingEntity` | `*adcp.BusinessEntity` |
| `MediaBuyData.InvoiceRecipient` | `*adcp.BusinessEntity` |
| `CreateMediaBuyRequest.InvoiceRecipient` | `*adcp.BusinessEntity` |
| `CreateMediaBuySuccess.InvoiceRecipient` | `*adcp.BusinessEntity` |
| `UpdateMediaBuyRequest.InvoiceRecipient` | `*adcp.BusinessEntity` |
| `CheckGovernanceRequest.InvoiceRecipient` | `*adcp.BusinessEntity` |
| `CreateMediaBuySuccess.PlannedDelivery` | `*adcp.PlannedDelivery` |
| `CheckGovernanceRequest.PlannedDelivery` | `*adcp.PlannedDelivery` |
| `CreateMediaBuyRequest.PushNotificationConfig` | `*adcp.PushNotificationConfig` |
| `UpdateMediaBuyRequest.PushNotificationConfig` | `*adcp.PushNotificationConfig` |
| `SyncAccountsRequest.PushNotificationConfig` | `*adcp.PushNotificationConfig` |
| `SyncCatalogsRequest.PushNotificationConfig` | `*adcp.PushNotificationConfig` |
| `SyncCreativesRequest.PushNotificationConfig` | `*adcp.PushNotificationConfig` |
| `CreateMediaBuyRequest.ReportingWebhook` | `*adcp.ReportingWebhook` |
| `UpdateMediaBuyRequest.ReportingWebhook` | `*adcp.ReportingWebhook` |
| `GetProductsRequest.PropertyList` | `*adcp.PropertyListRef` |
| `Targeting.PropertyList` | `*adcp.PropertyListRef` |
| `GetProductsRequest.TimeBudget` | `*adcp.Duration` |
| `Targeting.FrequencyCap` | `*adcp.FrequencyCap` |
| `Targeting.DaypartTargets` | `[]adcp.DaypartTarget` |
| `Catalog.FeedFieldMappings` | `[]adcp.CatalogFieldMapping` |
| `Event.UserMatch` | `*adcp.UserMatch` |
| `Event.CustomData` | `*adcp.EventCustomData` |
| `ProvidePerformanceFeedbackRequest.MeasurementPeriod` | `adcp.DatetimeRange` |
| `BusinessEntity.Address` | `*adcp.BusinessAddress` |
| `BusinessEntity.Contacts` | `[]adcp.BusinessContact` |
| `BusinessEntity.Bank` | `*adcp.BankAccount` |
| `PushNotificationConfig.Authentication` | `*adcp.LegacyWebhookAuthentication` |
| `ReportingWebhook.Authentication` | `adcp.LegacyWebhookAuthentication` |
| `CreateMediaBuyRequest.TotalBudget` | `*adcp.MediaBuyBudget` |
| `CreateMediaBuyRequest.IoAcceptance` | `*adcp.IOAcceptance` |
| `CreateMediaBuyRequest.ArtifactWebhook` | `*adcp.ArtifactWebhookConfig` |
| `ArtifactWebhookConfig.Authentication` | `adcp.LegacyWebhookAuthentication` |
| `GetAdcpCapabilitiesResponse.Adcp` | `adcp.ADCPVersion` |
| `GetAdcpCapabilitiesResponse.Account` | `*adcp.AccountCapabilities` |
| `GetAdcpCapabilitiesResponse.MediaBuy` | `*adcp.MediaBuyCapabilities` |
| `GetAdcpCapabilitiesResponse.Signals` | `*adcp.SignalsCapabilities` |
| `GetAdcpCapabilitiesResponse.Governance` | `*adcp.GovernanceCapabilities` |
| `GetAdcpCapabilitiesResponse.SponsoredIntelligence` | `*adcp.SICapabilities` |
| `GetAdcpCapabilitiesResponse.Brand` | `*adcp.BrandCapabilities` |
| `GetAdcpCapabilitiesResponse.Creative` | `*adcp.CreativeCapabilities` |
| `GetAdcpCapabilitiesResponse.RequestSigning` | `*adcp.RequestSigningCapabilities` |
| `GetAdcpCapabilitiesResponse.WebhookSigning` | `*adcp.WebhookSigningCapabilities` |
| `GetAdcpCapabilitiesResponse.Identity` | `*adcp.IdentityCapabilities` |
| `GetAdcpCapabilitiesResponse.ComplianceTesting` | `*adcp.ComplianceTestingCapabilities` |
| `Product.Placements` | `[]adcp.Placement` |
| `Product.DeliveryMeasurement` | `*adcp.ProductDeliveryMeasurement` |
| `Product.ProductCard` | `*adcp.ProductCard` |
| `Product.ProductCardDetailed` | `*adcp.ProductCardDetailed` |
| `Product.CatalogMatch` | `*adcp.ProductCatalogMatch` |
| `Product.Forecast` | `*adcp.DeliveryForecast` |
| `DeliveryForecast.Points` | `[]adcp.ForecastPoint` |
| `ForecastPoint.Metrics` | `map[string]adcp.ForecastRange` |
| `Product.OutcomeMeasurement` | `*adcp.OutcomeMeasurement` |
| `Product.ReportingCapabilities` | `adcp.ReportingCapabilities` |
| `ReportingCapabilities.SupportsGeoBreakdown` | `*adcp.GeoBreakdownSupport` |
| `Product.CreativePolicy` | `*adcp.CreativePolicy` |
| `Product.MeasurementReadiness` | `*adcp.MeasurementReadiness` |
| `MeasurementReadiness.Issues` | `[]adcp.DiagnosticIssue` |
| `Product.MetricOptimization` | `*adcp.ProductMetricOptimization` |
| `Product.ConversionTracking` | `*adcp.ProductConversionTracking` |
| `Product.TrustedMatch` | `*adcp.ProductTrustedMatch` |
| `ProductTrustedMatch.Providers` | `[]adcp.ProductTrustedMatchProvider` |
| `Product.MaterialSubmission` | `*adcp.ProductMaterialSubmission` |
| `Product.Collections` | `[]adcp.CollectionSelector` |
| `Product.DataProviderSignals` | `[]adcp.DataProviderSignalSelector` |
| `Product.Installments` | `[]adcp.Installment` |
| `Installment.Special` | `*adcp.Special` |
| `Installment.GuestTalent` | `[]adcp.Talent` |
| `Installment.AdInventory` | `*adcp.AdInventoryConfig` |
| `Installment.Deadlines` | `*adcp.InstallmentDeadlines` |
| `InstallmentDeadlines.MaterialDeadlines` | `[]adcp.MaterialDeadline` |
| `Installment.DerivativeOf` | `*adcp.InstallmentDerivative` |
| `GetProductsResponse.Proposals` | `[]adcp.Proposal` |
| `Proposal.Allocations` | `[]adcp.ProductAllocation` |
| `Proposal.InsertionOrder` | `*adcp.InsertionOrder` |
| `Proposal.TotalBudgetGuidance` | `*adcp.ProposalBudgetGuidance` |
| `InsertionOrder.Terms` | `*adcp.InsertionOrderTerms` |
| `InsertionOrderTerms.TotalBudget` | `*adcp.InsertionOrderBudget` |
| `Package.PriceBreakdown` | `*adcp.PriceBreakdown` |
| `PriceBreakdown.Adjustments` | `[]adcp.PriceAdjustment` |
| `Package.Cancellation` | `*adcp.PackageCancellation` |
| `CreativeFormat.Accessibility` | `*adcp.CreativeFormatAccessibility` |
| `CreativeFormat.FormatCard` | `*adcp.CreativeFormatCard` |
| `CreativeFormat.FormatCardDetailed` | `*adcp.CreativeFormatCardDetailed` |
| `CreativeFormat.DisclosureCapabilities` | `[]adcp.CreativeFormatDisclosureCapability` |
| `CreativeAsset.Inputs` | `[]adcp.CreativeAssetInput` |
| `CreativeAsset.Provenance` | `*adcp.Provenance` |
| `CreativeManifest.Provenance` | `*adcp.Provenance` |
| `Provenance.AITool` | `*adcp.ProvenanceAITool` |
| `Provenance.DeclaredBy` | `*adcp.ProvenanceDeclaredBy` |
| `Provenance.C2PA` | `*adcp.ProvenanceC2PA` |
| `Provenance.Disclosure` | `*adcp.ProvenanceDisclosure` |
| `Provenance.Disclosure.Jurisdictions` | `[]adcp.ProvenanceDisclosureJurisdiction` |
| `Provenance.Verification` | `[]adcp.ProvenanceVerification` |
| `Signal.Range` | `*adcp.SignalRange` |
| `Targeting.StoreCatchments` | `[]adcp.TargetingStoreCatchment` |
| `DeliveryTotals.ByEventType` | `[]adcp.DeliveryEventTypeMetrics` |
| `DeliveryTotals.QuartileData` | `*adcp.DeliveryQuartileData` |
| `DeliveryTotals.DoohMetrics` | `*adcp.DeliveryDOOHMetrics` |
| `DeliveryTotals.Viewability` | `*adcp.DeliveryViewability` |
| `DeliveryTotals.ByActionSource` | `[]adcp.DeliveryActionSourceMetrics` |
| `MediaBuyDeliveryTotals.ByEventType` | `[]adcp.DeliveryEventTypeMetrics` |
| `MediaBuyDeliveryTotals.QuartileData` | `*adcp.DeliveryQuartileData` |
| `MediaBuyDeliveryTotals.DoohMetrics` | `*adcp.DeliveryDOOHMetrics` |
| `MediaBuyDeliveryTotals.Viewability` | `*adcp.DeliveryViewability` |
| `MediaBuyDeliveryTotals.ByActionSource` | `[]adcp.DeliveryActionSourceMetrics` |
| `PackageDelivery.ByEventType` | `[]adcp.DeliveryEventTypeMetrics` |
| `PackageDelivery.QuartileData` | `*adcp.DeliveryQuartileData` |
| `PackageDelivery.DoohMetrics` | `*adcp.DeliveryDOOHMetrics` |
| `PackageDelivery.Viewability` | `*adcp.DeliveryViewability` |
| `PackageDelivery.ByActionSource` | `[]adcp.DeliveryActionSourceMetrics` |
| `MediaBuyDelivery.DailyBreakdown` | `[]adcp.MediaBuyDailyBreakdown` |
| `PackageDelivery.ByCatalogItem` | `[]adcp.PackageCatalogItemDelivery` |
| `PackageDelivery.ByCreative` | `[]adcp.PackageCreativeDelivery` |
| `PackageDelivery.ByKeyword` | `[]adcp.PackageKeywordDelivery` |
| `PackageDelivery.ByGeo` | `[]adcp.PackageGeoDelivery` |
| `PackageDelivery.ByDeviceType` | `[]adcp.PackageDeviceTypeDelivery` |
| `PackageDelivery.ByDevicePlatform` | `[]adcp.PackageDevicePlatformDelivery` |
| `PackageDelivery.ByAudience` | `[]adcp.PackageAudienceDelivery` |
| `PackageDelivery.ByPlacement` | `[]adcp.PackagePlacementDelivery` |
| `PackageDelivery.DailyBreakdown` | `[]adcp.PackageDailyBreakdown` |
| `Package.OptimizationGoals` | `[]adcp.OptimizationGoal` |
| `PackageInput.OptimizationGoals` | `[]adcp.OptimizationGoal` |
| `PackageUpdate.OptimizationGoals` | `[]adcp.OptimizationGoal` |
| `OptimizationGoal.Target` | `adcp.OptimizationGoalTarget` |
| `GetMediaBuysRequest.StatusFilter` | `*adcp.MediaBuyStatusFilter` |
| `GetMediaBuyDeliveryRequest.StatusFilter` | `*adcp.MediaBuyStatusFilter` |
| `GetMediaBuyDeliveryRequest.AttributionWindow` | `*adcp.DeliveryAttributionWindow` |
| `DeliveryAttributionWindow.PostClick` | `*adcp.Duration` |
| `DeliveryAttributionWindow.PostView` | `*adcp.Duration` |
| `GetMediaBuyDeliveryRequest.ReportingDimensions` | `*adcp.DeliveryReportingDimensions` |
| `DeliveryReportingDimensions.Geo` | `*adcp.DeliveryReportingGeoDimension` |
| `DeliveryReportingDimensions.DeviceType` | `*adcp.DeliveryReportingDimension` |
| `DeliveryReportingDimensions.DevicePlatform` | `*adcp.DeliveryReportingDimension` |
| `DeliveryReportingDimensions.Audience` | `*adcp.DeliveryReportingDimension` |
| `DeliveryReportingDimensions.Placement` | `*adcp.DeliveryReportingDimension` |
| `ListCreativeFormatsResponse.CreativeAgents` | `[]adcp.CreativeAgentRef` |
| `BuildCreativeRequest.PreviewInputs` | `[]adcp.BuildCreativePreviewInput` |
| `CreativeBrief.ReferenceAssets` | `[]adcp.ReferenceAsset` |
| `CreativeManifest.Rights` | `[]adcp.RightsConstraint` |
| `RightsConstraint.RightsAgent` | `adcp.RightsAgentRef` |
| `AudienceConstraints.Include` | `[]adcp.AudienceSelector` |
| `AudienceConstraints.Exclude` | `[]adcp.AudienceSelector` |
| `PlannedDelivery.AudienceTargeting` | `[]adcp.AudienceSelector` |
| `GetSignalsRequest.Destinations` | `[]adcp.Destination` |
| `Targeting.GeoMetros` / `GeoMetrosExclude` | `[]adcp.GeoMetroTarget` |
| `Targeting.GeoPostalAreas` / `GeoPostalAreasExclude` | `[]adcp.GeoPostalAreaTarget` |
| `Targeting.AgeRestriction` | `*adcp.AgeRestriction` |
| `Targeting.KeywordTargets` | `[]adcp.KeywordTarget` |
| `Targeting.NegativeKeywords` | `[]adcp.NegativeKeywordTarget` |
| `Account.CreditLimit` | `*adcp.AccountCreditLimit` |
| `Account.GovernanceAgents` | `[]adcp.AccountGovernanceAgent` |
| `Account.ReportingBucket` | `*adcp.ReportingBucket` |
| `GetCollectionListRequest.Pagination` | `*adcp.CollectionRequestPagination` |
| `CollectionListChangedWebhook.ChangeSummary` | `*adcp.CollectionChangeSummary` |
| `PropertyListChangedWebhook.ChangeSummary` | `*adcp.PropertyChangeSummary` |

Buyer request migration example:

```go
req := adcp.CreateMediaBuyRequest{
    InvoiceRecipient: adcp.Ptr(adcp.BusinessEntity{
        LegalName: "Acme Corporation",
        TaxID:     "12-3456789",
    }),
    PushNotificationConfig: adcp.Ptr(adcp.PushNotificationConfig{
        URL: "https://buyer.example/webhooks/tasks",
    }),
    ReportingWebhook: adcp.Ptr(adcp.ReportingWebhook{
        URL:                "https://buyer.example/webhooks/reports",
        Authentication:     adcp.LegacyWebhookAuthentication{Schemes: []string{"Bearer"}, Credentials: "0123456789abcdef0123456789abcdef"},
        ReportingFrequency: "daily",
    }),
}
```

Product lookup and targeting migration example:

```go
req := adcp.GetProductsRequest{
    PropertyList: adcp.Ptr(adcp.PropertyListRef{
        AgentURL: "https://lists.example/mcp",
        ListID:   "pl-123",
    }),
    TimeBudget: adcp.Ptr(adcp.Duration{Interval: 5, Unit: "minutes"}),
}
```

Status filter migration example:

```go
req := adcp.GetMediaBuysRequest{
    StatusFilter: adcp.NewMediaBuyStatusFilter(
        adcp.MediaBuyStatusActive,
        adcp.MediaBuyStatusPaused,
    ),
}
```

Price adjustment migration example:

```go
breakdown := adcp.PriceBreakdown{
    ListPrice: 20,
    Adjustments: []adcp.PriceAdjustment{{
        Kind:   "discount",
        Name:   "volume",
        Amount: 5,
    }},
}
```

Seller response and governance migration example:

```go
success := &adcp.CreateMediaBuySuccess{
    MediaBuyID: "mb-123",
    Packages:   []adcp.Package{pkg},
    PlannedDelivery: adcp.Ptr(adcp.PlannedDelivery{
        TotalBudget: 1000,
        Currency:    "USD",
    }),
}

feedback := adcp.ProvidePerformanceFeedbackRequest{
    MeasurementPeriod: adcp.DatetimeRange{
        Start: "2026-06-01T00:00:00Z",
        End:   "2026-06-30T23:59:59Z",
    },
}
```
- `DeliveryTotals.ReachUnit` is now `string` instead of `any`, matching the
  reach-unit enum's string wire form.
- `PackageUpdate` is now generated from `media-buy/package-update.json`. It
  exposes schema-backed package update fields such as `Pacing`, `Catalogs`,
  `OptimizationGoals`, keyword add/remove operations, `Creatives`, `Context`,
  and `Ext`.
- `OptimizationGoals` fields now use `[]adcp.OptimizationGoal` instead of
  `[]any`. Nested `event_sources`, `target_frequency`, and `attribution_window`
  are typed, and the nested `target` oneOf is now the
  `adcp.OptimizationGoalTarget` interface. Use concrete target variants such as
  `adcp.OptimizationGoalCostPerTarget`,
  `adcp.OptimizationGoalThresholdRateTarget`,
  `adcp.OptimizationGoalPerAdSpendTarget`, and
  `adcp.OptimizationGoalMaximizeValueTarget`. Unknown future target variants
  round-trip through `adcp.OptimizationGoalRawTarget`.
  `OptimizationGoal.Extra` preserves unknown top-level fields when
  round-tripping newer goal variants through replacement-style updates.

```go
goal := adcp.OptimizationGoal{
  Kind:   "metric",
  Metric: "reach",
  Target: adcp.OptimizationGoalThresholdRateTarget{Value: 0.7},
}

switch target := goal.Target.(type) {
case *adcp.OptimizationGoalThresholdRateTarget:
  _ = target.Value
case adcp.OptimizationGoalThresholdRateTarget:
  _ = target.Value
}
```

JSON unmarshal always produces the pointer form; the value form is what you get
when constructing goals directly in Go.

- `SyncCreativesRequest.Assignments` is now `[]adcp.SyncCreativeAssignment`.
- `Config.CreateMediaBuy` now returns `adcp.CreateMediaBuyResult`, which is
  implemented by the generated schema variants. Return
  `*adcp.CreateMediaBuySuccess` for synchronous success,
  `*adcp.CreateMediaBuySubmitted` for async submission, or
  `*adcp.CreateMediaBuyError` when building the schema error branch directly.
- `CreateMediaBuySubmitted` carries async `task_id` / `message` fields:
  `return &adcp.CreateMediaBuySubmitted{Status: "submitted", TaskID: taskID, Message: msg}, nil`.
- `Config.GetMediaBuys` now returns `*adcp.GetMediaBuysResponse` instead of
  `[]adcp.MediaBuyData`. Read pagination, context, and error envelope fields
  from the response struct; extract items via `response.MediaBuys`.
- `MediaBuyData` is now scoped to `get_media_buys` items. It carries fields such
  as `currency`, `total_budget`, `start_time`, `end_time`, `history`, and
  `valid_actions`, plus typed `invoice_recipient`, but not create-task fields
  like `task_id` / `message`.
- `MediaBuyData.Packages` is `[]adcp.PackageStatus` so `get_media_buys` can
  include creative approvals, pending formats, and delivery snapshots.
  `CreateMediaBuySuccess.Packages` remains `[]adcp.Package`.
- `PackageDelivery` and `MediaBuyDelivery` are generated from the delivery
  response inline schemas. Package-level metrics remain flat on
  `PackageDelivery`, and `pricing_model`, `rate`, `currency`, and `spend` are
  emitted as schema-required fields even when their Go values are zero.
  `MediaBuyDelivery.Totals` is now `adcp.MediaBuyDeliveryTotals`, which includes
  the schema-specific `effective_rate` field.
- Delivery metric breakdowns use the same generated metric helper types across
  `DeliveryTotals`, `MediaBuyDeliveryTotals`, package rows, and package
  breakdown rows: `DeliveryEventTypeMetrics`, `DeliveryQuartileData`,
  `DeliveryDOOHMetrics`, `DeliveryViewability`, and
  `DeliveryActionSourceMetrics`.
- `GetMediaBuyDeliveryResponse.ReportingPeriod`,
  `GetMediaBuyDeliveryResponse.AggregatedTotals`, and
  `GetMediaBuyDeliveryResponse.MediaBuyDeliveries` are now typed as
  `adcp.ReportingPeriod`, `*adcp.DeliveryAggregatedTotals`, and
  `[]adcp.MediaBuyDelivery` instead of `any` shapes.

## v3.0.0-rc.4 (governance / policy framework)

rc.4 lands the AdCP governance plan schema with breaking changes. If you
hand-construct `Plan` or `Budget` payloads, read the first section.

### Breaking: `budget.authority_level` is gone

The `authority_level` enum (`agent_full | agent_limited | human_required`) has
been split into two orthogonal concepts:

- `budget.reallocation_threshold` (`*float64`) — reallocation autonomy,
  denominated in `budget.currency`
- `budget.reallocation_unlimited` (`bool`) — full-autonomy sentinel, mutually
  exclusive with `reallocation_threshold`
- `plan.human_review_required` (`bool`) — decisions affecting data subjects
  must escalate to a human (GDPR Art 22, EU AI Act Annex III)

Mapping:

| was | now |
| --- | --- |
| `authority_level: agent_full`     | `Budget{ReallocationUnlimited: true}` |
| `authority_level: agent_limited`  | `Budget{ReallocationThreshold: &amount}` |
| `authority_level: human_required` | `Plan{HumanReviewRequired: true}` (+ threshold 0 if strict) |

**Enforcement.** Exactly one of `ReallocationThreshold` or `ReallocationUnlimited`
must be set. Go's type system cannot enforce this — call `plan.Validate()`
before sending:

```go
plan := adcp.Plan{
    PlanID:     "campaign-q4",
    Brand:      &adcp.BrandReference{Domain: "example.com"},
    Objectives: "brand awareness",
    Budget: adcp.PlanBudget{
        Total:                 500000,
        Currency:              "USD",
        ReallocationThreshold: ptr(25000.0),
    },
    Flight: adcp.PlanFlight{Start: start, End: end},
}
if errs := plan.Validate(); len(errs) > 0 {
    // Return stable codes, not raw messages. Messages may echo the caller's
    // input, which you don't want to reflect back to an untrusted sender.
    return adcp.NewError(errs[0].Code, adcp.ErrorOptions{Field: errs[0].Field})
}
```

### New: `plan.human_review_required` and Annex III invariants

The schema encodes two `if/then` rules that some codegen tools drop. `Plan.Validate`
enforces them client-side:

- `policy_categories` ∋ `fair_housing` / `fair_lending` / `fair_employment` /
  `pharmaceutical_advertising` ⇒ `human_review_required: true`
- `policy_ids` ∋ `eu_ai_act_annex_iii` ⇒ `human_review_required: true`

The exported lists are `adcp.RegulatedHumanReviewCategories` and
`adcp.AnnexIIIPolicyIDs` — use them in your own checks if you need to
introspect a plan before construction.

### New: `Plan.HumanOverride`

Downgrading `human_review_required` from `true` to `false` on re-sync requires
an artifact. Build one with `adcp.HumanOverride{Reason, Approver, ApprovedAt}`.
`Plan.Validate` enforces: `Reason` ≥ 20 characters (after trim), `Approver`
parses as an email address, and `ApprovedAt` (when non-empty) parses as RFC
3339. An empty `HumanOverride` is rejected — the artifact exists to evidence
a human decision, and shipping a blank one defeats the Art 22 audit trail.

### Expanded: `BrandReference`

`BrandReference` now carries rc.4's inline overrides:

- `BrandID` — scope to a specific brand within a house portfolio
- `Industries` — override for Annex III vertical detection when you can't
  modify the canonical `brand.json`
- `DataSubjectContestation` — Art 22(3) contestation contact point

Existing `BrandReference{Domain: "..."}` construction is source-compatible.

### Expanded: `restricted-attribute` enum

Two values added:

- `RestrictedAttributeAge` — FHA/ADEA (housing + employment)
- `RestrictedAttributeFamilialStatus` — FHA

If you hardcoded a list of 8 restricted-attribute values, widen it to 10.

### New tools

Types generated for all four governance tools; tool handlers are not yet
registered via `adcp.Config` and must be wired manually with `adcp.AddTool` if
you are building a governance agent:

- `sync_plans` — `SyncPlansRequest` / `SyncPlansResponse`
- `check_governance` — `CheckGovernanceRequest` / `CheckGovernanceResponse`
- `report_plan_outcome` — `ReportPlanOutcomeRequest` / `ReportPlanOutcomeResponse`
- `get_plan_audit_logs` — `GetPlanAuditLogsRequest` / `GetPlanAuditLogsResponse`

### Guidance for governance-agent implementors

`Plan.Validate` is the SDK's backstop for the invariants codegen tools drop. It
is advisory, not enforcing — a governance agent that accepts a plan without
calling it ships a server that violates the schema's load-bearing
human-oversight rules. Call it on receipt, before persisting anything.

Two invariants live outside the SDK and must be enforced in your governance
agent:

- **Industry normalization.** `BrandReference.Industries` is a freeform
  `[]string`. Normalize values (NFKC, strip combining marks, lowercase) before
  matching against Annex III vertical categories — a buyer shipping `"phárma"`
  or homoglyphed text will otherwise bypass vertical detection.
- **Registry vs inline policy segmentation.** `Plan.CustomPolicies` and
  registry-resolved policies share the `PolicyEntry` type. When assembling LLM
  evaluation prompts, pin registry-sourced policies (`Source == "registry"`)
  as system-level instructions and treat inline policies as
  additive-only — the schema is explicit that custom policies MUST NOT relax
  registry policies, and concatenating them into the same prompt section
  invites prompt-injection attacks via buyer-authored policy text.
