package cache

import (
	"testing"
	"time"

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
	key := GetCacheKey("val", id)
	var v TestStruct
	err := GetCacheObject(key, &v, 60, func() (interface{}, error) {
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

func TestCache(t *testing.T) {
	Init("127.0.0.1:6379", "", true, 200)
	v, e := getStruct(100)
	logger.Error(v, e)
}
