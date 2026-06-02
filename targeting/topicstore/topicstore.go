// Package topicstore wraps targeting.ContextStore with taxonomy-aware key
// construction for content topics. Topics in TMP are taxonomy-scoped: the
// topic id "632" means "Food & Drink" under IAB Content Taxonomy 3.0 but
// something different under a publisher's custom taxonomy. Without taxonomy
// in the storage key, cross-taxonomy collisions would silently produce wrong
// matches.
//
// Two surfaces:
//
//   - Writer: classifier pipelines (artifact-topic side) and media-buy /
//     governance pipelines (package-topic side) import the Writer and call
//     it with a Taxonomy + the topic ids. They do not assemble Valkey keys
//     themselves — the key shape lives in this package.
//   - Reader: the context engine and the resolver use the Reader to look up
//     topics for an artifact or a package, and to compute the
//     artifact ∩ package intersection. The engine also uses NamespaceTopic
//     to convert raw topic ids into the taxonomy-qualified form the
//     in-memory ResolvedPackages.TopicIndex is keyed on.
package topicstore

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Taxonomy identifies a topic taxonomy. Source is the publishing
// organization (e.g., "iab"); ID is the version within that source (e.g., 7
// for IAB Content Taxonomy 3.0). The pair (Source, ID) uniquely identifies
// the namespace topic strings live in.
//
// On the TMP wire this corresponds to ContextSignals.TaxonomySource +
// ContextSignals.TaxonomyID; the spec is taxonomy-agnostic so any
// (source, id) pair the deployment configures as accepted is valid.
type Taxonomy struct {
	Source string
	ID     int
}

// Validate returns an error if the taxonomy cannot be safely serialized
// into a Valkey key. Empty Source or characters that would break key
// parsing (colons, slashes, whitespace) are rejected.
func (t Taxonomy) Validate() error {
	if t.Source == "" {
		return errors.New("topicstore: taxonomy.source must be non-empty")
	}
	if t.ID < 0 {
		return errors.New("topicstore: taxonomy.id must be non-negative")
	}
	if strings.ContainsAny(t.Source, ":\n\r\t /\\") {
		return fmt.Errorf("topicstore: taxonomy.source %q contains invalid characters", t.Source)
	}
	return nil
}

// String returns the canonical "source:id" form used as the key namespace
// segment and as the prefix on namespaced topic strings in the in-memory
// TopicIndex.
func (t Taxonomy) String() string {
	return t.Source + ":" + strconv.Itoa(t.ID)
}

// ArtifactKey returns the Valkey key holding the set of topic ids for the
// given artifact under taxonomy tax.
func ArtifactKey(tax Taxonomy, ref string) string {
	return "topics:artifact:" + tax.String() + ":" + ref
}

// PackageKey returns the Valkey key holding the set of topic ids a package
// targets under taxonomy tax.
func PackageKey(tax Taxonomy, pkgID string) string {
	return "topics:package:" + tax.String() + ":" + pkgID
}

// NamespaceTopic returns the taxonomy-qualified topic string used by the
// engine and ResolvedPackages.TopicIndex. Two topics with the same raw id
// but different taxonomies serialize to distinct namespaced strings.
func NamespaceTopic(tax Taxonomy, topic string) string {
	return tax.String() + ":" + topic
}

// NamespaceTopics applies NamespaceTopic to every topic in the slice. The
// returned slice has the same length and order; allocates exactly one
// underlying array.
func NamespaceTopics(tax Taxonomy, topics []string) []string {
	if len(topics) == 0 {
		return nil
	}
	out := make([]string, len(topics))
	prefix := tax.String() + ":"
	for i, t := range topics {
		out[i] = prefix + t
	}
	return out
}

// Store is the subset of targeting.ContextStore that topicstore needs.
// Declared here so writers can depend on the smaller surface without
// pulling in the rest of the targeting package.
type Store interface {
	SetMembers(ctx context.Context, key string) ([]string, error)
	SetIntersect(ctx context.Context, keys ...string) ([]string, error)
	SetAdd(ctx context.Context, key string, members ...string) error
	SetRemove(ctx context.Context, key string, members ...string) error
	Del(ctx context.Context, keys ...string) error
}

// Writer writes artifact and package topic data. Callers construct one via
// NewWriter and use the Set/Add/Remove methods rather than building Valkey
// keys themselves.
type Writer struct {
	store Store
}

// NewWriter returns a Writer that persists topic data to the supplied
// store. A nil store returns an error.
func NewWriter(store Store) (*Writer, error) {
	if store == nil {
		return nil, errors.New("topicstore: store is required")
	}
	return &Writer{store: store}, nil
}

// SetArtifactTopics replaces the topic set for ref under tax with the
// supplied topics. An empty topics slice deletes the key (no topics for
// this artifact in this taxonomy). Idempotent.
func (w *Writer) SetArtifactTopics(ctx context.Context, tax Taxonomy, ref string, topics []string) error {
	if err := tax.Validate(); err != nil {
		return err
	}
	if ref == "" {
		return errors.New("topicstore: artifact ref must be non-empty")
	}
	key := ArtifactKey(tax, ref)
	if err := w.store.Del(ctx, key); err != nil {
		return fmt.Errorf("topicstore: clear artifact topics: %w", err)
	}
	if len(topics) == 0 {
		return nil
	}
	if err := w.store.SetAdd(ctx, key, topics...); err != nil {
		return fmt.Errorf("topicstore: write artifact topics: %w", err)
	}
	return nil
}

