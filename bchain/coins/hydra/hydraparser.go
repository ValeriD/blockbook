package hydra

import (
	"encoding/hex"
	"math/big"
	"reflect"

	"github.com/dcb9/go-ethereum/common/hexutil"
	proto "github.com/golang/protobuf/proto"
	"github.com/juju/errors"
	"github.com/martinboehm/btcd/wire"
	"github.com/martinboehm/btcutil/chaincfg"
	"github.com/trezor/blockbook/bchain"
	"github.com/trezor/blockbook/bchain/coins/btc"
	"github.com/trezor/blockbook/bchain/coins/eth"
)

// magic numbers
const (
	MainnetMagic wire.BitcoinNet = 0xafeae9f7
	TestnetMagic wire.BitcoinNet = 0x07131f03
)

// chain parameters
var (
	MainNetParams chaincfg.Params
	TestNetParams chaincfg.Params
)

func init() {
	MainNetParams = chaincfg.MainNetParams
	MainNetParams.Net = MainnetMagic
	MainNetParams.PubKeyHashAddrID = []byte{40}
	MainNetParams.ScriptHashAddrID = []byte{63}
	MainNetParams.Bech32HRPSegwit = "hc"

	TestNetParams = chaincfg.TestNet3Params
	TestNetParams.Net = TestnetMagic
	TestNetParams.PubKeyHashAddrID = []byte{120}
	TestNetParams.ScriptHashAddrID = []byte{110}
	TestNetParams.Bech32HRPSegwit = "th"
}

// HydraParser handle
type HydraParser struct {
	*btc.BitcoinLikeParser
	baseparser *bchain.BaseParser
}

// NewHydraParser returns new DashParser instance
func NewHydraParser(params *chaincfg.Params, c *btc.Configuration) *HydraParser {
	return &HydraParser{
		BitcoinLikeParser: btc.NewBitcoinLikeParser(params, c),
	}
}

type rpcLog struct {
	Address string   `json:"address"`
	Topics  []string `json:"topics"`
	Data    string   `json:"data"`
}

type rpcLogWithTxHash struct {
	rpcLog
	Hash string `json:"transactionHash"`
}

type rpcReceipt struct {
	GasUsed string    `json:"gasUsed"`
	Status  string    `json:"status"`
	Logs    []*rpcLog `json:"logs"`
}

type completeTransaction struct {
	Tx      *bchain.Tx  `json:"tx"`
	Receipt *rpcReceipt `json:"receipt,omitempty"`
}

type rpcBlockTransactions struct {
	Transactions []bchain.Tx `json:"transactions"`
}

type rpcBlockTxids struct {
	Transactions []string `json:"transactions"`
}

// GetChainParams contains network parameters for the main Hydra network,
// the regression test Hydra network, the test Hydra network and
// the simulation test Hydra network, in this order
func GetChainParams(chain string) *chaincfg.Params {
	if !chaincfg.IsRegistered(&MainNetParams) {
		err := chaincfg.Register(&MainNetParams)
		if err == nil {
			err = chaincfg.Register(&TestNetParams)
		}
		if err != nil {
			panic(err)
		}
	}
	switch chain {
	case "test":
		return &TestNetParams
	default:
		return &MainNetParams
	}
}

