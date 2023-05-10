package cache_test

import (
	"context"
	"errors"
	"log"
	"math"
	"time"

	"github.com/gomodule/redigo/redis"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/seaguest/cache"
)

type TestStruct struct {
	Name string
}

type metric struct {
	Key         string
	Type        cache.MetricType
	ElapsedTime time.Duration
}

// this will be called by deepcopy to improves reflect copy performance
func (p *TestStruct) DeepCopy() interface{} {
	c := *p
	return &c
}

var _ = Describe("Cache", func() {
	Context("Cache", func() {
		var (
			pool       *redis.Pool
			ehCache    cache.Cache
			metricChan chan *metric
			val        *TestStruct
			key        string
			delay      time.Duration
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
					return redis.Dial("tcp", "127.0.0.1:6379")
				},
			}
			metricChan = make(chan *metric, 1)

			val = &TestStruct{Name: "test"}
			key = "test#1"
			delay = time.Millisecond * 1200
		})

		Context("Test loadFunc", func() {
			BeforeEach(func() {
				ehCache = cache.New(
					cache.GetConn(pool.Get),
					cache.CleanInterval(time.Second),
					cache.OnMetric(func(key string, metricType cache.MetricType, elapsedTime time.Duration) {
						mc := &metric{
							Key:         key,
							Type:        metricType,
							ElapsedTime: elapsedTime,
						}
						metricChan <- mc

					}),
					cache.OnError(func(err error) {
						log.Printf("OnError:%+v", err)
					}),
				)
			})

			It("loadFunc succeed", func() {
				ehCache.Delete(key)

				loadFunc := func() (interface{}, error) {
					time.Sleep(delay)
					return val, nil
				}

				ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
				defer cancel()

				var v TestStruct
				err := ehCache.GetObject(ctx, key, &v, time.Second*3, loadFunc)
				Ω(err).ToNot(HaveOccurred())
				Ω(&v).To(Equal(val))

				mc := <-metricChan
				Ω(mc.Key).To(Equal(key))
				Ω(mc.Type).To(Equal(cache.MetricTypeMiss))
				Ω(math.Abs(float64(mc.ElapsedTime-delay)) < float64(time.Millisecond*10)).To(Equal(true))
			})

			It("loadFunc error", func() {
				ehCache.Delete(key)

				unkownErr := errors.New("unknown error")
				loadFunc := func() (interface{}, error) {
					return nil, unkownErr
				}

				ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
				defer cancel()

				var v TestStruct
				err := ehCache.GetObject(ctx, key, &v, time.Second*3, loadFunc)
				Ω(err).To(Equal(unkownErr))

				mc := <-metricChan
				Ω(mc.Key).To(Equal(key))
				Ω(mc.Type).To(Equal(cache.MetricTypeMiss))
			})

			It("loadFunc panic string", func() {
				panicMsg := "panic string"
				loadFunc := func() (interface{}, error) {
					panic(panicMsg)
					return val, nil
				}

				ctx, cancel := context.WithTimeout(context.Background(), time.Second*2)
				defer cancel()

				var v TestStruct
				err := ehCache.GetObject(ctx, key, &v, time.Second*3, loadFunc)
				Ω(err.Error()).To(Equal(panicMsg))

				mc := <-metricChan
				Ω(mc.Key).To(Equal(key))
				Ω(mc.Type).To(Equal(cache.MetricTypeMiss))

			})

			It("loadFunc panic error", func() {
				panicErr := errors.New("panic error")
				loadFunc := func() (interface{}, error) {
					panic(panicErr)
					return val, nil
				}

				ctx, cancel := context.WithTimeout(context.Background(), time.Second*2)
				defer cancel()

				var v TestStruct
				err := ehCache.GetObject(ctx, key, &v, time.Second*3, loadFunc)
				Ω(errors.Is(err, panicErr)).To(Equal(true))

				mc := <-metricChan
				Ω(mc.Key).To(Equal(key))
				Ω(mc.Type).To(Equal(cache.MetricTypeMiss))

			})

			It("loadFunc timeout", func() {
				ehCache.Delete(key)

				loadFunc := func() (interface{}, error) {
					time.Sleep(delay)
					return val, nil
				}

				ctx, cancel := context.WithTimeout(context.Background(), time.Second*1)
				defer cancel()

				var v TestStruct
				err := ehCache.GetObject(ctx, key, &v, time.Second*3, loadFunc)
				Ω(err).To(MatchError(context.DeadlineExceeded))

				mc := <-metricChan
				Ω(mc.Key).To(Equal(key))
				Ω(mc.Type).To(Equal(cache.MetricTypeMiss))
			})

		})

		Context("Test redis hit", func() {
			BeforeEach(func() {
				ehCache = cache.New(
					cache.GetConn(pool.Get),
					cache.CleanInterval(time.Second),
					cache.OnMetric(func(key string, metricType cache.MetricType, elapsedTime time.Duration) {
						mc := &metric{
							Key:         key,
							Type:        metricType,
							ElapsedTime: elapsedTime,
						}
						metricChan <- mc

					}),
					cache.OnError(func(err error) {
						log.Printf("OnError:%+v", err)
					}),
				)
			})

			It("redis hit ok", func() {
				ehCache.Delete(key)
				loadFunc := func() (interface{}, error) {
					time.Sleep(delay)
					return val, nil
				}

				ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
				defer cancel()

				var v TestStruct
				err := ehCache.GetObject(ctx, key, &v, time.Second*1, loadFunc)
				Ω(err).ToNot(HaveOccurred())
				Ω(&v).To(Equal(val))

				mc := <-metricChan
				Ω(mc.Key).To(Equal(key))
				Ω(mc.Type).To(Equal(cache.MetricTypeMiss))

				ehCache.FlushMem()

				ctx, cancel = context.WithTimeout(context.Background(), time.Second*5)
				defer cancel()
				err = ehCache.GetObject(ctx, key, &v, time.Second*1, loadFunc)
				Ω(err).ToNot(HaveOccurred())
				Ω(&v).To(Equal(val))

				mc = <-metricChan
				Ω(mc.Key).To(Equal(key))
				Ω(mc.Type).To(Equal(cache.MetricTypeRedisHit))
			})

			It("redis hit expired", func() {
				ehCache.Delete(key)
				loadFunc := func() (interface{}, error) {
					time.Sleep(delay)
					return val, nil
				}

				ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
				defer cancel()

				var v TestStruct
				err := ehCache.GetObject(ctx, key, &v, time.Second*1, loadFunc)
				Ω(err).ToNot(HaveOccurred())
				Ω(&v).To(Equal(val))

				mc := <-metricChan
				Ω(mc.Key).To(Equal(key))
				Ω(mc.Type).To(Equal(cache.MetricTypeMiss))

				// wait 2 second
				time.Sleep(time.Second * 2)
				ehCache.FlushMem()

				ctx, cancel = context.WithTimeout(context.Background(), time.Second*5)
				defer cancel()
				err = ehCache.GetObject(ctx, key, &v, time.Second*1, loadFunc)
				Ω(err).ToNot(HaveOccurred())
				Ω(&v).To(Equal(val))

				mc = <-metricChan
				Ω(mc.Key).To(Equal(key))
				Ω(mc.Type).To(Equal(cache.MetricTypeRedisHitExpired))
			})
		})

		Context("Test mem hit", func() {
			BeforeEach(func() {
				ehCache = cache.New(
					cache.GetConn(pool.Get),
					cache.CleanInterval(time.Second*2),
					cache.OnMetric(func(key string, metricType cache.MetricType, elapsedTime time.Duration) {
						mc := &metric{
							Key:         key,
							Type:        metricType,
							ElapsedTime: elapsedTime,
						}
						metricChan <- mc

					}),
					cache.OnError(func(err error) {
						log.Printf("OnError:%+v", err)
					}),
				)
			})

			It("mem hit ok", func() {
				ehCache.Delete(key)
				loadFunc := func() (interface{}, error) {
					time.Sleep(delay)
					return val, nil
				}

				ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
				defer cancel()

				var v TestStruct
				err := ehCache.GetObject(ctx, key, &v, time.Second*1, loadFunc)
				Ω(err).ToNot(HaveOccurred())
				Ω(&v).To(Equal(val))

				mc := <-metricChan
				Ω(mc.Key).To(Equal(key))
				Ω(mc.Type).To(Equal(cache.MetricTypeMiss))

				time.Sleep(time.Millisecond)

				ctx, cancel = context.WithTimeout(context.Background(), time.Second*5)
				defer cancel()
				err = ehCache.GetObject(ctx, key, &v, time.Second*1, loadFunc)
				Ω(err).ToNot(HaveOccurred())
				Ω(&v).To(Equal(val))

				mc = <-metricChan
				Ω(mc.Key).To(Equal(key))
				Ω(mc.Type).To(Equal(cache.MetricTypeMemHit))
			})

			It("mem hit expired", func() {
				ehCache.Delete(key)
				loadFunc := func() (interface{}, error) {
					time.Sleep(delay)
					return val, nil
				}

				ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
				defer cancel()

				var v TestStruct
				err := ehCache.GetObject(ctx, key, &v, time.Second*1, loadFunc)
				Ω(err).ToNot(HaveOccurred())
				Ω(&v).To(Equal(val))

				mc := <-metricChan
				Ω(mc.Key).To(Equal(key))
				Ω(mc.Type).To(Equal(cache.MetricTypeMiss))

				// wait 2 second
				time.Sleep(time.Millisecond * 2000)

				ctx, cancel = context.WithTimeout(context.Background(), time.Second*5)
				defer cancel()
				err = ehCache.GetObject(ctx, key, &v, time.Second*1, loadFunc)
				Ω(err).ToNot(HaveOccurred())
				Ω(&v).To(Equal(val))

				mc = <-metricChan
				Ω(mc.Key).To(Equal(key))
				Ω(mc.Type).To(Equal(cache.MetricTypeMemHitExpired))
			})
		})

	})
})
