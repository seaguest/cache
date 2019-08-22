# cache
A distributed two-level cache (memory + redis) with LoadFunc library for Go.

Mem cache is build on top of sync.Map.

When cache.Delete(key) is called, redis will publish to all cache nodes, and each node will delete the key from mem and redis.

Usage:


``` 
func getXXX(id uint32, db *gorm.DB) (*XXX, error) {
	key := fmt.Sprint(id)
	var v XXX
	err := cache.GetCacheObject(key, &v, 60, func() (interface{}, error) {
		var err error
		// DB find
		return *XXX, nil
	})
	if err != nil {
		logger.Error(err)
		return nil, err
	}
	return &v, nil
}

```
