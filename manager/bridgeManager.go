package manager

import (
	"context"
	"encoding/hex"
	"fmt"
	"math/big"
	"math/rand"
	"strconv"
	"strings"
	"time"

	"github.com/cosmos/cosmos-sdk/codec"
	sdktypes "github.com/cosmos/cosmos-sdk/types"
	sdk_auth "github.com/cosmos/cosmos-sdk/x/auth"
	"github.com/cosmos/cosmos-sdk/x/bank"
	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/accounts/abi"
	ethcommon "github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/polynetwork/cosmos-poly-module/btcx"
	"github.com/polynetwork/cosmos-poly-module/ccm"
	"github.com/polynetwork/cosmos-poly-module/ft"
	"github.com/polynetwork/cosmos-poly-module/headersync"
	"github.com/polynetwork/cosmos-poly-module/lockproxy"
	"github.com/polynetwork/eth-contracts/go_abi/eccd_abi"
	"github.com/polynetwork/eth-contracts/go_abi/eccm_abi"
	"github.com/tendermint/tendermint/crypto/merkle"
	"github.com/tendermint/tendermint/rpc/client"
	rpchttp "github.com/tendermint/tendermint/rpc/client/http"
	rpctypes "github.com/tendermint/tendermint/rpc/core/types"
	"github.com/terry108/eth-relayer/config"
	"github.com/terry108/eth-relayer/db"
	"github.com/terry108/eth-relayer/log"
	"github.com/terry108/eth-relayer/tools"
)

const (
	ChanLen = 64
)

const (
	PerPage                 = 100 // (0, 100]
	HdrLimitPerBatch        = 50
	QueryConsensusPath      = "/custom/" + headersync.ModuleName + "/" + headersync.QueryConsensusPeers
	CosmosCrossChainModName = ccm.ModuleName
	CosmosProofKey          = "make_from_cosmos_proof"
	ProofPath               = "/store/" + ccm.ModuleName + "/key"
	TxAlreadyExist          = "already done"
	NewEpoch                = "lower than epoch switching height"
	SeqErr                  = "verify correct account sequence and chain-id"
	BroadcastConnTimeOut    = "connection timed out"
	UtxoNotEnough           = "current utxo is not enoug"
	ChanBufSize             = 256
	QueryAccPath            = "/custom/acc/account"
	CosmosTxNotInEpoch      = "Compare height"
	NoUsefulHeaders         = "no header you commited is useful"
)

type BridgeManager struct {
	config        *config.ServiceConfig
	bridgeRpcCli  *rpchttp.HTTP
	currentHeight uint64
	contractAbi   *abi.ABI
	exitChan      chan int
	db            *db.BoltDB
	ethClient     *ethclient.Client
	senders       []*EthSender
	CMCdc         *codec.Codec
}

type EthTxInfo struct {
	txData       []byte
	gasLimit     uint64
	gasPrice     *big.Int
	contractAddr ethcommon.Address
	bridgeTxHash string
}

type BridgeTx struct {
	Tx          *rpctypes.ResultTx
	ProofHeight int64
	Proof       []byte
	PVal        []byte
}

type CosmosProofValue struct {
	Kp    string
	Value []byte
}

type EthSender struct {
	acc          accounts.Account
	keyStore     *tools.EthKeyStore
	cmap         map[string]chan *EthTxInfo
	nonceManager *tools.NonceManager
	ethClient    *ethclient.Client
	bridgeRpcCli *rpchttp.HTTP
	config       *config.ServiceConfig
	contractAbi  *abi.ABI
}

