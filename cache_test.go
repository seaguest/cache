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
