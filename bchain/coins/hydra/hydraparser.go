package hydra

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"strings"

	"github.com/bsm/go-vlq"
	"github.com/golang/glog"
	"github.com/martinboehm/btcd/blockchain"
	"github.com/martinboehm/btcd/wire"
	"github.com/martinboehm/btcutil/chaincfg"
	"github.com/trezor/blockbook/bchain"
	"github.com/trezor/blockbook/bchain/coins/btc"
	"github.com/trezor/blockbook/bchain/coins/utils"

	"github.com/ethereum/go-ethereum/common/hexutil"
)

// magic numbers
const (
	MainnetMagic            wire.BitcoinNet = 0xafeae9f7
	TestnetMagic            wire.BitcoinNet = 0x07131f03
	EtherAmountDecimalPoint                 = 18
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
}

// NewHydraParser returns new DashParser instance
func NewHydraParser(params *chaincfg.Params, c *btc.Configuration) *HydraParser {
	c.MinimumCoinbaseConfirmations = 500
	return &HydraParser{
		BitcoinLikeParser: btc.NewBitcoinLikeParser(params, c),
	}
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

func parseBlockHeader(r io.Reader) (*wire.BlockHeader, error) {
	h := &wire.BlockHeader{}
	err := h.Deserialize(r)
	if err != nil {
		return nil, err
	}

	// hash_state_root 32
	// hash_utxo_root 32
	// hash_prevout_stake 32
	// hash_prevout_n 4
	buf := make([]byte, 100)
	_, err = io.ReadFull(r, buf)
	if err != nil {
		return nil, err
	}

	sigLength, err := wire.ReadVarInt(r, 0)
	if err != nil {
		return nil, err
	}
	sigBuf := make([]byte, sigLength)
	_, err = io.ReadFull(r, sigBuf)
	if err != nil {
		return nil, err
	}

	return h, err
}

func (p *HydraParser) GetChainType() bchain.ChainType {
	return bchain.ChainHydraType
}

// TxFromMsgTx converts bitcoin wire Tx to bchain.Tx
func (p *HydraParser) TxFromMsgTx(t *wire.MsgTx, parseAddresses bool) bchain.Tx {
	vin := make([]bchain.Vin, len(t.TxIn))
	for i, in := range t.TxIn {
		if blockchain.IsCoinBaseTx(t) {
			vin[i] = bchain.Vin{
				Coinbase: hex.EncodeToString(in.SignatureScript),
				Sequence: in.Sequence,
			}
			break
		}
		s := bchain.ScriptSig{
			Hex: hex.EncodeToString(in.SignatureScript),
			// missing: Asm,
		}
		vin[i] = bchain.Vin{
			Txid:      in.PreviousOutPoint.Hash.String(),
			Vout:      in.PreviousOutPoint.Index,
			Sequence:  in.Sequence,
			ScriptSig: s,
		}
	}
	vout := make([]bchain.Vout, len(t.TxOut))
	for i, out := range t.TxOut {
		addrs := []string{}
		if parseAddresses {
			addrs, _, _ = p.OutputScriptToAddressesFunc(out.PkScript)
		}
		s := bchain.ScriptPubKey{
			Hex:       hex.EncodeToString(out.PkScript),
			Addresses: addrs,
			// missing: Asm,
			// missing: Type,
		}
		var vs big.Int
		vs.SetInt64(out.Value)

		vout[i] = bchain.Vout{
			ValueSat:     vs,
			N:            uint32(i),
			ScriptPubKey: s,
		}
	}

	tx := bchain.Tx{
		Txid:     t.TxHash().String(),
		Version:  t.Version,
		LockTime: t.LockTime,
		Vin:      vin,
		Vout:     vout,
		// skip: BlockHash,
		// skip: Confirmations,
		// skip: Time,
		// skip: Blocktime,
	}
	return tx
}

// UnpackTx unpacks transaction from byte array
func (p *HydraParser) UnpackTx(buf []byte) (*bchain.Tx, uint32, error) {
	height := binary.BigEndian.Uint32(buf)
	bt, l := vlq.Int(buf[4:])
	tx, err := p.ParseTx(buf[4+l:])
	if err != nil {
		return nil, 0, err
	}
	tx.Blocktime = bt

	return tx, height, nil
}

// ParseTx parses byte array containing transaction and returns Tx struct
func (p *HydraParser) ParseTx(b []byte) (*bchain.Tx, error) {
	t := wire.MsgTx{}
	r := bytes.NewReader(b)
	if err := t.Deserialize(r); err != nil {
		return nil, err
	}
	tx := p.TxFromMsgTx(&t, true)
	tx.Hex = hex.EncodeToString(b)
	return &tx, nil
}

func (p *HydraParser) ParseBlock(b []byte) (*bchain.Block, error) {
	r := bytes.NewReader(b)
	w := wire.MsgBlock{}

	h, err := parseBlockHeader(r)
	if err != nil {
		return nil, err
	}

	err = utils.DecodeTransactions(r, 0, wire.WitnessEncoding, &w)
	if err != nil {
		return nil, err
	}

	txs := make([]bchain.Tx, len(w.Transactions))
	for ti, t := range w.Transactions {
		txs[ti] = p.TxFromMsgTx(t, false)
	}

	return &bchain.Block{
		BlockHeader: bchain.BlockHeader{
			Size: len(b),
			Time: h.Timestamp.Unix(),
		},
		Txs: txs,
	}, nil
}

// ParseTxFromJson parses JSON message containing transaction and returns Tx struct
func (p *HydraParser) ParseTxFromJson(msg json.RawMessage) (*bchain.Tx, error) {
	var tx bchain.Tx
	err := json.Unmarshal(msg, &tx)
	if err != nil {
		return nil, err
	}

	for i := range tx.Vout {
		vout := &tx.Vout[i]
		// convert vout.JsonValue to big.Int and clear it, it is only temporary value used for unmarshal
		vout.ValueSat, err = p.AmountToBigInt(vout.JsonValue)
		if err != nil {
			return nil, err
		}
		vout.JsonValue = ""

		if vout.ScriptPubKey.Addresses == nil {
			vout.ScriptPubKey.Addresses = []string{}
		}
	}
	t, err := p.EthereumTypeGetErc20FromTx(tx)

	return &tx, nil
}

func (p *HydraParser) EthereumTypeGetErc20FromTx(tx *bchain.Tx) ([]bchain.Erc20Transfer, error) {
	var r []bchain.Erc20Transfer
	var err error
	csd, ok := tx.CoinSpecificData.(rpcReceipt)
	if ok {
		r, err = p.hrc20GetTransfersFromLog(csd.Logs)
		if err != nil {
			return nil, err
		}
	}
	return r, nil
}

func (p *HydraParser) hrc20GetTransfersFromLog(logs []*rpcLog) ([]bchain.Erc20Transfer, error) {
	var r []bchain.Erc20Transfer
	for _, l := range logs {
		if len(l.Topics) == 3 && l.Topics[0] == hrc20TransferEventSignature {
			var t big.Int

			_, ok := t.SetString(l.Data, 0)
			glog.Info(ok)
			if !ok {
				return nil, errors.New("Data is not a number")
			}
			from, err := p.HydraAddressFromAddress(l.Topics[1])
			if err != nil {
				return nil, err
			}
			to, err := p.HydraAddressFromAddress(l.Topics[2])
			if err != nil {
				return nil, err
			}
			r = append(r, bchain.Erc20Transfer{
				Contract: l.Address,
				From:     from,
				To:       to,
				Tokens:   t,
			})
			glog.Info(r[0].From)
		}
	}
	return r, nil
}

//FIXME
// func (p *HydraParser) hrc20GetTransfersFromTx(tx *rpcTransaction) ([]bchain.Erc20Transfer, error) {
// 	var r []bchain.Erc20Transfer
// 	if len(tx.Payload) == 128+len(hrc20TransferMethodSignature) && strings.HasPrefix(tx.Payload, erc20TransferMethodSignature) {
// 		to, err := addressFromPaddedHex(tx.Payload[len(hrc20TransferMethodSignature) : 64+len(erc20TransferMethodSignature)])
// 		if err != nil {
// 			return nil, err
// 		}
// 		var t big.Int
// 		_, ok := t.SetString(tx.Payload[len(hrc20TransferMethodSignature)+64:], 16)
// 		if !ok {
// 			return nil, errors.New("Data is not a number")
// 		}

// 		r = append(r, bchain.Erc20Transfer{
// 			Contract: tx.To,
// 			From:     p.HydraAddressFromAddress(tx.From),
// 			To:       p.HydraAddressFromAddress(to),
// 			Tokens:   t,
// 		})
// 	}
// 	return r, nil
// }

func (p *HydraParser) HydraAddressFromAddress(address string) (string, error) {
	s := strings.TrimLeft(address, "0")

	hexString, _ := hex.DecodeString(s)

	res, _, err := p.GetAddressesFromAddrDesc(hexString)
	if err != nil {
		return "", err
	}
	return res[0], nil
}

// // EthereumTypeAddressDescriptorLen - in case of EthereumType, the AddressDescriptor has fixed length
// const EthereumTypeAddressDescriptorLen = 20

// type rpcLog struct {
// 	Address string   `json:"address"`
// 	Topics  []string `json:"topics"`
// 	Data    string   `json:"data"`
// }

// type rpcLogWithTxHash struct {
// 	rpcLog
// 	Hash string `json:"transactionHash"`
// }

// type rpcReceipt struct {
// 	GasUsed string    `json:"gasUsed"`
// 	Status  string    `json:"status"`
// 	Logs    []*rpcLog `json:"logs"`
// }

// type completeTransaction struct {
// 	Tx      *rpcTransaction `json:"tx"`
// 	Receipt *rpcReceipt     `json:"receipt,omitempty"`
// }

// func (p *EthereumParser) ethTxToTx(tx *rpcTransaction, receipt *rpcReceipt, blocktime int64, confirmations uint32, fixEIP55 bool) (*bchain.Tx, error) {
// 	txid := tx.Hash
// 	var (
// 		fa, ta []string
// 		err    error
// 	)
// 	if len(tx.From) > 2 {
// 		if fixEIP55 {
// 			tx.From = EIP55AddressFromAddress(tx.From)
// 		}
// 		fa = []string{tx.From}
// 	}
// 	if len(tx.To) > 2 {
// 		if fixEIP55 {
// 			tx.To = EIP55AddressFromAddress(tx.To)
// 		}
// 		ta = []string{tx.To}
// 	}
// 	if fixEIP55 && receipt != nil && receipt.Logs != nil {
// 		for _, l := range receipt.Logs {
// 			if len(l.Address) > 2 {
// 				l.Address = EIP55AddressFromAddress(l.Address)
// 			}
// 		}
// 	}
// 	ct := completeTransaction{
// 		Tx:      tx,
// 		Receipt: receipt,
// 	}
// 	vs, err := hexutil.DecodeBig(tx.Value)
// 	if err != nil {
// 		return nil, err
// 	}
// 	return &bchain.Tx{
// 		Blocktime:     blocktime,
// 		Confirmations: confirmations,
// 		// Hex
// 		// LockTime
// 		Time: blocktime,
// 		Txid: txid,
// 		Vin: []bchain.Vin{
// 			{
// 				Addresses: fa,
// 				// Coinbase
// 				// ScriptSig
// 				// Sequence
// 				// Txid
// 				// Vout
// 			},
// 		},
// 		Vout: []bchain.Vout{
// 			{
// 				N:        0, // there is always up to one To address
// 				ValueSat: *vs,
// 				ScriptPubKey: bchain.ScriptPubKey{
// 					// Hex
// 					Addresses: ta,
// 				},
// 			},
// 		},
// 		CoinSpecificData: ct,
// 	}, nil
// }

func hexDecode(s string) ([]byte, error) {
	b, err := hexutil.Decode(s)
	if err != nil && err != hexutil.ErrEmptyString {
		return nil, err
	}
	return b, nil
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
func has0xPrefix(s string) bool {
	return len(s) >= 2 && s[0] == '0' && (s[1]|32) == 'x'
}
