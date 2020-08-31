package frontend

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"net"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/btcsuite/btcd/rpcclient"
	"github.com/sirupsen/logrus"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"

	"github.com/adityapk00/lightwalletd/common"
	"github.com/adityapk00/lightwalletd/walletrpc"
)

var (
	ErrUnspecified = errors.New("request for unspecified identifier")
)

type latencyCacheEntry struct {
	timeNanos   int64
	lastBlock   uint64
	totalBlocks uint64
}

// the service type
type SqlStreamer struct {
	cache        *common.BlockCache
	client       *rpcclient.Client
	log          *logrus.Entry
	metrics      *common.PrometheusMetrics
	latencyCache map[string]*latencyCacheEntry
	latencyMutex sync.RWMutex
}

func NewSQLiteStreamer(client *rpcclient.Client, cache *common.BlockCache, log *logrus.Entry, metrics *common.PrometheusMetrics) (walletrpc.CompactTxStreamerServer, error) {
	return &SqlStreamer{cache, client, log, metrics, make(map[string]*latencyCacheEntry), sync.RWMutex{}}, nil
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
		s.metrics.TotalErrors.Inc()

		return nil, errors.New("Cache is empty. Server is probably not yet ready.")
	}

	s.metrics.LatestBlockCounter.Inc()

	// TODO: also return block hashes here
	return &walletrpc.BlockID{Height: uint64(latestBlock)}, nil
}

func (s *SqlStreamer) GetAddressTxids(addressBlockFilter *walletrpc.TransparentAddressBlockFilter, resp walletrpc.CompactTxStreamer_GetAddressTxidsServer) error {
	var err error
	var errCode int64

	if addressBlockFilter == nil || addressBlockFilter.Range == nil || addressBlockFilter.Range.Start == nil || addressBlockFilter.Range.End == nil {
		s.log.Errorf("Bad Structure")
		return ErrUnspecified
	}

	// Test to make sure Address is a single t address
	match, err := regexp.Match("\\At[a-zA-Z0-9]{34}\\z", []byte(addressBlockFilter.Address))
	if err != nil || !match {
		s.metrics.TotalErrors.Inc()

		s.log.Errorf("Unrecognized address: %s", addressBlockFilter.Address)
		return errors.New("Unrecognized Address")
	}

	params := make([]json.RawMessage, 1)
	st := "{\"addresses\": [\"" + addressBlockFilter.Address + "\"]," +
		"\"start\": " + strconv.FormatUint(addressBlockFilter.Range.Start.Height, 10) +
		", \"end\": " + strconv.FormatUint(addressBlockFilter.Range.End.Height, 10) + "}"

	params[0] = json.RawMessage(st)

	result, rpcErr := s.client.RawRequest("getaddresstxids", params)

	// For some reason, the error responses are not JSON
	if rpcErr != nil {
		s.metrics.TotalErrors.Inc()

		s.log.Errorf("Got error: %s", rpcErr.Error())
		errParts := strings.SplitN(rpcErr.Error(), ":", 2)
		errCode, err = strconv.ParseInt(errParts[0], 10, 32)
		//Check to see if we are requesting a height the zcashd doesn't have yet
		if err == nil && errCode == -8 {
			return ErrUnspecified
		}
		return rpcErr
	}

	var txids []string
	err = json.Unmarshal(result, &txids)
	if err != nil {
		s.log.Errorf("Got error: %s", err.Error())
		return err
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
			s.metrics.TotalErrors.Inc()

			s.log.Errorf("Got error: %s", err.Error())
			return err
		}

		resp.Send(tx)
	}

	go func() {
		s.log.WithFields(logrus.Fields{
			"method":  "GetAddressTxids",
			"address": addressBlockFilter.Address,
			"start":   addressBlockFilter.Range.Start.Height,
			"end":     addressBlockFilter.Range.End.Height,
		}).Info("Service")
	}()

	return nil
}

func (s *SqlStreamer) peerIPFromContext(ctx context.Context) string {
	if xRealIP, ok := metadata.FromIncomingContext(ctx); ok {
		realIP := xRealIP.Get("x-real-ip")
		if len(realIP) > 0 {
			return realIP[0]
		}
	}

	if peerInfo, ok := peer.FromContext(ctx); ok {
		ip, _, err := net.SplitHostPort(peerInfo.Addr.String())
		if err == nil {
			return ip
		}
	}

	return "unknown"
}

func (s *SqlStreamer) dailyActiveBlock(height uint64, peerip string) {
	if height%1152 == 0 {
		s.log.WithFields(logrus.Fields{
			"method":       "DailyActiveBlock",
			"peer_addr":    peerip,
			"block_height": height,
		}).Info("Service")
	}
}

func (s *SqlStreamer) GetBlock(ctx context.Context, id *walletrpc.BlockID) (*walletrpc.CompactBlock, error) {

	if id == nil || (id.Height == 0 && id.Hash == nil) {
		return nil, ErrUnspecified
	}

	s.log.WithFields(logrus.Fields{
		"method": "GetBlockRange",
		"start":  id.Height,
		"end":    id.Height,
	}).Info("Service")

	// Log a daily active user if the user requests the day's "key block"
	go func() {
		s.dailyActiveBlock(id.Height, s.peerIPFromContext(ctx))
	}()

	// Precedence: a hash is more specific than a height. If we have it, use it first.
	if id.Hash != nil {
		// TODO: Get block by hash
		s.metrics.TotalErrors.Inc()

		return nil, errors.New("GetBlock by Hash is not yet implemented")
	} else {
		cBlock, err := common.GetBlock(s.client, s.cache, int(id.Height))

		if err != nil {
			return nil, err
		}

		s.metrics.TotalBlocksServedConter.Inc()
		return cBlock, err
	}

}

