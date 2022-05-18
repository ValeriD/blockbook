package hydra

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"strings"

	"github.com/golang/glog"
	"github.com/martinboehm/btcd/blockchain"
	"github.com/martinboehm/btcd/wire"
	"github.com/martinboehm/btcutil/chaincfg"
	"github.com/trezor/blockbook/bchain"
	"github.com/trezor/blockbook/bchain/coins/btc"
	"github.com/trezor/blockbook/bchain/coins/utils"
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
}

// NewHydraParser returns new DashParser instance
func NewHydraParser(params *chaincfg.Params, c *btc.Configuration) *HydraParser {
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
	return bchain.ChainBitcoinType
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
	//FIXME
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

	return &tx, nil
}

func (p *HydraParser) EthereumTypeGetHrc20FromTx(tx *bchain.Tx) ([]bchain.Erc20Transfer, error) {
	var r []bchain.Erc20Transfer
	var err error
	csd, ok := tx.CoinSpecificData.(RpcReceipt)
	if ok {
		r, err = p.hrc20GetTransfersFromLog(csd.Logs)
		if err != nil {
			return nil, err
		}
	}
	return r, nil
}

const hrc20TransferEventSignature = "ddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef"

func (p *HydraParser) hrc20GetTransfersFromLog(logs []*RpcLog) ([]bchain.Erc20Transfer, error) {
	var r []bchain.Erc20Transfer
	for _, l := range logs {
		if len(l.Topics) == 3 && l.Topics[0] == hrc20TransferEventSignature {
			var t big.Int
			_, ok := t.SetString(l.Data, 0)
			if !ok {
				return nil, errors.New("Data is not a number")
			}
			// from, err := hex.DecodeString(l.Topics[1])
			// if err != nil {
			// 	return nil, err
			// }
			// to, err := hex.DecodeString(l.Topics[2])
			// if err != nil {
			// 	return nil, err
			// }
			r = append(r, bchain.Erc20Transfer{
				Contract: l.Address,
				From:     p.HydraAddressFromAddress(l.Topics[1]),
				To:       p.HydraAddressFromAddress(l.Topics[2]),
				Tokens:   t,
			})
			glog.Info(r[0].From)
		}
	}
	return r, nil
}

func (p *HydraParser) HydraAddressFromAddress(address string) string {
	s := strings.TrimLeft(address, "0")

	hexString, _ := hex.DecodeString(s)

	res, _, _ := p.GetAddressesFromAddrDesc(hexString)
	return res[0]
}

// func erc20GetTransfersFromTx(tx *eth.rpcTransaction) ([]bchain.Erc20Transfer, error) {
// 	var r []bchain.Erc20Transfer
// 	if len(tx.Payload) == 128+len(eth.erc20TransferMethodSignature) && strings.HasPrefix(tx.Payload, erc20TransferMethodSignature) {
// 		to, err := eth.addressFromPaddedHex(tx.Payload[len(eth.erc20TransferMethodSignature) : 64+len(erc20TransferMethodSignature)])
// 		if err != nil {
// 			return nil, err
// 		}
// 		var t big.Int
// 		_, ok := t.SetString(tx.Payload[len(eth.erc20TransferMethodSignature)+64:], 16)
// 		if !ok {
// 			return nil, errors.New("Data is not a number")
// 		}
// 		r = append(r, bchain.Erc20Transfer{
// 			Contract: eth.EIP55AddressFromAddress(tx.To),
// 			From:     eth.EIP55AddressFromAddress(tx.From),
// 			To:       eth.EIP55AddressFromAddress(to),
// 			Tokens:   t,
// 		})
// 	}
// 	return r, nil
// }
