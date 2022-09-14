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

type cmdGetBlock struct {
	Method string `json:"method"`
	Params struct {
		BlockHash string `json:"blockhash"`
		Verbosity int    `json:"verbosity"`
	} `json:"params"`
}

type cmdSearchLogsRequest struct {
	Method string `json:"method"`
	Params struct {
		FromBlock int         `json:"fromBlock"`
		ToBlock   int         `json:"toBlock,omitempty"`
		Address   []string    `json:"addresses,omitempty"`
		Optional  interface{} `json:"omitempty"`
	} `json:"params"`
}
type Topics struct {
	Topics []string `json:"topics"`
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

func (b *HydraRPC) GetBlock(hash string, height uint32) (*bchain.Block, error) {
	var err error
	if hash == "" {
		hash, err = b.GetBlockHash(height)
		if err != nil {
			return nil, err
		}
	}
	block, err := b.GetBlockFull(hash)
	if err != nil {
		return nil, err
	}

	logs, err := b.getHrc20EventsForBlock(int(height))
	if err != nil {
		return nil, errors.Annotatef(err, "hash %v", hash)
	}

	for _, i := range block.Txs {
		receipt := &rpcReceipt{Logs: logs[i.Txid]}
		ct := &completeTransaction{Tx: &i, Receipt: receipt}
		i.CoinSpecificData = ct
	}

	return block, nil
}

// GetBlockFull returns block with given hash
func (b *HydraRPC) GetBlockFull(hash string) (*bchain.Block, error) {
	glog.V(1).Info("rpc: getblock (verbosity=2) ", hash)

	res := btc.ResGetBlockFull{}
	req := cmdGetBlock{Method: "getblock"}
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
		receipt := &rpcReceipt{Logs: logs[tx.Txid]}
		ct := &completeTransaction{Tx: tx, Receipt: receipt}
		tx.CoinSpecificData = ct
	}

	return &res.Result, nil
}

func isErrBlockNotFound(err *bchain.RPCError) bool {
	return err.Message == "Block not found" ||
		err.Message == "Block height out of range"
}

// GetBlockWithoutHeader is an optimization - it does not call GetBlockHeader to get prev, next hashes
// instead it sets to header only block hash and height passed in parameters
func (b *HydraRPC) GetBlockWithoutHeader(hash string, height uint32) (*bchain.Block, error) {
	block, err := b.GetBlockFull(hash)
	if err != nil {
		return nil, err
	}
	block.BlockHeader.Hash = hash
	block.BlockHeader.Height = height
	logs, err := b.getHrc20EventsForBlock(int(height))
	if err != nil {
		return nil, errors.Annotatef(err, "hash %v", hash)
	}

	for _, i := range block.Txs {
		receipt := &rpcReceipt{Logs: logs[i.Txid]}
		ct := &completeTransaction{Tx: &i, Receipt: receipt}
		i.CoinSpecificData = ct
	}

	return block, nil
}

type resGetRawTransaction struct {
	Error  *bchain.RPCError `json:"error"`
	Result bchain.Tx        `json:"result"`
}

type cmdGetRawTransaction struct {
	Method string `json:"method"`
	Params struct {
		Txid    string `json:"txid"`
		Verbose bool   `json:"verbose"`
	} `json:"params"`
}

// getRawTransaction returns json as returned by backend, with all coin specific data
func (b *HydraRPC) getRawTransaction(txid string) (*bchain.Tx, error) {
	glog.V(1).Info("rpc: getrawtransaction ", txid)

	res := resGetRawTransaction{}
	req := cmdGetRawTransaction{Method: "getrawtransaction"}
	req.Params.Txid = txid
	req.Params.Verbose = true
	err := b.Call(&req, &res)

	if err != nil {
		return nil, errors.Annotatef(err, "txid %v", txid)
	}
	if res.Error != nil {
		if btc.IsMissingTx(res.Error) {
			return nil, bchain.ErrTxNotFound
		}
		return nil, errors.Annotatef(res.Error, "txid %v", txid)
	}
	return &res.Result, nil
}

func (b *HydraRPC) GetTransaction(txid string) (*bchain.Tx, error) {
	tx, err := b.getRawTransaction(txid)
	if err != nil {
		return nil, err
	}
	receipt, err := b.getLogsForTx(txid)
	if err != nil {
		return nil, errors.Annotatef(err, "txid %v", txid)
	}
	if receipt != nil {
		getEthSpecificDataFromVouts(tx.Vout, receipt)
	}
	ct := completeTransaction{Tx: tx, Receipt: receipt}
	tx.CoinSpecificData = ct

	return tx, nil
}

// GetTransactionSpecific returns json as returned by backend, with all coin specific data
func (b *HydraRPC) GetTransactionSpecific(tx *bchain.Tx) (json.RawMessage, error) {
	csd, ok := tx.CoinSpecificData.(completeTransaction)
	if !ok {
		ntx, err := b.GetTransaction(tx.Txid)
		if err != nil {
			return nil, err
		}
		csd, ok = ntx.CoinSpecificData.(completeTransaction)
		if !ok {
			return nil, errors.New("Cannot get CoinSpecificData")
		}
	}
	m, err := json.Marshal(&csd)
	return json.RawMessage(m), err
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
