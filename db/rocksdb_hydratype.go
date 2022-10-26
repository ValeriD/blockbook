package db

import (
	"bytes"

	"github.com/golang/glog"
	"github.com/juju/errors"
	"github.com/trezor/blockbook/bchain"
	"github.com/trezor/blockbook/bchain/coins/eth"
)

// GetAddrDescContracts returns AddrContracts for given addrDesc
func (d *RocksDB) GetHydraAddrDescContracts(addrDesc bchain.AddressDescriptor) (*AddrContracts, error) {
	glog.Infof("Here")
	val, err := d.db.GetCF(d.ro, d.cfh[cfAddressContracts], addrDesc)

	if err != nil {
		return nil, err
	}
	glog.Infof("%+v", val)
	defer val.Free()
	buf := val.Data()
	if len(buf) == 0 {
		return nil, nil
	}
	tt, l := unpackVaruint(buf)
	buf = buf[l:]
	nct, l := unpackVaruint(buf)
	buf = buf[l:]
	c := make([]AddrContract, 0, 4)
	glog.Infof("%v", len(buf))
	for len(buf) > 0 {
		if len(buf) < eth.EthereumTypeAddressDescriptorLen-2 {
			return nil, errors.New("Invalid data stored in cfAddressContracts for AddrDesc " + addrDesc.String())
		}
		txs, l := unpackVaruint(buf[eth.EthereumTypeAddressDescriptorLen:])
		contract := append(bchain.AddressDescriptor(nil), buf[:eth.EthereumTypeAddressDescriptorLen]...)
		c = append(c, AddrContract{
			Contract: contract,
			Txs:      txs,
		})
		buf = buf[eth.EthereumTypeAddressDescriptorLen+l:]
		glog.Infof("%v", len(buf))
	}
	return &AddrContracts{
		TotalTxs:       tt,
		NonContractTxs: nct,
		Contracts:      c,
	}, nil
}

func (d *RocksDB) addToAddressesAndContractsHydraType(addrDesc bchain.AddressDescriptor, btxID []byte, index int32, contract bchain.AddressDescriptor, addresses addressesMap, addressContracts map[string]*AddrContracts, addTxCount bool) error {
	var err error
	glog.Infof("Here")

	strAddrDesc := string(addrDesc)
	ac, e := addressContracts[strAddrDesc]
	if !e {
		ac, err = d.GetHydraAddrDescContracts(addrDesc)
		if err != nil {
			glog.Infof("Error when fetching addr desc contracts")
			return err
		}
		if ac == nil {
			glog.Infof("Empty addrContracts")
			ac = &AddrContracts{}
		}
		addressContracts[strAddrDesc] = ac
		d.cbs.balancesMiss++
	} else {
		d.cbs.balancesHit++
	}
	if contract == nil {
		if addTxCount {
			ac.NonContractTxs++
		}
	} else {
		// do not store contracts for 0x0000000000000000000000000000000000000000 address
		if !isZeroAddress(addrDesc) {
			// locate the contract and set i to the index in the array of contracts
			i, found := findContractInAddressContracts(contract, ac.Contracts)
			if !found {
				i = len(ac.Contracts)
				ac.Contracts = append(ac.Contracts, AddrContract{Contract: contract})
			}
			// index 0 is for ETH transfers, contract indexes start with 1
			if index < 0 {
				index = ^int32(i + 1)
			} else {
				index = int32(i + 1)
			}
			if addTxCount {
				ac.Contracts[i].Txs++
			}
		}
	}
	counted := addToAddressesMap(addresses, strAddrDesc, btxID, index)
	if !counted {
		ac.TotalTxs++
	}
	return nil
}

