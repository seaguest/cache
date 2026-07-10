package cache

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"strings"
)

func storageKey(c *Client, route *route, versions versions) string {
	var fixed [48 + 8*32]byte
	material := fixed[:48+8*len(versions.tags)]
	copy(material, route.digest[:])
	binary.BigEndian.PutUint64(material[32:40], uint64(versions.namespace))
	binary.BigEndian.PutUint64(material[40:48], uint64(versions.key))
	for i, version := range versions.tags {
		binary.BigEndian.PutUint64(material[48+i*8:], uint64(version))
	}
	sum := sha256.Sum256(material)
	var encoded [64]byte
	hex.Encode(encoded[:], sum[:])
	var result strings.Builder
	result.Grow(len(c.dataPrefix) + len(encoded))
	result.WriteString(c.dataPrefix)
	result.Write(encoded[:])
	return result.String()
}
