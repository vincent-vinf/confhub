package syncer

import (
	"container/list"
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"gitlab.bodesitech.com/bodesi/confhub/internal/config"
	"golang.org/x/sync/singleflight"
)

type cacheEntry struct {
	key   config.Key
	state *config.State
	bytes int64
}
type stateCache struct {
	mu           sync.Mutex
	budget, used int64
	epoch        uint64
	entries      map[config.Key]*list.Element
	lru          *list.List
	loads        singleflight.Group
}

func newStateCache(budget int64) *stateCache {
	return &stateCache{budget: budget, entries: map[config.Key]*list.Element{}, lru: list.New()}
}
func (c *stateCache) clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.epoch++
	clear(c.entries)
	c.lru.Init()
	c.used = 0
}
func (c *stateCache) invalidate(k config.Key) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.epoch++
	if e := c.entries[k]; e != nil {
		c.used -= e.Value.(cacheEntry).bytes
		c.lru.Remove(e)
		delete(c.entries, k)
	}
}
func (c *stateCache) load(ctx context.Context, k config.Key, source Source) (*config.State, int64, error) {
	c.mu.Lock()
	if e := c.entries[k]; e != nil {
		c.lru.MoveToFront(e)
		v := e.Value.(cacheEntry).state
		c.mu.Unlock()
		return v, v.Sequence, nil
	}
	epoch := c.epoch
	c.mu.Unlock()
	// An invalidation uses a fresh flight key, so it cannot reuse an older in-flight read.
	flightKey := fmt.Sprintf("%d:%q:%q:%q", epoch, k.Namespace, k.Group, k.Name)
	result := c.loads.DoChan(flightKey, func() (any, error) {
		state, seq, err := source.Current(ctx, k)
		if err == nil && state != nil {
			size := int64(512 + len(k.Namespace) + len(k.Group) + len(k.Name))
			raw, _ := json.Marshal(state.Rules)
			size += int64(len(raw)) * 2
			if state.Beta != nil {
				size += int64(len(state.Beta.Content) + len(state.Beta.Description) + 256)
			}
			for _, v := range state.Versions {
				size += int64(len(v.Content) + len(v.Description) + 256)
			}
			c.mu.Lock()
			if c.epoch == epoch && size <= c.budget {
				if old := c.entries[k]; old != nil {
					c.used -= old.Value.(cacheEntry).bytes
					c.lru.Remove(old)
				}
				c.entries[k] = c.lru.PushFront(cacheEntry{key: k, state: state, bytes: size})
				c.used += size
				for c.used > c.budget {
					e := c.lru.Back()
					entry := e.Value.(cacheEntry)
					c.used -= entry.bytes
					delete(c.entries, entry.key)
					c.lru.Remove(e)
				}
			}
			c.mu.Unlock()
		}
		return struct {
			state *config.State
			seq   int64
		}{state, seq}, err
	})
	select {
	case <-ctx.Done():
		return nil, 0, ctx.Err()
	case r := <-result:
		if r.Val == nil {
			return nil, 0, r.Err
		}
		v := r.Val.(struct {
			state *config.State
			seq   int64
		})
		return v.state, v.seq, r.Err
	}
}
