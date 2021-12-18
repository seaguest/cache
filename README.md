# cache
A lightweight high-performance distributed cache, a cache-aside pattern implementation built on top of in-memory + redis.

Cache contains one global redis + multiple in-memory instances, data can't be synced among instances, but cache.Delete(key) can delete key from redis + all memory instances, which can be used to make data consistent among instances.

Keys stay ttl in in-memory cache, lazyFactor(256 default)*ttl in redis.

Cache can be disabled (cache.Disable()), thus GetObject will call directly loader function.

### Core code
Keys will be checked firstly in in-memory cache then redis, if neither found, loader function will be called to return, data will be updated asynchronously if outdated.
```bigquery
func (c *Cache) GetObject(key string, obj interface{}, ttl int, f LoadFunc) error {
	// is disabled, call loader function
	if c.disabled {
		o, err := f()
		if err != nil {
			return err
		}
		return copy(o, obj)
	}
	return c.getObject(key, obj, ttl, f)
}

func (c *Cache) getObject(key string, obj interface{}, ttl int, f LoadFunc) error {
	v, ok := c.mem.get(key)
	if ok {
		if v.Outdated() {
			dst := deepcopy.Copy(obj)
			go c.syncMem(key, dst, ttl, f)
		}
		return copy(v.Object, obj)
	}

	v, ok = c.rds.get(key, obj)
	if ok {
		if v.Outdated() {
			go c.rds.load(key, nil, ttl, f, false)
		}
		return copy(v.Object, obj)
	}
	return c.rds.load(key, obj, ttl, f, true)
}
```


### Installation

`go get -u github.com/seaguest/cache`


### Tips

```github.com/seaguest/deepcopy```is adopted for deepcopy, returned value is deepcopied to avoid dirty data.
please implement DeepCopy interface if you encounter deepcopy performance trouble.

```bigquery
func (p *TestStruct) DeepCopy() interface{} {
	c := *p
	return &c
}
```

### Usage

``` bigquery
package cache

import (
	"log"
	"testing"
	"time"
)

type TestStruct struct {
	Name string
}

// this will be called by deepcopy to improves reflect copy performance
func (p *TestStruct) DeepCopy() interface{} {
	c := *p
	return &c
}

func getStruct(id uint32) (*TestStruct, error) {
	key := GetKey("val", id)
	var v TestStruct
	err := GetObject(key, &v, 60, func() (interface{}, error) {
		// data fetch logic to be done here
		time.Sleep(time.Millisecond * 100)
		return &TestStruct{Name: "test"}, nil
	})
	if err != nil {
		log.Println(err)
		return nil, err
	}
	return &v, nil
}

func TestCache(t *testing.T) {
	Init("127.0.0.1:6379", "", 200)
	v, e := getStruct(100)
	log.Println(v, e)
}



```
