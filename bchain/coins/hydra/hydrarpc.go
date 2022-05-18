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

type RpcReceipt struct {
	GasUsed string    `json:"gasUsed"`
	Status  string    `json:"status"`
	Logs    []*RpcLog `json:"logs"`
}
type cmdTransactionReceipt struct {
	Method string   `json:"method"`
	Params []string `json:"params"`
}
type resTransactionReceipt struct {
	Error  *bchain.RPCError `json:"error"`
	Result struct {
		RpcReceipt RpcReceipt `json:"result"`
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
	glog.Info("Here")
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
	glog.Info("Here1")
	// optimization
	if height > 0 {
		return b.GetBlockWithoutHeader(hash, height)
	}
	glog.Info("Here2")
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
	for _, i := range block.Txs {
		ct, _ := b.GetHRC20LogsForTransaction(i.Txid)
		i.CoinSpecificData = ct
	}
	block.BlockHeader = *header
	glog.Info(block)
	return block, nil
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
		tx.CoinSpecificData, _ = b.GetHRC20LogsForTransaction(tx.Txid)
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
	for _, i := range block.Txs {
		ct, _ := b.GetHRC20LogsForTransaction(i.Txid)
		i.CoinSpecificData = ct
	}
	glog.Info("Block without header: ", block)

	return block, nil
}

func (b *HydraRPC) GetHRC20LogsForTransaction(transactionHash string) (*RpcReceipt, error) {
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

	return &res.Result.RpcReceipt, nil
}
