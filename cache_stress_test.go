package cache_test

import (
	"context"
	"fmt"
	"log"
	"math/rand"
	"time"

	"github.com/gomodule/redigo/redis"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/seaguest/cache"
)

type ComplexStruct1 struct {
	ID           int
	StrField     string
	IntField     int
	FloatField   float64
	BoolField    bool
	SliceField   []string
	MapField     map[string]int
	NestedStruct struct {
		StringField string
		IntField    int
	}
}

type ComplexStruct2 struct {
	ID           int
	StrField     string
	IntField     int
	FloatField   float64
	BoolField    bool
	SliceField   []string
	MapField     map[string]int
	NestedStruct struct {
		StringField string
		IntField    int
	}
}

func (p *ComplexStruct1) DeepCopy() interface{} {
	c := *p
	if p.MapField != nil {
		c.MapField = make(map[string]int)
		for k, v := range p.MapField {
			c.MapField[k] = v
		}
	}
	return &c
}

func (p *ComplexStruct2) DeepCopy() interface{} {
	c := *p
	if p.MapField != nil {
		c.MapField = make(map[string]int)
		for k, v := range p.MapField {
			c.MapField[k] = v
		}
	}
	return &c
}

// Returns a random string of length 5
func getRandomString() string {
	const charset = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"
	b := make([]byte, 5)
	for i := range b {
		b[i] = charset[rand.Intn(len(charset))]
	}
	return string(b)
}

var _ = Describe("Cache", func() {
	Context("Cache", func() {
		var (
			pool    *redis.Pool
			ehCache cache.Cache
			cs1     ComplexStruct1
			cs2     ComplexStruct2
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

			cs1 = ComplexStruct1{
				StrField:   getRandomString(),
				IntField:   rand.Intn(100),
				FloatField: rand.Float64() * 10,
				BoolField:  rand.Intn(2) == 1,
				SliceField: []string{getRandomString(), getRandomString(), getRandomString()},
				MapField:   map[string]int{getRandomString(): rand.Intn(100), getRandomString(): rand.Intn(100)},
				NestedStruct: struct {
					StringField string
					IntField    int
				}{
					StringField: getRandomString(),
					IntField:    rand.Intn(100),
				},
			}

			cs2 = ComplexStruct2{
				StrField:   getRandomString(),
				IntField:   rand.Intn(100),
				FloatField: rand.Float64() * 10,
				BoolField:  rand.Intn(2) == 1,
				SliceField: []string{getRandomString(), getRandomString(), getRandomString()},
				MapField:   map[string]int{getRandomString(): rand.Intn(100), getRandomString(): rand.Intn(100)},
				NestedStruct: struct {
					StringField string
					IntField    int
				}{
					StringField: getRandomString(),
					IntField:    rand.Intn(100),
				},
			}
		})

		Context("stress test", func() {
			BeforeEach(func() {
				ehCache = cache.New(
					cache.GetConn(pool.Get),
					cache.CleanInterval(time.Second),
					cache.OnError(func(err error) {
						log.Printf("OnError:%+v", err)
					}),
				)
			})

			It("stress test", func() {
				for j := 0; j < 100; j++ {
					go func(id int) {
						for {
							ctx, cancel := context.WithTimeout(context.Background(), time.Second*2)
							defer cancel()

							cs := cs1
							cs.ID = id

							var v ComplexStruct1
							err := ehCache.GetObject(ctx, fmt.Sprintf("complex_struct_1#%d", id), &v, time.Second*3, func() (interface{}, error) {
								time.Sleep(time.Millisecond * 10)
								cs := cs1
								cs.ID = id
								return &cs, nil
							})
							if err != nil {
								log.Println(err)
							}

							Ω(err).ToNot(HaveOccurred())
							Ω(v).To(Equal(cs))
							time.Sleep(time.Millisecond * 10)
						}
					}(j)
				}

				for j := 0; j < 100; j++ {
					go func(id int) {
						for {
							ctx, cancel := context.WithTimeout(context.Background(), time.Second*2)
							defer cancel()

							cs := cs2
							cs.ID = id

							var v ComplexStruct2
							err := ehCache.GetObject(ctx, fmt.Sprintf("complex_struct_2#%d", id), &v, time.Second*3, func() (interface{}, error) {
								time.Sleep(time.Millisecond * 10)
								cs := cs2
								cs.ID = id
								return &cs, nil
							})
							if err != nil {
								log.Println(err)
							}

							Ω(err).ToNot(HaveOccurred())
							Ω(v).To(Equal(cs))
							time.Sleep(time.Millisecond * 10)
						}
					}(j)
				}

				time.Sleep(time.Second * 10)
			})
		})
	})
})
