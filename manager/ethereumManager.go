package manager

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"math/big"
	"strings"
	"sync"
	"time"

	"github.com/cosmos/cosmos-sdk/codec"
	"github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/x/auth"
	"github.com/cosmos/cosmos-sdk/x/auth/exported"
	auth_types "github.com/cosmos/cosmos-sdk/x/auth/types"
	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	ethcommon "github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/polynetwork/cosmos-poly-module/ccm"
	"github.com/polynetwork/cosmos-poly-module/headersync"
	"github.com/polynetwork/eth-contracts/go_abi/eccm_abi"
	"github.com/polynetwork/poly/common"
	common2 "github.com/polynetwork/poly/native/service/cross_chain_manager/common"
	"github.com/polynetwork/poly/native/service/cross_chain_manager/eth"
	"github.com/tendermint/tendermint/crypto"
	tmclient "github.com/tendermint/tendermint/rpc/client"
	"github.com/tendermint/tendermint/rpc/client/http"
	core_types "github.com/tendermint/tendermint/rpc/core/types"
	tmtype "github.com/tendermint/tendermint/types"
	"github.com/terry108/eth-relayer/config"
	"github.com/terry108/eth-relayer/db"
	"github.com/terry108/eth-relayer/log"
	"github.com/terry108/eth-relayer/tools"
)

type EthereumManager struct {
	config         *config.ServiceConfig
	restClient     *tools.RestClient
	client         *ethclient.Client
	currentHeight  uint64
	forceHeight    uint64
	lockerContract *bind.BoundContract

	exitChan     chan int
	header4sync  []string
	crosstx4sync []*CrossTransfer
	db           *db.BoltDB

	// Bridge
	bridgeSdk *http.HTTP
	CMPrivk   crypto.PrivKey
	CMAcc     types.AccAddress
	CMSeq     *CosmosSeq
	CMAccNum  uint64
	CMFees    types.Coins
	CMGas     uint64
	CMCdc     *codec.Codec
}

type CrossTransfer struct {
	txIndex string
	txId    []byte
	value   []byte
	toChain uint32
	height  uint64
}

type CosmosSeq struct {
	lock sync.Mutex
	val  uint64
}

func (seq *CosmosSeq) GetAndAdd() uint64 {
	seq.lock.Lock()
	defer func() {
		seq.val += 1
		seq.lock.Unlock()
	}()
	return seq.val
}

type BridgeHeader struct {
	Header  tmtype.Header
	Commit  *tmtype.Commit
	Valsets []*tmtype.Validator
}

func (ct *CrossTransfer) Serialization(sink *common.ZeroCopySink) {
	sink.WriteString(ct.txIndex)
	sink.WriteVarBytes(ct.txId)
	sink.WriteVarBytes(ct.value)
	sink.WriteUint32(ct.toChain)
	sink.WriteUint64(ct.height)
}

func (ct *CrossTransfer) Deserialization(source *common.ZeroCopySource) error {
	txIndex, eof := source.NextString()
	if eof {
		return fmt.Errorf("Waiting deserialize txIndex error")
	}
	txId, eof := source.NextVarBytes()
	if eof {
		return fmt.Errorf("Waiting deserialize txId error")
	}
	value, eof := source.NextVarBytes()
	if eof {
		return fmt.Errorf("Waiting deserialize value error")
	}
	toChain, eof := source.NextUint32()
	if eof {
		return fmt.Errorf("Waiting deserialize toChain error")
	}
	height, eof := source.NextUint64()
	if eof {
		return fmt.Errorf("Waiting deserialize height error")
	}
	ct.txIndex = txIndex
	ct.txId = txId
	ct.value = value
	ct.toChain = toChain
	ct.height = height
	return nil
}

