package newznab

import (
	"crypto/sha256"
	"encoding/json"
	"sync"
	"time"

	"github.com/braedonsaunders/ether/internal/config"
	"github.com/braedonsaunders/ether/internal/core"
)

const (
	maxTorrentDescriptors = 2048
	torrentDescriptorTTL  = time.Hour
)

type torrentDescriptor struct {
	hash, magnet string
	expires      time.Time
}

type torrentDescriptorCache struct {
	mu      sync.Mutex
	entries map[[32]byte]torrentDescriptor
}

// Include the configuration and download identity, so replacing an indexer or
// its credentials cannot reuse an old descriptor. The cache stores no release
// targets, ranking, viewer information or negative availability decisions.
func torrentDescriptorKey(cfg config.Indexer, r core.Release) [32]byte {
	b, _ := json.Marshal(struct {
		Config        config.Indexer
		ID, GUID, URL string
	}{cfg, r.ID, r.GUID, r.NZBURL})
	return sha256.Sum256(b)
}

func (c *torrentDescriptorCache) get(key [32]byte, now time.Time) (string, string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if ok && !now.Before(e.expires) {
		delete(c.entries, key)
		ok = false
	}
	return e.hash, e.magnet, ok
}

func (c *torrentDescriptorCache) put(key [32]byte, hash, magnet string, now time.Time) {
	// Trackers and display names come from external torrent documents. Do
	// not let one oversized magnet defeat the cache's memory bound.
	if len(magnet) > 16<<10 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = make(map[[32]byte]torrentDescriptor)
	}
	if len(c.entries) >= maxTorrentDescriptors {
		var oldest [32]byte
		var expiry time.Time
		for k, e := range c.entries {
			if !now.Before(e.expires) {
				delete(c.entries, k)
			}
			if expiry.IsZero() || e.expires.Before(expiry) {
				oldest, expiry = k, e.expires
			}
		}
		if len(c.entries) >= maxTorrentDescriptors {
			delete(c.entries, oldest)
		}
	}
	c.entries[key] = torrentDescriptor{hash: hash, magnet: magnet, expires: now.Add(torrentDescriptorTTL)}
}
