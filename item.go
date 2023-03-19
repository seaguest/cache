package cache

import "time"

type Item struct {
	Object   interface{} `json:"object"`    // object
	TTL      int         `json:"ttl"`       // key ttl, in second
	ExpireAt int64       `json:"expire_at"` // data expiration timestamp.
}

func newItem(v interface{}, ttl int) *Item {
	var expiredAt int64
	if ttl > 0 {
		expiredAt = time.Now().Add(time.Duration(ttl) * time.Second).Unix()
	}

	return &Item{
		Object:   v,
		TTL:      ttl,
		ExpireAt: expiredAt,
	}
}