func NewEthereumManager(cfg *config.ServiceConfig, startheight uint64, startforceheight uint64, bridgeSdk *http.HTTP, client *ethclient.Client, boltDB *db.BoltDB) (*EthereumManager, error) {
	// Load pi bridge wallet
	CMPrivk, CMAcc, err := tools.GetBridgePrivateKey(cfg.BridgeConfig.BridgeWallet, []byte(cfg.BridgeConfig.BridgeWalletPwd))
	if err != nil {
		return nil, err
	}
	CMCdc := tools.NewCodecForRelayer()
	rawParam, err := CMCdc.MarshalJSON(auth.NewQueryAccountParams(CMAcc))
	if err != nil {
		return nil, err
	}
	res, err := bridgeSdk.ABCIQueryWithOptions(tools.QueryAccPath, rawParam, tmclient.ABCIQueryOptions{Prove: true})
	if err != nil {
		return nil, err
	}
	if !res.Response.IsOK() {
		return nil, fmt.Errorf("failed to get response for accout-query: %v", res.Response)
	}
	var exportedAcc exported.Account
	var gasPrice types.DecCoins
	if err := CMCdc.UnmarshalJSON(res.Response.Value, &exportedAcc); err != nil {
		return nil, fmt.Errorf("unmarshal query-account-resp failed, err: %v", err)
	}
	CMSeq := &CosmosSeq{
		lock: sync.Mutex{},
		val:  exportedAcc.GetSequence(),
	}
	CMAccNum := exportedAcc.GetAccountNumber()
	if gasPrice, err = types.ParseDecCoins(cfg.BridgeConfig.BridgeGasPrice); err != nil {
		return nil, err
	}
	CMFees, err := tools.CalcCosmosFees(gasPrice, cfg.BridgeConfig.BridgeGas)
	if err != nil {
		return nil, err
	}
	CMGas := cfg.BridgeConfig.BridgeGas

	mgr := &EthereumManager{
		config:   cfg,
		db:       boltDB,
		exitChan: make(chan int),
		// eth
		currentHeight: startheight,
		forceHeight:   startforceheight,
		restClient:    tools.NewRestClient(),
		client:        client,
		header4sync:   make([]string, 0),
		crosstx4sync:  make([]*CrossTransfer, 0),
		// pi bridge
		bridgeSdk: bridgeSdk,
		CMPrivk:   CMPrivk,
		CMAcc:     CMAcc,
		CMSeq:     CMSeq,
		CMAccNum:  CMAccNum,
		CMFees:    CMFees,
		CMGas:     CMGas,
	}
	err = mgr.init()
	if err != nil {
		return nil, err
	} else {
		return mgr, nil
	}
}

func (em *EthereumManager) init() error {
	// get eth latest height in pi bridge
	latestHeight, err := em.findLastestHeight()
	if err != nil {
		return err
	}
	if latestHeight == 0 {
		return fmt.Errorf("init - the genesis block has not synced!")
	}
	if em.forceHeight > 0 && em.forceHeight < latestHeight {
		em.currentHeight = em.forceHeight
	} else {
		em.currentHeight = latestHeight
	}
	log.Infof("EthereumManager init - start height: %d", em.currentHeight)
	return nil
}

// TODO Check ConsensusPeers
func (em *EthereumManager) findLastestHeight() (uint64, error) {
	data, err := em.CMCdc.MarshalJSON(headersync.NewQueryConsensusPeersParams(em.config.ETHConfig.SideChainId))
	if err != nil {
		return 0, err
	}
	res, err := em.bridgeSdk.ABCIQuery(tools.QueryConsensusPath, data)
	if err != nil {
		return 0, err
	}
	if res == nil || len(res.Response.Value) == 0 {
		return 0, fmt.Errorf("current height of Poly on COSMOS not set")
	}
	cps := &headersync.ConsensusPeers{}
	if err = cps.Deserialization(common.NewZeroCopySource(res.Response.GetValue())); err != nil {
		return 0, err
	}
	return uint64(cps.Height), nil
}

