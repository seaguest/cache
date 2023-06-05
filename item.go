package cache

import "time"

type Item struct {
	Object   interface{} `json:"object"`    // object
	Size     int         `json:"size"`      // object size, in bytes.
	ExpireAt int64       `json:"expire_at"` // data expiration timestamp. in milliseconds.
}

func newItem(v interface{}, ttl time.Duration) *Item {
	var expiredAt int64
	if ttl > 0 {
		expiredAt = time.Now().Add(ttl).UnixMilli()
	}

	return &Item{
		Object:   v,
		ExpireAt: expiredAt,
	}
}

func (it *Item) Expired() bool {
	return it.ExpireAt != 0 && it.ExpireAt < time.Now().UnixMilli()
}