func (p *HydraParser) PackTx(tx *bchain.Tx, height uint32, blockTime int64) ([]byte, error) {
	var err error
	r, ok := tx.CoinSpecificData.(completeTransaction)
	if !ok {
		return nil, errors.New("Missing CoinSpecificData")
	}

	pti := make([]*ProtoCompleteTransaction_VinType, len(tx.Vin))
	for i, vi := range tx.Vin {
		hex, err := hex.DecodeString(vi.ScriptSig.Hex)
		if err != nil {
			return nil, errors.Annotatef(err, "Vin %v Hex %v", i, vi.ScriptSig.Hex)
		}
		itxid, err := p.PackTxid(vi.Txid)
		if err != nil && err != bchain.ErrTxidMissing {
			return nil, errors.Annotatef(err, "Vin %v Txid %v", i, vi.Txid)
		}

		pti[i] = &ProtoCompleteTransaction_VinType{
			Addresses:    vi.Addresses,
			Coinbase:     vi.Coinbase,
			ScriptSigHex: hex,
			Sequence:     vi.Sequence,
			Txid:         itxid,
			Vout:         vi.Vout,
		}
	}
	pto := make([]*ProtoCompleteTransaction_VoutType, len(tx.Vout))
	for i, vo := range tx.Vout {
		hex, err := hex.DecodeString(vo.ScriptPubKey.Hex)
		if err != nil {
			return nil, errors.Annotatef(err, "Vout %v Hex %v", i, vo.ScriptPubKey.Hex)
		}
		pto[i] = &ProtoCompleteTransaction_VoutType{
			Addresses:       vo.ScriptPubKey.Addresses,
			N:               vo.N,
			ScriptPubKeyHex: hex,
			ValueSat:        vo.ValueSat.Bytes(),
		}
	}
	pt := &ProtoCompleteTransaction{
		Blocktime: uint64(blockTime),
		Height:    height,
		Locktime:  tx.LockTime,
		Vin:       pti,
		Vout:      pto,
		Version:   tx.Version,
	}
	if pt.Hex, err = hex.DecodeString(tx.Hex); err != nil {
		return nil, errors.Annotatef(err, "Hex %v", tx.Hex)
	}
	if pt.Txid, err = p.PackTxid(tx.Txid); err != nil {
		return nil, errors.Annotatef(err, "Txid %v", tx.Txid)
	}
	if !reflect.ValueOf(r).IsNil() {
		pt.Receipt = &ProtoCompleteTransaction_ReceiptType{}
		if pt.Receipt.GasUsed, err = hexDecodeBig(r.Receipt.GasUsed); err != nil {
			return nil, errors.Annotatef(err, "GasUsed %v", r.Receipt.GasUsed)
		}
		if r.Receipt.Status != "" {
			if pt.Receipt.Status, err = hexDecodeBig(r.Receipt.Status); err != nil {
				return nil, errors.Annotatef(err, "Status %v", r.Receipt.Status)
			}
		} else {
			// unknown status, use 'U' as status bytes
			// there is a potential for conflict with value 0x55 but this is not used by any chain at this moment
			pt.Receipt.Status = []byte{'U'}
		}
		ptLogs := make([]*ProtoCompleteTransaction_ReceiptType_LogType, len(r.Receipt.Logs))
		for i, l := range r.Receipt.Logs {
			a, err := hexutil.Decode(l.Address)
			if err != nil {
				return nil, errors.Annotatef(err, "Address cannot be decoded %v", l)
			}
			d, err := hexutil.Decode(l.Data)
			if err != nil {
				return nil, errors.Annotatef(err, "Data cannot be decoded %v", l)
			}
			t := make([][]byte, len(l.Topics))
			for j, s := range l.Topics {
				t[j], err = hexutil.Decode(s)
				if err != nil {
					return nil, errors.Annotatef(err, "Topic cannot be decoded %v", l)
				}
			}
			ptLogs[i] = &ProtoCompleteTransaction_ReceiptType_LogType{
				Address: a,
				Data:    d,
				Topics:  t,
			}

		}
		pt.Receipt.Log = ptLogs
	}

	return proto.Marshal(pt)
}