func NewBridgeManager(servCfg *config.ServiceConfig, startblockHeight uint64, bridgeRpcCli *rpchttp.HTTP, ethereumsdk *ethclient.Client, boltDB *db.BoltDB) (*BridgeManager, error) {
	contractabi, err := abi.JSON(strings.NewReader(eccm_abi.EthCrossChainManagerABI))
	if err != nil {
		return nil, err
	}
	chainId, err := ethereumsdk.ChainID(context.Background())
	if err != nil {
		return nil, err
	}
	ks := tools.NewEthKeyStore(servCfg.ETHConfig, chainId)
	accArr := ks.GetAccounts()
	if len(servCfg.ETHConfig.KeyStorePwdSet) == 0 {
		fmt.Println("please input the passwords for ethereum keystore: ")
	}
	if err = ks.UnlockKeys(servCfg.ETHConfig); err != nil {
		return nil, err
	}

	CMCdc := NewCodecForRelayer()

	senders := make([]*EthSender, len(accArr))
	for i, v := range senders {
		v = &EthSender{}
		v.acc = accArr[i]

		v.ethClient = ethereumsdk
		v.keyStore = ks
		v.config = servCfg
		v.bridgeRpcCli = bridgeRpcCli
		v.contractAbi = &contractabi
		v.nonceManager = tools.NewNonceManager(ethereumsdk)
		v.cmap = make(map[string]chan *EthTxInfo)

		senders[i] = v
	}
	return &BridgeManager{
		exitChan:      make(chan int),
		config:        servCfg,
		bridgeRpcCli:  bridgeRpcCli,
		currentHeight: startblockHeight,
		contractAbi:   &contractabi,
		db:            boltDB,
		ethClient:     ethereumsdk,
		senders:       senders,
		CMCdc:         CMCdc,
	}, nil
}

func NewCodecForRelayer() *codec.Codec {
	cdc := codec.New()
	bank.RegisterCodec(cdc)
	sdktypes.RegisterCodec(cdc)
	codec.RegisterCrypto(cdc)
	sdk_auth.RegisterCodec(cdc)
	btcx.RegisterCodec(cdc)
	ccm.RegisterCodec(cdc)
	ft.RegisterCodec(cdc)
	headersync.RegisterCodec(cdc)
	lockproxy.RegisterCodec(cdc)
	return cdc
}

func (bm *BridgeManager) init() bool {
	if bm.currentHeight > 0 {
		log.Infof("PolyManager init - start height from flag: %d", bm.currentHeight)
		return true
	}
	bm.currentHeight = uint64(bm.db.GetBridgeHeight())
	latestHeight := bm.findLatestHeight()
	if latestHeight > bm.currentHeight {
		bm.currentHeight = latestHeight
		log.Infof("PolyManager init - latest height from ECCM: %d", bm.currentHeight)
		return true
	}
	log.Infof("PolyManager init - latest height from DB: %d", bm.currentHeight)

	return true
}

func (bm *BridgeManager) findLatestHeight() uint64 {
	address := ethcommon.HexToAddress(bm.config.ETHConfig.ECCDContractAddress)
	instance, err := eccd_abi.NewEthCrossChainData(address, bm.ethClient)
	if err != nil {
		log.Errorf("findLatestHeight - new eth cross chain failed: %s", err.Error())
		return 0
	}
	height, err := instance.GetCurEpochStartHeight(nil)
	if err != nil {
		log.Errorf("findLatestHeight - GetLatestHeight failed: %s", err.Error())
		return 0
	}
	return uint64(height)
}

func (bm *BridgeManager) MonitorChain() {
	ret := bm.init()
	if !ret {
		log.Errorf("MonitorChain - init failed\n")
	}
	monitorTicker := time.NewTicker(config.BEIDGE_MONITOR_INTERVAL)
	var blockHandleResult bool
	for {
		select {
		case <-monitorTicker.C:
			status, err := bm.bridgeRpcCli.Status()
			if err != nil {
				log.Errorf("MonitorChain - get pibridge chain block height error: %s", err)
				continue
			}
			latestheight := uint64(status.SyncInfo.LatestBlockHeight)
			latestheight--
			if latestheight-bm.currentHeight < config.ONT_USEFUL_BLOCK_NUM {
				continue
			}
			log.Infof("MonitorChain - pibridge chain current height: %d", latestheight)
			blockHandleResult = true
			for bm.currentHeight <= latestheight-config.ONT_USEFUL_BLOCK_NUM {
				if bm.currentHeight%10 == 0 {
					log.Infof("handle confirmed pibridge Block height: %d", bm.currentHeight)
				}
				// 处理区块数据
				// 1、先校验区块
				// checkBridgeHeight(bm.currentHeight)
				blockHandleResult = bm.handleDepositTxs(bm.currentHeight)
				if !blockHandleResult {
					break
				}
				bm.currentHeight++
			}
			if err = bm.db.UpdateBridgeHeight(uint32(bm.currentHeight - 1)); err != nil {
				log.Errorf("MonitorChain - failed to save height of poly: %v", err)
			}
		case <-bm.exitChan:
			return
		}
	}
}

