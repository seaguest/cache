package tiercache

import (
	"time"

	"github.com/vmihailenco/msgpack/v5"
)

type record struct {
	Version      uint8            `json:"v"`
	Codec        string           `json:"codec"`
	Payload      []byte           `json:"payload"`
	CreatedAt    int64            `json:"created_at"`
	SoftExpireAt int64            `json:"soft_expire_at"`
	HardExpireAt int64            `json:"hard_expire_at"`
	Negative     bool             `json:"negative"`
	KeyHash      string           `json:"key_hash"`
	TagHashes    []string         `json:"tag_hashes,omitempty"`
	NamespaceVer int64            `json:"namespace_version"`
	KeyVersion   int64            `json:"key_version"`
	TagVersions  map[string]int64 `json:"tag_versions,omitempty"`
	StorageHash  uint64           `json:"storage_hash"`
}

type recordState int

const (
	recordFresh recordState = iota
	recordStale
	recordExpired
)

func encodeRecord(r record) ([]byte, error) {
	return msgpack.Marshal(r)
}

func decodeRecord(data []byte) (record, error) {
	var r record
	err := msgpack.Unmarshal(data, &r)
	return r, err
}

func (r record) state(now time.Time) recordState {
	ts := now.UnixMilli()
	if r.HardExpireAt > 0 && ts >= r.HardExpireAt {
		return recordExpired
	}
	if r.SoftExpireAt > 0 && ts >= r.SoftExpireAt {
		return recordStale
	}
	return recordFresh
}

func (r record) ttl(now time.Time) time.Duration {
	if r.HardExpireAt <= 0 {
		return 0
	}
	d := time.Until(time.UnixMilli(r.HardExpireAt))
	if d < 0 {
		return 0
	}
	return d
}
