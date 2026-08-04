# Changelog

## [0.2.0](https://github.com/adcontextprotocol/adcp-go/compare/registry/v0.1.0...registry/v0.2.0) (2026-08-04)


### ⚠ BREAKING CHANGES

* **context-agent:** targeting public API changes in this PR. ContextEngineConfig.SellerAgentURL is removed (the seller now arrives per-request as ContextMatchRequest.SellerAgentURL); ContextStorage gains a required SignalMGet method; registry.Property.PropertyRID is now string (was uint64), with LookupByRID/PropertyRID signatures changing accordingly. External code constructing targeting.ContextEngineConfig, implementing targeting.ContextStorage, or reading registry.Property.PropertyRID must update.

### Features

* **adcp:** pin schemas to AdCP 3.1.0 ([#388](https://github.com/adcontextprotocol/adcp-go/issues/388)) ([b799400](https://github.com/adcontextprotocol/adcp-go/commit/b7994000296aba7f329c3fe802492e14abbdfeb4))
* **context-agent:** registry-fed property bitmap + context-signal targeting ([0d8d1fe](https://github.com/adcontextprotocol/adcp-go/commit/0d8d1fe43f991fea995a37cacaa87de450378f4f))
* **context-agent:** registry-fed property bitmap + context-signal targeting ([de999a5](https://github.com/adcontextprotocol/adcp-go/commit/de999a5852ab20260cc6b53ebd5476fd96ac1883))
* **registry:** optional persistent Store with Valkey + Redis backends ([0a0089b](https://github.com/adcontextprotocol/adcp-go/commit/0a0089b1de22ce9d7ddcd2243746c959c02940d2))
* **registry:** optional persistent Store with Valkey + Redis backends ([013a6cb](https://github.com/adcontextprotocol/adcp-go/commit/013a6cb1b81b51db37ef08731f4402dafb0091d4))
* **targeting:** extract fcap into Service + Valkey 9 + glide ([61cb66a](https://github.com/adcontextprotocol/adcp-go/commit/61cb66a9ea8bf55587ca1b8df366808380b0c176))


### Bug Fixes

* **context-agent:** address PR review — liveness gate, gofmt, doc fixes ([6259f73](https://github.com/adcontextprotocol/adcp-go/commit/6259f7348b18ca0b4b613bff68da58a0cc1b79bd))
* **context-agent:** address review nits on PR [#363](https://github.com/adcontextprotocol/adcp-go/issues/363) ([b80a1a2](https://github.com/adcontextprotocol/adcp-go/commit/b80a1a2956f686ac943518c8398e6dcbd090fb43))
* **reference-seller:** pass 8.1 storyboard gate ([82c9187](https://github.com/adcontextprotocol/adcp-go/commit/82c918704452049b1223563bc2fa76b58757798e))
* **registry:** address PR review — gate cursor on persist; harden encoding ([c222c6b](https://github.com/adcontextprotocol/adcp-go/commit/c222c6b02d06103ac2ca64cf0590d400792fd814))
* **registry:** drop events with permanent validation errors ([6073689](https://github.com/adcontextprotocol/adcp-go/commit/6073689f05977486b78b828513670b9576d611d6))
* **registry:** property_rid is a UUID-v7 string, not uint64 ([f7aef33](https://github.com/adcontextprotocol/adcp-go/commit/f7aef336b03966578657fd98c7dd09e6736cac18))
