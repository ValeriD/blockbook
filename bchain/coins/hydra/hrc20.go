package hydra

import (
	"bytes"
	"encoding/hex"
	"math/big"
	"sync"
	"unicode/utf8"

	"github.com/dcb9/go-ethereum/common/hexutil"
	ethcommon "github.com/ethereum/go-ethereum/common"
	"github.com/golang/glog"
	"github.com/juju/errors"
	"github.com/trezor/blockbook/bchain"
	"github.com/trezor/blockbook/bchain/coins/eth"
)

var hrc20abi = `[{"constant":true,"inputs":[],"name":"name","outputs":[{"name":"","type":"string"}],"payable":false,"type":"function","signature":"0x06fdde03"},
{"constant":true,"inputs":[],"name":"symbol","outputs":[{"name":"","type":"string"}],"payable":false,"type":"function","signature":"0x95d89b41"},
{"constant":true,"inputs":[],"name":"decimals","outputs":[{"name":"","type":"uint8"}],"payable":false,"type":"function","signature":"0x313ce567"},
{"constant":true,"inputs":[],"name":"totalSupply","outputs":[{"name":"","type":"uint256"}],"payable":false,"type":"function","signature":"0x18160ddd"},
{"constant":true,"inputs":[{"name":"_owner","type":"address"}],"name":"balanceOf","outputs":[{"name":"balance","type":"uint256"}],"payable":false,"type":"function","signature":""},
{"constant":false,"inputs":[{"name":"_to","type":"address"},{"name":"_value","type":"uint256"}],"name":"transfer","outputs":[{"name":"success","type":"bool"}],"payable":false,"type":"function","signature":"0xa9059cbb"},
{"constant":false,"inputs":[{"name":"_from","type":"address"},{"name":"_to","type":"address"},{"name":"_value","type":"uint256"}],"name":"transferFrom","outputs":[{"name":"success","type":"bool"}],"payable":false,"type":"function","signature":"0x23b872dd"},
{"constant":false,"inputs":[{"name":"_spender","type":"address"},{"name":"_value","type":"uint256"}],"name":"approve","outputs":[{"name":"success","type":"bool"}],"payable":false,"type":"function","signature":"0x095ea7b3"},
{"constant":true,"inputs":[{"name":"_owner","type":"address"},{"name":"_spender","type":"address"}],"name":"allowance","outputs":[{"name":"remaining","type":"uint256"}],"payable":false,"type":"function","signature":"0xdd62ed3e"},
{"anonymous":false,"inputs":[{"indexed":true,"name":"_from","type":"address"},{"indexed":true,"name":"_to","type":"address"},{"indexed":false,"name":"_value","type":"uint256"}],"name":"Transfer","type":"event","signature":"0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef"},
{"anonymous":false,"inputs":[{"indexed":true,"name":"_owner","type":"address"},{"indexed":true,"name":"_spender","type":"address"},{"indexed":false,"name":"_value","type":"uint256"}],"name":"Approval","type":"event","signature":"0x8c5be1e5ebec7d5bd14f71427d1e84f3dd0314c0f7b2291e5b200ac8c7c3b925"},
{"inputs":[{"name":"_initialAmount","type":"uint256"},{"name":"_tokenName","type":"string"},{"name":"_decimalUnits","type":"uint8"},{"name":"_tokenSymbol","type":"string"}],"payable":false,"type":"constructor"},
{"constant":false,"inputs":[{"name":"_spender","type":"address"},{"name":"_value","type":"uint256"},{"name":"_extraData","type":"bytes"}],"name":"approveAndCall","outputs":[{"name":"success","type":"bool"}],"payable":false,"type":"function","signature":"0xcae9ca51"},
{"constant":true,"inputs":[],"name":"version","outputs":[{"name":"","type":"string"}],"payable":false,"type":"function","signature":"0x54fd4d50"}]`

var cachedContracts = make(map[string]*bchain.Erc20Contract)
var cachedContractsMux sync.Mutex

const hrc20TransferMethodSignature = "a9059cbb"
const hrc20TransferEventSignature = "ddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef"
const hrc20NameSignature = "06fdde03"
const hrc20SymbolSignature = "95d89b41"
const hrc20DecimalsSignature = "313ce567"
const hrc20BalanceOf = "70a08231"

func addressFromPaddedHex(s string) (string, error) {
	var t big.Int
	var ok bool
	if has0xPrefix(s) {
		_, ok = t.SetString(s[2:], 16)
	} else {
		_, ok = t.SetString(s, 16)
	}
	if !ok {
		return "", errors.New("Data is not a number")
	}
	a := ethcommon.BigToAddress(&t)
	return a.String(), nil
}

func hrc20GetTransfersFromLog(logs []*rpcLog) ([]bchain.Erc20Transfer, error) {
	var r []bchain.Erc20Transfer
	for _, l := range logs {
		if len(l.Topics) == 3 && l.Topics[0] == hrc20TransferEventSignature {
			var t big.Int
			_, ok := t.SetString(l.Data, 0)
			if !ok {
				return nil, errors.New("Data is not a number")
			}
			from, err := addressFromPaddedHex(l.Topics[1])
			if err != nil {
				return nil, err
			}
			to, err := addressFromPaddedHex(l.Topics[2])
			if err != nil {
				return nil, err
			}
			r = append(r, bchain.Erc20Transfer{
				Contract: l.Address,
				From:     from,
				To:       to,
				Tokens:   t,
			})
		}
	}
	return r, nil
}

type cmdCallContract struct {
	Method string   `json:"method"`
	Params []string `json:"params"`
}

