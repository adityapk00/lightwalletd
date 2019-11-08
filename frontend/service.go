package frontend

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/btcsuite/btcd/rpcclient"
	"github.com/sirupsen/logrus"

	"github.com/OleksandrBlack/safecoin-lightwalletd/common"
	"github.com/OleksandrBlack/safecoin-lightwalletd/walletrpc"
)

var (
	ErrUnspecified = errors.New("request for unspecified identifier")
)

// the service type
type SqlStreamer struct {
	cache  *common.BlockCache
	client *rpcclient.Client
	log    *logrus.Entry
}

func NewSQLiteStreamer(client *rpcclient.Client, cache *common.BlockCache, log *logrus.Entry) (walletrpc.CompactTxStreamerServer, error) {
	return &SqlStreamer{cache, client, log}, nil
}

func (s *SqlStreamer) GracefulStop() error {
	return nil
}

func (s *SqlStreamer) GetCache() *common.BlockCache {
	return s.cache
}

func (s *SqlStreamer) GetLatestBlock(ctx context.Context, placeholder *walletrpc.ChainSpec) (*walletrpc.BlockID, error) {
	latestBlock := s.cache.GetLatestBlock()

	if latestBlock == -1 {
		return nil, errors.New("Cache is empty. Server is probably not yet ready.")
	}

	// TODO: also return block hashes here
	return &walletrpc.BlockID{Height: uint64(latestBlock)}, nil
}

func (s *SqlStreamer) GetAddressTxids(addressBlockFilter *walletrpc.TransparentAddressBlockFilter, resp walletrpc.CompactTxStreamer_GetAddressTxidsServer) error {
	params := make([]json.RawMessage, 1)
	st := "{\"addresses\": [\"" + addressBlockFilter.Address + "\"]," +
		"\"start\": " + strconv.FormatUint(addressBlockFilter.Range.Start.Height, 10) +
		", \"end\": " + strconv.FormatUint(addressBlockFilter.Range.End.Height, 10) + "}"

	params[0] = json.RawMessage(st)

	result, rpcErr := s.client.RawRequest("getaddresstxids", params)

	var err error
	var errCode int64

	// For some reason, the error responses are not JSON
	if rpcErr != nil {
		s.log.Errorf("Got error: %s", rpcErr.Error())
		errParts := strings.SplitN(rpcErr.Error(), ":", 2)
		errCode, err = strconv.ParseInt(errParts[0], 10, 32)
		//Check to see if we are requesting a height the safecoind doesn't have yet
		if err == nil && errCode == -8 {
			return nil
		}
		return nil
	}

	var txids []string
	err = json.Unmarshal(result, &txids)
	if err != nil {
		s.log.Errorf("Got error: %s", err.Error())
		return nil
	}

	timeout, cancel := context.WithTimeout(resp.Context(), 30*time.Second)
	defer cancel()

	for _, txidstr := range txids {
		txid, _ := hex.DecodeString(txidstr)
		// Txid is read as a string, which is in big-endian order. But when converting
		// to bytes, it should be little-endian
		for left, right := 0, len(txid)-1; left < right; left, right = left+1, right-1 {
			txid[left], txid[right] = txid[right], txid[left]
		}

		tx, err := s.GetTransaction(timeout, &walletrpc.TxFilter{Hash: txid})
		if err != nil {
			s.log.Errorf("Got error: %s", err.Error())
			return nil
		}

		resp.Send(tx)
	}

	return nil
}

func (s *SqlStreamer) GetBlock(ctx context.Context, id *walletrpc.BlockID) (*walletrpc.CompactBlock, error) {
	if id.Height == 0 && id.Hash == nil {
		return nil, ErrUnspecified
	}

	// Precedence: a hash is more specific than a height. If we have it, use it first.
	if id.Hash != nil {
		// TODO: Get block by hash

		return nil, errors.New("GetBlock by Hash is not yet implemented")
	} else {
		cBlock, err := common.GetBlock(s.client, s.cache, int(id.Height))

		if err != nil {
			return nil, err
		}

		return cBlock, err
	}

}

func (s *SqlStreamer) GetBlockRange(span *walletrpc.BlockRange, resp walletrpc.CompactTxStreamer_GetBlockRangeServer) error {
	blockChan := make(chan walletrpc.CompactBlock)
	errChan := make(chan error)

	go common.GetBlockRange(s.client, s.cache, blockChan, errChan, int(span.Start.Height), int(span.End.Height))

	for {
		select {
		case err := <-errChan:
			// this will also catch context.DeadlineExceeded from the timeout
			return err
		case cBlock := <-blockChan:
			err := resp.Send(&cBlock)
			if err != nil {
				return err
			}
		}
	}

	return nil
}

