# Changelog

## [0.3.2](https://github.com/adcontextprotocol/adcp-go/compare/tmproto/v0.3.1...tmproto/v0.3.2) (2026-09-08)


### Bug Fixes

* **tmproto:** preserve integer precision in UnknownAsset scrub ([f91d3ea](https://github.com/adcontextprotocol/adcp-go/commit/f91d3ea92ed26a676a52735ab783dfa8963070bd))
* **tmproto:** StripAccess covers signed-URL residue and unknown asset types ([7f1a640](https://github.com/adcontextprotocol/adcp-go/commit/7f1a6407f0e894670ae2f9f4153772bdbf9a52bf))
* **tmproto:** StripAccess covers signed-URL residue and unknown asset types ([b6a8cba](https://github.com/adcontextprotocol/adcp-go/commit/b6a8cba9eedb4a71fb01e2867263c70e594786f6))

## [0.3.1](https://github.com/adcontextprotocol/adcp-go/compare/tmproto/v0.3.0...tmproto/v0.3.1) (2026-09-08)


### Bug Fixes

* **tmproto:** accept optional $schema/format_kind, relax read-path ID charset, fix verifier error envelope ([103ee2b](https://github.com/adcontextprotocol/adcp-go/commit/103ee2b120f96f2a6b8e6db9e24e3421abcd16e3))
* **tmproto:** accept optional $schema/format_kind, relax wire-ID charset, fix verifier error envelope ([966b12b](https://github.com/adcontextprotocol/adcp-go/commit/966b12bfe510dd372a5505862863ecc789446410))

## [0.3.0](https://github.com/adcontextprotocol/adcp-go/compare/tmproto/v0.2.0...tmproto/v0.3.0) (2026-09-07)


### ⚠ BREAKING CHANGES

* **tmproto:** source schemas from adcp/v3 (3.2.0-rc.1)

### Features

* **tmproto:** source schemas from adcp/v3 (3.2.0-rc.1) ([5a48a97](https://github.com/adcontextprotocol/adcp-go/commit/5a48a97e612e2b963422e419d8140f45b68b5579))

## [0.2.0](https://github.com/adcontextprotocol/adcp-go/compare/tmproto/v0.1.2...tmproto/v0.2.0) (2026-09-04)


### Features

* **tmproto:** add SSRF-safe URL validation for TMP content URL fields ([27eeba4](https://github.com/adcontextprotocol/adcp-go/commit/27eeba432a03f2265d9870b4cab56a92e3c9fc4b))

## [0.1.2](https://github.com/adcontextprotocol/adcp-go/compare/tmproto/v0.1.1...tmproto/v0.1.2) (2026-08-28)


### Bug Fixes

* **tmproto:** guard JS safe-integer range in jcsEncodeJSONNumber ([4cb69cc](https://github.com/adcontextprotocol/adcp-go/commit/4cb69cccb8c0e4ca5c66c52f4060d3b7c78d7e1e))
* **tmproto:** guard JS safe-integer range in jcsEncodeJSONNumber ([b84b9f1](https://github.com/adcontextprotocol/adcp-go/commit/b84b9f1dd067421daad201e348a323dcf30f5ce9))

## [0.1.1](https://github.com/adcontextprotocol/adcp-go/compare/tmproto/v0.1.0...tmproto/v0.1.1) (2026-08-12)


### Bug Fixes

* **router:** close TMP spec conformance gaps in transport, response merge, and agent docs ([fcafdf5](https://github.com/adcontextprotocol/adcp-go/commit/fcafdf5ba6f9f53ba6bf054a58c488d4b5b2f36a))