type resCallContract struct {
	Error  *bchain.RPCError `json:"error"`
	Result struct {
		ExecutionResult struct {
			output string `json:"output"`
		} `json:"executionResult"`
	}
}

func (b *HydraRPC) hydraCall(data, to string) (string, error) {

	req := &cmdCallContract{Method: "callcontract"}
	req.Params = append(req.Params, to)
	req.Params = append(req.Params, data)

	res := &resCallContract{}

	err := b.Call(req, res)
	if err == nil {
		return "", err
	}
	if res.Error != nil {
		return "", res.Error
	}

	return res.Result.ExecutionResult.output, nil
}

func parseErc20NumericProperty(contractDesc bchain.AddressDescriptor, data string) *big.Int {
	if has0xPrefix(data) {
		data = data[2:]
	}
	if len(data) > 64 {
		data = data[:64]
	}
	if len(data) == 64 {
		var n big.Int
		_, ok := n.SetString(data, 16)
		if ok {
			return &n
		}
	}
	if glog.V(1) {
		glog.Warning("Cannot parse '", data, "' for contract ", contractDesc)
	}
	return nil
}

func parseHrc20StringProperty(contractDesc bchain.AddressDescriptor, data string) string {
	if has0xPrefix(data) {
		data = data[2:]
	}
	if len(data) > 128 {
		n := parseErc20NumericProperty(contractDesc, data[64:128])
		if n != nil {
			l := n.Uint64()
			if l > 0 && 2*int(l) <= len(data)-128 {
				b, err := hex.DecodeString(data[128 : 128+2*l])
				if err == nil {
					return string(b)
				}
			}
		}
	}
	// allow string properties as UTF-8 data
	b, err := hex.DecodeString(data)
	if err == nil {
		i := bytes.Index(b, []byte{0})
		if i > 32 {
			i = 32
		}
		if i > 0 {
			b = b[:i]
		}
		if utf8.Valid(b) {
			return string(b)
		}
	}
	if glog.V(1) {
		glog.Warning("Cannot parse '", data, "' for contract ", contractDesc)
	}
	return ""
}

// EthereumTypeGetErc20ContractInfo returns information about ERC20 contract
func (b *HydraRPC) EthereumTypeGetErc20ContractInfo(contractDesc bchain.AddressDescriptor) (*bchain.Erc20Contract, error) {
	cds := string(contractDesc)
	cachedContractsMux.Lock()
	contract, found := cachedContracts[cds]
	cachedContractsMux.Unlock()
	if !found {
		address := hexutil.Encode(contractDesc)
		data, err := b.hydraCall(hrc20NameSignature, address)
		if err != nil {
			// ignore the error from the eth_call - since geth v1.9.15 they changed the behavior
			// and returning error "execution reverted" for some non contract addresses
			// https://github.com/ethereum/go-ethereum/issues/21249#issuecomment-648647672
			glog.Warning(errors.Annotatef(err, "erc20NameSignature %v", address))
			return nil, nil
			// return nil, errors.Annotatef(err, "erc20NameSignature %v", address)
		}
		name := parseHrc20StringProperty(contractDesc, data)
		if name != "" {
			data, err = b.hydraCall(hrc20SymbolSignature, address)
			if err != nil {
				glog.Warning(errors.Annotatef(err, "erc20SymbolSignature %v", address))
				return nil, nil
				// return nil, errors.Annotatef(err, "erc20SymbolSignature %v", address)
			}
			symbol := parseHrc20StringProperty(contractDesc, data)
			data, err = b.hydraCall(hrc20DecimalsSignature, address)
			if err != nil {
				glog.Warning(errors.Annotatef(err, "erc20DecimalsSignature %v", address))
				// return nil, errors.Annotatef(err, "erc20DecimalsSignature %v", address)
			}
			contract = &bchain.Erc20Contract{
				Contract: address,
				Name:     name,
				Symbol:   symbol,
			}
			d := parseErc20NumericProperty(contractDesc, data)
			if d != nil {
				contract.Decimals = int(uint8(d.Uint64()))
			} else {
				contract.Decimals = eth.EtherAmountDecimalPoint
			}
		} else {
			contract = nil
		}
		cachedContractsMux.Lock()
		cachedContracts[cds] = contract
		cachedContractsMux.Unlock()
	}
	return contract, nil
}

// EthereumTypeGetErc20ContractBalance returns balance of ERC20 contract for given address
func (b *HydraRPC) EthereumTypeGetErc20ContractBalance(addrDesc, contractDesc bchain.AddressDescriptor) (*big.Int, error) {
	addr := hexutil.Encode(addrDesc)
	contract := hexutil.Encode(contractDesc)
	req := hrc20BalanceOf + "0000000000000000000000000000000000000000000000000000000000000000"[len(addr)-2:] + addr[2:]
	data, err := b.hydraCall(req, contract)
	if err != nil {
		return nil, err
	}
	r := parseErc20NumericProperty(contractDesc, data)
	if r == nil {
		return nil, errors.New("Invalid balance")
	}
	return r, nil
}

type cmdGetTransactionReceipt struct {
	Method string `json:"method"`
	Params struct {
		Hash string `json:"hash"`
	} `json:"params"`
}

type resGetTransactionReceipt struct {
	Error  *bchain.RPCError `json:"error"`
	Result rpcReceipt       `json:"result"`
}

func (b *HydraRPC) getLogsForTx(txid string) (*rpcReceipt, error) {

	res := resGetTransactionReceipt{}
	req := cmdGetTransactionReceipt{Method: "gettransactionreceipt"}
	req.Params.Hash = txid

	err := b.Call(req, res)

	if err != nil {
		return nil, err
	}

	if res.Error != nil {
		return nil, res.Error
	}

	return &res.Result, nil
}
