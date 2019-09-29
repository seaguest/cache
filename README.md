# cache
A lightweight high-performance distributed two-level cache (in-memory + redis) with loader function library for Go.

This is a cache-aside pattern implementation for two-level cache, it does support multiple cache nodes, all cache nodes share one redis but maintains its own in-memory cache. When cache.Delete(key) is called, redis will publish deletion message to all cache nodes, then an delete action (in-memory + redis) is performed on each cache node.

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
package main

import (
	"time"

	"github.com/seaguest/cache"
	"github.com/seaguest/common/logger"
)

type TestStruct struct {
	Name string
}

// this will be called by deepcopy to improves reflect copy performance
func (p TestStruct) DeepCopy() interface{} {
	c := p
	return &c
}

func getStruct(id uint32) (*TestStruct, error) {
	key := cache.GetCacheKey("val", id)
	var v TestStruct
	err := cache.GetCacheObject(key, &v, 60, func() (interface{}, error) {
		// data fetch logic to be done here
		time.Sleep(time.Millisecond * 100)
		return &TestStruct{Name: "test"}, nil
	})
	if err != nil {
		logger.Error(err)
		return nil, err
	}
	return &v, nil
}

func main() {
	cache.Init("127.0.0.1:6379", "", true, 200)
	v, e := getStruct(100)
	logger.Error(v, e)
}


```
