package server

import (
	"fmt"
	"testing"
)

func TestUKICacheEvictsLeastRecentlyUsed(t *testing.T) {
	ukiCache.Lock()
	saved, savedTick := ukiCache.m, ukiCache.tick
	ukiCache.m, ukiCache.tick = map[string]*cachedUKIEntry{}, 0
	for i := range maxCachedUKIs {
		ukiCache.tick++
		ukiCache.m[fmt.Sprint(i)] = &cachedUKIEntry{lastUse: ukiCache.tick}
	}
	// Entry 0 is the oldest insert but was just used; 1 is now the LRU.
	ukiCache.tick++
	ukiCache.m["0"].lastUse = ukiCache.tick
	evictLRUUKI()
	_, has0 := ukiCache.m["0"]
	_, has1 := ukiCache.m["1"]
	n := len(ukiCache.m)
	ukiCache.m, ukiCache.tick = saved, savedTick
	ukiCache.Unlock()
	if !has0 || has1 || n != maxCachedUKIs-1 {
		t.Fatalf("want only the LRU entry evicted: has0=%v has1=%v len=%d", has0, has1, n)
	}
}
