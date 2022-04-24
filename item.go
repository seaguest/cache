package cache

import "time"

type Item struct {
	Object     interface{} `json:"object"`     // object
	TTL        int         `json:"ttl"`        // key ttl, in second
	Outdate    int64       `json:"outdate"`    // outdated keys will be deleted from in-memory cache, but staty in redis.
	Expiration int64       `json:"expiration"` // expired keys will be deleted from redis.
}

// Outdated returns true if data is outdated.
func (i *Item) Outdated() bool {
	if i.Outdate != 0 && i.Outdate < time.Now().Unix() {
		return true
	}
	return false
}

func newItem(v interface{}, d int) *Item {
	ttl := d
	var od, e int64
	if d > 0 {
		od = time.Now().Add(time.Duration(d) * time.Second).Unix()
		e = time.Now().Add(time.Duration(d*redisFactor) * time.Second).Unix()
	}

	return &Item{
		Object:     v,
		TTL:        ttl,
		Outdate:    od,
		Expiration: e,
	}
}