// Monitor ethereum chain and commit headers to pi bridge
func (em *EthereumManager) MonitorChain() {
	fetchBlockTicker := time.NewTicker(time.Duration(em.config.ETHConfig.MonitorInterval) * time.Second)
	var blockHandleResult bool
	for {
		select {
		case <-fetchBlockTicker.C:
			height, err := tools.GetNodeHeight(em.config.ETHConfig.RestURL, em.restClient)
			if err != nil {
				log.Infof("MonitorChain - cannot get node height, err: %s", err)
				continue
			}
			if height-em.currentHeight <= config.ETH_USEFUL_BLOCK_NUM {
				continue
			}
			log.Infof("MonitorChain - eth height is %d", height)
			blockHandleResult = true
			for em.currentHeight < height-config.ETH_USEFUL_BLOCK_NUM {
				if em.currentHeight%10 == 0 {
					log.Infof("handle confirmed eth Block height: %d", em.currentHeight)
				}
				blockHandleResult = em.handleNewBlock(em.currentHeight + 1)
				if blockHandleResult == false {
					break
				}
				em.currentHeight++
				// try to commit header if more than 50 headers needed to be syned
				if len(em.header4sync) >= em.config.ETHConfig.HeadersPerBatch {
					if res := em.commitHeader(); res != 0 {
						blockHandleResult = false
						break
					}
				}
			}
			if blockHandleResult && len(em.header4sync) > 0 {
				em.commitHeader()
			}
		case <-em.exitChan:
			return
		}
	}
}

func (em *EthereumManager) handleNewBlock(height uint64) bool {
	ret := em.handleBlockHeader(height)
	if !ret {
		log.Errorf("handleNewBlock - handleBlockHeader on height :%d failed", height)
		return false
	}
	ret = em.fetchLockDepositEvents(height, em.client)
	if !ret {
		log.Errorf("handleNewBlock - fetchLockDepositEvents on height :%d failed", height)
	}
	return true
}

func (em *EthereumManager) handleBlockHeader(height uint64) bool {
	hdrStr := em.getBlockHeader(height)
	if hdrStr == "" {
		return false
	}
	// Get eth header in bridge by height
	latestHeight, err := em.findLastestHeight()
	if err != nil {
		return false
	}
	if latestHeight+1 == height {
		em.header4sync = append(em.header4sync, hdrStr)
	}
	return true
}

func (em *EthereumManager) getBlockHeader(height uint64) string {
	hdr, err := em.client.HeaderByNumber(context.Background(), big.NewInt(int64(height)))
	if err != nil {
		log.Errorf("handleBlockHeader - GetNodeHeader on height :%d failed", height)
		return ""
	}
	rawHdr, _ := hdr.MarshalJSON()
	return hex.EncodeToString(rawHdr)
}

