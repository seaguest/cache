package cache

import "time"

type Item struct {
	Object     interface{} `json:"object"`     // object
	TTL        int         `json:"ttl"`        // key ttl, in second
	Outdate    int64       `json:"outdate"`    // outdate means the time when data become non-fresh. In lazy mode, non-fresh data may stay in cache
	Expiration int64       `json:"expiration"` // expiration time (key will be deleted)
}

// Returns true if data is outdated.
func (item Item) Outdated() bool {
	if item.Outdate == 0 {
		return false
	}

	if item.Outdate < time.Now().UnixNano() {
		return true
	}
	return false
}

func NewItem(v interface{}, d int, lazyMode bool) *Item {
	var od, e int64
	var ttl int
	od = time.Now().Add(time.Duration(d) * time.Second).UnixNano()
	if d > 0 {
		ttl = d
		factor := 1
		if lazyMode {
			factor = lazyFactor
		}
		e = time.Now().Add(time.Duration(d*factor) * time.Second).UnixNano()
	}

	return &Item{
		Object:     v,
		TTL:        ttl,
		Outdate:    od,
		Expiration: e,
	}
}
