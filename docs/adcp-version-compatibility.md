# AdCP 3.x Version Compatibility

The Go SDK is generated from the latest supported AdCP 3.x schema bundle, but
the wire contract is not "latest only." It supports both 3.0 and 3.1 clients
and servers.

## Contract

- `adcp_version` is the authoritative release-precision version pin.
- `adcp_major_version` remains accepted and emitted through 3.x for backward
  compatibility.
- `adcp.ADCPVersion.supported_versions` advertises the release-precision
  versions a seller speaks. By default the SDK advertises `["3.0", "3.1"]`.
- Requests without a version pin are served as the highest supported 3.x
  release.
- Requests pinned to `3.0` are served as `3.0` even when the seller also
  supports `3.1`.
- Requests pinned to an unsupported major return `VERSION_UNSUPPORTED`.
- Full semver build identifiers are normalized before use on the wire: for
  example, `3.1.0-rc.3` becomes `3.1-rc.3`.

## Type Policy

Go structs are additive across AdCP 3.x minor releases. Fields introduced in
3.1 stay optional unless the schema says otherwise, and deprecated 3.0 fields
remain present while the 3.x protocol requires them for compatibility.

Generated enum aliases preserve unknown string values on JSON decode. Handler
code that needs strict validation should use the generated `Known*Values`,
`IsKnown*`, or `Parse*` helpers.

Strict validators must validate responses against the served `adcp_version`,
not the client pin or the SDK's generated bundle version. This matters for 3.1
tightenings such as required wholesale cache-scope fields: a response served as
`3.0` remains valid even when the latest 3.1 schema has stricter constraints.