func (em *EthereumManager) fetchLockDepositEvents(height uint64, client *ethclient.Client) bool {
	lockAddress := ethcommon.HexToAddress(em.config.ETHConfig.ECCMContractAddress)
	lockContract, err := eccm_abi.NewEthCrossChainManager(lockAddress, client)
	if err != nil {
		return false
	}
	opt := &bind.FilterOpts{
		Start:   height,
		End:     &height,
		Context: context.Background(),
	}
	events, err := lockContract.FilterCrossChainEvent(opt, nil)
	if err != nil {
		log.Errorf("fetchLockDepositEvents - FilterCrossChainEvent error :%s", err.Error())
		return false
	}
	if events == nil {
		log.Infof("fetchLockDepositEvents - no events found on FilterCrossChainEvent")
		return false
	}

	for events.Next() {
		evt := events.Event
		var isTarget bool
		if len(em.config.TargetContracts) > 0 {
			toContractStr := evt.ProxyOrAssetContract.String()
			for _, v := range em.config.TargetContracts {
				toChainIdArr, ok := v[toContractStr]
				if ok {
					if len(toChainIdArr["outbound"]) == 0 {
						isTarget = true
						break
					}
					for _, id := range toChainIdArr["outbound"] {
						if id == evt.ToChainId {
							isTarget = true
							break
						}
					}
					if isTarget {
						break
					}
				}
			}
			if !isTarget {
				continue
			}
		}
		param := &common2.MakeTxParam{}
		_ = param.Deserialization(common.NewZeroCopySource([]byte(evt.Rawdata)))
		resTx, _ := em.bridgeSdk.Tx(evt.Raw.TxHash.Bytes(), false)
		if resTx != nil {
			log.Debugf("fetchLockDepositEvents - ccid %s (tx_hash: %s) already on poly",
				hex.EncodeToString(param.CrossChainID), evt.Raw.TxHash.Hex())
			continue
		}
		index := big.NewInt(0)
		index.SetBytes(evt.TxId)
		crossTx := &CrossTransfer{
			txIndex: tools.EncodeBigInt(index),
			txId:    evt.Raw.TxHash.Bytes(),
			toChain: uint32(evt.ToChainId),
			value:   []byte(evt.Rawdata),
			height:  height,
		}
		sink := common.NewZeroCopySink(nil)
		crossTx.Serialization(sink)
		err = em.db.PutRetry(sink.Bytes())
		if err != nil {
			log.Errorf("fetchLockDepositEvents - em.db.PutRetry error: %s", err)
		}
		log.Infof("fetchLockDepositEvent -  height: %d", height)
	}
	return true
}

// Commit header to bridge
func (em *EthereumManager) commitHeader() int {
	res, seq, err := em.sendCosmosTx([]types.Msg{
		headersync.NewMsgSyncHeadersParam(em.CMAcc, em.header4sync),
	})
	if err != nil {
		errDesc := err.Error()
		if strings.Contains(errDesc, "get the parent block failed") || strings.Contains(errDesc, "missing required field") {
			log.Warnf("commitHeader - send transaction to poly chain err: %s", errDesc)
			em.rollBackToCommAncestor()
			return 0
		} else {
			log.Errorf("commitHeader - send transaction to poly chain err: %s", errDesc)
			return 1
		}
	}
	tick := time.NewTicker(100 * time.Millisecond)
	var h uint32
	var resTx *core_types.ResultTx
	for range tick.C {
		resTx, _ = em.bridgeSdk.Tx(res.Hash, false)
		h := uint64(resTx.Height)
		curr, _ := em.findLastestHeight()
		if h > 0 && curr > h {
			break
		}
	}
	log.Infof("commitHeader - send transaction %s to poly chain and confirmed on height %d, sequence: %d", res.Hash.String(), h, seq)
	em.header4sync = make([]string, 0)
	return 0
}

func (em *EthereumManager) rollBackToCommAncestor() {
	for ; ; em.currentHeight-- {
		raw, err := tools.GetBridgeHdr(em.bridgeSdk, int64(em.currentHeight))
		if raw == nil || err != nil {
			continue
		}
		hdr, err := em.client.HeaderByNumber(context.Background(), big.NewInt(int64(em.currentHeight)))
		if err != nil {
			log.Errorf("rollBackToCommAncestor - failed to get header by number, so we wait for one second to retry: %v", err)
			time.Sleep(time.Second)
			em.currentHeight++
		}
		if bytes.Equal(hdr.Hash().Bytes(), raw.Header.DataHash) {
			log.Infof("rollBackToCommAncestor - find the common ancestor: %s(number: %d)", hdr.Hash().String(), em.currentHeight)
			break
		}
	}
	em.header4sync = make([]string, 0)
}

