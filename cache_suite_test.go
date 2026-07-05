package cache_test

import (
	"testing"

	"github.com/alicebob/miniredis/v2"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var cacheSuiteRedis *miniredis.Miniredis

func testRedisAddr() string {
	if cacheSuiteRedis == nil {
		return "127.0.0.1:7379"
	}
	return cacheSuiteRedis.Addr()
}

var _ = BeforeSuite(func() {
	var err error
	cacheSuiteRedis, err = miniredis.Run()
	Expect(err).ToNot(HaveOccurred())
})

var _ = AfterSuite(func() {
	if cacheSuiteRedis != nil {
		cacheSuiteRedis.Close()
	}
})

func TestCache(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Cache Suite")
}
