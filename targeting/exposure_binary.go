package targeting

import (
	"encoding/binary"
	"errors"
	"fmt"
	"hash/fnv"
)

// Binary exposure log format:
//
//	Header (4 bytes): version(uint16) + entrySize(uint16)
//	Entries: N fixed-size records, layout depends on version.
//
// Version 3 entry (40 bytes):
//
//	timestamp(8) + impressionHash(8) + advertiserHash(8) + campaignHash(8) + creativeHash(8)
const (
	binaryHeaderSize   = 4
	binaryVersion3     = 3
	binaryEntrySize    = 40
	maxExposureEntries = 10000 // cap per-user log to bound linear scan cost (~400 KB)
)

var (
	ErrBinaryTooShort       = errors.New("binary log too short for header")
	ErrBinaryUnknownVersion = fmt.Errorf("unknown binary log version (supported: %d)", binaryVersion3)
	ErrBinaryCorrupt        = errors.New("binary log size not aligned to entry size")
)

// BinaryExposureLog is a compact byte-slice exposure log.
// Format: 4-byte header + N fixed-size entries.
type BinaryExposureLog []byte

// newBinaryLog allocates a v3 binary log buffer with capacity for n entries.
func newBinaryLog(n int) []byte {
	buf := make([]byte, binaryHeaderSize, binaryHeaderSize+n*binaryEntrySize)
	binary.LittleEndian.PutUint16(buf[0:], binaryVersion3)
	binary.LittleEndian.PutUint16(buf[2:], binaryEntrySize)
	return buf
}

// EncodeBinaryExposureLog converts a JSON exposure log to v3 binary format.
func EncodeBinaryExposureLog(log ExposureLog) BinaryExposureLog {
	buf := newBinaryLog(len(log))
	buf = buf[:binaryHeaderSize+len(log)*binaryEntrySize]
	for i, e := range log {
		offset := binaryHeaderSize + i*binaryEntrySize
		binary.LittleEndian.PutUint64(buf[offset:], uint64(e.Timestamp)) //nolint:gosec // timestamp is always positive
		binary.LittleEndian.PutUint64(buf[offset+8:], hashString(e.ImpressionID))
		binary.LittleEndian.PutUint64(buf[offset+16:], hashString(e.AdvertiserID))
		binary.LittleEndian.PutUint64(buf[offset+24:], hashString(e.CampaignID))
		binary.LittleEndian.PutUint64(buf[offset+32:], hashString(e.CreativeID))
	}
	return buf
}

// ValidateBinaryLog checks that the header is well-formed and the payload is aligned.
func ValidateBinaryLog(b BinaryExposureLog) error {
	if len(b) < binaryHeaderSize {
		return ErrBinaryTooShort
	}
	version := binary.LittleEndian.Uint16(b[0:])
	entrySize := int(binary.LittleEndian.Uint16(b[2:]))
	if version != binaryVersion3 {
		return ErrBinaryUnknownVersion
	}
	if entrySize != binaryEntrySize {
		return ErrBinaryCorrupt
	}
	payload := len(b) - binaryHeaderSize
	if payload%entrySize != 0 {
		return ErrBinaryCorrupt
	}
	return nil
}

// Version returns the format version from the header. Returns 0 for nil/short slices.
func (b BinaryExposureLog) Version() uint16 {
	if len(b) < binaryHeaderSize {
		return 0
	}
	return binary.LittleEndian.Uint16(b[0:])
}

// EntrySize returns the per-entry byte size from the header. Returns 0 for nil/short slices.
func (b BinaryExposureLog) EntrySize() uint16 {
	if len(b) < binaryHeaderSize {
		return 0
	}
	return binary.LittleEndian.Uint16(b[2:])
}

// Len returns the number of entries. Returns 0 for nil/short/corrupt slices.
func (b BinaryExposureLog) Len() int {
	if len(b) < binaryHeaderSize {
		return 0
	}
	es := int(b.EntrySize())
	if es == 0 {
		return 0
	}
	return (len(b) - binaryHeaderSize) / es
}

// entryOffset returns the byte offset for entry i.
// Caller must ensure 0 <= i < Len().
func (b BinaryExposureLog) entryOffset(i int) int {
	return binaryHeaderSize + i*int(b.EntrySize())
}

// Timestamp returns the timestamp of entry i. Panics if i >= Len().
func (b BinaryExposureLog) Timestamp(i int) int64 {
	return int64(binary.LittleEndian.Uint64(b[b.entryOffset(i):])) //nolint:gosec // timestamp stored as uint64, always positive
}

// ImpressionHash returns the impression ID hash of entry i. Panics if i >= Len().
func (b BinaryExposureLog) ImpressionHash(i int) uint64 {
	return binary.LittleEndian.Uint64(b[b.entryOffset(i)+8:])
}

// AdvertiserHash returns the advertiser ID hash of entry i. Panics if i >= Len().
func (b BinaryExposureLog) AdvertiserHash(i int) uint64 {
	return binary.LittleEndian.Uint64(b[b.entryOffset(i)+16:])
}