func (em *EthereumManager) sendCosmosTx(msgs []types.Msg) (*core_types.ResultBroadcastTx, uint64, error) {
	seq := em.CMSeq.GetAndAdd()
	toSign := auth_types.StdSignMsg{
		Sequence:      seq,
		AccountNumber: em.CMAccNum,
		Msgs:          msgs,
		Fee:           auth_types.NewStdFee(em.CMGas, em.CMFees),
	}
	sig, err := em.CMPrivk.Sign(toSign.Bytes())
	if err != nil {
		return nil, seq, fmt.Errorf("failed to sign raw tx: (error: %v, raw tx: %x)", err, toSign.Bytes())
	}

	tx := auth_types.NewStdTx(msgs, toSign.Fee, []auth.StdSignature{{em.CMPrivk.PubKey(), sig}}, toSign.Memo)
	encoder := auth.DefaultTxEncoder(em.CMCdc)
	rawTx, err := encoder(tx)
	if err != nil {
		return nil, seq, fmt.Errorf("failed to encode signed tx: %v", err)
	}
	var res *core_types.ResultBroadcastTx
	for {
		res, err = em.bridgeSdk.BroadcastTxSync(rawTx)
		if err != nil {
			if strings.Contains(err.Error(), tools.BroadcastConnTimeOut) {
				tools.SleepSecs(10)
				continue
			}
			return nil, seq, fmt.Errorf("failed to broadcast tx: (error: %v, raw tx: %x)", err, rawTx)
		}
		if res.Code != 0 {
			if strings.Contains(res.Log, tools.SeqErr) {
				tools.SleepSecs(1)
				continue
			}
			return nil, seq, fmt.Errorf("failed to check tx: (code: %d, sequence: %d, log: %s)", res.Code, seq, res.Log)
		} else {
			break
		}
	}

	return res, seq, nil
}

func (em *EthereumManager) MonitorDeposit() {
	monitorTicker := time.NewTicker(time.Duration(em.config.ETHConfig.MonitorInterval) * time.Second)
	for {
		select {
		case <-monitorTicker.C:
			height, err := tools.GetNodeHeight(em.config.ETHConfig.RestURL, em.restClient)
			if err != nil {
				log.Infof("MonitorDeposit - cannot get eth node height, err: %s", err)
				continue
			}
			snycheight, err := em.findLastestHeight()
			if err != nil {
				log.Infof("MonitorDeposit - cannot get pi bridge node height, err: %s", err)
				continue
			}
			log.Log.Info("MonitorDeposit from eth - snyced eth height", snycheight, "eth height", height, "diff", height-snycheight)
			em.handleLockDepositEvents(snycheight)
		case <-em.exitChan:
			return
		}
	}
}

func (em *EthereumManager) handleLockDepositEvents(refHeight uint64) error {
	retryList, err := em.db.GetAllRetry()
	if err != nil {
		return fmt.Errorf("handleLockDepositEvents - em.db.GetAllRetry error: %s", err)
	}
	for _, v := range retryList {
		time.Sleep(time.Second * 1)
		crosstx := new(CrossTransfer)
		err := crosstx.Deserialization(common.NewZeroCopySource(v))
		if err != nil {
			log.Errorf("handleLockDepositEvents - retry.Deserialization error: %s", err)
			continue
		}
		//1. decode events
		key := crosstx.txIndex
		keyBytes, err := eth.MappingKeyAt(key, "01")
		if err != nil {
			log.Errorf("handleLockDepositEvents - MappingKeyAt error:%s\n", err.Error())
			continue
		}
		if refHeight <= crosstx.height+em.config.ETHConfig.BlockConfig {
			continue
		}
		height := int64(refHeight - em.config.ETHConfig.BlockConfig)
		heightHex := hexutil.EncodeBig(big.NewInt(height))
		proofKey := hexutil.Encode(keyBytes)
		//2. get proof
		proof, err := tools.GetProof(em.config.ETHConfig.RestURL, em.config.ETHConfig.ECCDContractAddress, proofKey, heightHex, em.restClient)
		if err != nil {
			log.Errorf("handleLockDepositEvents - error :%s\n", err.Error())
			continue
		}

		//3. commit proof to pi bridge
		txHash, err := em.commitProof(uint32(height), proof, crosstx.value, crosstx.txId)
		if err != nil {
			if strings.Contains(err.Error(), "chooseUtxos, current utxo is not enough") {
				log.Infof("handleLockDepositEvents - invokeNativeContract error: %s", err)
				continue
			} else {
				if err := em.db.DeleteRetry(v); err != nil {
					log.Errorf("handleLockDepositEvents - em.db.DeleteRetry error: %s", err)
				}
				if strings.Contains(err.Error(), "tx already done") {
					log.Debugf("handleLockDepositEvents - eth_tx %s already on poly", ethcommon.BytesToHash(crosstx.txId).String())
				} else {
					log.Errorf("handleLockDepositEvents - invokeNativeContract error for eth_tx %s: %s", ethcommon.BytesToHash(crosstx.txId).String(), err)
				}
				continue
			}
		}

		//4. put to check db for checking
		err = em.db.PutCheck(txHash, v)
		if err != nil {
			log.Errorf("handleLockDepositEvents - em.db.PutCheck error: %s", err)
		}
		err = em.db.DeleteRetry(v)
		if err != nil {
			log.Errorf("handleLockDepositEvents - em.db.PutCheck error: %s", err)
		}
		log.Infof("handleLockDepositEvents - syncProofToAlia txHash is %s", txHash)
	}
	return nil
}

