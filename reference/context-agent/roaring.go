package contextagent

import (
	"github.com/RoaringBitmap/roaring/roaring64"
	"github.com/adcontextprotocol/adcp-go/targeting"
)

// RoaringBitmap wraps *roaring64.Bitmap to satisfy targeting.Bitmap.
type RoaringBitmap struct {
	*roaring64.Bitmap
}

// Ensure RoaringBitmap satisfies the interface.
var _ targeting.Bitmap = (*RoaringBitmap)(nil)

// Contains reports whether v is in the bitmap.
func (r *RoaringBitmap) Contains(v uint64) bool {
	return r.Bitmap.Contains(v)
}