func (s *SqlStreamer) GetTransaction(ctx context.Context, txf *walletrpc.TxFilter) (*walletrpc.RawTransaction, error) {
	var txBytes []byte
	var txHeight float64

	if txf.Hash != nil {
		txid := txf.Hash
		for left, right := 0, len(txid)-1; left < right; left, right = left+1, right-1 {
			txid[left], txid[right] = txid[right], txid[left]
		}
		leHashString := hex.EncodeToString(txid)

		// First call to get the raw transaction bytes
		params := make([]json.RawMessage, 1)
		params[0] = json.RawMessage("\"" + leHashString + "\"")

		result, rpcErr := s.client.RawRequest("getrawtransaction", params)

		var err error
		var errCode int64
		// For some reason, the error responses are not JSON
		if rpcErr != nil {
			s.log.Errorf("Got error: %s", rpcErr.Error())
			errParts := strings.SplitN(rpcErr.Error(), ":", 2)
			errCode, err = strconv.ParseInt(errParts[0], 10, 32)
			//Check to see if we are requesting a height the safecoind doesn't have yet
			if err == nil && errCode == -8 {
				return nil, err
			}
			return nil, err
		}

		var txhex string
		err = json.Unmarshal(result, &txhex)
		if err != nil {
			return nil, err
		}

		txBytes, err = hex.DecodeString(txhex)
		if err != nil {
			return nil, err
		}

		// Second call to get height
		params = make([]json.RawMessage, 2)
		params[0] = json.RawMessage("\"" + leHashString + "\"")
		params[1] = json.RawMessage("1")

		result, rpcErr = s.client.RawRequest("getrawtransaction", params)

		// For some reason, the error responses are not JSON
		if rpcErr != nil {
			s.log.Errorf("Got error: %s", rpcErr.Error())
			errParts := strings.SplitN(rpcErr.Error(), ":", 2)
			errCode, err = strconv.ParseInt(errParts[0], 10, 32)
			//Check to see if we are requesting a height the safecoind doesn't have yet
			if err == nil && errCode == -8 {
				return nil, err
			}
			return nil, err
		}
		var txinfo interface{}
		err = json.Unmarshal(result, &txinfo)
		if err != nil {
			return nil, err
		}
		txHeight = txinfo.(map[string]interface{})["height"].(float64)

		return &walletrpc.RawTransaction{Data: txBytes, Height: uint64(txHeight)}, nil
	}

	if txf.Block.Hash != nil {
		s.log.Error("Can't GetTransaction with a blockhash+num. Please call GetTransaction with txid")
		return nil, errors.New("Can't GetTransaction with a blockhash+num. Please call GetTransaction with txid")
	}

	return &walletrpc.RawTransaction{Data: txBytes, Height: uint64(txHeight)}, nil
}

// GetLightdInfo gets the LightWalletD (this server) info
func (s *SqlStreamer) GetLightdInfo(ctx context.Context, in *walletrpc.Empty) (*walletrpc.LightdInfo, error) {
	saplingHeight, blockHeight, chainName, consensusBranchId, err := common.GetSaplingInfo(s.client)

	if err != nil {
		s.log.WithFields(logrus.Fields{
			"error": err,
		}).Warn("Unable to get sapling activation height")
		return nil, err
	}

	// TODO these are called Error but they aren't at the moment.
	// A success will return code 0 and message txhash.
	return &walletrpc.LightdInfo{
		Version:                 "0.1-safelightd",
		Vendor:                  "SafeWallet LightWalletD",
		TaddrSupport:            true,
		ChainName:               chainName,
		SaplingActivationHeight: uint64(saplingHeight),
		ConsensusBranchId:       consensusBranchId,
		BlockHeight:             uint64(blockHeight),
	}, nil
}

// SendTransaction forwards raw transaction bytes to a safecoind instance over JSON-RPC
func (s *SqlStreamer) SendTransaction(ctx context.Context, rawtx *walletrpc.RawTransaction) (*walletrpc.SendResponse, error) {
	// sendrawtransaction "hexstring" ( allowhighfees )
	//
	// Submits raw transaction (serialized, hex-encoded) to local node and network.
	//
	// Also see createrawtransaction and signrawtransaction calls.
	//
	// Arguments:
	// 1. "hexstring"    (string, required) The hex string of the raw transaction)
	// 2. allowhighfees    (boolean, optional, default=false) Allow high fees
	//
	// Result:
	// "hex"             (string) The transaction hash in hex

	// Construct raw JSON-RPC params
	params := make([]json.RawMessage, 1)
	txHexString := hex.EncodeToString(rawtx.Data)
	params[0] = json.RawMessage("\"" + txHexString + "\"")
	result, rpcErr := s.client.RawRequest("sendrawtransaction", params)

	var err error
	var errCode int64
	var errMsg string

	// For some reason, the error responses are not JSON
	if rpcErr != nil {
		errParts := strings.SplitN(rpcErr.Error(), ":", 2)
		errMsg = strings.TrimSpace(errParts[1])
		errCode, err = strconv.ParseInt(errParts[0], 10, 32)
		if err != nil {
			// This should never happen. We can't panic here, but it's that class of error.
			// This is why we need integration testing to work better than regtest currently does. TODO.
			return nil, errors.New("SendTransaction couldn't parse error code")
		}
	} else {
		errMsg = string(result)
	}

	// TODO these are called Error but they aren't at the moment.
	// A success will return code 0 and message txhash.
	return &walletrpc.SendResponse{
		ErrorCode:    int32(errCode),
		ErrorMessage: errMsg,
	}, nil
}