func (d *RocksDB) processAddressesHydraType(block *bchain.Block, addresses addressesMap, txAddressesMap map[string]*TxAddresses, balances map[string]*AddrBalance, addressContracts map[string]*AddrContracts) error {
	blockTxIDs := make([][]byte, len(block.Txs))
	blockTxAddresses := make([]*TxAddresses, len(block.Txs))
	// first process all outputs so that inputs can refer to txs in this block
	for txi := range block.Txs {
		tx := &block.Txs[txi]
		btxID, err := d.chainParser.PackTxid(tx.Txid)
		if err != nil {
			return err
		}
		blockTxIDs[txi] = btxID
		ta := TxAddresses{Height: block.Height}
		ta.Outputs = make([]TxOutput, len(tx.Vout))
		txAddressesMap[string(btxID)] = &ta
		blockTxAddresses[txi] = &ta
		for i, output := range tx.Vout {
			tao := &ta.Outputs[i]
			tao.ValueSat = output.ValueSat
			addrDesc, err := d.chainParser.GetAddrDescFromVout(&output)
			if err != nil || len(addrDesc) == 0 || len(addrDesc) > maxAddrDescLen {
				if err != nil {
					// do not log ErrAddressMissing, transactions can be without to address (for example eth contracts)
					if err != bchain.ErrAddressMissing {
						glog.Warningf("rocksdb: addrDesc: %v - height %d, tx %v, output %v, error %v", err, block.Height, tx.Txid, output, err)
					}
				} else {
					glog.V(1).Infof("rocksdb: height %d, tx %v, vout %v, skipping addrDesc of length %d", block.Height, tx.Txid, i, len(addrDesc))
				}
				continue
			}
			tao.AddrDesc = addrDesc
			if d.chainParser.IsAddrDescIndexable(addrDesc) {
				strAddrDesc := string(addrDesc)
				balance, e := balances[strAddrDesc]
				if !e {
					balance, err = d.GetAddrDescBalance(addrDesc, addressBalanceDetailUTXOIndexed)
					if err != nil {
						return err
					}
					if balance == nil {
						balance = &AddrBalance{}
					}
					balances[strAddrDesc] = balance
					d.cbs.balancesMiss++
				} else {
					d.cbs.balancesHit++
				}
				balance.BalanceSat.Add(&balance.BalanceSat, &output.ValueSat)
				balance.addUtxo(&Utxo{
					BtxID:    btxID,
					Vout:     int32(i),
					Height:   block.Height,
					ValueSat: output.ValueSat,
				})
				counted := addToAddressesMap(addresses, strAddrDesc, btxID, int32(i))
				if !counted {
					balance.Txs++
				}
			}
		}
	}
	// process inputs
	for txi := range block.Txs {
		tx := &block.Txs[txi]
		spendingTxid := blockTxIDs[txi]
		ta := blockTxAddresses[txi]
		ta.Inputs = make([]TxInput, len(tx.Vin))
		logged := false
		for i, input := range tx.Vin {
			tai := &ta.Inputs[i]
			btxID, err := d.chainParser.PackTxid(input.Txid)
			if err != nil {
				// do not process inputs without input txid
				if err == bchain.ErrTxidMissing {
					continue
				}
				return err
			}
			stxID := string(btxID)
			ita, e := txAddressesMap[stxID]
			if !e {
				ita, err = d.getTxAddresses(btxID)
				if err != nil {
					return err
				}
				if ita == nil {
					// allow parser to process unknown input, some coins may implement special handling, default is to log warning
					tai.AddrDesc = d.chainParser.GetAddrDescForUnknownInput(tx, i)
					continue
				}
				txAddressesMap[stxID] = ita
				d.cbs.txAddressesMiss++
			} else {
				d.cbs.txAddressesHit++
			}
			if len(ita.Outputs) <= int(input.Vout) {
				glog.Warningf("rocksdb: height %d, tx %v, input tx %v vout %v is out of bounds of stored tx", block.Height, tx.Txid, input.Txid, input.Vout)
				continue
			}
			spentOutput := &ita.Outputs[int(input.Vout)]
			if spentOutput.Spent {
				glog.Warningf("rocksdb: height %d, tx %v, input tx %v vout %v is double spend", block.Height, tx.Txid, input.Txid, input.Vout)
			}
			tai.AddrDesc = spentOutput.AddrDesc
			tai.ValueSat = spentOutput.ValueSat
			// mark the output as spent in tx
			spentOutput.Spent = true
			if len(spentOutput.AddrDesc) == 0 {
				if !logged {
					glog.V(1).Infof("rocksdb: height %d, tx %v, input tx %v vout %v skipping empty address", block.Height, tx.Txid, input.Txid, input.Vout)
					logged = true
				}
				continue
			}
			if d.chainParser.IsAddrDescIndexable(spentOutput.AddrDesc) {
				strAddrDesc := string(spentOutput.AddrDesc)
				balance, e := balances[strAddrDesc]
				if !e {
					balance, err = d.GetAddrDescBalance(spentOutput.AddrDesc, addressBalanceDetailUTXOIndexed)
					if err != nil {
						return err
					}
					if balance == nil {
						balance = &AddrBalance{}
					}
					balances[strAddrDesc] = balance
					d.cbs.balancesMiss++
				} else {
					d.cbs.balancesHit++
				}
				counted := addToAddressesMap(addresses, strAddrDesc, spendingTxid, ^int32(i))
				if !counted {
					balance.Txs++
				}
				balance.BalanceSat.Sub(&balance.BalanceSat, &spentOutput.ValueSat)
				balance.markUtxoAsSpent(btxID, int32(input.Vout))
				if balance.BalanceSat.Sign() < 0 {
					d.resetValueSatToZero(&balance.BalanceSat, spentOutput.AddrDesc, "balance")
				}
				balance.SentSat.Add(&balance.SentSat, &spentOutput.ValueSat)
			}
		}
	}
	for txi := range block.Txs {
		tx := &block.Txs[txi]
		btxID, err := d.chainParser.PackTxid(tx.Txid)
		erc20, err := d.chainParser.EthereumTypeGetErc20FromTx(tx)
		if err != nil {
			continue
		}
		for i, t := range erc20 {
			var contract, from, to bchain.AddressDescriptor
			contract, err = d.chainParser.GetAddrDescFromAddress(t.Contract)
			if err == nil {
				from, err = d.chainParser.GetAddrDescFromAddress(t.From)
				if err == nil {
					to, err = d.chainParser.GetAddrDescFromAddress(t.To)
				}
			}

			if err != nil {
				glog.Warningf("rocksdb: GetErc20FromTx %v - height %d, tx %v, transfer %v", err, block.Height, tx.Txid, t)
				continue
			}

			if err = d.addToAddressesAndContractsHydraType(to, btxID, int32(i), contract, addresses, addressContracts, true); err != nil {
				return err
			}
			eq := bytes.Equal(from, to)
			if err = d.addToAddressesAndContractsHydraType(from, btxID, ^int32(i), contract, addresses, addressContracts, !eq); err != nil {
				return err
			}
		}
	}
	return nil
}
