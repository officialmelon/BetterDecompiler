package engine

import (
	"container/list"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

// Cache is a two-level result cache: an in-memory LRU backed by an optional
// on-disk store, so repeated decompiles of the same script are instant and
// cost nothing, even across restarts.
type Cache struct {
	mu    sync.Mutex
	max   int
	ll    *list.List
	items map[string]*list.Element
	dir   string
}

type cacheEntry struct {
	key string
	res *Result
}

// NewCache creates a cache holding up to max results in memory. When dir is
// non-empty, results are also persisted there.
func NewCache(max int, dir string) *Cache {
	if max <= 0 {
		max = 256
	}
	return &Cache{max: max, ll: list.New(), items: map[string]*list.Element{}, dir: dir}
}

func (c *Cache) path(key string) string { return filepath.Join(c.dir, key[:2], key+".json") }

// Get returns a cached result.
func (c *Cache) Get(key string) (*Result, bool) {
	if c == nil {
		return nil, false
	}
	c.mu.Lock()
	if el, ok := c.items[key]; ok {
		c.ll.MoveToFront(el)
		r := el.Value.(*cacheEntry).res
		c.mu.Unlock()
		return r, true
	}
	c.mu.Unlock()
	if c.dir == "" {
		return nil, false
	}
	data, err := os.ReadFile(c.path(key))
	if err != nil {
		return nil, false
	}
	var r Result
	if json.Unmarshal(data, &r) != nil || r.Source == "" {
		return nil, false
	}
	c.putMem(key, &r)
	return &r, true
}

// Put stores a result.
func (c *Cache) Put(key string, r *Result) {
	if c == nil {
		return
	}
	c.putMem(key, r)
	if c.dir == "" {
		return
	}
	data, err := json.Marshal(r)
	if err != nil {
		return
	}
	p := c.path(key)
	if os.MkdirAll(filepath.Dir(p), 0o700) != nil {
		return
	}
	tmp, err := os.CreateTemp(filepath.Dir(p), ".tmp-*")
	if err != nil {
		return
	}
	_, werr := tmp.Write(data)
	cerr := tmp.Close()
	if werr != nil || cerr != nil || os.Rename(tmp.Name(), p) != nil {
		os.Remove(tmp.Name())
	}
}

func (c *Cache) putMem(key string, r *Result) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.items[key]; ok {
		el.Value.(*cacheEntry).res = r
		c.ll.MoveToFront(el)
		return
	}
	c.items[key] = c.ll.PushFront(&cacheEntry{key, r})
	for c.ll.Len() > c.max {
		old := c.ll.Back()
		c.ll.Remove(old)
		delete(c.items, old.Value.(*cacheEntry).key)
	}
}

// Len returns the number of in-memory entries.
func (c *Cache) Len() int {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ll.Len()
}
