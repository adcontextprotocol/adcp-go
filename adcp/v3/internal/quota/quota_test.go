package quota

import "testing"

func TestTableRefusesInsteadOfEvicting(t *testing.T) {
	tb := New[int](4, 2)
	if !tb.Put("a1", "a", 1) || !tb.Put("a2", "a", 2) {
		t.Fatal("within quota refused")
	}
	if tb.Put("a3", "a", 3) {
		t.Fatal("over group quota accepted")
	}
	if !tb.Put("a1", "a", 10) {
		t.Fatal("update of an existing key refused")
	}
	if v, _ := tb.Get("a1"); v != 10 {
		t.Fatalf("got %d", v)
	}
	if !tb.Put("b1", "b", 1) || !tb.Put("c1", "c", 1) {
		t.Fatal("other groups refused")
	}
	if tb.Put("d1", "d", 1) {
		t.Fatal("over global limit accepted")
	}
	for _, k := range []string{"a1", "a2", "b1", "c1"} {
		if _, ok := tb.Get(k); !ok {
			t.Fatalf("%s evicted", k)
		}
	}
	tb.Delete("a2")
	if tb.GroupLen("a") != 1 || tb.Len() != 3 || !tb.Put("a3", "a", 3) {
		t.Fatal("delete did not free the slot")
	}
}
