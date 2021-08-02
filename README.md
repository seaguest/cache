# cache
A lightweight high-performance distributed cache, built on top of in-memory + redis.

This is a cache-aside pattern implementation for two-level cache, it does support multiple cache nodes, all cache nodes share one redis but maintains its own in-memory cache. When cache.Delete(key) is called, redis will publish deletion message to all cache nodes to delete key on each cache node.

In latest code, redis is set to lazy mode internally which means redis will keep keys for lazyFactor(256 as default)*ttl, while in-memory keeps for ttl.

If cache is disabled (disabled by cache.Disabled()), GetObject will call directly loader function.

### Installation

`go get -u github.com/seaguest/cache`


### Tips

object is deeply cloned (github.com/mohae/deepcopy is adopted) before returned to avoid dirty data, to make it more efficient, please implement DeepCopy method if you encounter deepcopy performance trouble. 

```
func (p TestStruct) DeepCopy() interface{} {
	c := p
	return &c
}
```

### Usage

``` 
package cache

import (
	"testing"
	"time"

	"github.com/seaguest/log"
)

type TestStruct struct {
	Name string
}

// this will be called by deepcopy to improves reflect copy performance
func (p *TestStruct) DeepCopy() interface{} {
	c := p
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
		log.Error(err)
		return nil, err
	}
	return &v, nil
}

func TestCache(t *testing.T) {
	Init("127.0.0.1:6379", "", 200)
	v, e := getStruct(100)
	log.Error(v, e)
}


```
