// Package quota is a bounded table for security history that must not be
// evicted by a flood: per-operator authority and revocation history. Each
// entry counts against a group (an operator, see psl.QuotaKey) and a global
// limit. A full group or table refuses new entries; it never evicts an
// existing entry to make room, so one party cannot push another party's
// history out. Callers synchronize access.
package quota

// Table maps keys to values under per-group and global entry limits.
type Table[V any] struct {
	limit    int
	perGroup int
	entries  map[string]entry[V]
	counts   map[string]int
}

type entry[V any] struct {
	group string
	value V
}

// New returns a Table holding at most limit entries, and at most perGroup
// entries in any one group.
func New[V any](limit, perGroup int) *Table[V] {
	return &Table[V]{limit: limit, perGroup: perGroup, entries: map[string]entry[V]{}, counts: map[string]int{}}
}

// Get returns the value stored for key.
func (t *Table[V]) Get(key string) (V, bool) {
	e, ok := t.entries[key]
	return e.value, ok
}

// Put stores value for key in group. An existing key is always updated. A
// new key is refused (false) when its group or the table is full.
func (t *Table[V]) Put(key, group string, value V) bool {
	if e, ok := t.entries[key]; ok {
		e.value = value
		t.entries[key] = e
		return true
	}
	if len(t.entries) >= t.limit || t.counts[group] >= t.perGroup {
		return false
	}
	t.entries[key] = entry[V]{group: group, value: value}
	t.counts[group]++
	return true
}

// Delete removes key.
func (t *Table[V]) Delete(key string) {
	if e, ok := t.entries[key]; ok {
		delete(t.entries, key)
		if t.counts[e.group]--; t.counts[e.group] <= 0 {
			delete(t.counts, e.group)
		}
	}
}

// Len returns the number of entries.
func (t *Table[V]) Len() int { return len(t.entries) }

// GroupLen returns the number of entries in group.
func (t *Table[V]) GroupLen(group string) int { return t.counts[group] }
