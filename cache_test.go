package cache_test

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/gomodule/redigo/redis"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
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

var _ = Describe("Cache", func() {
	Context("Cache", func() {
		var (
			pool    *redis.Pool
			ehCache cache.Cache
		)

		BeforeEach(func() {
			pool = &redis.Pool{
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
		})

		It("Test cache get", func() {
			ehCache = cache.New(
				cache.GetConn(pool.Get),
				cache.OnMetric(func(key string, metric cache.MetricType, elapsedTime int) {
					log.Println("x---------", metric, "-", key, "-", elapsedTime)
				}),
				cache.OnError(func(err error) {
					log.Printf("%+v", err)
				}),
			)

			val := &TestStruct{Name: "test"}
			key := "test#1"

			loadFunc := func() (interface{}, error) {
				// data fetch logic to be done here
				time.Sleep(time.Millisecond * 1200 * 1)
				return val, nil
			}

			ctx, cancel := context.WithTimeout(context.Background(), time.Second*2)
			defer cancel()

			var v TestStruct
			err := ehCache.GetObject(ctx, key, &v, time.Second*3, loadFunc)
			if err != nil {
				fmt.Printf("%+v", err)
				return
			}

			log.Println("xxxxxxxxxxxx")
			log.Println(v, err)

			Ω(err).ToNot(HaveOccurred())
			Ω(&v).To(Equal(val))

			time.Sleep(time.Second * 3)
		})
	})
})
