package cache_test

import (
	"context"
	"fmt"
	"log"
	"math/rand"
	"sync"
	"time"

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

var _ = Describe("cache stress", func() {
	Context("Cache", func() {
		var (
			cs1 ComplexStruct1
			cs2 ComplexStruct2
		)

		BeforeEach(func() {
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
			It("stress test", func() {
				mock := newMockCache("stress_test#1", time.Millisecond*1200, time.Second, false, cache.GetPolicyReloadOnExpiry)
				bgCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer cancel()
				var wg sync.WaitGroup

				for j := 0; j < 100; j++ {
					wg.Add(1)
					go func(id int) {
						defer GinkgoRecover()
						defer wg.Done()
						for {
							if bgCtx.Err() != nil {
								return
							}
							ctx, cancel := context.WithTimeout(bgCtx, time.Second*2)
							cs := cs1
							cs.ID = id

							var v ComplexStruct1
							err := mock.ehCache.GetObject(ctx, fmt.Sprintf("complex_struct_1#%d", id), &v, time.Second*3, func() (interface{}, error) {
								time.Sleep(time.Millisecond * 10)
								cs := cs1
								cs.ID = id
								return &cs, nil
							})
							cancel()
							if err != nil {
								if bgCtx.Err() != nil {
									return
								}
								log.Println(err)
							}
							Ω(err).ToNot(HaveOccurred())
							Ω(v).To(Equal(cs))
							time.Sleep(time.Millisecond * 10)
						}
					}(j)
				}

				for j := 0; j < 100; j++ {
					wg.Add(1)
					go func(id int) {
						defer GinkgoRecover()
						defer wg.Done()
						for {
							if bgCtx.Err() != nil {
								return
							}
							ctx, cancel := context.WithTimeout(bgCtx, time.Second*2)
							cs := cs2
							cs.ID = id

							var v ComplexStruct2
							err := mock.ehCache.GetObject(ctx, fmt.Sprintf("complex_struct_2#%d", id), &v, time.Second*3, func() (interface{}, error) {
								time.Sleep(time.Millisecond * 10)
								cs := cs2
								cs.ID = id
								return &cs, nil
							})
							cancel()
							if err != nil {
								if bgCtx.Err() != nil {
									return
								}
								log.Println(err)
							}
							Ω(err).ToNot(HaveOccurred())
							Ω(v).To(Equal(cs))
							time.Sleep(time.Millisecond * 10)
						}
					}(j)
				}

				wg.Wait()
			})
		})
	})
})
