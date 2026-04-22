# Changelog

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
