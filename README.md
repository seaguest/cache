# cache
A distributed two-level cache (memory + redis) with loader function library for Go.

Mem cache is built on top of sync.Map.

When cache.Delete(key) is called, redis will publish to all cache nodes, then an delete 
 action (mem+redis) is performed by each cache node.

Usage:


``` 
package main

import (
	"github.com/jinzhu/gorm"
	"github.com/seaguest/cache"
	"github.com/seaguest/common/logger"
)

func getVal(id uint32, db *gorm.DB) (uint32, error) {
	key := cache.GetCacheKey("val", id)
	var v uint32
	err := cache.GetCacheObject(key, &v, 60, func() (interface{}, error) {
		// DB query
		var res uint32 = 100
		return res, nil
	})
	if err != nil {
		logger.Error(err)
		return uint32(0), err
	}
	return v, nil
}

func main() {
	cache.Init("127.0.0.1:6379", "", true, 200)

	v, e := getVal(100, nil)
	logger.Error(v, e)
}

```