// UnpackTx unpacks transaction from protobuf byte array
func (p *HydraParser) UnpackTx(buf []byte) (*bchain.Tx, uint32, error) {
	var pt ProtoCompleteTransaction
	err := proto.Unmarshal(buf, &pt)
	if err != nil {
		return nil, 0, err
	}
	txid, err := p.UnpackTxid(pt.Txid)
	if err != nil {
		return nil, 0, err
	}
	vin := make([]bchain.Vin, len(pt.Vin))
	for i, pti := range pt.Vin {
		itxid, err := p.UnpackTxid(pti.Txid)
		if err != nil {
			return nil, 0, err
		}
		vin[i] = bchain.Vin{
			Addresses: pti.Addresses,
			Coinbase:  pti.Coinbase,
			ScriptSig: bchain.ScriptSig{
				Hex: hex.EncodeToString(pti.ScriptSigHex),
			},
			Sequence: pti.Sequence,
			Txid:     itxid,
			Vout:     pti.Vout,
		}
	}
	vout := make([]bchain.Vout, len(pt.Vout))
	for i, pto := range pt.Vout {
		var vs big.Int
		vs.SetBytes(pto.ValueSat)
		vout[i] = bchain.Vout{
			N: pto.N,
			ScriptPubKey: bchain.ScriptPubKey{
				Addresses: pto.Addresses,
				Hex:       hex.EncodeToString(pto.ScriptPubKeyHex),
			},
			ValueSat: vs,
		}
	}
	var rr *rpcReceipt
	if pt.Receipt != nil {
		logs := make([]*rpcLog, len(pt.Receipt.Log))
		for i, l := range pt.Receipt.Log {
			topics := make([]string, len(l.Topics))
			for j, t := range l.Topics {
				topics[j] = hexutil.Encode(t)
			}
			logs[i] = &rpcLog{
				Address: hexutil.Encode(l.Address),
				Data:    hexutil.Encode(l.Data),
				Topics:  topics,
			}
		}
		status := ""
		// handle a special value []byte{'U'} as unknown state
		if len(pt.Receipt.Status) != 1 || pt.Receipt.Status[0] != 'U' {
			status = hexEncodeBig(pt.Receipt.Status)
		}
		rr = &rpcReceipt{
			GasUsed: hexEncodeBig(pt.Receipt.GasUsed),
			Status:  status,
			Logs:    logs,
		}
	}
	tx := bchain.Tx{
		Blocktime:        int64(pt.Blocktime),
		Hex:              hex.EncodeToString(pt.Hex),
		LockTime:         pt.Locktime,
		Time:             int64(pt.Blocktime),
		Txid:             txid,
		Vin:              vin,
		Vout:             vout,
		Version:          pt.Version,
		CoinSpecificData: rr,
	}
	return &tx, pt.Height, nil
}

func has0xPrefix(s string) bool {
	return len(s) >= 2 && s[0] == '0' && (s[1]|32) == 'x'
}

func hexDecodeBig(s string) ([]byte, error) {
	b, err := hexutil.DecodeBig(s)
	if err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

func hexEncodeBig(b []byte) string {
	var i big.Int
	i.SetBytes(b)
	return hexutil.EncodeBig(&i)
}

// EthereumTypeGetErc20FromTx returns Erc20 data from bchain.Tx
func (p *HydraParser) EthereumTypeGetErc20FromTx(tx *bchain.Tx) ([]bchain.Erc20Transfer, error) {
	var r []bchain.Erc20Transfer
	var err error
	csd, ok := tx.CoinSpecificData.(completeTransaction)
	if ok {
		if csd.Receipt != nil {
			r, err = hrc20GetTransfersFromLog(csd.Receipt.Logs)
		}
		if err != nil {
			return nil, err
		}
	}
	return r, nil
}

// GetEthereumTxData returns EthereumTxData from bchain.Tx
func GetEthereumTxData(tx *bchain.Tx) *eth.EthereumTxData {
	return GetEthereumTxDataFromSpecificData(tx.CoinSpecificData)
}

//FIXME
// GetEthereumTxDataFromSpecificData returns EthereumTxData from coinSpecificData
func GetEthereumTxDataFromSpecificData(coinSpecificData interface{}) *eth.EthereumTxData {

	etd := eth.EthereumTxData{Status: eth.TxStatusPending}
	csd, ok := coinSpecificData.(completeTransaction)
	if ok {
		if csd.Receipt != nil {
			switch csd.Receipt.Status {
			case "0x1":
				etd.Status = eth.TxStatusOK
			case "": // old transactions did not set status
				etd.Status = eth.TxStatusUnknown
			default:
				etd.Status = eth.TxStatusFailure
			}
			etd.GasUsed, _ = hexutil.DecodeBig(csd.Receipt.GasUsed)
		}
	}
	return &etd
}