// CampaignHash returns the campaign ID hash of entry i. Panics if i >= Len().
func (b BinaryExposureLog) CampaignHash(i int) uint64 {
	return binary.LittleEndian.Uint64(b[b.entryOffset(i)+24:])
}

// CreativeHash returns the creative ID hash of entry i. Panics if i >= Len().
func (b BinaryExposureLog) CreativeHash(i int) uint64 {
	return binary.LittleEndian.Uint64(b[b.entryOffset(i)+32:])
}

// filterHash returns the hash for the configured filter kind.
func (b BinaryExposureLog) filterHash(i int, kind ExposureFilterKind) uint64 {
	switch kind {
	case FilterAdvertiser:
		return b.AdvertiserHash(i)
	case FilterCampaign:
		return b.CampaignHash(i)
	case FilterCreative:
		return b.CreativeHash(i)
	}
	return 0
}

// MergeBinaryLogs merges multiple binary logs, deduplicating by impression hash.
// Returns a new v3 binary log.
func MergeBinaryLogs(logs ...BinaryExposureLog) BinaryExposureLog {
	total := 0
	for _, log := range logs {
		total += log.Len()
	}
	seen := make(map[uint64]struct{}, total)
	result := newBinaryLog(total)

	for _, log := range logs {
		for i := range log.Len() {
			impHash := log.ImpressionHash(i)
			if _, dup := seen[impHash]; dup {
				continue
			}
			seen[impHash] = struct{}{}
			offset := log.entryOffset(i)
			result = append(result, log[offset:offset+binaryEntrySize]...)
		}
	}
	return result
}

// CheckFrequencyRulesBinary checks frequency rules against a binary exposure log.
// No dedup — assumes the log is already deduped or caller accepts potential double-count.
func CheckFrequencyRulesBinary(log BinaryExposureLog, kind ExposureFilterKind, filterHash uint64, rules []FrequencyRule, nowUnix int64) bool {
	for _, rule := range rules {
		cutoff := nowUnix - int64(rule.Window.Seconds())
		count := 0
		for i := range log.Len() {
			if log.Timestamp(i) < cutoff {
				continue
			}
			if log.filterHash(i, kind) == filterHash {
				count++
			}
		}
		if count >= rule.MaxCount {
			return true
		}
	}
	return false
}

// LatestExposureBinary returns the most recent timestamp for a hash of the given kind.
func LatestExposureBinary(log BinaryExposureLog, kind ExposureFilterKind, filterHash uint64) int64 {
	var latest int64
	for i := range log.Len() {
		if log.filterHash(i, kind) == filterHash && log.Timestamp(i) > latest {
			latest = log.Timestamp(i)
		}
	}
	return latest
}

// CheckFrequencyRulesMultiLog checks frequency rules across multiple binary logs
// without merging first. Deduplicates lazily — only tracks impression hashes for
// entries that match the filter, avoiding a full upfront merge.
func CheckFrequencyRulesMultiLog(logs []BinaryExposureLog, kind ExposureFilterKind, filterHash uint64, rules []FrequencyRule, nowUnix int64) bool {
	for _, rule := range rules {
		cutoff := nowUnix - int64(rule.Window.Seconds())
		seen := make(map[uint64]struct{})
		count := 0
		for _, log := range logs {
			for i := range log.Len() {
				if log.Timestamp(i) < cutoff {
					continue
				}
				if log.filterHash(i, kind) != filterHash {
					continue
				}
				impHash := log.ImpressionHash(i)
				if _, dup := seen[impHash]; dup {
					continue
				}
				seen[impHash] = struct{}{}
				count++
			}
		}
		if count >= rule.MaxCount {
			return true
		}
	}
	return false
}

// LatestExposureMultiLog finds the latest timestamp for a hash of the given kind
// across multiple logs.
func LatestExposureMultiLog(logs []BinaryExposureLog, kind ExposureFilterKind, filterHash uint64) int64 {
	var latest int64
	for _, log := range logs {
		for i := range log.Len() {
			if log.filterHash(i, kind) == filterHash && log.Timestamp(i) > latest {
				latest = log.Timestamp(i)
			}
		}
	}
	return latest
}

// TruncateBinaryLog keeps only the last maxEntries entries (appended most recently).
// Returns the input unchanged if it is already within the limit.
func TruncateBinaryLog(b BinaryExposureLog, maxEntries int) BinaryExposureLog {
	n := b.Len()
	if n <= maxEntries {
		return b
	}
	keepFrom := n - maxEntries
	result := newBinaryLog(maxEntries)
	result = result[:binaryHeaderSize+maxEntries*binaryEntrySize]
	copy(result[binaryHeaderSize:], b[b.entryOffset(keepFrom):])
	return result
}

// hashString returns an FNV-1a 64-bit hash. Used for compact binary storage
// of advertiser/campaign/creative/impression IDs. Collision probability is
// ~0.0003% at 10M unique strings (birthday bound). Acceptable for frequency
// cap counting where an occasional collision causes slight over/under-counting.
func hashString(s string) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(s)) // fnv.Write never returns an error
	return h.Sum64()
}
