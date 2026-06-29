package tmproto

// TmpxProviderEntry is the per-provider value type in
// IdentityMatchResponse.TmpxProviders. The schema defines this inline as
// `{ macros: [TmpxMacro] }`; expressed as a Go type here because the schema
// generator does not synthesize structs for inline object schemas. See
// docs/trusted-match/specification.mdx §IdentityMatchResponse.
type TmpxProviderEntry struct {
	Macros []TmpxMacro `json:"macros"`
}
