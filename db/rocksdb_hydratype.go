package db

import (
	"bytes"
	"encoding/hex"
	"fmt"

	"github.com/golang/glog"
	"github.com/juju/errors"
	"github.com/trezor/blockbook/bchain"
	"github.com/trezor/blockbook/bchain/coins/eth"
)

// GetAddrDescContracts returns AddrContracts for given addrDesc
func (d *RocksDB) GetHydraAddrDescContracts(addrDesc bchain.AddressDescriptor) (*AddrContracts, error) {
	glog.Infof("Here")
	glog.Warningf("addr: %d \n", addrDesc)
	val, err := d.db.GetCF(d.ro, d.cfh[cfAddresses], addrDesc)
	iterate2 := d.db.NewIteratorCF(d.ro, d.cfh[cfAddressContracts])

	for iterate2.SeekToFirst(); iterate2.Valid(); iterate2.Next() {

		address, stuff, err := d.chainParser.GetAddressesFromAddrDesc(iterate2.Key().Data())

		fmt.Println()

		if len(address) != 0 {
			fmt.Printf("Address2 %v\n With stuff2 %v\n and err2 %v\n", address, stuff, err)

			tx, s, err := d.chainParser.UnpackTx(iterate2.Value().Data())

			fmt.Println("Length: ", len(iterate2.Value().Data()))
			if err == nil {
				fmt.Printf("Decoded %v with %s and err %v\n", tx.Hex, s, err)
			}
		}
	}

	fmt.Println("Slice is: \n", val)
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

func (d *RocksDB) addToHydraContractsMap(txid string, from string, to string) map[string]string {
	return nil
}

func (d *RocksDB) processAddressesHydraType(block *bchain.Block, addresses addressesMap, txAddressesMap map[string]*TxAddresses, balances map[string]*AddrBalance, addressContracts map[string]*AddrContracts) error {
	blockTxIDs := make([][]byte, len(block.Txs))
	blockTxAddresses := make([]*TxAddresses, len(block.Txs))

	fmt.Println("First address map:")
	fmt.Println(addresses)

	for tx := range block.Txs {
		mytx := &block.Txs[tx]

		fmt.Println("Tx id: ", mytx.Txid)
		// contracts
		contracts, _ := d.chainParser.EthereumTypeGetErc20FromTx(mytx)
		exLogs, err := d.chainParser.GetTransactionHydraParser(mytx.Txid)

		if exLogs != nil {
			fmt.Println("get example tx logs hydra: ")
			fmt.Println(exLogs)
			if len(exLogs.Logs) != 0 {
				for _, rl := range exLogs.Logs {
					fmt.Println("Log hydra: ")
					fmt.Println(rl)
				}
				fmt.Println(err)
			}
		}

		// fmt.Println("\n")
		// fmt.Println("Receipt is: ")
		// fmt.Println(txReceipt)
		// fmt.Println("\n")
		// fmt.Println("Error is: ")
		// fmt.Println(err)
		// fmt.Println("\n")
		fmt.Println("Contracts are: ")

		// if found
		// add from, to with cotnract address map
		// check that map on address indexing
		// check each contract balance
		fmt.Println(contracts)
		// when connected to new block this is called.
		for i, v := range mytx.Vin {
			for i2, v2 := range v.Addresses {
				mytx.Vin[i].Addresses[i2] = hex.EncodeToString([]byte(v2))
			}
		}
		for i, v := range mytx.Vout {
			for i2, v2 := range v.ScriptPubKey.Addresses {
				mytx.Vout[i].ScriptPubKey.Addresses[i2] = hex.EncodeToString([]byte(v2))
			}
		}
		for _, v := range mytx.Vin {
			for _, v2 := range v.Addresses {
				fmt.Println("VIN ADDRESS: " + v2)
			}
		}

		for _, v := range mytx.Vout {
			for _, v2 := range v.ScriptPubKey.Addresses {
				fmt.Println("VOUT ADDRESS: " + v2)
			}
		}

		btxID, err := d.chainParser.PackTxid(mytx.Txid)
		if err != nil {
			return err
		}

		fmt.Println("Hex transaction id: %v \n", btxID)
	}

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
		fmt.Println("Tx addresses map: ")
		fmt.Println(txAddressesMap)
		blockTxAddresses[txi] = &ta
		for i, output := range tx.Vout {
			tao := &ta.Outputs[i]
			tao.ValueSat = output.ValueSat
			addrDesc, err := d.chainParser.GetAddrDescFromVout(&output)
			fmt.Println("Addr descriptor: " + addrDesc.String())
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
				fmt.Println("Real addreses list: ")
				fmt.Println(addresses)
				for _, v := range addresses {
					for _, ti := range v {
						fmt.Println(ti.indexes)
						fmt.Println(ti.btxID)
					}
				}
				fmt.Println("btxid: ")
				fmt.Println(btxID)
				fmt.Println(string(btxID))
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
				for _, ti := range ita.Inputs {
					fmt.Println("Input addrdesc: " + ti.AddrDesc.String())
					fmt.Println("Input valueSat: " + ti.ValueSat.String())
					fmt.Println("Input ValueSatBase10: " + ti.ValueSat.Text(10))
				}
				for _, ti := range ita.Outputs {
					fmt.Println("Output addrdesc: " + ti.AddrDesc.String())
					fmt.Println("Output valueSat: " + ti.ValueSat.String())
					fmt.Println("Output ValueSatBase10: " + ti.ValueSat.Text(10))
				}
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
		fmt.Println("Ercs we get: ", erc20)
		fmt.Println("Err is: ", err)
		if len(erc20) != 0 {
			for _, et := range erc20 {
				fmt.Printf("Any erc20s2:? %s \n", et.Contract)
				fmt.Printf("from:? %s \n", et.From)
				fmt.Printf("to:? %s \n", et.To)
				fmt.Printf("token2:? %s \n", &et.Tokens)
			}
		}
		if err != nil {
			continue
		}
		fmt.Printf("Any erc20s?: %s", erc20)
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

			fmt.Println("Adding addresses: ")

			for k, v := range addresses {
				fmt.Printf("What is k?: %v \n", k)
				for i2, ti := range v {
					fmt.Printf("What is this hydratype: %v \n ", i2)

					fmt.Println("Indexes hydratype: ")
					for i3, v2 := range ti.indexes {
						fmt.Printf("Index: %v \n", v2)
						fmt.Printf("Index: %v \n", i3)
					}
				}
			}

			for k, ac := range addressContracts {
				fmt.Printf("What is k2?: %v \n", k)
				fmt.Printf("Total txs: %v \n", ac.TotalTxs)
				for i2, ac2 := range ac.Contracts {
					fmt.Printf("Contract tx: %v \n", ac2.Contract.String())
					fmt.Printf("Txs: %v \n", ac2.Txs)
					fmt.Printf("What is i2: %v \n", i2)
				}
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
