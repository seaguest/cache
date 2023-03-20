# cache
A lightweight and high-performance distributed caching, a cache-aside pattern implementation built on top of in-memory + Redis. The cache consists of one global Redis instance and multiple in-memory instances, with any data changes being synchronized across all instances.

The library is designed to prioritize retrieving data from the in-memory cache first, followed by the Redis cache if the data is not found locally. If the data is still not found in either cache, the library will call a loader function to retrieve the data and store it in the cache for future access.

One of the key benefits of this library is its performance. By leveraging both in-memory and Redis caches, the library can quickly retrieve frequently accessed data without having to rely solely on network calls to Redis. Additionally, the use of a loader function allows for on-demand retrieval of data, reducing the need for expensive data preloading.

![alt text](./assets/cache.png "cache-aside pattern")

### Features

* two-level cache
* data synchronization
* metrics

### Installation

`go get -u github.com/seaguest/cache`


### Tips

```github.com/seaguest/deepcopy```is adopted for deepcopy, returned value is deepcopied to avoid dirty data.
please implement DeepCopy interface if you encounter deepcopy performance trouble.

```go
func (p *TestStruct) DeepCopy() interface{} {
	c := *p
	return &c
}
```

### Usage

``` go
package cache_test

import (
	"context"
	"fmt"
	"log"
	"testing"
	"time"

	"github.com/gomodule/redigo/redis"
	"github.com/seaguest/cache"
)

type TestStruct struct {
	Name string
}

// this will be called by deepcopy to improves reflect copy performance
func (p *TestStruct) DeepCopy() interface{} {
	c := *p
	return &c
}

func getStruct(ctx context.Context, id uint32, cache *cache.Cache) (*TestStruct, error) {
	var v TestStruct
	err := cache.GetObject(ctx, fmt.Sprintf("TestStruct:%d", id), &v, time.Second*3, func() (interface{}, error) {
		// data fetch logic to be done here
		time.Sleep(time.Millisecond * 1200 * 1)
		return &TestStruct{Name: "test"}, nil
	})
	if err != nil {
		fmt.Printf("%+v", err)
		return nil, err
	}
	return &v, nil
}

func TestCache(t *testing.T) {
	pool := &redis.Pool{
		MaxIdle:     1000,
		MaxActive:   1000,
		Wait:        true,
		IdleTimeout: 240 * time.Second,
		TestOnBorrow: func(c redis.Conn, t time.Time) error {
			_, err := c.Do("PING")
			return err
		},
		Dial: func() (redis.Conn, error) {
			return redis.Dial("tcp", "127.0.0.1:7379")
		},
	}

	cfg := cache.Config{
		GetConn: pool.Get,
		OnMetric: func(metric cache.MetricType, objectType string, elapsedTime int) {
			// obtain metrics here
		},
		OnError: func(err error) {
			log.Printf("%+v", err)
		},
	}

	supercache := cache.New(cfg)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second*2)
	defer cancel()
	v, e := getStruct(ctx, 100, supercache)
	log.Println(v, e)
}
```

### JetBrains

Goland is an excellent IDE, thank JetBrains for their free Open Source licenses.