func (s *SqlStreamer) GetBlockRange(span *walletrpc.BlockRange, resp walletrpc.CompactTxStreamer_GetBlockRangeServer) error {
	if span == nil || span.Start == nil || span.End == nil {
		return ErrUnspecified
	}

	blockChan := make(chan walletrpc.CompactBlock)
	errChan := make(chan error)

	peerip := s.peerIPFromContext(resp.Context())

	// Latency logging
	go func() {
		// If there is no ip, ignore
		if peerip == "unknown" {
			return
		}

		// Log only if bulk requesting blocks
		if span.End.Height-span.Start.Height < 100 {
			return
		}

		now := time.Now().UnixNano()
		s.latencyMutex.Lock()
		defer s.latencyMutex.Unlock()

		// remove all old entries
		for ip, entry := range s.latencyCache {
			if entry.timeNanos+int64(30*math.Pow10(9)) < now { // delete after 30 seconds
				delete(s.latencyCache, ip)
			}
		}

		// Look up if this ip address has a previous getblock range
		if entry, ok := s.latencyCache[peerip]; ok {
			// Log only continous blocks
			if entry.lastBlock+1 == span.Start.Height {
				s.log.WithFields(logrus.Fields{
					"method":         "GetBlockRangeLatency",
					"peer_addr":      peerip,
					"num_blocks":     entry.totalBlocks,
					"end_height":     entry.lastBlock,
					"latency_millis": (now - entry.timeNanos) / int64(math.Pow10(6)),
				}).Info("Service")
			}
		}

		// Add or update the ip entry
		s.latencyCache[peerip] = &latencyCacheEntry{
			lastBlock:   span.End.Height,
			totalBlocks: span.End.Height - span.Start.Height + 1,
			timeNanos:   now,
		}
	}()

	// Log a daily active user if the user requests the day's "key block"
	go func() {
		for height := span.Start.Height; height <= span.End.Height; height++ {
			s.dailyActiveBlock(height, peerip)
		}
	}()

	s.log.WithFields(logrus.Fields{
		"method":    "GetBlockRange",
		"start":     span.Start.Height,
		"end":       span.End.Height,
		"peer_addr": peerip,
	}).Info("Service")

	go common.GetBlockRange(s.client, s.cache, blockChan, errChan, int(span.Start.Height), int(span.End.Height))

	for {
		select {
		case err := <-errChan:
			// this will also catch context.DeadlineExceeded from the timeout
			s.metrics.TotalErrors.Inc()
			return err
		case cBlock := <-blockChan:
			s.metrics.TotalBlocksServedConter.Inc()
			err := resp.Send(&cBlock)
			if err != nil {
				return err
			}
		}
	}

}

func (s *SqlStreamer) GetTransaction(ctx context.Context, txf *walletrpc.TxFilter) (*walletrpc.RawTransaction, error) {

	var txBytes []byte
	var txHeight float64

	if txf == nil {
		return nil, ErrUnspecified
	}

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
			s.metrics.TotalErrors.Inc()

			s.log.Errorf("Got error: %s", rpcErr.Error())
			errParts := strings.SplitN(rpcErr.Error(), ":", 2)
			errCode, err = strconv.ParseInt(errParts[0], 10, 32)
			//Check to see if we are requesting a height the zcashd doesn't have yet
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
			s.metrics.TotalErrors.Inc()

			s.log.Errorf("Got error: %s", rpcErr.Error())
			errParts := strings.SplitN(rpcErr.Error(), ":", 2)
			errCode, err = strconv.ParseInt(errParts[0], 10, 32)
			//Check to see if we are requesting a height the zcashd doesn't have yet
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

		go func() {
			peerip := s.peerIPFromContext(ctx)

			s.log.WithFields(logrus.Fields{
				"method":    "GetTransaction",
				"hash":      leHashString,
				"peer_addr": peerip,
			}).Info("Service")
		}()

		return &walletrpc.RawTransaction{Data: txBytes, Height: uint64(txHeight)}, nil
	}

	if txf.Block.Hash != nil {
		s.metrics.TotalErrors.Inc()

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

		s.metrics.TotalErrors.Inc()
		return nil, err
	}

	// TODO these are called Error but they aren't at the moment.
	// A success will return code 0 and message txhash.
	return &walletrpc.LightdInfo{
		Version:                 "0.1-zeclightd",
		Vendor:                  "ZecWallet LightWalletD",
		TaddrSupport:            true,
		ChainName:               chainName,
		SaplingActivationHeight: uint64(saplingHeight),
		ConsensusBranchId:       consensusBranchId,
		BlockHeight:             uint64(blockHeight),
	}, nil
}

// SendTransaction forwards raw transaction bytes to a zcashd instance over JSON-RPC
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

	if rawtx == nil || rawtx.Data == nil {
		return nil, ErrUnspecified
	}

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
	resp := &walletrpc.SendResponse{
		ErrorCode:    int32(errCode),
		ErrorMessage: errMsg,
	}

	s.metrics.SendTransactionsCounter.Inc()

	return resp, nil
}
