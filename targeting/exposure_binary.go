package targeting

import (
	"encoding/binary"
	"hash/fnv"
)

// BinaryExposureEntry is a fixed-size exposure record (32 bytes).
// Layout: timestamp(8) + impressionHash(8) + packageHash(8) + campaignHash(8)
const binaryEntrySize = 32

// BinaryExposureLog is a compact byte-slice exposure log.
// No parsing needed — direct offset arithmetic.
type BinaryExposureLog []byte

// EncodeBinaryExposureLog converts a JSON exposure log to binary format.
func EncodeBinaryExposureLog(log ExposureLog) BinaryExposureLog {
	buf := make([]byte, len(log)*binaryEntrySize)
	for i, e := range log {
		offset := i * binaryEntrySize
		binary.LittleEndian.PutUint64(buf[offset:], uint64(e.Timestamp))
		binary.LittleEndian.PutUint64(buf[offset+8:], hashString(e.ImpressionID))
		binary.LittleEndian.PutUint64(buf[offset+16:], hashString(e.PackageID))
		binary.LittleEndian.PutUint64(buf[offset+24:], hashString(e.CampaignID))
	}
	return buf
}

// Len returns the number of entries.
func (b BinaryExposureLog) Len() int {
	return len(b) / binaryEntrySize
}

// Timestamp returns the timestamp of entry i.
func (b BinaryExposureLog) Timestamp(i int) int64 {
	return int64(binary.LittleEndian.Uint64(b[i*binaryEntrySize:]))
}

// ImpressionHash returns the impression ID hash of entry i.
func (b BinaryExposureLog) ImpressionHash(i int) uint64 {
	return binary.LittleEndian.Uint64(b[i*binaryEntrySize+8:])
}

// PackageHash returns the package ID hash of entry i.
func (b BinaryExposureLog) PackageHash(i int) uint64 {
	return binary.LittleEndian.Uint64(b[i*binaryEntrySize+16:])
}

// CampaignHash returns the campaign ID hash of entry i.
func (b BinaryExposureLog) CampaignHash(i int) uint64 {
	return binary.LittleEndian.Uint64(b[i*binaryEntrySize+24:])
}

// MergeBinaryLogs merges multiple binary logs, deduplicating by impression hash.
// Returns a new binary log.
func MergeBinaryLogs(logs ...BinaryExposureLog) BinaryExposureLog {
	// Count total entries.
	total := 0
	for _, log := range logs {
		total += log.Len()
	}
	if total == 0 {
		return nil
	}

	seen := make(map[uint64]struct{}, total)
	result := make([]byte, 0, total*binaryEntrySize)

	for _, log := range logs {
		for i := range log.Len() {
			impHash := log.ImpressionHash(i)
			if _, dup := seen[impHash]; dup {
				continue
			}
			seen[impHash] = struct{}{}
			offset := i * binaryEntrySize
			result = append(result, log[offset:offset+binaryEntrySize]...)
		}
	}
	return result
}

// CheckFrequencyRulesBinary checks frequency rules against a binary exposure log.
// No dedup — assumes the log is already deduped or caller accepts potential double-count.
func CheckFrequencyRulesBinary(log BinaryExposureLog, filterHash uint64, isCampaign bool, rules []FrequencyRule, nowUnix int64) bool {
	for _, rule := range rules {
		cutoff := nowUnix - int64(rule.Window.Seconds())
		count := 0
		for i := range log.Len() {
			if log.Timestamp(i) < cutoff {
				continue
			}
			var entryHash uint64
			if isCampaign {
				entryHash = log.CampaignHash(i)
			} else {
				entryHash = log.PackageHash(i)
			}
			if entryHash == filterHash {
				count++
			}
		}
		if count >= rule.MaxCount {
			return true
		}
	}
	return false
}

// LatestExposureBinary returns the most recent timestamp for a package hash.
func LatestExposureBinary(log BinaryExposureLog, pkgHash uint64) int64 {
	var latest int64
	for i := range log.Len() {
		if log.PackageHash(i) == pkgHash && log.Timestamp(i) > latest {
			latest = log.Timestamp(i)
		}
	}
	return latest
}

// CheckFrequencyRulesMultiLog checks frequency rules across multiple binary logs
// without merging first. Deduplicates lazily — only tracks impression hashes for
// entries that match the filter, avoiding a full upfront merge.
func CheckFrequencyRulesMultiLog(logs []BinaryExposureLog, filterHash uint64, isCampaign bool, rules []FrequencyRule, nowUnix int64) bool {
	for _, rule := range rules {
		cutoff := nowUnix - int64(rule.Window.Seconds())
		seen := make(map[uint64]struct{})
		count := 0
		for _, log := range logs {
			for i := range log.Len() {
				if log.Timestamp(i) < cutoff {
					continue
				}
				var entryHash uint64
				if isCampaign {
					entryHash = log.CampaignHash(i)
				} else {
					entryHash = log.PackageHash(i)
				}
				if entryHash != filterHash {
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

// LatestExposureMultiLog finds the latest timestamp for a package across multiple logs.
func LatestExposureMultiLog(logs []BinaryExposureLog, pkgHash uint64) int64 {
	var latest int64
	for _, log := range logs {
		for i := range log.Len() {
			if log.PackageHash(i) == pkgHash && log.Timestamp(i) > latest {
				latest = log.Timestamp(i)
			}
		}
	}
	return latest
}

// hashString returns an FNV-1a 64-bit hash. Used for compact binary storage
// of package/campaign/impression IDs. Collision probability is ~0.0003% at
// 10M unique strings (birthday bound). Acceptable for frequency cap counting
// where an occasional collision causes slight over/under-counting.
func hashString(s string) uint64 {
	h := fnv.New64a()
	h.Write([]byte(s))
	return h.Sum64()
}
