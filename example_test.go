package cache_test

import (
	"context"
	"fmt"
	"time"

	cache "github.com/seaguest/cache"
)

func ExampleGet() {
	client, _ := cache.New(cache.Config{
		Namespace: "example",
		Store:     cache.NewMemoryStore(),
	})
	defer client.Close()

	profile, _ := cache.Get(
		context.Background(),
		client,
		cache.MakeKey("UserService.Profile", int64(42)),
		5*time.Minute,
		func(context.Context) (string, error) { return "Ada", nil },
		cache.WithTags(cache.MakeTag("user", int64(42))),
	)

	fmt.Println(profile)
	// Output: Ada
}

func ExampleGetMany() {
	client, _ := cache.New(cache.Config{
		Namespace: "batch-example",
		Store:     cache.NewMemoryStore(),
	})
	defer client.Close()

	result, _ := cache.GetMany(
		context.Background(),
		client,
		[]cache.Item[int]{
			{ID: 1, Key: cache.MakeKey("UserService.Profile", 1)},
			{ID: 2, Key: cache.MakeKey("UserService.Profile", 2)},
		},
		5*time.Minute,
		func(_ context.Context, ids []int) (map[int]string, error) {
			return map[int]string{ids[0]: "Ada", ids[1]: "Lin"}, nil
		},
	)

	fmt.Println(result.Values[1], result.Values[2])
	// Output: Ada Lin
}
