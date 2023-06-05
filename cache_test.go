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
	Type        string
	ElapsedTime time.Duration
}

// this will be called by deepcopy to improves reflect copy performance
func (p *TestStruct) DeepCopy() interface{} {
	c := *p
	return &c
}

type mockCache struct {
	ehCache    cache.Cache
	metricChan chan metric
	val        *TestStruct
	key        string
	delay      time.Duration
}

func newMockCache(key string, delay, ci time.Duration, checkMetric bool) mockCache {
	mock := mockCache{}
	pool := &redis.Pool{
		MaxIdle:     2,
		MaxActive:   5,
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
	metricChan := make(chan metric, 20)
	mock.ehCache = cache.New(
		cache.GetConn(pool.Get),
		cache.CleanInterval(ci),
		cache.Separator("#"),
		cache.OnMetric(func(key, objectType string, metricType string, count int, elapsedTime time.Duration) {
			if metricType == cache.MetricTypeCount || metricType == cache.MetricTypeMemUsage {
				return
			}

			if !checkMetric {
				return
			}
			mc := metric{
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
	mock.metricChan = metricChan
	mock.key = key
	mock.val = &TestStruct{Name: "value for" + key}
	mock.delay = delay
	return mock
}

var _ = Describe("cache test", func() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)

	Context("cache unit test", func() {
		Context("Test loadFunc", func() {
			It("loadFunc succeed", func() {
				mock := newMockCache("load_func_succeed#1", time.Millisecond*1200, time.Second, true)
				mock.ehCache.DeleteFromRedis(mock.key)
				mock.ehCache.DeleteFromMem(mock.key)

				loadFunc := func() (interface{}, error) {
					time.Sleep(mock.delay)
					return mock.val, nil
				}

				ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
				defer cancel()

				var v TestStruct
				err := mock.ehCache.GetObject(ctx, mock.key, &v, time.Second*3, loadFunc)
				Ω(err).ToNot(HaveOccurred())
				Ω(&v).To(Equal(mock.val))

				// make sure redis pub finished
				time.Sleep(time.Millisecond * 10)

				metricList := []string{cache.MetricTypeDeleteRedis, cache.MetricTypeDeleteMem, cache.MetricTypeGetMemMiss, cache.MetricTypeGetRedisMiss, cache.MetricTypeSetRedis,
					cache.MetricTypeLoad, cache.MetricTypeGetCache, cache.MetricTypeSetMem}

				for idx, metricType := range metricList {
					select {
					case mc := <-mock.metricChan:
						Ω(mc.Key).To(Equal(mock.key))
						Ω(mc.Type).To(Equal(metricType))
						if mc.Type == cache.MetricTypeLoad || (mc.Type == cache.MetricTypeGetCache && idx == 6) {
							// the first get_cache should be same as delay
							Ω(math.Abs(float64(mc.ElapsedTime-mock.delay)) < float64(time.Millisecond*10)).To(Equal(true))
						} else {
							Ω(math.Abs(float64(mc.ElapsedTime)) < float64(time.Millisecond*10)).To(Equal(true))
						}
					default:
					}
				}
			})

			It("loadFunc error", func() {
				mock := newMockCache("load_func_error#1", time.Millisecond*1200, time.Second, true)
				mock.ehCache.DeleteFromRedis(mock.key)
				mock.ehCache.DeleteFromMem(mock.key)

				unkownErr := errors.New("unknown error")
				loadFunc := func() (interface{}, error) {
					return nil, unkownErr
				}

				ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
				defer cancel()

				var v TestStruct
				err := mock.ehCache.GetObject(ctx, mock.key, &v, time.Second*3, loadFunc)
				Ω(err).To(Equal(unkownErr))

				metricList := []string{cache.MetricTypeDeleteRedis, cache.MetricTypeDeleteMem, cache.MetricTypeGetMemMiss, cache.MetricTypeGetRedisMiss}
				for _, metricType := range metricList {
					select {
					case mc := <-mock.metricChan:
						Ω(mc.Key).To(Equal(mock.key))
						Ω(mc.Type).To(Equal(metricType))
						Ω(math.Abs(float64(mc.ElapsedTime)) < float64(time.Millisecond*10)).To(Equal(true))
					default:
					}
				}
			})

			It("loadFunc panic string", func() {
				mock := newMockCache("load_func_panic_string#1", time.Millisecond*1200, time.Second, true)
				mock.ehCache.DeleteFromRedis(mock.key)
				mock.ehCache.DeleteFromMem(mock.key)

				panicMsg := "panic string"
				loadFunc := func() (interface{}, error) {
					panic(panicMsg)
					return mock.val, nil
				}

				ctx, cancel := context.WithTimeout(context.Background(), time.Second*2)
				defer cancel()

				var v TestStruct
				err := mock.ehCache.GetObject(ctx, mock.key, &v, time.Second*3, loadFunc)
				Ω(err.Error()).To(Equal(panicMsg))

				metricList := []string{cache.MetricTypeDeleteRedis, cache.MetricTypeDeleteMem, cache.MetricTypeGetMemMiss, cache.MetricTypeGetRedisMiss}
				for _, metricType := range metricList {
					select {
					case mc := <-mock.metricChan:
						Ω(mc.Key).To(Equal(mock.key))
						Ω(mc.Type).To(Equal(metricType))
						Ω(math.Abs(float64(mc.ElapsedTime)) < float64(time.Millisecond*10)).To(Equal(true))
					default:
					}
				}
			})

			It("loadFunc panic error", func() {
				mock := newMockCache("load_func_panic_error#1", time.Millisecond*1200, time.Second, true)
				mock.ehCache.DeleteFromRedis(mock.key)
				mock.ehCache.DeleteFromMem(mock.key)

				panicErr := errors.New("panic error")
				loadFunc := func() (interface{}, error) {
					panic(panicErr)
					return mock.val, nil
				}

				ctx, cancel := context.WithTimeout(context.Background(), time.Second*2)
				defer cancel()

				var v TestStruct
				err := mock.ehCache.GetObject(ctx, mock.key, &v, time.Second*3, loadFunc)
				Ω(errors.Is(err, panicErr)).To(Equal(true))

				metricList := []string{cache.MetricTypeDeleteRedis, cache.MetricTypeDeleteMem, cache.MetricTypeGetMemMiss, cache.MetricTypeGetRedisMiss}
				for _, metricType := range metricList {
					select {
					case mc := <-mock.metricChan:
						Ω(mc.Key).To(Equal(mock.key))
						Ω(mc.Type).To(Equal(metricType))
						Ω(math.Abs(float64(mc.ElapsedTime)) < float64(time.Millisecond*10)).To(Equal(true))
					default:
					}
				}
			})

			It("loadFunc timeout", func() {
				mock := newMockCache("load_func_panic_timeout#1", time.Millisecond*1200, time.Second, true)
				mock.ehCache.DeleteFromRedis(mock.key)
				mock.ehCache.DeleteFromMem(mock.key)

				loadFunc := func() (interface{}, error) {
					time.Sleep(mock.delay)
					return mock.val, nil
				}

				ctx, cancel := context.WithTimeout(context.Background(), time.Second*1)
				defer cancel()

				var v TestStruct
				err := mock.ehCache.GetObject(ctx, mock.key, &v, time.Second*3, loadFunc)
				Ω(err).To(MatchError(context.DeadlineExceeded))

				// must wait goroutine to exit successfully, otherwise the other test will receive the redis-pub notification
				time.Sleep(time.Millisecond * 300)

				metricList := []string{cache.MetricTypeDeleteRedis, cache.MetricTypeDeleteMem, cache.MetricTypeGetMemMiss, cache.MetricTypeGetRedisMiss}
				for _, metricType := range metricList {
					select {
					case mc := <-mock.metricChan:
						Ω(mc.Key).To(Equal(mock.key))
						Ω(mc.Type).To(Equal(metricType))
						Ω(math.Abs(float64(mc.ElapsedTime)) < float64(time.Millisecond*10)).To(Equal(true))
					default:
					}
				}
			})
		})

		Context("Test redis hit", func() {
			It("redis hit ok", func() {
				mock := newMockCache("redis_hit_ok#1", time.Millisecond*1200, time.Second, true)
				mock.ehCache.DeleteFromRedis(mock.key)
				mock.ehCache.DeleteFromMem(mock.key)

				loadFunc := func() (interface{}, error) {
					time.Sleep(mock.delay)
					return mock.val, nil
				}

				ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
				defer cancel()

				var v TestStruct
				err := mock.ehCache.GetObject(ctx, mock.key, &v, time.Second*1, loadFunc)
				Ω(err).ToNot(HaveOccurred())
				Ω(&v).To(Equal(mock.val))

				// make sure redis pub finished, mem get updated
				time.Sleep(time.Millisecond * 10)
				mock.ehCache.DeleteFromMem(mock.key)

				ctx, cancel = context.WithTimeout(context.Background(), time.Second*5)
				defer cancel()
				err = mock.ehCache.GetObject(ctx, mock.key, &v, time.Second*1, loadFunc)
				Ω(err).ToNot(HaveOccurred())
				Ω(&v).To(Equal(mock.val))

				// make sure redis pub finished, mem get updated
				time.Sleep(time.Millisecond * 10)

				metricList := []string{cache.MetricTypeDeleteRedis, cache.MetricTypeDeleteMem, cache.MetricTypeGetMemMiss, cache.MetricTypeGetRedisMiss,
					cache.MetricTypeSetRedis, cache.MetricTypeLoad, cache.MetricTypeGetCache, cache.MetricTypeSetMem, cache.MetricTypeDeleteMem, cache.MetricTypeGetMemMiss,
					cache.MetricTypeGetRedisHit, cache.MetricTypeSetMem, cache.MetricTypeGetCache,
				}

				for idx, metricType := range metricList {
					select {
					case mc := <-mock.metricChan:
						Ω(mc.Key).To(Equal(mock.key))
						Ω(mc.Type).To(Equal(metricType))
						if mc.Type == cache.MetricTypeLoad || (mc.Type == cache.MetricTypeGetCache && idx == 6) {
							// the first get_cache should be same as delay
							Ω(math.Abs(float64(mc.ElapsedTime-mock.delay)) < float64(time.Millisecond*10)).To(Equal(true))
						} else {
							Ω(math.Abs(float64(mc.ElapsedTime)) < float64(time.Millisecond*10)).To(Equal(true))
						}
					default:
					}
				}
			})
		})

		It("redis hit expired", func() {
			mock := newMockCache("redis_hit_expired#1", time.Millisecond*1200, time.Second, true)
			mock.ehCache.DeleteFromRedis(mock.key)
			mock.ehCache.DeleteFromMem(mock.key)

			loadFunc := func() (interface{}, error) {
				time.Sleep(mock.delay)
				return mock.val, nil
			}

			ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
			defer cancel()

			var v TestStruct
			err := mock.ehCache.GetObject(ctx, mock.key, &v, time.Second*1, loadFunc)
			Ω(err).ToNot(HaveOccurred())
			Ω(&v).To(Equal(mock.val))

			// wait redis expired
			time.Sleep(time.Millisecond * 1010)
			mock.ehCache.DeleteFromMem(mock.key)

			ctx, cancel = context.WithTimeout(context.Background(), time.Second*5)
			defer cancel()
			err = mock.ehCache.GetObject(ctx, mock.key, &v, time.Second*1, loadFunc)
			Ω(err).ToNot(HaveOccurred())
			Ω(&v).To(Equal(mock.val))

			// make sure redis pub finished, mem get updated
			time.Sleep(mock.delay + time.Millisecond*10)

			metricList := []string{cache.MetricTypeDeleteRedis, cache.MetricTypeDeleteMem, cache.MetricTypeGetMemMiss, cache.MetricTypeGetRedisMiss,
				cache.MetricTypeSetRedis, cache.MetricTypeLoad, cache.MetricTypeGetCache, cache.MetricTypeSetMem, cache.MetricTypeDeleteMem, cache.MetricTypeGetMemMiss,
				cache.MetricTypeGetRedisExpired, cache.MetricTypeGetCache, cache.MetricTypeSetRedis, cache.MetricTypeLoad, cache.MetricTypeAsyncLoad, cache.MetricTypeSetMem,
			}

			for idx, metricType := range metricList {
				select {
				case mc := <-mock.metricChan:
					Ω(mc.Key).To(Equal(mock.key))
					Ω(mc.Type).To(Equal(metricType))
					if mc.Type == cache.MetricTypeLoad || mc.Type == cache.MetricTypeAsyncLoad || (mc.Type == cache.MetricTypeGetCache && idx == 6) {
						// the first get_cache should be same as delay
						Ω(math.Abs(float64(mc.ElapsedTime-mock.delay)) < float64(time.Millisecond*10)).To(Equal(true))
					} else {
						Ω(math.Abs(float64(mc.ElapsedTime)) < float64(time.Millisecond*10)).To(Equal(true))
					}
				default:
				}
			}
		})

		Context("Test mem hit", func() {
			It("mem hit ok", func() {
				mock := newMockCache("mem_hit_ok#1", time.Millisecond*1200, time.Second*1, true)
				mock.ehCache.DeleteFromRedis(mock.key)
				mock.ehCache.DeleteFromMem(mock.key)

				loadFunc := func() (interface{}, error) {
					time.Sleep(mock.delay)
					return mock.val, nil
				}

				ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
				defer cancel()

				var v TestStruct
				err := mock.ehCache.GetObject(ctx, mock.key, &v, time.Second*3, loadFunc)
				Ω(err).ToNot(HaveOccurred())
				Ω(&v).To(Equal(mock.val))

				// make sure redis pub finished
				time.Sleep(time.Millisecond * 10)

				ctx, cancel = context.WithTimeout(context.Background(), time.Second*5)
				defer cancel()
				err = mock.ehCache.GetObject(ctx, mock.key, &v, time.Second*1, loadFunc)
				Ω(err).ToNot(HaveOccurred())
				Ω(&v).To(Equal(mock.val))

				metricList := []string{cache.MetricTypeDeleteRedis, cache.MetricTypeDeleteMem, cache.MetricTypeGetMemMiss, cache.MetricTypeGetRedisMiss,
					cache.MetricTypeSetRedis, cache.MetricTypeLoad, cache.MetricTypeGetCache, cache.MetricTypeSetMem, cache.MetricTypeGetMemHit, cache.MetricTypeGetCache,
				}

				for idx, metricType := range metricList {
					select {
					case mc := <-mock.metricChan:
						Ω(mc.Key).To(Equal(mock.key))
						Ω(mc.Type).To(Equal(metricType))
						if mc.Type == cache.MetricTypeLoad || (mc.Type == cache.MetricTypeGetCache && idx == 6) {
							// the first get_cache should be same as delay
							Ω(math.Abs(float64(mc.ElapsedTime-mock.delay)) < float64(time.Millisecond*10)).To(Equal(true))
						} else {
							Ω(math.Abs(float64(mc.ElapsedTime)) < float64(time.Millisecond*10)).To(Equal(true))
						}
					default:
					}
				}
			})

			It("mem hit expired", func() {
				mock := newMockCache("mem_hit_expired#1", time.Millisecond*1200, time.Second*5, true)
				mock.ehCache.DeleteFromRedis(mock.key)
				mock.ehCache.DeleteFromMem(mock.key)

				loadFunc := func() (interface{}, error) {
					time.Sleep(mock.delay)
					return mock.val, nil
				}

				ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
				defer cancel()

				var v TestStruct
				err := mock.ehCache.GetObject(ctx, mock.key, &v, time.Second*1, loadFunc)
				Ω(err).ToNot(HaveOccurred())
				Ω(&v).To(Equal(mock.val))

				// wait 1100 ms to expire mem
				time.Sleep(time.Millisecond * 1100)

				ctx, cancel = context.WithTimeout(context.Background(), time.Second*5)
				defer cancel()
				err = mock.ehCache.GetObject(ctx, mock.key, &v, time.Second*2, loadFunc)
				Ω(err).ToNot(HaveOccurred())
				Ω(&v).To(Equal(mock.val))

				// wait last async load finish
				time.Sleep(mock.delay + time.Millisecond*10)

				metricList := []string{cache.MetricTypeDeleteRedis, cache.MetricTypeDeleteMem, cache.MetricTypeGetMemMiss, cache.MetricTypeGetRedisMiss,
					cache.MetricTypeSetRedis, cache.MetricTypeLoad, cache.MetricTypeGetCache, cache.MetricTypeSetMem, cache.MetricTypeGetMemExpired, cache.MetricTypeGetCache,
					cache.MetricTypeSetRedis, cache.MetricTypeLoad, cache.MetricTypeAsyncLoad, cache.MetricTypeSetMem,
				}

				for idx, metricType := range metricList {
					select {
					case mc := <-mock.metricChan:
						Ω(mc.Key).To(Equal(mock.key))
						Ω(mc.Type).To(Equal(metricType))
						if mc.Type == cache.MetricTypeLoad || mc.Type == cache.MetricTypeAsyncLoad || (mc.Type == cache.MetricTypeGetCache && idx == 6) {
							// the first get_cache should be same as delay
							Ω(math.Abs(float64(mc.ElapsedTime-mock.delay)) < float64(time.Millisecond*10)).To(Equal(true))
						} else {
							Ω(math.Abs(float64(mc.ElapsedTime)) < float64(time.Millisecond*10)).To(Equal(true))
						}
					default:
					}
				}
			})
		})

		Context("Test delete", func() {
			It("delete ok", func() {
				mock := newMockCache("delete_ok#1", time.Millisecond*1200, time.Second*1, true)
				mock.ehCache.DeleteFromRedis(mock.key)
				mock.ehCache.DeleteFromMem(mock.key)
				mock.ehCache.Delete(mock.key)

				// wait redis-pub received
				time.Sleep(time.Millisecond * 10)

				metricList := []string{cache.MetricTypeDeleteRedis, cache.MetricTypeDeleteMem, cache.MetricTypeDeleteRedis, cache.MetricTypeDeleteCache, cache.MetricTypeDeleteMem}

				for _, metricType := range metricList {
					select {
					case mc := <-mock.metricChan:
						Ω(mc.Key).To(Equal(mock.key))
						Ω(mc.Type).To(Equal(metricType))
						Ω(math.Abs(float64(mc.ElapsedTime)) < float64(time.Millisecond*10)).To(Equal(true))
					default:
					}
				}
			})
		})

		Context("Test setobject", func() {
			It("setobject ok", func() {
				mock := newMockCache("set_object_ok#1", time.Millisecond*1200, time.Second*1, true)
				mock.ehCache.DeleteFromRedis(mock.key)
				mock.ehCache.DeleteFromMem(mock.key)

				ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
				defer cancel()
				err := mock.ehCache.SetObject(ctx, mock.key, mock.val, time.Second)
				Ω(err).ToNot(HaveOccurred())

				// wait redis-pub received
				time.Sleep(time.Millisecond * 10)

				metricList := []string{cache.MetricTypeDeleteRedis, cache.MetricTypeDeleteMem, cache.MetricTypeSetRedis, cache.MetricTypeSetCache, cache.MetricTypeSetMem}

				for _, metricType := range metricList {
					select {
					case mc := <-mock.metricChan:
						Ω(mc.Key).To(Equal(mock.key))
						Ω(mc.Type).To(Equal(metricType))
						Ω(math.Abs(float64(mc.ElapsedTime)) < float64(time.Millisecond*10)).To(Equal(true))
					default:
					}
				}
			})
		})
	})
})
