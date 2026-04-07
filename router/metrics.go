package router

// SigCacheStats reports signature cache utilization.
type SigCacheStats struct {
	Size    int   `json:"size"`
	MaxSize int   `json:"max_size"`
	Hits    int64 `json:"hits"`
	Misses  int64 `json:"misses"`
}
