package cache

// Testing provides access to internal cache methods for testing purposes.
// This should only be used in test files.
type Testing struct {
	cache *cache
}

// NewForTesting returns a cache instance with testing capabilities.
// This function should only be used in tests.
func NewForTesting(options ...Option) (*Testing, Cache) {
	c := New(options...)
	return &Testing{cache: c.(*cache)}, c
}

// DeleteFromMem allows to delete key from mem, for test purpose
func (t *Testing) DeleteFromMem(key string) {
	namespacedKey := t.cache.namespacedKey(key)
	t.cache.mem.delete(namespacedKey)
}

// DeleteFromRedis allows to delete key from redis, for test purpose
func (t *Testing) DeleteFromRedis(key string) error {
	namespacedKey := t.cache.namespacedKey(key)
	return t.cache.rds.delete(namespacedKey)
}