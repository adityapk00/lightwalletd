package common

import (
	"bytes"
	"sync"

	"github.com/OleksandrBlack/safecoin-lightwalletd/walletrpc"
	"github.com/golang/protobuf/proto"
)

type BlockCacheEntry struct {
	data []byte
	hash []byte
}

type BlockCache struct {
	MaxEntries int

	FirstBlock int
	LastBlock  int

	m map[int]*BlockCacheEntry

	mutex sync.RWMutex
}

func NewBlockCache(maxEntries int) *BlockCache {
	return &BlockCache{
		MaxEntries: maxEntries,
		FirstBlock: -1,
		LastBlock:  -1,
		m:          make(map[int]*BlockCacheEntry),
	}
}

func (c *BlockCache) Add(height int, block *walletrpc.CompactBlock) (error, bool) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	//println("Cache add", height)
	if c.FirstBlock == -1 && c.LastBlock == -1 {
		// If this is the first block, prep the data structure
		c.FirstBlock = height
		c.LastBlock = height - 1
	}

	// If we're adding a block in the middle of the cache, remove all
	// blocks after it, since this might be a reorg, and we don't want
	// Any outdated blocks returned
	if height >= c.FirstBlock && height <= c.LastBlock {
		for i := height; i <= c.LastBlock; i++ {
			delete(c.m, i)
		}
		c.LastBlock = height - 1
	}

	// Don't allow out-of-order blocks. This is more of a sanity check than anything
	// If there is a reorg, then the ingestor needs to handle it.
	if c.m[height-1] != nil && !bytes.Equal(block.PrevHash, c.m[height-1].hash) {
		return nil, true
	}

	// Add the entry and update the counters
	data, err := proto.Marshal(block)
	if err != nil {
		println("Error marshalling block!")
		return err, false
	}

	c.m[height] = &BlockCacheEntry{
		data: data,
		hash: block.GetHash(),
	}

	c.LastBlock = height

	// If the cache is full, remove the oldest block
	if c.LastBlock-c.FirstBlock+1 > c.MaxEntries {
		//println("Deleteing at height", c.FirstBlock)
		delete(c.m, c.FirstBlock)
		c.FirstBlock = c.FirstBlock + 1
	}

	//println("Cache size is ", len(c.m))
	return nil, false
}

func (c *BlockCache) Get(height int) *walletrpc.CompactBlock {
	c.mutex.RLock()
	defer c.mutex.RUnlock()

	//println("Cache get", height)
	if c.LastBlock == -1 || c.FirstBlock == -1 {
		return nil
	}

	if height < c.FirstBlock || height > c.LastBlock {
		//println("Cache miss: index out of range")
		return nil
	}

	//println("Cache returned")
	serialized := &walletrpc.CompactBlock{}
	err := proto.Unmarshal(c.m[height].data, serialized)
	if err != nil {
		println("Error unmarshalling compact block")
		return nil
	}

	return serialized
}

func (c *BlockCache) GetLatestBlock() int {
	c.mutex.RLock()
	defer c.mutex.RUnlock()

	return c.LastBlock
}
