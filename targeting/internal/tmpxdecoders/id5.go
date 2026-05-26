package tmpxdecoders

import "context"

// ID5 decodes an ID5 identifier. The wire user_token arrives in the form
// downstream consumers (TMPX buyer master, audience joins) already
// expect, so the decoder is a pure pass-through; selectEntries enforces
// the 32-byte length contract from the TMPX type registry at the
// boundary.
//
// Lives alongside MAID and HashedEmail in the real-decoder set because
// it produces canonical bytes that buyer masters can resolve, even
// though there is no per-byte transformation performed here.
type ID5 struct{}

// Decode returns the raw bytes of userToken. ctx is unused — ID5 is a
// pure pass-through.
func (ID5) Decode(_ context.Context, userToken string) ([]byte, error) {
	return []byte(userToken), nil
}
