package cache

import (
	"encoding/binary"
	"errors"
	"time"
)

const (
	recordHeaderSize = 22
	recordVersion    = 1
	recordNegative   = 1 << 0
)

var errInvalidRecord = errors.New("cache: invalid record")

type record struct {
	softExpireAt int64
	hardExpireAt int64
	negative     bool
	payload      []byte
}

func encodeRecord(r record) []byte {
	data := make([]byte, recordHeaderSize+len(r.payload))
	copy(data[:4], "CAC1")
	data[4] = recordVersion
	if r.negative {
		data[5] = recordNegative
	}
	binary.BigEndian.PutUint64(data[6:14], uint64(r.softExpireAt))
	binary.BigEndian.PutUint64(data[14:22], uint64(r.hardExpireAt))
	copy(data[recordHeaderSize:], r.payload)
	return data
}

func decodeRecord(data []byte) (record, error) {
	if len(data) < recordHeaderSize || string(data[:4]) != "CAC1" || data[4] != recordVersion || data[5]&^byte(recordNegative) != 0 {
		return record{}, errInvalidRecord
	}
	rec := record{
		softExpireAt: int64(binary.BigEndian.Uint64(data[6:14])),
		hardExpireAt: int64(binary.BigEndian.Uint64(data[14:22])),
		negative:     data[5]&recordNegative != 0,
		payload:      data[recordHeaderSize:],
	}
	if rec.softExpireAt <= 0 || rec.hardExpireAt < rec.softExpireAt {
		return record{}, errInvalidRecord
	}
	return rec, nil
}

type recordState uint8

const (
	recordFresh recordState = iota
	recordStale
	recordExpired
)

func (r record) state(now time.Time) recordState {
	ms := now.UnixMilli()
	if ms >= r.hardExpireAt {
		return recordExpired
	}
	if ms >= r.softExpireAt {
		return recordStale
	}
	return recordFresh
}

func (r record) ttl(now time.Time) time.Duration {
	d := time.UnixMilli(r.hardExpireAt).Sub(now)
	if d < 0 {
		return 0
	}
	return d
}
