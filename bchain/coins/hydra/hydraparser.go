package hydra

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math/big"
	"strconv"

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
	MainNetParams.CoinbaseMaturity = 500
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
}

// NewHydraParser returns new DashParser instance
func NewHydraParser(params *chaincfg.Params, c *btc.Configuration) *HydraParser {
	return &HydraParser{
		BitcoinLikeParser: btc.NewBitcoinLikeParser(params, c),
	}
}

// GetChainType returns EthereumType
func (p *HydraParser) GetChainType() bchain.ChainType {
	return bchain.ChainHydraType
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

type completeTransaction struct {
	Tx      *bchain.Tx         `json:"tx"`
	Receipt *bchain.RpcReceipt `json:"receipt,omitempty"`
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

func (p *HydraParser) GetAddrDescFromAddress(address string) (bchain.AddressDescriptor, error) {
	if has0xPrefix(address) {
		address = address[2:]
	}

	if len(address) == eth.EthereumTypeAddressDescriptorLen*2 {
		return hex.DecodeString(address)
	}
	return p.BitcoinLikeParser.GetAddrDescFromAddress(address)
}

func (p *HydraParser) PackTx(tx *bchain.Tx, height uint32, blockTime int64) ([]byte, error) {
	var err error
	r, ok := tx.CoinSpecificData.(completeTransaction)
	if !ok {
		return nil, errors.New("Missing CoinSpecificData")
	}

	pti := make([]*ProtoCompleteTransaction_VinType, len(tx.Vin))
	for i, vi := range tx.Vin {
		hexstr, err := hex.DecodeString(vi.ScriptSig.Hex)
		if err != nil {
			return nil, errors.Annotatef(err, "Vin %v Hex %v", i, vi.ScriptSig.Hex)
		}
		itxid, err := p.PackTxid(vi.Txid)
		if err != nil && err != bchain.ErrTxidMissing {
			return nil, errors.Annotatef(err, "Vin %v Txid %v", i, vi.Txid)
		}

		// TODO: Not sure if this is needed.
		vinHexAddresses := vi.Addresses

		for i2, v := range vinHexAddresses {
			vinHexAddresses[i2] = hex.EncodeToString([]byte(v))

		}

		pti[i] = &ProtoCompleteTransaction_VinType{
			Addresses:    vinHexAddresses,
			Coinbase:     vi.Coinbase,
			ScriptSigHex: hexstr,
			Sequence:     vi.Sequence,
			Txid:         itxid,
			Vout:         vi.Vout,
		}
	}
	pto := make([]*ProtoCompleteTransaction_VoutType, len(tx.Vout))
	for i, vo := range tx.Vout {

		//  TODO: Not sure if this is needed too.
		vinHexAddresses := vo.ScriptPubKey.Addresses

		for i2, v := range vinHexAddresses {
			vinHexAddresses[i2] = hex.EncodeToString([]byte(v))
		}

		hexstr, err := hex.DecodeString(vo.ScriptPubKey.Hex)

		if err != nil {
			return nil, errors.Annotatef(err, "Vout %v Hex %v", i, vo.ScriptPubKey.Hex)
		}
		pto[i] = &ProtoCompleteTransaction_VoutType{
			Addresses:       vinHexAddresses,
			N:               vo.N,
			ScriptPubKeyHex: hexstr,
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
	if r.Receipt != nil {
		pt.Receipt = &ProtoCompleteTransaction_ReceiptType{}
		if pt.Receipt.GasUsed, err = hexDecodeBig(string(r.Receipt.GasUsed)); err != nil {
			return nil, errors.Annotatef(err, "GasUsed %v", r.Receipt.GasUsed)
		}
		if pt.Receipt.GasLimit, err = hexDecodeBig(r.Receipt.GasLimit); err != nil {
			return nil, errors.Annotatef(err, "GasLimit %v", r.Receipt.GasLimit)
		}
		if pt.Receipt.GasPrice, err = hexDecodeBig(r.Receipt.GasPrice); err != nil {
			return nil, errors.Annotatef(err, "Price %v", r.Receipt.GasPrice)
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
			l.Address = "0x" + l.Address
			a, err := hexutil.Decode(l.Address)
			if err != nil {
				return nil, errors.Annotatef(err, "Address cannot be decoded %v", l)
			}
			d, err := hexutil.Decode("0x" + l.Data)
			if err != nil {
				return nil, errors.Annotatef(err, "Data cannot be decoded %v", l)
			}
			t := make([][]byte, len(l.Topics))
			for j, s := range l.Topics {
				t[j], err = hexutil.Decode("0x" + s)
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

		// TODO: this is for unhexing if above is used.
		hexToNormalAddresses := pti.Addresses
		// for i2, v := range hexToNormalAddresses {
		// 	res, err := hex.DecodeString(v)
		// 	hexToNormalAddresses[i2] = string(res)
		// }

		vin[i] = bchain.Vin{
			Addresses: hexToNormalAddresses,
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

		hexToNormalAddresses := pto.Addresses
		//for i2, v := range hexToNormalAddresses {
		//res, err := hex.DecodeString(v)
		//hexToNormalAddresses[i2] = string(res)
		//}
		var vs big.Int
		vs.SetBytes(pto.ValueSat)

		vout[i] = bchain.Vout{
			N: pto.N,
			ScriptPubKey: bchain.ScriptPubKey{
				Addresses: hexToNormalAddresses,
				Hex:       hex.EncodeToString(pto.ScriptPubKeyHex),
			},
			ValueSat: vs,
		}
	}
	var rr *bchain.RpcReceipt
	if pt.Receipt != nil {
		logs := make([]*bchain.RpcLog, len(pt.Receipt.Log))
		for i, l := range pt.Receipt.Log {
			topics := make([]string, len(l.Topics))
			for j, t := range l.Topics {
				topics[j] = hexutil.Encode(t)[2:]
			}
			logs[i] = &bchain.RpcLog{
				Address: hexutil.Encode(l.Address)[2:],
				Data:    hexutil.Encode(l.Data),
				Topics:  topics,
			}
		}
		status := ""
		// handle a special value []byte{'U'} as unknown state
		if len(pt.Receipt.Status) != 1 || pt.Receipt.Status[0] != 'U' {
			status = hexEncodeBig(pt.Receipt.Status)
		}
		rr = &bchain.RpcReceipt{
			GasUsed:  hexEncodeBig(pt.Receipt.GasUsed),
			GasLimit: hexEncodeBig(pt.Receipt.GasLimit),
			GasPrice: hexEncodeBig(pt.Receipt.GasPrice),
			Status:   status,
			Logs:     logs,
		}
	}

	tx := bchain.Tx{
		Blocktime: int64(pt.Blocktime),
		Hex:       hex.EncodeToString(pt.Hex),
		LockTime:  pt.Locktime,
		Time:      22222,
		Txid:      txid,
		Vin:       vin,
		Vout:      vout,
		Version:   pt.Version,
	}

	completeTx := completeTransaction{
		Tx:      &tx,
		Receipt: rr,
	}
	tx.CoinSpecificData = completeTx
	return &tx, pt.Height, nil
}

func getEthSpecificDataFromVouts(vouts []bchain.Vout, receipt *bchain.RpcReceipt) {
	for _, v := range vouts {
		if len(v.ScriptPubKey.Addresses) == 0 {
			gasPrice, _ := hex.DecodeString("20")
			receipt.GasLimit = getGasLimitFromHex(v.ScriptPubKey.Hex)
			receipt.GasPrice = hexEncodeBig(gasPrice)
		}
	}
}

func getGasLimitFromHex(data string) string {
	// Retrieve the size of the gas limit data
	var sizeStart int64
	var sizeEnd int64
	if data[0:2] == "01" {
		sizeStart = 4
		sizeEnd = 6
	} else {
		sizeStart = 2
		sizeEnd = 4
	}
	size, _ := strconv.ParseInt(data[sizeStart:sizeEnd], 16, 0)

	gasLimitLast := size*2 + sizeEnd

	//Fetch the size
	b, _ := hex.DecodeString(data[sizeEnd:gasLimitLast])
	if len(b) < 8 {
		b = append(b, []byte{0x00}...)
	}

	a := binary.LittleEndian.Uint32(b)

	s := strconv.FormatUint(uint64(a), 16)
	// if len(s)%2 != 0 {
	// 	s = "0" + s
	// }
	return "0x" + s
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

func (p *HydraParser) GetTransactionHydraParser(txid string) (*bchain.RpcReceipt, error) {
	secondlogsTx, _ := MyHydraRPC.GetTransaction(txid)

	mysecondlogs, _ := MyHydraRPC.getLogsForTx(secondlogsTx.Txid)

	if mysecondlogs != nil {
		if len(mysecondlogs.Logs) != 0 {
			for _, rl := range mysecondlogs.Logs {
				fmt.Println("Log2: ")
				fmt.Println(rl.Address)
				fmt.Println(rl.Data)
				fmt.Println(rl.Topics)
			}
		}
		return mysecondlogs, nil
	}

	return nil, nil
}

func (p *HydraParser) EthereumTypeGetErc20FromTx(tx *bchain.Tx) ([]bchain.Erc20Transfer, error) {
	fmt.Printf("Called on: %s \n", tx.Txid)
	var r []bchain.Erc20Transfer
	var err error
	csd, ok := tx.CoinSpecificData.(completeTransaction)

	// if csd.Tx != nil {
	// 	fmt.Println("Complete tx: ")
	// 	fmt.Println(csd.Tx)
	// }
	// if len(&csd.Tx.Vin) != 0 && len(csd.Tx.Vout) != 0 {
	fmt.Println(csd)
	if csd.Tx != nil {
		for _, v := range csd.Tx.Vin {
			for _, v2 := range v.Addresses {
				fmt.Println("vin Tx address: " + v2)
			}
		}
		for _, v := range csd.Tx.Vout {
			for _, v2 := range v.ScriptPubKey.Addresses {
				fmt.Println("vout Tx address: " + v2)
			}
		}
	}

	// }

	if ok {

		fmt.Println("Receipt tx: ")
		fmt.Println(&csd.Receipt)
		fmt.Println(&csd.Receipt == nil)
		fmt.Println(csd.Receipt == nil)
	}
	if ok {
		if csd.Receipt != nil {
			r, err = p.hrc20GetTransfersFromLog(csd.Receipt.Logs)
			fmt.Printf("hrc20transfers: %s \n", r)
		}
		if err != nil {
			fmt.Println("Err is here: ", err)
			return nil, err
		}
	}
	fmt.Println("Returning hrc transfers: ")
	fmt.Println(r)
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
			etd.GasLimit, _ = hexutil.DecodeBig(csd.Receipt.GasLimit)
			etd.GasUsed, _ = hexutil.DecodeBig(csd.Receipt.GasUsed)
			etd.GasPrice, _ = hexutil.DecodeBig(csd.Receipt.GasPrice)
		}
	}
	return &etd
}
