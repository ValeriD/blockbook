package hydra

import (
	"encoding/json"
	"math/big"

	"github.com/golang/glog"
	"github.com/juju/errors"
	"github.com/trezor/blockbook/bchain"
	"github.com/trezor/blockbook/bchain/coins/btc"
	"github.com/trezor/blockbook/common"
)

// HydraRPC is an interface to JSON-RPC bitcoind service.
type HydraRPC struct {
	*btc.BitcoinRPC
	minFeeRate *big.Int // satoshi per kb
}

type RpcLog struct {
	Address string   `json:"address"`
	Topics  []string `json:"topics"`
	Data    string   `json:"data"`
}

type rpcLogWithTxHash struct {
	RpcLog
	Hash string `json:"transactionHash"`
}

type cmdTransactionReceipt struct {
	Method string   `json:"method"`
	Params []string `json:"params"`
}
type resTransactionReceipt struct {
	Error  *bchain.RPCError `json:"error"`
	Result struct {
		RpcReceipt rpcReceipt `json:"result"`
	}
}
type resEstimateSmartFee struct {
	Error  *bchain.RPCError `json:"error"`
	Result struct {
		GasPrice  common.JSONNumber `json:"gasPrice"`
		BytePrice common.JSONNumber `json:"bytePrice"`
	} `json:"result"`
}
type cmdEstimateSmartFee struct {
	Method string `json:"method"`
}

// NewHydraRPC returns new HydraRPC instance.
func NewHydraRPC(config json.RawMessage, pushHandler func(bchain.NotificationType)) (bchain.BlockChain, error) {
	b, err := btc.NewBitcoinRPC(config, pushHandler)
	if err != nil {
		return nil, err
	}

	s := &HydraRPC{
		b.(*btc.BitcoinRPC),
		big.NewInt(400000),
	}
	s.RPCMarshaler = btc.JSONMarshalerV1{}
	s.ChainConfig.SupportsEstimateSmartFee = true

	return s, nil
}

// Initialize initializes HydraRPC instance.
func (b *HydraRPC) Initialize() error {
	ci, err := b.GetChainInfo()
	if err != nil {
		return err
	}
	chainName := ci.Chain

	params := GetChainParams(chainName)

	// always create parser
	b.Parser = NewHydraParser(params, b.ChainConfig)

	// parameters for getInfo request
	if params.Net == MainnetMagic {
		b.Testnet = false
		b.Network = "livenet"
	} else {
		b.Testnet = true
		b.Network = "testnet"
	}

	glog.Info("rpc: block chain ", params.Name)

	return nil
}

// GetTransactionForMempool returns a transaction by the transaction ID
// It could be optimized for mempool, i.e. without block time and confirmations
func (b *HydraRPC) GetTransactionForMempool(txid string) (*bchain.Tx, error) {
	return b.GetTransaction(txid)
}

// EstimateSmartFee returns fee estimation
func (b *HydraRPC) EstimateSmartFee(blocks int, conservative bool) (big.Int, error) {

	res := resEstimateSmartFee{}
	req := cmdEstimateSmartFee{Method: "getoracleinfo"}
	err := b.Call(&req, &res)

	var r big.Int
	if err != nil {
		return r, nil
	}
	if res.Error != nil {
		return r, res.Error
	}
	n, err := res.Result.BytePrice.Int64()
	if err != nil {
		return r, err
	}
	n *= 1000
	r.SetInt64(n)

	return r, nil
}

func (b *HydraRPC) GetBlock(hash string, height uint32) (*bchain.Block, error) {
	var err error
	if hash == "" {
		hash, err = b.GetBlockHash(height)
		if err != nil {
			return nil, err
		}
	}
	if !b.ParseBlocks {
		return b.GetBlockFull(hash)
	}
	// optimization
	if height > 0 {
		return b.GetBlockWithoutHeader(hash, height)
	}
	header, err := b.GetBlockHeader(hash)
	if err != nil {
		return nil, err
	}
	data, err := b.GetBlockBytes(hash)
	if err != nil {
		return nil, err
	}
	block, err := b.Parser.ParseBlock(data)
	if err != nil {
		return nil, errors.Annotatef(err, "hash %v", hash)
	}

	logs, err := b.getHrc20EventsForBlock(int(height))
	if err != nil {
		return nil, errors.Annotatef(err, "hash %v", hash)
	}

	for _, i := range block.Txs {
		ct := &rpcReceipt{Logs: logs[i.Txid]}
		i.CoinSpecificData = ct
	}

	block.BlockHeader = *header
	return block, nil
}

// GetBlockInfo returns extended header (more info than in bchain.BlockHeader) with a list of txids
func (b *HydraRPC) GetBlockInfo(hash string) (*bchain.BlockInfo, error) {
	glog.V(1).Info("rpc: getblock (verbosity=1) ", hash)

	res := btc.ResGetBlockInfo{}
	req := btc.CmdGetBlock{Method: "getblock"}
	req.Params.BlockHash = hash
	req.Params.Verbosity = 1
	err := b.Call(&req, &res)

	if err != nil {
		return nil, errors.Annotatef(err, "hash %v", hash)
	}
	if res.Error != nil {
		if btc.IsErrBlockNotFound(res.Error) {
			return nil, bchain.ErrBlockNotFound
		}
		return nil, errors.Annotatef(res.Error, "hash %v", hash)
	}
	return &res.Result, nil
}

