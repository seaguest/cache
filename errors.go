package cache

import (
	"errors"
	"fmt"
	"sort"
)

var (
	ErrNotFound         = errors.New("cache: not found")
	ErrNoClient         = errors.New("cache: nil client")
	ErrInvalidKey       = errors.New("cache: invalid key")
	ErrInvalidTag       = errors.New("cache: invalid tag")
	ErrInvalidTTL       = errors.New("cache: invalid ttl")
	ErrInvalidConfig    = errors.New("cache: invalid config")
	ErrInvalidNamespace = errors.New("cache: invalid namespace")
	ErrInvalidCodec     = errors.New("cache: invalid codec")
	ErrNoStore          = errors.New("cache: nil store")
	ErrNoLockStore      = errors.New("cache: store does not support distributed locking")
	ErrNoLoader         = errors.New("cache: nil loader")
	ErrNoItems          = errors.New("cache: no items")
	ErrDuplicateKey     = errors.New("cache: duplicate key")
	ErrDuplicateID      = errors.New("cache: duplicate batch id")
	ErrClosed           = errors.New("cache: closed")
	ErrNoValue          = errors.New("cache: loader returned nil value")
	ErrTypeMismatch     = errors.New("cache: internal value type mismatch")
)

// ItemError associates a batch error with one business ID.
type ItemError[ID comparable] struct {
	ID  ID
	Err error
}

func (e ItemError[ID]) Error() string { return fmt.Sprintf("%v: %v", e.ID, e.Err) }
func (e ItemError[ID]) Unwrap() error { return e.Err }

func joinItemErrors[ID comparable](byID map[ID]error, order []ID) error {
	if len(byID) == 0 {
		return nil
	}
	if len(order) > 0 {
		errs := make([]error, 0, len(byID))
		for _, id := range order {
			if err, ok := byID[id]; ok {
				errs = append(errs, ItemError[ID]{ID: id, Err: err})
			}
		}
		return errors.Join(errs...)
	}
	type item struct {
		text string
		id   ID
	}
	items := make([]item, 0, len(byID))
	for id := range byID {
		items = append(items, item{text: fmt.Sprintf("%T:%#v", id, id), id: id})
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].text < items[j].text })
	errs := make([]error, 0, len(items))
	for _, item := range items {
		errs = append(errs, ItemError[ID]{ID: item.id, Err: byID[item.id]})
	}
	return errors.Join(errs...)
}
