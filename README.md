# cache
The library is a lightweight and high-performance distributed caching solution that implements the cache-aside pattern using a combination of in-memory and Redis data stores. The cache consists of one global Redis instance and multiple in-memory instances, with any data changes being synchronized across all instances.

The library is designed to prioritize retrieving data from the in-memory cache first, followed by the Redis cache if the data is not found locally. If the data is still not found in either cache, the library will call a loader function to retrieve the data and store it in the cache for future access.

One of the key benefits of this library is its performance. By leveraging both in-memory and Redis caches, the library can quickly retrieve frequently accessed data without having to rely solely on network calls to Redis. Additionally, the use of a loader function allows for on-demand retrieval of data, reducing the need for expensive data preloading.

Overall, the library is an effective solution for implementing distributed caching in Go applications, offering a balance of performance and simplicity through its use of a cache-aside pattern and combination of in-memory and Redis data stores.

### Features

* two-level cache
* data synchronized among instances.

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
	"strings"
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
		GetObjectType: func(key string) string {
			ss := strings.Split(key, ":")
			return ss[0]
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