type cmdSearchLogsRequest struct {
	Method string `json:"method"`
	Params struct {
		FromBlock int         `json:"fromBlock"`
		ToBlock   int         `json:"toBlock,omitempty"`
		Address   []string    `json:"addresses,omitempty"`
		Optional  interface{} `json:"omitempty"`

		// Address   struct {
		// 	Address []string `json:"addresses,omitempty"`
		// }
		// Topics struct {
		// 	Topics []string `json:"topics,omitempty"`
		// }
		// Minconf uint `json:"minconf,omitempty"`
	} `json:"params"`
}
type Topics struct {
	Topics []string `json:"topics"`
}
type rpcReceipt struct {
	GasUsed string    `json:"gasUsed"`
	Status  string    `json:"status"`
	Logs    []*rpcLog `json:"logs"`
}

type rpcLog struct {
	Address string   `json:"address"`
	Topics  []string `json:"topics"`
	Data    string   `json:"data"`
}

type rpcLogsWithTxHash struct {
	Hash string   `json:"transactionHash,omitempty"`
	Logs []rpcLog `json:"log,omitempty"`
}

type rpcSearchLogsRes struct {
	Error  interface{}         `json:"error"`
	Id     string              `json:"id"`
	Result []rpcLogsWithTxHash `json:"result"`
}

func (b *HydraRPC) getHrc20EventsForBlock(blockNumber int) (map[string][]*rpcLog, error) {
	if blockNumber == 0 {
		return nil, nil
	}
	glog.V(1).Info("rpc: searchlogs ", blockNumber)

	//Fetch the data from the rpc about the logs in the current block number
	req := cmdSearchLogsRequest{Method: "searchlogs"}
	req.Params.FromBlock = blockNumber
	req.Params.ToBlock = blockNumber
	topics := Topics{}
	topics.Topics = []string{hrc20TransferEventSignature}
	req.Params.Address = []string{}
	req.Params.Optional = topics

	var res rpcSearchLogsRes
	err := b.Call(&req, &res)
	if err != nil || res.Error != nil {
		return nil, errors.Annotatef(err, "blockNumber %v", blockNumber)
	}

	rpcLogs := res.Result

	r := make(map[string][]*rpcLog)
	for i := range rpcLogs {
		l := &rpcLogs[i]
		for j := range l.Logs {
			r[l.Hash] = append(r[l.Hash], &l.Logs[j])
		}
	}
	return r, nil
}

// GetBlockFull returns block with given hash
func (b *HydraRPC) GetBlockFull(hash string) (*bchain.Block, error) {
	glog.V(1).Info("rpc: getblock (verbosity=2) ", hash)

	res := btc.ResGetBlockFull{}
	req := btc.CmdGetBlock{Method: "getblock"}
	req.Params.BlockHash = hash
	req.Params.Verbosity = 2
	err := b.Call(&req, &res)

	if err != nil {
		return nil, errors.Annotatef(err, "hash %v", hash)
	}
	if res.Error != nil {
		if isErrBlockNotFound(res.Error) {
			return nil, bchain.ErrBlockNotFound
		}
		return nil, errors.Annotatef(res.Error, "hash %v", hash)
	}
	logs, err := b.getHrc20EventsForBlock(int(res.Result.Height))
	if err != nil {
		return nil, errors.Annotatef(err, "hash %v", hash)
	}

	for i := range res.Result.Txs {
		tx := &res.Result.Txs[i]
		for j := range tx.Vout {
			vout := &tx.Vout[j]
			// convert vout.JsonValue to big.Int and clear it, it is only temporary value used for unmarshal
			vout.ValueSat, err = b.Parser.AmountToBigInt(vout.JsonValue)
			if err != nil {
				return nil, err
			}
			vout.JsonValue = ""
		}
		ct := &rpcReceipt{Logs: logs[tx.Txid]}
		tx.CoinSpecificData = ct
	}

	glog.Info("Full block: ", res.Result)

	return &res.Result, nil
}

func isErrBlockNotFound(err *bchain.RPCError) bool {
	return err.Message == "Block not found" ||
		err.Message == "Block height out of range"
}

// GetBlockWithoutHeader is an optimization - it does not call GetBlockHeader to get prev, next hashes
// instead it sets to header only block hash and height passed in parameters
func (b *HydraRPC) GetBlockWithoutHeader(hash string, height uint32) (*bchain.Block, error) {
	data, err := b.GetBlockBytes(hash)
	if err != nil {
		return nil, err
	}
	block, err := b.Parser.ParseBlock(data)
	if err != nil {
		return nil, errors.Annotatef(err, "%v %v", height, hash)
	}
	block.BlockHeader.Hash = hash
	block.BlockHeader.Height = height
	logs, err := b.getHrc20EventsForBlock(int(height))
	if err != nil {
		return nil, errors.Annotatef(err, "hash %v", hash)
	}

	for _, i := range block.Txs {
		ct := &rpcReceipt{Logs: logs[i.Txid]}
		i.CoinSpecificData = ct
	}

	return block, nil
}

func (b *HydraRPC) GetHRC20LogsForTransaction(transactionHash string) (*rpcReceipt, error) {
	res := resTransactionReceipt{}
	req := cmdTransactionReceipt{Method: "gettransactionreceipt"}
	req.Params = []string{transactionHash}

	err := b.Call(&req, &res)
	if err != nil {
		return nil, err
	}
	if res.Error != nil {
		return nil, res.Error
	}
	rpcLogs := res.Result.RpcReceipt.Logs
	var rpcReceipt rpcReceipt
	rpcReceipt.GasUsed = res.Result.RpcReceipt.GasUsed
	for j := range rpcLogs {
		if rpcLogs[j].Topics[0] == hrc20TransferEventSignature {
			rpcReceipt.Logs = append(rpcReceipt.Logs, rpcLogs[j])
		}
	}

	return &rpcReceipt, nil
}
