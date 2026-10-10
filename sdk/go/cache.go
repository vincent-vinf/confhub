package confhub

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

func digest(raw []byte) string { value := sha256.Sum256(raw); return hex.EncodeToString(value[:]) }

func (c *Client) initCache(directory string) error {
	if directory == "" {
		return nil
	}
	addresses := append([]string(nil), c.addresses...)
	sort.Strings(addresses)
	raw, _ := json.Marshal(struct {
		Addresses []string
		Tags      string
	}{addresses, c.tags})
	c.cachePath = filepath.Join(directory, digest(raw))
	if err := os.MkdirAll(c.cachePath, 0700); err != nil {
		return fmt.Errorf("confhub: cannot create cache directory: %w", err)
	}
	return nil
}

func (c *Client) cacheFile(key Key) string {
	raw, _ := json.Marshal(key)
	return filepath.Join(c.cachePath, digest(raw)+".json")
}

func (c *Client) persist(value Snapshot) error {
	if c.cachePath == "" {
		return nil
	}
	path := c.cacheFile(value.Key)
	if value.Deleted {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(c.cachePath, ".snapshot-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err = file.Write(raw); err != nil {
		file.Close()
		return err
	}
	if err = file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}

// applyLocked orders HTTP and watch results by commit sequence, never by the
// effective version number. Tombstones retain the watermark but no content.
func (c *Client) applyLocked(value Snapshot) Snapshot {
	if previous, ok := c.records[value.Key]; ok {
		if value.Sequence < previous.Sequence || (value.Sequence == previous.Sequence && (value.Revision < previous.Revision || value.ID != previous.ID)) {
			return previous
		}
	}
	value.Source = Online
	if value.Deleted {
		value.Content = ""
		value.Format = ""
	}
	c.records[value.Key] = value
	if err := c.persist(value); err != nil {
		c.report(fmt.Errorf("confhub: cache persistence failed: %w", err))
	}
	return value
}

func (c *Client) accept(value Snapshot) Snapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.applyLocked(value)
}

func (c *Client) cached(key Key) (Snapshot, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return Snapshot{}, ErrClosed
	}
	if value, ok := c.records[key]; ok {
		if value.Deleted {
			return value, ErrNotFound
		}
		value.Source = Memory
		return value, nil
	}
	if c.cachePath != "" {
		raw, err := os.ReadFile(c.cacheFile(key))
		if err == nil && len(raw) <= 2<<20 {
			var value Snapshot
			if json.Unmarshal(raw, &value) == nil && value.valid(key) && !value.Deleted {
				value.Source = Disk
				return value, nil
			}
		}
	}
	return Snapshot{}, ErrUnavailable
}
