package common

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/OleksandrBlack/safecoin-lightwalletd/parser"
	"github.com/OleksandrBlack/safecoin-lightwalletd/walletrpc"
	"github.com/btcsuite/btcd/rpcclient"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
)

func GetSaplingInfo(rpcClient *rpcclient.Client) (int, int, string, string, error) {
	result, rpcErr := rpcClient.RawRequest("getblockchaininfo", make([]json.RawMessage, 0))

	var err error
	var errCode int64

	// For some reason, the error responses are not JSON
	if rpcErr != nil {
		errParts := strings.SplitN(rpcErr.Error(), ":", 2)
		errCode, err = strconv.ParseInt(errParts[0], 10, 32)
		//Check to see if we are requesting a height the safecoind doesn't have yet
		if err == nil && errCode == -8 {
			return -1, -1, "", "", nil
		}
		return -1, -1, "", "", errors.Wrap(rpcErr, "error requesting block")
	}

	var f interface{}
	err = json.Unmarshal(result, &f)
	if err != nil {
		return -1, -1, "", "", errors.Wrap(err, "error reading JSON response")
	}

	chainName := f.(map[string]interface{})["chain"].(string)

	upgradeJSON := f.(map[string]interface{})["upgrades"]
	saplingJSON := upgradeJSON.(map[string]interface{})["76b809bb"] // Sapling ID
	saplingHeight := saplingJSON.(map[string]interface{})["activationheight"].(float64)

	blockHeight := f.(map[string]interface{})["headers"].(float64)

	consensus := f.(map[string]interface{})["consensus"]
	branchID := consensus.(map[string]interface{})["nextblock"].(string)

	return int(saplingHeight), int(blockHeight), chainName, branchID, nil
}

func getBlockFromRPC(rpcClient *rpcclient.Client, height int) (*walletrpc.CompactBlock, error) {
	params := make([]json.RawMessage, 2)
	params[0] = json.RawMessage("\"" + strconv.Itoa(height) + "\"")
	params[1] = json.RawMessage("0")
	result, rpcErr := rpcClient.RawRequest("getblock", params)

	var err error
	var errCode int64

	// For some reason, the error responses are not JSON
	if rpcErr != nil {
		errParts := strings.SplitN(rpcErr.Error(), ":", 2)
		errCode, err = strconv.ParseInt(errParts[0], 10, 32)
		//Check to see if we are requesting a height the safecoind doesn't have yet
		if err == nil && errCode == -8 {
			return nil, nil
		}
		return nil, errors.Wrap(rpcErr, "error requesting block")
	}

	var blockDataHex string
	err = json.Unmarshal(result, &blockDataHex)
	if err != nil {
		return nil, errors.Wrap(err, "error reading JSON response")
	}

	blockData, err := hex.DecodeString(blockDataHex)
	if err != nil {
		return nil, errors.Wrap(err, "error decoding getblock output")
	}

	block := parser.NewBlock()
	rest, err := block.ParseFromSlice(blockData)
	if err != nil {
		return nil, errors.Wrap(err, "error parsing block")
	}
	if len(rest) != 0 {
		return nil, errors.New("received overlong message")
	}

	return block.ToCompact(), nil
}

func BlockIngestor(rpcClient *rpcclient.Client, cache *BlockCache, log *logrus.Entry,
	stopChan chan bool, startHeight int) {
	reorgCount := 0
	height := startHeight
	timeoutCount := 0

	// Start listening for new blocks
	for {
		select {
		case <-stopChan:
			break

		case <-time.After(15 * time.Second):
			for {
				if reorgCount > 0 {
					height -= 10
				}

				if reorgCount > 10 {
					log.Error("Reorg exceeded max of 100 blocks! Help!")
					return
				}

				block, err := getBlockFromRPC(rpcClient, height)

				if err != nil {
					log.WithFields(logrus.Fields{
						"height": height,
						"error":  err,
					}).Warn("error with getblock")

					timeoutCount++
					if timeoutCount == 3 {
						log.WithFields(logrus.Fields{
							"timeouts": timeoutCount,
						}).Warn("unable to issue RPC call to safecoind node 3 times")
						break
					}
				}

				if block != nil {
					if timeoutCount > 0 {
						timeoutCount--
					}

					log.Info("Ingestor adding block to cache: ", height)
					err, reorg := cache.Add(height, block)

					if err != nil {
						log.Error("Error adding block to cache: ", err)
						continue
					}

					//check for reorgs once we have inital block hash from startup
					if reorg {
						reorgCount++

						log.WithFields(logrus.Fields{
							"height": height,
							"hash":   displayHash(block.Hash),
							"phash":  displayHash(block.PrevHash),
							"reorg":  reorgCount,
						}).Warn("REORG")
					} else {
						reorgCount = 0

						height++
					}
				} else {
					break
				}
			}
		}
	}
}

func GetBlock(rpcClient *rpcclient.Client, cache *BlockCache, height int) (*walletrpc.CompactBlock, error) {
	// First, check the cache to see if we have the block
	block := cache.Get(height)
	if block != nil {
		return block, nil
	}

	// If a block was not found, make sure user is requesting a historical block
	if height > cache.GetLatestBlock() {
		return nil, errors.New(
			fmt.Sprintf(
				"Block requested is newer than latest block. Requested: %d Latest: %d",
				height, cache.GetLatestBlock()))
	}

	block, err := getBlockFromRPC(rpcClient, height)
	if err != nil {
		return nil, err
	}

	return block, nil
}

func GetBlockRange(rpcClient *rpcclient.Client, cache *BlockCache,
	blockOut chan<- walletrpc.CompactBlock, errOut chan<- error, start, end int) {

	// Go over [start, end] inclusive
	for i := start; i <= end; i++ {
		block, err := GetBlock(rpcClient, cache, i)
		if err != nil {
			errOut <- err
			return
		}

		blockOut <- *block
	}

	errOut <- nil
}

func displayHash(hash []byte) string {
	rhash := make([]byte, len(hash))
	copy(rhash, hash)
	// Reverse byte order
	for i := 0; i < len(rhash)/2; i++ {
		j := len(rhash) - 1 - i
		rhash[i], rhash[j] = rhash[j], rhash[i]
	}

	return hex.EncodeToString(rhash)
}