func getTxQuery(h uint64) string {
	return fmt.Sprintf("tx.height=%d AND make_from_cosmos_proof.status='1'", h)
}

func getKeyHash(tx *rpctypes.ResultTx) []byte {
	var hash []byte
	for _, e := range tx.TxResult.Events {
		if e.Type == CosmosProofKey {
			hash, _ = hex.DecodeString(string(e.Attributes[2].Value))
			break
		}
	}
	return hash
}

func (bm *BridgeManager) getBridgeTxs(h uint64) ([]*BridgeTx, error) {
	txs := make([]*BridgeTx, 0)
	query := getTxQuery(h)
	res, err := bm.bridgeRpcCli.TxSearch(query, true, 1, 100, "asc")
	if err != nil {
		return nil, err
	}
	pages := ((res.TotalCount - 1) / 100) + 1
	for p := 1; p <= pages; p++ {
		// already have page 1
		if p > 1 {
			if res, err = bm.bridgeRpcCli.TxSearch(query, true, p, 100, "asc"); err != nil {
				return txs, err
			}
		}
		// get proof for every tx, and add them to txArr prepared to commit
		for _, tx := range res.Txs {
			hash := getKeyHash(tx)
			res, _ := bm.bridgeRpcCli.ABCIQueryWithOptions(ProofPath, ccm.GetCrossChainTxKey(hash),
				client.ABCIQueryOptions{Prove: true, Height: int64(h)})
			if res == nil || res.Response.GetValue() == nil {
				return txs, fmt.Errorf("get transactions faild at height %d", h)
			}
			proof, _ := res.Response.Proof.Marshal()

			kp := merkle.KeyPath{}
			kp = kp.AppendKey([]byte(CosmosCrossChainModName), merkle.KeyEncodingURL)
			kp = kp.AppendKey(res.Response.Key, merkle.KeyEncodingURL)
			pv, _ := bm.CMCdc.MarshalBinaryBare(&CosmosProofValue{
				Kp:    kp.String(),
				Value: res.Response.GetValue(),
			})
			txs = append(txs, &BridgeTx{
				Tx:          tx,
				ProofHeight: res.Response.Height,
				Proof:       proof,
				PVal:        pv,
			})
		}
	}
	return txs, nil
}

// Relay PiBridge cross-chain tx to eth.
func (bm *BridgeManager) handleDepositTxs(height uint64) bool {
	hdr, err := tools.GetBridgeHdr(bm.bridgeRpcCli, int64(height))
	if err != nil {
		log.Errorf("handleDepositTxs - get block header at height:%d error: %s", height, err.Error())
		return false
	}
	// 1. get bridge txs
	txs, err := bm.getBridgeTxs(height)
	for err != nil {
		log.Errorf("handleDepositTxs - get block transactions at height:%d error: %s", height, err.Error())
		return false
	}
	cnt := 0
	// 2. relay tx to eth
	for _, tx := range txs {
		// TODO 将Bridge上的交易发送到以太坊上
		_ = tx.Tx.Index

		sender := bm.selectSender()
		log.Infof("sender %s is handling bridge tx ( hash: %s, height: %d )",
			sender.acc.Address.String(), tx.Tx.Hash, height)
		// temporarily ignore the error for tx
		// sender.commitDepositEventsWithHeader(hdr, param, hp, anchor, event.TxHash, auditpath)
	}
	if cnt == 0 {
		sender := bm.selectSender()
		return sender.commitHeader(hdr)
	}

	return true
}

func (bm *BridgeManager) selectSender() *EthSender {
	sum := big.NewInt(0)
	balArr := make([]*big.Int, len(bm.senders))
	for i, v := range bm.senders {
	RETRY:
		bal, err := v.Balance()
		if err != nil {
			log.Errorf("failed to get balance for %s: %v", v.acc.Address.String(), err)
			time.Sleep(time.Second)
			goto RETRY
		}
		sum.Add(sum, bal)
		balArr[i] = big.NewInt(sum.Int64())
	}
	sum.Rand(rand.New(rand.NewSource(time.Now().Unix())), sum)
	for i, v := range balArr {
		res := v.Cmp(sum)
		if res == 1 || res == 0 {
			return bm.senders[i]
		}
	}
	return bm.senders[0]
}

