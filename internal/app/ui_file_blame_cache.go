package app

import (
	"container/list"

	"github.com/zhdsmy/branchline/internal/git"
)

// fileBlameCacheMaxFiles and fileBlameCacheMaxLines bound the in-memory blame
// cache: at most this many files and this many lines in total stay cached,
// and a single file longer than the line cap is never stored at all. They
// are package-level test seams, as the package's other such hooks are: tests
// shrink them to exercise eviction with small fixtures and restore the
// originals with their cleanup.
var fileBlameCacheMaxFiles = 16
var fileBlameCacheMaxLines = 200000

// fileBlameCacheKey identifies one cached blame result: the full commit hash
// the file was blamed at and the file's path. A blame of one file at one
// commit never changes, so nothing else — no branch, no time — belongs in
// the key.
type fileBlameCacheKey struct {
	commit string
	path   string
}

// fileBlameCacheValue is one cached entry: the finished attribution and its
// line count, kept separately because evicting compares counts without
// walking slices.
type fileBlameCacheValue struct {
	lines []git.BlameLine
}

// fileBlameCache is the app's in-memory cache of finished blame reads,
// keyed by commit hash and path. It lives in memory only: nothing of it is
// ever written to disk.
//
// Entries belong to one repository generation: the generation moving on — a
// repository opening or closing — empties the cache, because a commit hash
// of one repository means nothing in another. The check runs lazily on every
// lookup and store rather than on the generation change itself.
//
// The cache is least-recently-used: every lookup of an entry and every store
// count as a use, and once either cap is exceeded the entry unused the
// longest is dropped first.
//
// A cached slice is never written again after it is stored. applyFileBlameBatch
// is the only writer of the elements of a blameLines slice, and it runs only
// for batches of a streaming read — whose first batch always replaces the
// slice wholesale before any hunk touches it. When the inspector shows a
// cached result no read is running for it at all, so no batch arrives that
// could write into the slice, and a later read of the file starts over with
// its own fresh content slice. Aliasing the cached slice into the inspector
// state is therefore safe.
type fileBlameCache struct {
	gen     uint64
	entries map[fileBlameCacheKey]*list.Element
	order   *list.List
	lines   int
}

// fileBlameCacheEntry is the payload of one node of the cache's use order:
// the front of the list is the most recently used entry, the back the one
// evicted first.
type fileBlameCacheEntry struct {
	key   fileBlameCacheKey
	value fileBlameCacheValue
}

// fileBlameCache returns the blame result cache of this app instance. It is
// deliberately not part of the inspector state: closing the inspector resets
// that state wholesale, while the cache is exactly what must survive the
// close so the next open of the same file at the same commit reads no Git.
func (a *app) fileBlameCache() *fileBlameCache {
	return featureState(a, func() *fileBlameCache {
		return &fileBlameCache{}
	})
}

// prune rolls the cache onto the current repository generation, emptying it
// when the generation moved, and then evicts least-recently-used entries
// until both caps hold again. The entry at the front of the use order —
// the one just stored or just hit — is never evicted: an entry that fits by
// itself must survive its own store, however tight the caps.
func (c *fileBlameCache) prune(gen uint64) {
	if c.entries == nil {
		c.entries = map[fileBlameCacheKey]*list.Element{}
		c.order = list.New()
		c.gen = gen
		return
	}
	if c.gen != gen {
		clear(c.entries)
		c.order.Init()
		c.lines = 0
		c.gen = gen
		return
	}
	for c.order.Len() > fileBlameCacheMaxFiles || c.lines > fileBlameCacheMaxLines {
		back, front := c.order.Back(), c.order.Front()
		if back == nil || back == front {
			break
		}
		entry := back.Value.(fileBlameCacheEntry)
		delete(c.entries, entry.key)
		c.lines -= len(entry.value.lines)
		c.order.Remove(back)
	}
}

// get looks up the finished blame of path at commit, counting a hit as a use
// of the entry. The second result is false when nothing is cached — and a
// cached empty file counts as cached, so callers tell it apart by the bool,
// never by the slice.
func (c *fileBlameCache) get(commit, path string, gen uint64) ([]git.BlameLine, bool) {
	c.prune(gen)
	el, ok := c.entries[fileBlameCacheKey{commit: commit, path: path}]
	if !ok {
		return nil, false
	}
	c.order.MoveToFront(el)
	return el.Value.(fileBlameCacheEntry).value.lines, true
}

// put stores the finished blame of path at commit. A file whose line count
// exceeds the line cap is never stored — and evicts nothing, since it never
// enters — and storing counts as a use, so the fresh entry is the last one
// eviction would pick.
func (c *fileBlameCache) put(commit, path string, lines []git.BlameLine, gen uint64) {
	c.prune(gen)
	if lines == nil || len(lines) > fileBlameCacheMaxLines {
		return
	}
	key := fileBlameCacheKey{commit: commit, path: path}
	if el, ok := c.entries[key]; ok {
		// A completed read of the same commit and path cannot differ from the
		// entry already cached; keep the first slice and count the store as a
		// use. Subtracting nothing, the line total does not move.
		c.order.MoveToFront(el)
		return
	}
	c.entries[key] = c.order.PushFront(fileBlameCacheEntry{
		key:   key,
		value: fileBlameCacheValue{lines: lines},
	})
	c.lines += len(lines)
	c.prune(gen)
}
