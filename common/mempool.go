package common

import (
	"encoding/hex"
	"encoding/json"
	"sync"
	"sync/atomic"
	"time"

	"github.com/adityapk00/lightwalletd/walletrpc"
)

var (
	// List of all mempool transactions
	txns map[string]*walletrpc.RawTransaction = make(map[string]*walletrpc.RawTransaction)

	// List of all clients waiting to recieve mempool txns
	clients []chan<- *walletrpc.RawTransaction

	// Last height of the blocks. If this changes, then close all the clients and flush the mempool
	lastHeight int

	// A pointer to the blockcache
	blockcache *BlockCache

	// Mutex to lock the above 2 structs
	lock sync.RWMutex

	// Since the mutex doesn't have a "try_lock" method, we'll have to improvize with this
	refreshing int32 = 0
)

// AddNewClient adds a new client to the list of clients to notify for mempool txns
func AddNewClient(client chan<- *walletrpc.RawTransaction) {
	lock.Lock()
	clients = append(clients, client)
	println("Adding new client")

	// Also send all pending mempool txns
	for _, rtx := range txns {
		println("Sending tx to new client")
		client <- rtx
	}

	lock.Unlock()
}

// RefreshMempoolTxns gets all new mempool txns and sends any new ones to waiting clients
func refreshMempoolTxns() error {
	// First check if another refresh is running, if it is, just return
	if !atomic.CompareAndSwapInt32(&refreshing, 0, 1) {
		return nil
	}
	defer atomic.CompareAndSwapInt32(&refreshing, 1, 0)

	// Check if the blockchain has changed, and if it has, then clear everything
	lock.Lock()
	if lastHeight < blockcache.GetLatestHeight() {
		println("Block height changed, clearing everything")
		// Flush all the clients
		for _, client := range clients {
			client <- nil
		}

		clients = make([]chan<- *walletrpc.RawTransaction, 0)

		// Clear txns
		txns = make(map[string]*walletrpc.RawTransaction)

		lastHeight = blockcache.GetLatestHeight()
	}
	lock.Unlock()

	var mempoolList []string
	params := make([]json.RawMessage, 0)
	result, rpcErr := RawRequest("getrawmempool", params)
	if rpcErr != nil {
		return rpcErr
	}
	err := json.Unmarshal(result, &mempoolList)
	if err != nil {
		return err
	}

	newTxns := make(map[string]*walletrpc.RawTransaction)

	lock.RLock()
	// Fetch all new mempool txns and add them into `newTxns`
	for _, txidstr := range mempoolList {

		if _, ok := txns[txidstr]; !ok {
			txidJSON, err := json.Marshal(txidstr)
			if err != nil {
				return err
			}
			// The "0" is because we only need the raw hex, which is returned as
			// just a hex string, and not even a json string (with quotes).
			params := []json.RawMessage{txidJSON, json.RawMessage("0")}
			result, rpcErr := RawRequest("getrawtransaction", params)
			if rpcErr != nil {
				// Not an error; mempool transactions can disappear
				continue
			}
			// strip the quotes
			var txStr string
			err = json.Unmarshal(result, &txStr)
			if err != nil {
				return err
			}

			// conver to binary
			txBytes, err := hex.DecodeString(txStr)
			if err != nil {
				return err
			}

			newTxns[txidstr] = &walletrpc.RawTransaction{
				Data:   txBytes,
				Height: 0,
			}
		}
	}
	lock.RUnlock()

	if len(newTxns) == 0 {
		return nil
	}

	// Add new txns into the `txns` struct, and notify all clients about them
	lock.Lock()
	for txidstr, rawtx := range newTxns {
		txns[txidstr] = rawtx

		println("Sending txid:", txidstr)

		// Notify waiting clients
		for _, client := range clients {
			client <- rawtx
		}
	}
	lock.Unlock()

	return nil
}

// StartMempoolMonitor starts monitoring the mempool
func StartMempoolMonitor(cache *BlockCache, done <-chan bool) {
	go func() {
		ticker := time.NewTicker(2 * time.Second)
		blockcache = cache
		lastHeight = blockcache.GetLatestHeight()

		for {
			select {
			case <-ticker.C:
				err := refreshMempoolTxns()
				if err != nil {
					println("Refresh error:", err.Error())
				}

			case <-done:
				return
			}
		}
	}()
}
