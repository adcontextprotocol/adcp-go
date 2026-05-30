# AdCP 3.x Version Compatibility

The Go SDK is generated from the latest supported AdCP 3.x schema bundle, but
the wire contract is not "latest only." It supports both 3.0 and 3.1 clients
and servers.

## Contract

- `adcp_version` is the buyer's release-precision version pin.
- `adcp_major_version` remains accepted and emitted through 3.x for backward
  compatibility.
- `adcp.ADCPVersion.supported_versions` advertises the release-precision
  versions a seller speaks. By default the SDK advertises `["3.0", "3.1"]`.
- Requests without a version pin are served as the highest supported 3.x
  release.
- Requests pinned to `3.0` are served as `3.0` even when the seller also
  supports `3.1`.
- Requests pinned to a newer supported-minor prerelease can be served by the
  matching stable release. For example, a `3.1-rc.3` request against a seller
  that supports `["3.0", "3.1"]` is served as `3.1`.
- Requests pinned above the seller's support downshift to the highest supported
  release in the same major. For example, a `3.1` request against a `["3.0"]`
  seller is served as `3.0`.
- Requests pinned to an unsupported major return `VERSION_UNSUPPORTED`.
- Full semver build identifiers are normalized before use on the wire: for
  example, `3.1.0-rc.3` becomes `3.1-rc.3`.

## Go Usage

| Helper | Use |
| --- | --- |
| `SupportedADCPVersions()` | Default seller `ADCPVersion.SupportedVersions` value. |
| `DefaultADCPVersion()` | Highest SDK-supported 3.x release for unpinned responses. |
| `VersionEnvelopeFor(version)` | Buyer-side helper for filling `AdcpVersion` and `AdcpMajorVersion` together. |
| `NormalizeADCPVersion(version)` | Convert bundle/build versions such as `3.1.0-rc.3` to wire versions. |
| `NegotiateADCPVersion(requestedVersion, requestedMajor, supported)` | Seller-side compatibility resolution. |

Buyer requests should set both fields through 3.x:

```go
env, ok := adcp.VersionEnvelopeFor("3.1")
if !ok {
    return fmt.Errorf("invalid AdCP version")
}
req := adcp.GetProductsRequest{
    AdcpVersion:      env.AdcpVersion,
    AdcpMajorVersion: env.AdcpMajorVersion,
    BuyingMode:       "brief",
    Brief:            "Launch a test campaign.",
}
```

Sellers using `adcp.Register` get automatic version negotiation for
`get_adcp_capabilities`, including the `protocols` filter on the generated
`GetAdcpCapabilitiesRequest`. Other tool handlers receive the decoded request
type and can call `NegotiateADCPVersion` when they need to choose a
version-specific response shape:

```go
served, ok := adcp.NegotiateADCPVersion(
    req.AdcpVersion,
    req.AdcpMajorVersion,
    adcp.SupportedADCPVersions(),
)
if !ok {
    return nil, adcp.NewError("VERSION_UNSUPPORTED", adcp.ErrorOptions{
        Message: "unsupported AdCP version",
    })
}
```

The convenience response builders keep the existing payload-oriented SDK
surface. If a seller needs to force a full generated response envelope for a
specific tool before the SDK exposes a dedicated builder, it should construct
the generated response type directly and return it in MCP structured content
from a custom handler.

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