// AddArtifactTopics adds topics to ref's set under tax without removing
// existing ones. Use when a classifier emits incremental updates.
func (w *Writer) AddArtifactTopics(ctx context.Context, tax Taxonomy, ref string, topics []string) error {
	if err := tax.Validate(); err != nil {
		return err
	}
	if ref == "" {
		return errors.New("topicstore: artifact ref must be non-empty")
	}
	if len(topics) == 0 {
		return nil
	}
	return w.store.SetAdd(ctx, ArtifactKey(tax, ref), topics...)
}

// RemoveArtifact deletes every topic for ref under tax. Use when content is
// retracted or the classification is invalidated.
func (w *Writer) RemoveArtifact(ctx context.Context, tax Taxonomy, ref string) error {
	if err := tax.Validate(); err != nil {
		return err
	}
	if ref == "" {
		return errors.New("topicstore: artifact ref must be non-empty")
	}
	return w.store.Del(ctx, ArtifactKey(tax, ref))
}

// SetPackageTopics replaces the targeted-topic set for pkgID under tax with
// topics. An empty topics slice deletes the key (the package no longer
// targets any topic in this taxonomy). Idempotent.
func (w *Writer) SetPackageTopics(ctx context.Context, tax Taxonomy, pkgID string, topics []string) error {
	if err := tax.Validate(); err != nil {
		return err
	}
	if pkgID == "" {
		return errors.New("topicstore: package id must be non-empty")
	}
	key := PackageKey(tax, pkgID)
	if err := w.store.Del(ctx, key); err != nil {
		return fmt.Errorf("topicstore: clear package topics: %w", err)
	}
	if len(topics) == 0 {
		return nil
	}
	if err := w.store.SetAdd(ctx, key, topics...); err != nil {
		return fmt.Errorf("topicstore: write package topics: %w", err)
	}
	return nil
}

// AddPackageTopics adds topics to pkgID's targeted set under tax without
// removing existing ones.
func (w *Writer) AddPackageTopics(ctx context.Context, tax Taxonomy, pkgID string, topics []string) error {
	if err := tax.Validate(); err != nil {
		return err
	}
	if pkgID == "" {
		return errors.New("topicstore: package id must be non-empty")
	}
	if len(topics) == 0 {
		return nil
	}
	return w.store.SetAdd(ctx, PackageKey(tax, pkgID), topics...)
}

// RemovePackageTopics removes topics from pkgID's targeted set under tax.
// Missing topics are silently skipped.
func (w *Writer) RemovePackageTopics(ctx context.Context, tax Taxonomy, pkgID string, topics []string) error {
	if err := tax.Validate(); err != nil {
		return err
	}
	if pkgID == "" {
		return errors.New("topicstore: package id must be non-empty")
	}
	if len(topics) == 0 {
		return nil
	}
	return w.store.SetRemove(ctx, PackageKey(tax, pkgID), topics...)
}

// RemovePackage deletes pkgID's targeted-topic key under tax. Use when the
// package is retired from a taxonomy.
func (w *Writer) RemovePackage(ctx context.Context, tax Taxonomy, pkgID string) error {
	if err := tax.Validate(); err != nil {
		return err
	}
	if pkgID == "" {
		return errors.New("topicstore: package id must be non-empty")
	}
	return w.store.Del(ctx, PackageKey(tax, pkgID))
}

// Reader reads artifact and package topic data. The engine and the resolver
// hold a Reader; production writers (classifier pipelines, media-buy sync)
// hold a Writer.
type Reader struct {
	store Store
}

// NewReader returns a Reader backed by store.
func NewReader(store Store) (*Reader, error) {
	if store == nil {
		return nil, errors.New("topicstore: store is required")
	}
	return &Reader{store: store}, nil
}

// ArtifactTopics returns the topic ids stored for ref under tax. Returns
// nil (no error) when no topics are stored. The returned slice contains
// raw topic ids in the taxonomy's namespace; use NamespaceTopic to lift
// them into the namespaced form the engine indexes on.
func (r *Reader) ArtifactTopics(ctx context.Context, tax Taxonomy, ref string) ([]string, error) {
	if err := tax.Validate(); err != nil {
		return nil, err
	}
	if ref == "" {
		return nil, errors.New("topicstore: artifact ref must be non-empty")
	}
	return r.store.SetMembers(ctx, ArtifactKey(tax, ref))
}

// PackageTopics returns the topic ids pkgID targets under tax.
func (r *Reader) PackageTopics(ctx context.Context, tax Taxonomy, pkgID string) ([]string, error) {
	if err := tax.Validate(); err != nil {
		return nil, err
	}
	if pkgID == "" {
		return nil, errors.New("topicstore: package id must be non-empty")
	}
	return r.store.SetMembers(ctx, PackageKey(tax, pkgID))
}

// IntersectArtifactPackage returns the topic ids that appear in both the
// artifact's topic set and the package's targeted-topic set under tax. A
// non-empty result means the package's TopicTargets rule is satisfied by
// the artifact under this taxonomy.
//
// This is the round-trip-minimal primitive the engine uses on the
// non-resolved path: one Valkey SINTER per (artifact, package, taxonomy).
func (r *Reader) IntersectArtifactPackage(ctx context.Context, tax Taxonomy, pkgID, ref string) ([]string, error) {
	if err := tax.Validate(); err != nil {
		return nil, err
	}
	if pkgID == "" {
		return nil, errors.New("topicstore: package id must be non-empty")
	}
	if ref == "" {
		return nil, errors.New("topicstore: artifact ref must be non-empty")
	}
	return r.store.SetIntersect(ctx, PackageKey(tax, pkgID), ArtifactKey(tax, ref))
}