func (es *EthSender) Balance() (*big.Int, error) {
	balance, err := es.ethClient.BalanceAt(context.Background(), es.acc.Address, nil)
	if err != nil {
		return nil, err
	}
	return balance, nil
}

func (es *EthSender) commitDepositEventsWithHeader(header *tools.BridgeHeader, tx *BridgeTx) bool {
	var (
	// sigs       []byte
	// headerData []byte
	)
	eccdAddr := ethcommon.HexToAddress(es.config.ETHConfig.ECCDContractAddress)
	eccd, err := eccd_abi.NewEthCrossChainData(eccdAddr, es.ethClient)
	if err != nil {
		panic(fmt.Errorf("failed to new eccm: %v", err))
	}
	// TODO check
	_ = eccd
	// fromTx := [32]byte{}
	// copy(fromTx[:], param.TxHash[:32])
	// res, _ := eccd.CheckIfFromChainTxExist(nil, tx.ChainID, fromTx)
	// if res {
	// 	log.Debugf("already relayed to eth: ( from_chain_id: %d, from_txhash: %x,  param.Txhash: %x)",
	// 		param.FromChainID, param.TxHash, param.MakeTxParam.TxHash)
	// 	return true
	// }
	// log.Infof("poly proof with header, height: %d, key: %s, proof: %s", header.Height-1, string(key), proof.AuditPath)

	headerdata := header.Header
	txData, err := es.contractAbi.Pack("verifyHeaderAndExecuteTx", headerdata, header.Valsets, header.Valsets)
	if err != nil {
		log.Errorf("commitDepositEventsWithHeader - err:" + err.Error())
		return false
	}

	gasPrice, err := es.ethClient.SuggestGasPrice(context.Background())
	if err != nil {
		log.Errorf("commitDepositEventsWithHeader - get suggest sas price failed error: %s", err.Error())
		return false
	}
	contractaddr := ethcommon.HexToAddress(es.config.ETHConfig.ECCMContractAddress)
	callMsg := ethereum.CallMsg{
		From: es.acc.Address, To: &contractaddr, Gas: 0, GasPrice: gasPrice,
		Value: big.NewInt(0), Data: txData,
	}
	gasLimit, err := es.ethClient.EstimateGas(context.Background(), callMsg)
	if err != nil {
		log.Errorf("commitDepositEventsWithHeader - estimate gas limit error: %s", err.Error())
		return false
	}

	k := es.getRouter()
	c, ok := es.cmap[k]
	if !ok {
		c = make(chan *EthTxInfo, ChanLen)
		es.cmap[k] = c
		go func() {
			for v := range c {
				if err = es.sendTxToEth(v); err != nil {
					log.Errorf("failed to send tx to ethereum: error: %v, txData: %s", err, hex.EncodeToString(v.txData))
				}
			}
		}()
	}
	//TODO: could be blocked
	c <- &EthTxInfo{
		txData:       txData,
		contractAddr: contractaddr,
		gasPrice:     gasPrice,
		gasLimit:     gasLimit,
		bridgeTxHash: string(tx.Tx.Hash),
	}
	return true
}

func (es *EthSender) getRouter() string {
	return strconv.FormatInt(rand.Int63n(es.config.RoutineNum), 10)
}

func (es *EthSender) sendTxToEth(info *EthTxInfo) error {
	nonce := es.nonceManager.GetAddressNonce(es.acc.Address)
	tx := types.NewTransaction(nonce, info.contractAddr, big.NewInt(0), info.gasLimit, info.gasPrice, info.txData)
	signedtx, err := es.keyStore.SignTransaction(tx, es.acc)
	if err != nil {
		es.nonceManager.ReturnNonce(es.acc.Address, nonce)
		return fmt.Errorf("commitDepositEventsWithHeader - sign raw tx error and return nonce %d: %v", nonce, err)
	}
	err = es.ethClient.SendTransaction(context.Background(), signedtx)
	if err != nil {
		es.nonceManager.ReturnNonce(es.acc.Address, nonce)
		return fmt.Errorf("commitDepositEventsWithHeader - send transaction error and return nonce %d: %v", nonce, err)
	}
	hash := signedtx.Hash()

	isSuccess := es.waitTransactionConfirm(info.bridgeTxHash, hash)
	if isSuccess {
		log.Infof("successful to relay tx to ethereum: (eth_hash: %s, nonce: %d, poly_hash: %s, eth_explorer: %s)",
			hash.String(), nonce, info.bridgeTxHash, tools.GetExplorerUrl(es.keyStore.GetChainId())+hash.String())
	} else {
		log.Errorf("failed to relay tx to ethereum: (eth_hash: %s, nonce: %d, poly_hash: %s, eth_explorer: %s)",
			hash.String(), nonce, info.bridgeTxHash, tools.GetExplorerUrl(es.keyStore.GetChainId())+hash.String())
	}
	return nil
}