// TODO Head Info
func (em *EthereumManager) commitProof(height uint32, proof []byte, value []byte, txhash []byte) (string, error) {
	hdrStr := em.getBlockHeader(uint64(height))
	res, seq, err := em.sendCosmosTx([]types.Msg{
		ccm.NewMsgProcessCrossChainTx(em.CMAcc, em.config.ETHConfig.SideChainId, hex.EncodeToString(proof),
			hex.EncodeToString(value), "", hdrStr),
	})
	if err != nil {
		log.Fatalf("[commitProof] commitProof error: %v", err)
		panic(err)
	}

	log.Infof("[commitProof] relay tx success: (cosmos_txhash: %s, code: %d, log: %s, poly_height: %d, "+
		"poly_hash: %s, sequence: %d)", res.Hash.String(), res.Code, res.Log, "", "", seq)
	return res.Hash.String(), nil
}

func (em *EthereumManager) CheckDeposit() {
	checkTicker := time.NewTicker(time.Duration(em.config.ETHConfig.MonitorInterval) * time.Second)
	for {
		select {
		case <-checkTicker.C:
			// try to check deposit
			em.checkLockDepositEvents()
		case <-em.exitChan:
			return
		}
	}
}
func (em *EthereumManager) checkLockDepositEvents() error {
	checkMap, err := em.db.GetAllCheck()
	if err != nil {
		return fmt.Errorf("checkLockDepositEvents - em.db.GetAllCheck error: %s", err)
	}
	for k, v := range checkMap {
		resTx, err := em.bridgeSdk.Tx([]byte(k), false)
		if resTx != nil {
			log.Debugf("fetchLockDepositEvents - (tx_hash: %s) already on pi bridge",
				hex.EncodeToString(resTx.Hash))
			continue
		}

		// event, err := em.polySdk.GetSmartContractEvent(k)
		if err != nil {
			log.Errorf("checkLockDepositEvents - em.aliaSdk.GetSmartContractEvent error: %s", err)
			continue
		}
		if resTx == nil {
			continue
		}

		if resTx.TxResult.Code != 1 {
			log.Infof("checkLockDepositEvents - state of poly tx %s is not success", k)
			err := em.db.PutRetry(v)
			if err != nil {
				log.Errorf("checkLockDepositEvents - em.db.PutRetry error:%s", err)
			}
		}
		err = em.db.DeleteCheck(k)
		if err != nil {
			log.Errorf("checkLockDepositEvents - em.db.DeleteRetry error:%s", err)
		}
	}
	return nil
}
