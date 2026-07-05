package tiercache

import "errors"

var (
	ErrNotFound     = errors.New("tiercache: not found")
	ErrNoClient     = errors.New("tiercache: nil client")
	ErrInvalidKey   = errors.New("tiercache: invalid key")
	ErrInvalidTag   = errors.New("tiercache: invalid tag")
	ErrInvalidTTL   = errors.New("tiercache: invalid ttl")
	ErrNoStore      = errors.New("tiercache: nil store")
	ErrNoLockStore  = errors.New("tiercache: store does not support distributed locking")
	ErrNoLoader     = errors.New("tiercache: nil loader")
	ErrNoItems      = errors.New("tiercache: no items")
	ErrDuplicateKey = errors.New("tiercache: duplicate key")
	ErrClosed       = errors.New("tiercache: closed")
	ErrNoValue      = errors.New("tiercache: loader returned nil value")
	ErrInvalidName  = errors.New("tiercache: invalid name")
)