// TODO: check the status of tx
func (es *EthSender) waitTransactionConfirm(polyTxHash string, hash ethcommon.Hash) bool {
	for {
		time.Sleep(time.Second * 1)
		_, ispending, err := es.ethClient.TransactionByHash(context.Background(), hash)
		if err != nil {
			continue
		}
		log.Debugf("( eth_transaction %s, poly_tx %s ) is pending: %v", hash.String(), polyTxHash, ispending)
		if ispending == true {
			continue
		} else {
			receipt, err := es.ethClient.TransactionReceipt(context.Background(), hash)
			if err != nil {
				continue
			}
			return receipt.Status == types.ReceiptStatusSuccessful
		}
	}
}

func (es *EthSender) commitHeader(header *tools.BridgeHeader) bool {
	headerdata := header.Header
	var (
		txData []byte
		txErr  error
	)
	gasPrice, err := es.ethClient.SuggestGasPrice(context.Background())
	if err != nil {
		log.Errorf("commitHeader - get suggest sas price failed error: %s", err.Error())
		return false
	}
	// for _, sig := range header.SigData {
	// 	temp := make([]byte, len(sig))
	// 	copy(temp, sig)
	// 	newsig, _ := signature.ConvertToEthCompatible(temp)
	// 	sigs = append(sigs, newsig...)
	// }
	txData, txErr = es.contractAbi.Pack("changeBookKeeper", headerdata, header.Valsets, header.Commit.Signatures)
	if txErr != nil {
		log.Errorf("commitHeader - err:" + err.Error())
		return false
	}

	contractaddr := ethcommon.HexToAddress(es.config.ETHConfig.ECCMContractAddress)
	callMsg := ethereum.CallMsg{
		From: es.acc.Address, To: &contractaddr, Gas: 0, GasPrice: gasPrice,
		Value: big.NewInt(0), Data: txData,
	}

	gasLimit, err := es.ethClient.EstimateGas(context.Background(), callMsg)
	if err != nil {
		log.Errorf("commitHeader - estimate gas limit error: %s", err.Error())
		return false
	}

	nonce := es.nonceManager.GetAddressNonce(es.acc.Address)
	tx := types.NewTransaction(nonce, contractaddr, big.NewInt(0), gasLimit, gasPrice, txData)
	signedtx, err := es.keyStore.SignTransaction(tx, es.acc)
	if err != nil {
		log.Errorf("commitHeader - sign raw tx error: %s", err.Error())
		return false
	}
	if err = es.ethClient.SendTransaction(context.Background(), signedtx); err != nil {
		log.Errorf("commitHeader - send transaction error:%s\n", err.Error())
		return false
	}

	hash := header.Header.Hash()
	txhash := signedtx.Hash()
	isSuccess := es.waitTransactionConfirm(fmt.Sprintf("header: %d", header.Header.Height), txhash)
	if isSuccess {
		log.Infof("successful to relay bridge header to ethereum: (header_hash: %s, height: %d, eth_txhash: %s, nonce: %d, eth_explorer: %s)",
			hash.String(), header.Header.Height, txhash.String(), nonce, tools.GetExplorerUrl(es.keyStore.GetChainId())+txhash.String())
	} else {
		log.Errorf("failed to relay poly bridge to ethereum: (header_hash: %s, height: %d, eth_txhash: %s, nonce: %d, eth_explorer: %s)",
			hash.String(), header.Header.Height, txhash.String(), nonce, tools.GetExplorerUrl(es.keyStore.GetChainId())+txhash.String())
	}
	return true
}
