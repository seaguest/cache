# cache
A lightweight distributed two-level cache (memory + redis) with loader function library for Go.

This is an cache-aside pattern implementation for two-level cache, in-memory cache is buit on top of sync.Map which is thread-safe.

It does support multiple cache nodes, all cache nodes share one redis but maintains its own in-memory cache. When cache.Delete(key) is called, redis will publish to all cache nodes, then an delete 
 action (mem+redis) is performed by each cache node.

### Installation

`go get github.com/seaguest/cache`


### Tips

github.com/mohae/deepcopy is needed for object copy, if you want the copy to be efficient , please implement DeepCopy method. 

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

// this is called by deepcopy, this improves reflect performance
func (p TestStruct) DeepCopy() interface{} {
	c := p
	return &c
}

func getStruct(id uint32) (*TestStruct, error) {
	key := cache.GetCacheKey("val", id)
	var v TestStruct
	err := cache.GetCacheObject(key, &v, 60, func() (interface{}, error) {
		// DB query
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
