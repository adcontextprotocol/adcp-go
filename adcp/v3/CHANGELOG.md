# Changelog

## [2.1.0](https://github.com/adcontextprotocol/adcp-go/compare/adcp-v2.0.1...adcp-v2.1.0) (2026-06-18)


### Features

* **adcp:** pin schemas to AdCP 3.1.0 ([#388](https://github.com/adcontextprotocol/adcp-go/issues/388)) ([b799400](https://github.com/adcontextprotocol/adcp-go/commit/b7994000296aba7f329c3fe802492e14abbdfeb4))
* **context-agent:** registry-fed property bitmap + context-signal targeting ([0d8d1fe](https://github.com/adcontextprotocol/adcp-go/commit/0d8d1fe43f991fea995a37cacaa87de450378f4f))
* **targeting:** canonicalize seller_agent_url via shared urlcanon ([c722633](https://github.com/adcontextprotocol/adcp-go/commit/c722633e8fcac0d8d631d63ac37c11884a4c588b))
* **targeting:** canonicalize seller_agent_url via shared urlcanon ([2416ac1](https://github.com/adcontextprotocol/adcp-go/commit/2416ac18602af50c057033e4b418b909d2c5819b))


### Bug Fixes

* harden AddTool permissive schema traversal ([3cba266](https://github.com/adcontextprotocol/adcp-go/commit/3cba2663d11a1f944d56542868d14424d2ba32ed))
* **schemas:** reconcile rc.11 hand-written type drift + optional-numeric pointers ([d367d9b](https://github.com/adcontextprotocol/adcp-go/commit/d367d9be715091aa6c957ccef6b0a4918d22b6e1))
* **schemas:** type structured build_creative fields; allowlist genuinely-open ones ([6f9f0c1](https://github.com/adcontextprotocol/adcp-go/commit/6f9f0c16aeb1713445574ecf7d3f1bf3fff4d106))

## [2.0.1](https://github.com/adcontextprotocol/adcp-go/compare/adcp-v2.0.0...adcp-v2.0.1) (2026-05-31)


### Bug Fixes

* **reference-seller:** pass 8.1 storyboard gate ([82c9187](https://github.com/adcontextprotocol/adcp-go/commit/82c918704452049b1223563bc2fa76b58757798e))

## [2.0.0](https://github.com/adcontextprotocol/adcp-go/compare/adcp-v1.0.0...adcp-v2.0.0) (2026-05-30)


### ⚠ BREAKING CHANGES

* **adcp:** enforce optional scalar pointer policy
* **adcp:** 3.1.0-rc.4 removes MediaBuyActionModeRequiresProposal, changes GetSignalsResponse.Signals from []SignalListing to []GetSignalsResponseSignal, and removes SignalDataSubjectRights.GpcHonored. Callers should migrate requires_proposal handling to requires_approval plus ProposalStatus, use the typed GetSignalsResponseSignal row shape for get_signals responses, and read GPC posture from the remaining consent/rights fields where applicable.
* **adcp:** type get products refinement items
* **adcp:** generate typed response union interfaces
* **adcp:** type forced test controller directives
* **adcp:** type report plan outcome seller response
* **adcp:** type preview creative request items
* **adcp:** type sync governance account results
* **tmproto:** sync 3.1.0-rc.3 schemas
* **adcp:** GetPlanAuditLogsResponse.Plans now uses []PlanAuditLog instead of []any.
* **adcp:** CheckGovernanceResponse.Conditions now uses []CheckGovernanceCondition instead of []any.
* **adcp:** CheckGovernanceResponse.Findings and ReportPlanOutcomeResponse.Findings now use typed finding structs instead of []any.
* **adcp:** SyncPlansResponse.Plans now uses []SyncPlansPlan instead of []any.
* **adcp:** GetProductsResponse.Incomplete now uses []GetProductsIncompleteItem instead of []any.
* **adcp:** PolicyEntry.Exemplars now uses *PolicyExemplars instead of any.
* **adcp:** ReportPlanOutcomeResponse.PlanSummary now uses *ReportPlanOutcomePlanSummary instead of any.
* **adcp:** ReportPlanOutcomeRequest.Error now uses *ReportPlanOutcomeError instead of any.
* **adcp:** ReportPlanOutcomeRequest.Delivery now uses *ReportPlanOutcomeDelivery instead of any.
* **adcp:** CheckGovernanceRequest.DeliveryMetrics now uses *GovernanceDeliveryMetrics instead of any.
* **adcp:** Targeting.GeoProximity now uses []GeoProximityTarget instead of []any.
* **adcp:** GetProductsRequest.Filters now uses *ProductFilters instead of any.
* **adcp:** CreativeFormat.SupportedMacros is now []string and UserMatch.UIDs is now []UserMatchUID instead of []any.
* **adcp:** CreativeBrief.Compliance, EventCustomData.Contents, and PolicyCategoryDefinition.RegulatoryFrameworks now use schema-backed generated types instead of any.
* **adcp:** AccountSetup.Message now always marshals when AccountSetup is present to match the schema-required message field.
* **adcp:** CreativeBriefMessaging.Cta is now CreativeBriefMessaging.CTA. This is a Go source-only rename; wire JSON remains cta.
* **adcp:** Account.Setup is now *AccountSetup and CreativeBrief.Messaging is now *CreativeBriefMessaging.
* **adcp:** PerformanceFeedback.MeasurementPeriod is now DatetimeRange and PlannedDelivery.Geo is now *PlannedDeliveryGeo.
* **adcp:** ListCreativesRequest.Sort is now *ListCreativesSort and ArtifactWebhookPayload.Pagination is now *ArtifactWebhookPagination.
* **adcp:** optional package numeric request fields now use *float64 so explicit zero values survive omitempty. Use adcp.Float64 or adcp.Ptr for PackageInput.BidPrice, PackageInput.Impressions, PackageUpdate.Budget, PackageUpdate.BidPrice, PackageUpdate.Impressions, and KeywordTargetUpdate.BidPrice.
* **adcp:** preserve optional numeric zero values ([#223](https://github.com/adcontextprotocol/adcp-go/issues/223))
* **adcp:** OptimizationGoal.Target now uses adcp.OptimizationGoalTarget instead of any.
* **adcp:** Package.OptimizationGoals, PackageInput.OptimizationGoals, and PackageUpdate.OptimizationGoals now use []adcp.OptimizationGoal instead of []any.
* **adcp:** type media buy status filter unions ([#179](https://github.com/adcontextprotocol/adcp-go/issues/179))
* **adcp:** type delivery reporting request refs
* **adcp:** type media buy acceptance webhooks
* **adcp:** type proposal and installment refs
* **adcp:** type card and provenance refs ([#171](https://github.com/adcontextprotocol/adcp-go/issues/171))
* **adcp:** type delivery and product inline refs ([#170](https://github.com/adcontextprotocol/adcp-go/issues/170))
* **adcp:** type more closed inline refs ([#169](https://github.com/adcontextprotocol/adcp-go/issues/169))
* **adcp:** type closed generated inline refs ([#168](https://github.com/adcontextprotocol/adcp-go/issues/168))
* **adcp:** generate flattened core oneof refs ([#161](https://github.com/adcontextprotocol/adcp-go/issues/161))
* **adcp:** type generated forecast and creative refs ([#160](https://github.com/adcontextprotocol/adcp-go/issues/160))
* **adcp:** several Product and Package fields are typed instead of any.
* **adcp:** GetAdcpCapabilitiesResponse capability blocks are typed instead of any.
* **adcp:** several generated SDK fields that were any are now schema-backed typed structs or typed slices.
* **adcp:** generate media-buy schema helpers
* **adcp:** type media-buy seller follow-ups

### Features

* **adcp:** add typed validation helpers ([#227](https://github.com/adcontextprotocol/adcp-go/issues/227)) ([5a90e03](https://github.com/adcontextprotocol/adcp-go/commit/5a90e03bbedfd389e867c6a370e3ad6ac9271430))
* **adcp:** add union helper constructors ([#184](https://github.com/adcontextprotocol/adcp-go/issues/184)) ([f189179](https://github.com/adcontextprotocol/adcp-go/commit/f18917950a4e98cf714a15d754469fc554648ccd))
* **adcp:** alias create media buy result to response union ([67619ba](https://github.com/adcontextprotocol/adcp-go/commit/67619ba7889b43cb765f3f53c508abd2ff04aca1)), closes [#334](https://github.com/adcontextprotocol/adcp-go/issues/334)
* **adcp:** detect hand-written inline schema divergence ([2cb0289](https://github.com/adcontextprotocol/adcp-go/commit/2cb0289863461ded2d9aecceaa68fb360932cb6d)), closes [#272](https://github.com/adcontextprotocol/adcp-go/issues/272)
* **adcp:** enforce optional scalar pointer policy ([b75fe86](https://github.com/adcontextprotocol/adcp-go/commit/b75fe86755c84f0ba63ea4734cea7c824281f7c5))
* **adcp:** generate enum validation helpers ([#193](https://github.com/adcontextprotocol/adcp-go/issues/193)) ([7bbf595](https://github.com/adcontextprotocol/adcp-go/commit/7bbf595346061f8ff671ea10dfec6eeefea902dc))
* **adcp:** generate flattened core oneof refs ([#161](https://github.com/adcontextprotocol/adcp-go/issues/161)) ([6506f84](https://github.com/adcontextprotocol/adcp-go/commit/6506f844b65321622080df442bff87f75f80b749))
* **adcp:** generate media-buy schema helpers ([85b7411](https://github.com/adcontextprotocol/adcp-go/commit/85b7411c82b4f6bd33cb61d23399d4df4bb4acd5))
* **adcp:** generate optimization metric enum helpers ([#231](https://github.com/adcontextprotocol/adcp-go/issues/231)) ([13aa257](https://github.com/adcontextprotocol/adcp-go/commit/13aa257c49c6e5202947d6bdfa54e25d665f1874))
* **adcp:** generate typed response union interfaces ([4972e2b](https://github.com/adcontextprotocol/adcp-go/commit/4972e2b0ac8643f2f4f116d41d414c8e5693d113)), closes [#176](https://github.com/adcontextprotocol/adcp-go/issues/176)
* **adcp:** guard inline schema closure policy ([ddc3cc6](https://github.com/adcontextprotocol/adcp-go/commit/ddc3cc6b85a76187e123f2c8c44d07d24c9272f7)), closes [#298](https://github.com/adcontextprotocol/adcp-go/issues/298)
* **adcp:** harden schema generator for 3.1 refs ([#309](https://github.com/adcontextprotocol/adcp-go/issues/309)) ([5fe2ab4](https://github.com/adcontextprotocol/adcp-go/commit/5fe2ab43046b5fd8289c96089f57c9f8207f6e47))
* **adcp:** preserve enum aliases in generated fields ([40c8fc7](https://github.com/adcontextprotocol/adcp-go/commit/40c8fc714216c694e5756f4932831bcad83f5426)), closes [#262](https://github.com/adcontextprotocol/adcp-go/issues/262)
* **adcp:** preserve optional numeric zero values ([#223](https://github.com/adcontextprotocol/adcp-go/issues/223)) ([2fec145](https://github.com/adcontextprotocol/adcp-go/commit/2fec1457bba3c2148156aae781abcb774e0bbb4e))
* **adcp:** preserve package request zero values ([0806019](https://github.com/adcontextprotocol/adcp-go/commit/080601954fcabd1791ac236dea0b193c450d5d2a))
* **adcp:** report generated any coverage ([2db0e16](https://github.com/adcontextprotocol/adcp-go/commit/2db0e16a2551751cdb2ed6c3e4303a0692e26929))
* **adcp:** type account setup and creative messaging ([72fa722](https://github.com/adcontextprotocol/adcp-go/commit/72fa722fdf1914f9616ba45409a2fea7e58dc166))
* **adcp:** type capabilities response ([#158](https://github.com/adcontextprotocol/adcp-go/issues/158)) ([b3e69eb](https://github.com/adcontextprotocol/adcp-go/commit/b3e69eb62caf0aa3182032485ae3c96a0fc063ef))
* **adcp:** type card and provenance refs ([#171](https://github.com/adcontextprotocol/adcp-go/issues/171)) ([cb2c447](https://github.com/adcontextprotocol/adcp-go/commit/cb2c447e5843ba8dffcd4e2ced81b4cb43be3e0a))
* **adcp:** type closed generated inline refs ([#168](https://github.com/adcontextprotocol/adcp-go/issues/168)) ([b461f07](https://github.com/adcontextprotocol/adcp-go/commit/b461f079709c43669339c396dd3969da7e50cc6e))
* **adcp:** type compliance policy and event inline fields ([dda1db9](https://github.com/adcontextprotocol/adcp-go/commit/dda1db96e5a2272dd21408f8514fc5b0e94072db))
* **adcp:** type delivery and product inline refs ([#170](https://github.com/adcontextprotocol/adcp-go/issues/170)) ([c39f417](https://github.com/adcontextprotocol/adcp-go/commit/c39f4173b6272d5c4d794be257597f51609034ee))
* **adcp:** type delivery reporting request refs ([564a6eb](https://github.com/adcontextprotocol/adcp-go/commit/564a6eba3bbffe46f13dfb2334afb9ecbafeef46))
* **adcp:** type forced test controller directives ([4fd1d6e](https://github.com/adcontextprotocol/adcp-go/commit/4fd1d6e563de76a350c3640655fa2a374f6dbd9b)), closes [#259](https://github.com/adcontextprotocol/adcp-go/issues/259)
* **adcp:** type generated core refs ([#156](https://github.com/adcontextprotocol/adcp-go/issues/156)) ([0e1b8a4](https://github.com/adcontextprotocol/adcp-go/commit/0e1b8a4a96026a8e5034d98a8ac70240feddca19))
* **adcp:** type generated forecast and creative refs ([#160](https://github.com/adcontextprotocol/adcp-go/issues/160)) ([7ad3ca7](https://github.com/adcontextprotocol/adcp-go/commit/7ad3ca793e6f5347787991a57d5fb06572b60894))
* **adcp:** type get products incomplete ([#297](https://github.com/adcontextprotocol/adcp-go/issues/297)) ([2bd7b84](https://github.com/adcontextprotocol/adcp-go/commit/2bd7b8406de6ff14a313689f80d200cb4d48bf3d))
* **adcp:** type get products refinement items ([f8acf1a](https://github.com/adcontextprotocol/adcp-go/commit/f8acf1ad1bf512e63eb3dd314d3bfbe9fbf5c9f0)), closes [#176](https://github.com/adcontextprotocol/adcp-go/issues/176)
* **adcp:** type governance conditions ([19ac77e](https://github.com/adcontextprotocol/adcp-go/commit/19ac77ee0685198491365dd6310b1bee12533cbd))
* **adcp:** type governance delivery metrics ([#288](https://github.com/adcontextprotocol/adcp-go/issues/288)) ([c224c04](https://github.com/adcontextprotocol/adcp-go/commit/c224c04829f92b078adc5a80a5ece5059456eaf5))
* **adcp:** type governance findings ([#300](https://github.com/adcontextprotocol/adcp-go/issues/300)) ([24b8a62](https://github.com/adcontextprotocol/adcp-go/commit/24b8a6265d7d4bd384069952918077fc0ec42040))
* **adcp:** type inline feedback and delivery fields ([4498a15](https://github.com/adcontextprotocol/adcp-go/commit/4498a1519dea95afa1d7ea6b45667a67ab509c65))
* **adcp:** type inline sort and artifact pagination ([3ff2a02](https://github.com/adcontextprotocol/adcp-go/commit/3ff2a0264a71b098d4fe07d1b20726c43d638510))
* **adcp:** type media buy acceptance webhooks ([cf02c97](https://github.com/adcontextprotocol/adcp-go/commit/cf02c97b6129a1523bfd607da46484ed1551f58b))
* **adcp:** type media buy status filter unions ([#179](https://github.com/adcontextprotocol/adcp-go/issues/179)) ([c5e2b2f](https://github.com/adcontextprotocol/adcp-go/commit/c5e2b2f1f10a304decbf37e0947e7ff3db99e60d))
* **adcp:** type media-buy seller follow-ups ([9621ce8](https://github.com/adcontextprotocol/adcp-go/commit/9621ce8a78ffba66dc6073487bc1fa49799a5c33))
* **adcp:** type more closed inline refs ([#169](https://github.com/adcontextprotocol/adcp-go/issues/169)) ([183f736](https://github.com/adcontextprotocol/adcp-go/commit/183f736776c513438960d338f9a6909a57683003))
* **adcp:** type optimization goal targets ([bc43e2e](https://github.com/adcontextprotocol/adcp-go/commit/bc43e2e23a03e5399b1e34f0c7a46fc10c3a2cea))
* **adcp:** type optimization goals ([c71fd73](https://github.com/adcontextprotocol/adcp-go/commit/c71fd7396493d22c583e9c3061ec319ec36f63f4))
* **adcp:** type plan audit logs ([eb055f6](https://github.com/adcontextprotocol/adcp-go/commit/eb055f602edd1325809326570261c4858c6f0534))
* **adcp:** type policy entry exemplars ([#295](https://github.com/adcontextprotocol/adcp-go/issues/295)) ([86c8912](https://github.com/adcontextprotocol/adcp-go/commit/86c89129007c42bdfa88dfa8e798b30d4e85c48d))
* **adcp:** type preview creative request items ([250f2ce](https://github.com/adcontextprotocol/adcp-go/commit/250f2ceb02380dfc27e7cd9dd3c36ba92bfd94f3)), closes [#259](https://github.com/adcontextprotocol/adcp-go/issues/259)
* **adcp:** type product filters ([4486297](https://github.com/adcontextprotocol/adcp-go/commit/44862974af35bea645924573855c081c00404e54))
* **adcp:** type product reference fields ([#159](https://github.com/adcontextprotocol/adcp-go/issues/159)) ([0a87533](https://github.com/adcontextprotocol/adcp-go/commit/0a87533b86eca5ef490ce1cbb70fcc9326b7a2c3))
* **adcp:** type proposal and installment refs ([a7e176e](https://github.com/adcontextprotocol/adcp-go/commit/a7e176ee8f9416568ee956f95b5eab61beb2560c))
* **adcp:** type report plan outcome delivery ([#290](https://github.com/adcontextprotocol/adcp-go/issues/290)) ([c210b87](https://github.com/adcontextprotocol/adcp-go/commit/c210b871f84f46c3ea9427dfe466b95daece2344))
* **adcp:** type report plan outcome error ([#292](https://github.com/adcontextprotocol/adcp-go/issues/292)) ([9080637](https://github.com/adcontextprotocol/adcp-go/commit/9080637246377d01e21ea61ac0f9fd15545eb576))
* **adcp:** type report plan outcome seller response ([0062e21](https://github.com/adcontextprotocol/adcp-go/commit/0062e21c06a3437c32c09f096ffe70b6464d2e4c)), closes [#259](https://github.com/adcontextprotocol/adcp-go/issues/259)
* **adcp:** type report plan outcome summary ([#293](https://github.com/adcontextprotocol/adcp-go/issues/293)) ([787f3ce](https://github.com/adcontextprotocol/adcp-go/commit/787f3ce399497d03e016d07f8cdb916e93a28e54))
* **adcp:** type sync governance account results ([26e10b7](https://github.com/adcontextprotocol/adcp-go/commit/26e10b730d013ca48e66abcfe951b35a6c676841)), closes [#259](https://github.com/adcontextprotocol/adcp-go/issues/259)
* **adcp:** type sync plans response ([#299](https://github.com/adcontextprotocol/adcp-go/issues/299)) ([44a4d84](https://github.com/adcontextprotocol/adcp-go/commit/44a4d84fbdf24757482979cb838f360f1de41e0a))
* **adcp:** type targeting geo proximity ([9364c22](https://github.com/adcontextprotocol/adcp-go/commit/9364c22e185ddb6859ec383a585630ab6b482f18))
* **adcp:** type user match UIDs and format macros ([f8ac306](https://github.com/adcontextprotocol/adcp-go/commit/f8ac30636f25e933ea6166bf5eb50650c79a8e94))
* **adcp:** update schemas to 3.1.0-rc.4 ([e757ed0](https://github.com/adcontextprotocol/adcp-go/commit/e757ed07f6f82ef505d7fc7d02ea6958bfc47cd6))
* **adcp:** validate cross-type inline hints ([53a5e53](https://github.com/adcontextprotocol/adcp-go/commit/53a5e53bc00442e844ce868620f5b05664a9eb32)), closes [#264](https://github.com/adcontextprotocol/adcp-go/issues/264)
* **adcp:** validate flattened signal unions ([fdf0c62](https://github.com/adcontextprotocol/adcp-go/commit/fdf0c622ba825dc937e9b771b3141b6612b58d9d)), closes [#278](https://github.com/adcontextprotocol/adcp-go/issues/278)
* **codegen:** eliminate unreviewed generated any baseline ([04c066b](https://github.com/adcontextprotocol/adcp-go/commit/04c066bb8797118be46802548b3fe7a710612560))
* **schemas:** manifest-driven sync of canonical agent skills ([#100](https://github.com/adcontextprotocol/adcp-go/issues/100)) ([b8882ac](https://github.com/adcontextprotocol/adcp-go/commit/b8882ac5e67f0b1b42e73543fc6e9f0015bf6224))
* **signing:** default replay store + NewSignedHTTPClient preset ([#88](https://github.com/adcontextprotocol/adcp-go/issues/88)) ([723edc2](https://github.com/adcontextprotocol/adcp-go/commit/723edc2358172116cf8548bab05fe29ff6c85aec))
* **targeting:** extract audience membership into audience.Service ([8e836d1](https://github.com/adcontextprotocol/adcp-go/commit/8e836d1cb7792fe230a35799c2ff32816fcc2a6f))
* **targeting:** extract fcap into Service + Valkey 9 + glide ([61cb66a](https://github.com/adcontextprotocol/adcp-go/commit/61cb66a9ea8bf55587ca1b8df366808380b0c176))
* **tmp:** adopt seller_agent_url on identity match, regen schemas to latest ([20ffd2b](https://github.com/adcontextprotocol/adcp-go/commit/20ffd2b674c83934cada3f520c1e63d69d7dcd4f))
* **tmp:** adopt seller_agent_url on identity match, regen schemas to latest ([c3fb813](https://github.com/adcontextprotocol/adcp-go/commit/c3fb81342668c16d3a8dcf063361cd7b4278ae7c))
* **tmp:** Ed25519 request signing + HPKE TMPX exposure tokens ([c996655](https://github.com/adcontextprotocol/adcp-go/commit/c9966552b7c87e056a61c3d0038cc3d5c0ae8325))
* **tmproto:** sync 3.1.0-rc.3 schemas ([718b29f](https://github.com/adcontextprotocol/adcp-go/commit/718b29f78b6dc4a6ea8013bbae6df838f9f16119))


### Bug Fixes

* **adcp:** clarify version and serve window edges ([f3b9e07](https://github.com/adcontextprotocol/adcp-go/commit/f3b9e07592908527efa5b9218d9434ba062c3748))
* **adcp:** drift-check account setup schema shape ([544133a](https://github.com/adcontextprotocol/adcp-go/commit/544133a6d4171caa61fc3fddfb3d71b30590badb))
* **adcp:** guard custom wire field allowances ([0d3c026](https://github.com/adcontextprotocol/adcp-go/commit/0d3c026a07537cb4102b14a4c0406c54409f1979)), closes [#303](https://github.com/adcontextprotocol/adcp-go/issues/303)
* **adcp:** harden 3.1 follow-up edges ([024f118](https://github.com/adcontextprotocol/adcp-go/commit/024f118fea24c2ec664ba00c6338b241240cfd3d))
* **adcp:** improve rc4 generated signal docs ([0bdba88](https://github.com/adcontextprotocol/adcp-go/commit/0bdba887c9b2d1df3638a8461064d2b82cbe05db))
* **adcp:** keep enum handling forward compatible ([#187](https://github.com/adcontextprotocol/adcp-go/issues/187)) ([4bd5c24](https://github.com/adcontextprotocol/adcp-go/commit/4bd5c24b6fdbe650b775f6d268440784bceef182))
* **adcp:** narrow optimization target tool schema ([#222](https://github.com/adcontextprotocol/adcp-go/issues/222)) ([3ab5d98](https://github.com/adcontextprotocol/adcp-go/commit/3ab5d98ba59401178b63a3f3ff08178328b9a0a6))
* **adcp:** preserve CTA acronym casing ([ce5b3ea](https://github.com/adcontextprotocol/adcp-go/commit/ce5b3ea646043584633182697e407a526f95589d))
* **adcp:** stamp create media buy response metadata ([#250](https://github.com/adcontextprotocol/adcp-go/issues/250)) ([e62da42](https://github.com/adcontextprotocol/adcp-go/commit/e62da42937c8fcc73da7f2c69c57fd0858b6ba16))
* **schemas:** preserve lint tests during bundle sync ([#248](https://github.com/adcontextprotocol/adcp-go/issues/248)) ([6491827](https://github.com/adcontextprotocol/adcp-go/commit/64918278fe7e91d17014660a107de62784c89986))

## 1.0.0 (2026-04-22)


### Features

* ADCP 3.0 collection domain, schema-generated types, Register API ([0be468e](https://github.com/adcontextprotocol/adcp-go/commit/0be468e5307e43471d243c89da155f5465cf2836))
* ADCP 3.0 collection domain, schema-generated types, Register API ([dd7ba67](https://github.com/adcontextprotocol/adcp-go/commit/dd7ba67074b701177e6fc30faff21edb69d070f1))
* AdCP 3.0 webhook support (signing + idempotency) ([79156f8](https://github.com/adcontextprotocol/adcp-go/commit/79156f880560325dd500f3af1b5bdb02b7fdf00f))
* AdCP 3.0-rc.4 governance plan types + Annex III validator ([50047ca](https://github.com/adcontextprotocol/adcp-go/commit/50047caab296cc7d115a75229cd4019f53b38bba))
* AdCP 3.0-rc.4 governance plan types + Annex III validator ([4a6a510](https://github.com/adcontextprotocol/adcp-go/commit/4a6a5106697a544ddd02c7917b5c50830a3c62ae))
* adcp/ package + skills for Go agent generation ([c28bd27](https://github.com/adcontextprotocol/adcp-go/commit/c28bd27a224c6e0b739e3dd541a9ea10e96ea2f5))
* adcp/idempotency package for AdCP idempotency_key ([b14d757](https://github.com/adcontextprotocol/adcp-go/commit/b14d7577e0d88985b4074ab4581a8b5791b316cf))
* adcp/idempotency package for AdCP idempotency_key ([418a3bb](https://github.com/adcontextprotocol/adcp-go/commit/418a3bb4908d77888822d25df5d030e174c8a856))
* **adcp:** type 3.0 capabilities + require idempotency replay TTL ([fb56992](https://github.com/adcontextprotocol/adcp-go/commit/fb56992fe3feedd732b67f35f447997360d71d55))
* **adcp:** type 3.0 capabilities + require idempotency replay TTL ([94d0bdd](https://github.com/adcontextprotocol/adcp-go/commit/94d0bddf61a44d6ba925a8c060762dd2fba2b1d9))
* add adcp/ package for building MCP-based AdCP agents in Go ([0c6e05d](https://github.com/adcontextprotocol/adcp-go/commit/0c6e05dae2fc8a269e9689dbeb11f17d341e567f)), closes [#26](https://github.com/adcontextprotocol/adcp-go/issues/26)
* creative 5/6 — fix preview_creative manifest handling ([c3e1926](https://github.com/adcontextprotocol/adcp-go/commit/c3e1926ba4aef3e17e884e61731e963c1c497c0a))
* fetch schemas from /protocol/{version}.tgz bundle ([48543a3](https://github.com/adcontextprotocol/adcp-go/commit/48543a3bc2db9ee05f152441e78aa923cf1244f5))
* fetch schemas from /protocol/{version}.tgz bundle ([586806e](https://github.com/adcontextprotocol/adcp-go/commit/586806e1acd36946f73b2c1d227173c48812c909)), closes [#40](https://github.com/adcontextprotocol/adcp-go/issues/40)
* gen-seller 9/9, retail 9/9 — all seller-type skills validated ([4251121](https://github.com/adcontextprotocol/adcp-go/commit/42511217c86e2df3f66575a15b488dcbca0a8373))
* generate TMP types from upstream schemas ([5b74704](https://github.com/adcontextprotocol/adcp-go/commit/5b74704fb591997fe9928e4d57cff8b6536094e9))
* optional Sigstore verification of protocol bundle ([1179180](https://github.com/adcontextprotocol/adcp-go/commit/1179180c706dcca22038442d771386bb6f3cbbf0))
* **pricing:** per_unit + custom vendor pricing variants (3.0 GA) ([9840c1c](https://github.com/adcontextprotocol/adcp-go/commit/9840c1c7375dfce363c028d4eccd11a20781b274))
* **pricing:** support per_unit + custom vendor pricing variants (3.0 GA) ([5011e62](https://github.com/adcontextprotocol/adcp-go/commit/5011e62160ecbd2db6f2271db340650b68aaa512))
* RFC 9421 request signing (AdCP 3.0 optional profile) ([ec8cdb4](https://github.com/adcontextprotocol/adcp-go/commit/ec8cdb4b12092e9dfc70ab6695d502426d987ffd)), closes [#43](https://github.com/adcontextprotocol/adcp-go/issues/43)
* schema-generated ProviderRegistration and ProviderStatus ([0b53b0f](https://github.com/adcontextprotocol/adcp-go/commit/0b53b0f668bc9790c4397597dbfced613e180202))
* **signing:** RFC 9421 request signing (closes [#43](https://github.com/adcontextprotocol/adcp-go/issues/43)) ([aec4f4e](https://github.com/adcontextprotocol/adcp-go/commit/aec4f4e76bc30c5e5e32c726ffbd5b07b46af897))
* **signing:** tri-state VerifyRequest + JWK.Public + MIGRATION.md ([fc982f4](https://github.com/adcontextprotocol/adcp-go/commit/fc982f49054d00938a0e9cc94f75c515815f1251))
* **signing:** tri-state VerifyRequest, JWK.Public, MIGRATION guide ([2131b07](https://github.com/adcontextprotocol/adcp-go/commit/2131b07edc45d34c5e6eda7909a287d5b467737c))
* Sigstore verification + TMP multi-identity migration ([189799f](https://github.com/adcontextprotocol/adcp-go/commit/189799f197336e7e72b6046165b11429a378f38d))
* TMPX exposure tokens and country-partitioned identity ([38a3a77](https://github.com/adcontextprotocol/adcp-go/commit/38a3a77ad42f2b7048ea194a5ea34dd928afd92e))
* TMPX exposure tokens, country-partitioned identity, spec alignment ([0d746f2](https://github.com/adcontextprotocol/adcp-go/commit/0d746f25a454f096fc1ff65458444b3929867bea))
* typed response builders for all tools, strengthen README ([2157a87](https://github.com/adcontextprotocol/adcp-go/commit/2157a87a8ea0a7819e6d3cc05292289774aa5ca7))
* upstream refresh — capability blocks, error codes, anti-downgrade ([e88218a](https://github.com/adcontextprotocol/adcp-go/commit/e88218a69b60d7708c973fcb12e8b655f36ed604))
* upstream refresh — capability blocks, error codes, anti-downgrade check ([461d692](https://github.com/adcontextprotocol/adcp-go/commit/461d6924f31ae8ecb6fd2eef6e3611c96761b9af))
* **webhook:** AdCP 3.0 webhook support (signing + idempotency) ([a36baa7](https://github.com/adcontextprotocol/adcp-go/commit/a36baa77f705ecef8940b865351bec102aeed806))


### Bug Fixes

* **adcp:** rename capability-block AttributionWindow → AttributionWindowOption ([2f0ca4f](https://github.com/adcontextprotocol/adcp-go/commit/2f0ca4f7135d9d661fa1d5eafa7c375e9ffee2c1))
* address PR [#27](https://github.com/adcontextprotocol/adcp-go/issues/27) review comments ([0601c70](https://github.com/adcontextprotocol/adcp-go/commit/0601c7098a632791486ae0f072a8036b62428d03))
* address PR [#27](https://github.com/adcontextprotocol/adcp-go/issues/27) review comments on addtool.go ([99c057f](https://github.com/adcontextprotocol/adcp-go/commit/99c057f3d1e2bfaf843922d3bfff6720612d83cb))
* address PR review feedback — trim tests, derive go version, fix nil guards ([2292653](https://github.com/adcontextprotocol/adcp-go/commit/229265367639bfd8526c5e5938f3a8dd353839ab))
* address review feedback on testify migration ([f120e37](https://github.com/adcontextprotocol/adcp-go/commit/f120e37dc3cc7c3d4607d997d2631b308f5155dc))
* drop sync_creatives dual-key, fix status enum per adcp-client 4.25.0 ([c8d46b4](https://github.com/adcontextprotocol/adcp-go/commit/c8d46b4f4cebb84179eea8f7d2e3d59c754febaa))
* harden review findings from expert pass ([f292ca3](https://github.com/adcontextprotocol/adcp-go/commit/f292ca31d280083329352141a0cfefc1687ea494))
* harden review findings from expert pass ([593c26d](https://github.com/adcontextprotocol/adcp-go/commit/593c26d0c19f94b78683f6fd71a02bb9a361e09a))
* remove delivery dual-key per adcp[#2056](https://github.com/adcontextprotocol/adcp-go/issues/2056) resolution ([3f957be](https://github.com/adcontextprotocol/adcp-go/commit/3f957be9960288574745a33a03196eeea14572ce))
* **signing:** harden against security review defense-in-depth findings ([bfaf76f](https://github.com/adcontextprotocol/adcp-go/commit/bfaf76f90564b8e700bc538cd1118d61baa86256))
* **signing:** harden parser against edge-case conformance vectors ([#56](https://github.com/adcontextprotocol/adcp-go/issues/56)) ([93d4c11](https://github.com/adcontextprotocol/adcp-go/commit/93d4c111d1ece2a75c513eae20737e3acc154d2d))
