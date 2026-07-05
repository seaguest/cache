package tiercache

import (
	"strconv"
	"strings"
)

func (c *Client) storageKey(key Key, tags []Tag, v versions, codec Codec) string {
	var b strings.Builder
	b.WriteString("ns=")
	b.WriteString(c.namespace)
	b.WriteString("|nv=")
	b.WriteString(strconv.FormatInt(v.Namespace, 10))
	b.WriteString("|key=")
	b.WriteString(string(key))
	b.WriteString("|kv=")
	b.WriteString(strconv.FormatInt(v.Key, 10))
	b.WriteString("|codec=")
	b.WriteString(codec.Name())
	for _, tag := range tags {
		b.WriteString("|tag=")
		b.WriteString(string(tag))
		b.WriteString("@")
		b.WriteString(strconv.FormatInt(v.Tags[tag], 10))
	}
	return c.namespace + ":d:" + hashHex([]byte(b.String()))
}

func tagVersionMap(tags []Tag, v versions) map[string]int64 {
	if len(tags) == 0 {
		return nil
	}
	out := make(map[string]int64, len(tags))
	for _, tag := range tags {
		out[hashHex([]byte(tag))] = v.Tags[tag]
	}
	return out
}

func tagHashes(tags []Tag) []string {
	if len(tags) == 0 {
		return nil
	}
	out := make([]string, len(tags))
	for i, tag := range tags {
		out[i] = hashHex([]byte(tag))
	}
	return out
}
