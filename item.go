package cache

import "time"

type Item struct {
	Object   interface{}   `json:"object"`    // object
	TTL      time.Duration `json:"ttl"`       // key ttl, in second
	ExpireAt int64         `json:"expire_at"` // data expiration timestamp.
}

func newItem(v interface{}, ttl time.Duration) *Item {
	var expiredAt int64
	if ttl > 0 {
		expiredAt = time.Now().Add(ttl).Unix()
	}

	return &Item{
		Object:   v,
		TTL:      ttl,
		ExpireAt: expiredAt,
	}
}
