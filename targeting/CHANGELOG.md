# Changelog

## [0.3.4](https://github.com/adcontextprotocol/adcp-go/compare/targeting/v0.3.3...targeting/v0.3.4) (2026-09-09)


### Bug Fixes

* **identityagent:** fail closed on fcap when identities can't be canonicalized ([6172683](https://github.com/adcontextprotocol/adcp-go/commit/6172683b84a3ddeaa2825fbce2cad9164054a992))
* **identityagent:** fail closed on fcap when identities can't be canonicalized ([6d42349](https://github.com/adcontextprotocol/adcp-go/commit/6d423495c82ee287b9ec557467ae63d75bdd7ef3))

## [0.3.3](https://github.com/adcontextprotocol/adcp-go/compare/targeting/v0.3.2...targeting/v0.3.3) (2026-09-09)


### Bug Fixes

* **tmpxdecoders:** drop raw MAID from decode-error string ([d08e879](https://github.com/adcontextprotocol/adcp-go/commit/d08e8793c40b7c1192ed6f244d24b2c6f6d00084))
* **tmpxdecoders:** drop raw MAID from decode-error string ([66240ba](https://github.com/adcontextprotocol/adcp-go/commit/66240babe3ff3fa6b2574e2bb2a718b922791a7f))

## [0.3.2](https://github.com/adcontextprotocol/adcp-go/compare/targeting/v0.3.1...targeting/v0.3.2) (2026-09-08)


### Bug Fixes

* **tmproto:** accept optional $schema/format_kind, relax read-path ID charset, fix verifier error envelope ([103ee2b](https://github.com/adcontextprotocol/adcp-go/commit/103ee2b120f96f2a6b8e6db9e24e3421abcd16e3))
* **tmproto:** accept optional $schema/format_kind, relax wire-ID charset, fix verifier error envelope ([966b12b](https://github.com/adcontextprotocol/adcp-go/commit/966b12bfe510dd372a5505862863ecc789446410))

## [0.3.1](https://github.com/adcontextprotocol/adcp-go/compare/targeting/v0.3.0...targeting/v0.3.1) (2026-09-07)


### Bug Fixes

* **targeting:** pin uid2client to v0.2.0 (v0.1.0 tag never existed) ([605d299](https://github.com/adcontextprotocol/adcp-go/commit/605d2991a7c506ddbd55115aa6ff22953e49eaf6))
* **targeting:** pin uid2client to v0.2.0 (v0.1.0 tag never existed) ([ad415a7](https://github.com/adcontextprotocol/adcp-go/commit/ad415a7d93909a12436c77b52bf642c891c95f8d))

## [0.3.0](https://github.com/adcontextprotocol/adcp-go/compare/targeting/v0.2.1...targeting/v0.3.0) (2026-09-07)


### ⚠ BREAKING CHANGES

* **targeting:** adopt tmproto v0.3.0 (AdCP 3.2 Trusted-Match)

### Features

* **downstream:** adopt tmproto v0.3.0 across tmpclient, targeting, reference/context-agent, cmd/context-agent ([8eebc47](https://github.com/adcontextprotocol/adcp-go/commit/8eebc47388c967100c43457da53f87c9d79827fc))
* **targeting:** adopt tmproto v0.3.0 (AdCP 3.2 Trusted-Match) ([5622651](https://github.com/adcontextprotocol/adcp-go/commit/562265160418e18ab070e1aa7c91f452eaf48057))


### Bug Fixes

* **adcp:** adopt protocol 3.2.0-rc.1 ([#493](https://github.com/adcontextprotocol/adcp-go/issues/493)) ([e9543ab](https://github.com/adcontextprotocol/adcp-go/commit/e9543abf0d75c671874cda0f5e0d8fe4081b7639))

## [0.2.1](https://github.com/adcontextprotocol/adcp-go/compare/targeting/v0.2.0...targeting/v0.2.1) (2026-09-04)


### Bug Fixes

* **targeting:** sanitize context-agent validation error responses ([56ba6f3](https://github.com/adcontextprotocol/adcp-go/commit/56ba6f31897faa22bf9c70df4859224ae2262bc4))
* **targeting:** sanitize context-agent validation error responses ([#484](https://github.com/adcontextprotocol/adcp-go/issues/484)) ([8d7d4b4](https://github.com/adcontextprotocol/adcp-go/commit/8d7d4b42311b3cef5f7173354de6f86e63763993))

## [0.2.0](https://github.com/adcontextprotocol/adcp-go/compare/targeting/v0.1.1...targeting/v0.2.0) (2026-08-20)


### Features

* **targeting:** wire native UID2/EUID operator decoders into the identityagent ([89ff50f](https://github.com/adcontextprotocol/adcp-go/commit/89ff50f599a544f37caf8eb23a08859df027816e))
* **uid2client:** native Go UID2/EUID decryption client ([1642481](https://github.com/adcontextprotocol/adcp-go/commit/1642481c8df714b73febfc8d799ebd8a1401ae73))

## [0.1.1](https://github.com/adcontextprotocol/adcp-go/compare/targeting/v0.1.0...targeting/v0.1.1) (2026-08-12)


### Bug Fixes

* **router:** close TMP spec conformance gaps in transport, response merge, and agent docs ([fcafdf5](https://github.com/adcontextprotocol/adcp-go/commit/fcafdf5ba6f9f53ba6bf054a58c488d4b5b2f36a))
* **targeting:** floor serve_window_sec, and add agent endpoint validation ([7d9bce7](https://github.com/adcontextprotocol/adcp-go/commit/7d9bce7ec833eaedcd001a2273454278fab45731))
