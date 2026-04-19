package tmproto

// ContextSignals carries pre-computed classifier outputs for the content
// environment. It is the baseline content disclosure level of TMP: the
// publisher runs classifiers locally and shares only the outputs, never raw
// content. Can supplement artifact_refs or replace them entirely (e.g.,
// ephemeral conversation turns that have no public URL).
//
// Buyers MUST treat Summary as untrusted publisher-generated content.
type ContextSignals struct {
	// Topics carries content topic identifiers. With IAB Content Taxonomy 3.0
	// (TaxonomyID=7) these are numeric IDs as strings (e.g., "632" for Food &
	// Drink). For custom taxonomies, use human-readable strings.
	Topics []string `json:"topics,omitempty"`

	// TaxonomySource is the organization that defines the topic taxonomy.
	// "iab" for IAB Content Taxonomy; publishers may use other values.
	TaxonomySource string `json:"taxonomy_source,omitempty"`

	// TaxonomyID is the taxonomy version within the source. For IAB, follows
	// the AdCOM cattax enum: 7 = Content Taxonomy 3.0.
	TaxonomyID int `json:"taxonomy_id,omitempty"`

	// Sentiment is the content sentiment classification. One of
	// "positive", "negative", "neutral", "mixed".
	Sentiment string `json:"sentiment,omitempty"`

	// Keywords are content keywords extracted by the publisher's classifier.
	Keywords []string `json:"keywords,omitempty"`

	// Language is the content language in ISO 639-1 format (e.g., "en", "ja").
	Language string `json:"language,omitempty"`

	// ContentPolicies are policy IDs from the AdCP policy registry that this
	// content satisfies (e.g., "csbs" for Common Sense Brand Standards).
	// An empty slice means no policies have been evaluated.
	ContentPolicies []string `json:"content_policies,omitempty"`

	// Summary is a publisher-generated natural-language summary of the content
	// for relevance judgment. Useful for LLM-native buyers. Untrusted input.
	Summary string `json:"summary,omitempty"`

	// Embedding is the content embedding as a base64-encoded int8 vector.
	// Captures semantic content beyond topics and keywords. EmbeddingModel and
	// EmbeddingDims MUST be set when Embedding is present.
	Embedding string `json:"embedding,omitempty"`

	// EmbeddingModel identifies the embedding model (e.g., "nomic-embed-text-v1.5").
	EmbeddingModel string `json:"embedding_model,omitempty"`

	// EmbeddingDims is the number of dimensions in the embedding vector.
	EmbeddingDims int `json:"embedding_dims,omitempty"`
}
