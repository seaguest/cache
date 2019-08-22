package cache

import "time"

type Item struct {
	Object     interface{} `json:"object"`     // stored object
	TTL        int         `json:"ttl"`        // key ttl, in second
	Outdate    int64       `json:"outdate"`    // the object's outdate timestamp (key will not be deleted)
	Expiration int64       `json:"expiration"` // expiration time (key will be deleted)
}

// Returns true if the item has expired.
func (item Item) Expired() bool {
	if item.Expiration == 0 {
		return false
	}
	return time.Now().UnixNano() > item.Expiration
}

// Returns true if the item has expired.
func (item Item) Outdated() bool {
	if item.Expired() {
		return true
	}

	if item.Outdate < time.Now().UnixNano() {
		return true
	}
	return false
}

func NewItem(v interface{}, d int, lazy bool) *Item {
	var od, e int64
	var ttl int
	od = time.Now().Add(time.Duration(d) * time.Second).UnixNano()
	if d > 0 {
		ttl = d
		factor := 1
		if lazy {
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
