package tools

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"strconv"

	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/params"
)

type jsonError struct {
	Code    int         `json:"code"`
	Message string      `json:"message"`
	Data    interface{} `json:"data,omitempty"`
}

type heightReq struct {
	JsonRpc string   `json:"jsonrpc"`
	Method  string   `json:"method"`
	Params  []string `json:"params"`
	Id      uint     `json:"id"`
}

type heightRsp struct {
	JsonRpc string     `json:"jsonrpc"`
	Result  string     `json:"result,omitempty"`
	Error   *jsonError `json:"error,omitempty"`
	Id      uint       `json:"id"`
}

type proofReq struct {
	JsonRPC string        `json:"jsonrpc"`
	Method  string        `json:"method"`
	Params  []interface{} `json:"params"`
	Id      uint          `json:"id"`
}

type proofRsp struct {
	JsonRPC string     `json:"jsonrpc"`
	Result  ETHProof   `json:"result,omitempty"`
	Error   *jsonError `json:"error,omitempty"`
	Id      uint       `json:"id"`
}

type ETHProof struct {
	Address       string         `json:"address"`
	Balance       string         `json:"balance"`
	CodeHash      string         `json:"codeHash"`
	Nonce         string         `json:"nonce"`
	StorageHash   string         `json:"storageHash"`
	AccountProof  []string       `json:"accountProof"`
	StorageProofs []StorageProof `json:"storageProof"`
}

type StorageProof struct {
	Key   string   `json:"key"`
	Value string   `json:"value"`
	Proof []string `json:"proof"`
}

type blockReq struct {
	JsonRpc string        `json:"jsonrpc"`
	Method  string        `json:"method"`
	Params  []interface{} `json:"params"`
	Id      uint          `json:"id"`
}

type blockRsp struct {
	JsonRPC string        `json:"jsonrpc"`
	Result  *types.Header `json:"result,omitempty"`
	Error   *jsonError    `json:"error,omitempty"`
	Id      uint          `json:"id"`
}

func GetNodeHeader(url string, restClient *RestClient, height uint64) ([]byte, error) {
	params := []interface{}{fmt.Sprintf("0x%x", height), true}
	req := &blockReq{
		JsonRpc: "2.0",
		Method:  "eth_getBlockByNumber",
		Params:  params,
		Id:      1,
	}
	reqdata, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("GetNodeHeight: marshal req err: %s", err)
	}
	rspdata, err := restClient.SendRestRequest(url, reqdata)
	if err != nil {
		return nil, fmt.Errorf("GetNodeHeight err: %s", err)
	}
	rsp := &blockRsp{}
	err = json.Unmarshal(rspdata, rsp)
	if err != nil {
		return nil, fmt.Errorf("GetNodeHeight, unmarshal resp err: %s", err)
	}
	if rsp.Error != nil {
		return nil, fmt.Errorf("GetNodeHeight, unmarshal resp err: %s", rsp.Error.Message)
	}
	block, _ := json.Marshal(rsp.Result)
	return block, nil
}

func GetNodeHeight(url string, restClient *RestClient) (uint64, error) {
	req := &heightReq{
		JsonRpc: "2.0",
		Method:  "eth_blockNumber",
		Params:  make([]string, 0),
		Id:      1,
	}
	reqData, err := json.Marshal(req)
	if err != nil {
		return 0, fmt.Errorf("GetNodeHeight: marshal req err: %s", err)
	}
	rspData, err := restClient.SendRestRequest(url, reqData)
	if err != nil {
		return 0, fmt.Errorf("GetNodeHeight err: %s", err)
	}
	rsp := &heightRsp{}
	err = json.Unmarshal(rspData, rsp)
	if err != nil {
		return 0, fmt.Errorf("GetNodeHeight, unmarshal resp err: %s", err)
	}
	if rsp.Error != nil {
		return 0, fmt.Errorf("GetNodeHeight, unmarshal resp err: %s", rsp.Error.Message)
	}
	height, err := strconv.ParseUint(rsp.Result, 0, 64)
	if err != nil {
		return 0, fmt.Errorf("GetNodeHeight, parse resp height %s failed", rsp.Result)
	} else {
		return height, nil
	}
}

func GetProof(url string, contractAddress string, key string, blockheight string, restClient *RestClient) ([]byte, error) {
	req := &proofReq{
		JsonRPC: "2.0",
		Method:  "eth_getProof",
		Params:  []interface{}{contractAddress, []string{key}, blockheight},
		Id:      1,
	}
	reqdata, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("get_ethproof: marshal req err: %s", err)
	}
	rspdata, err := restClient.SendRestRequest(url, reqdata)
	if err != nil {
		return nil, fmt.Errorf("GetProof: send request err: %s", err)
	}
	rsp := &proofRsp{}
	err = json.Unmarshal(rspdata, rsp)
	if err != nil {
		return nil, fmt.Errorf("GetProof, unmarshal resp err: %s", err)
	}
	if rsp.Error != nil {
		return nil, fmt.Errorf("GetProof, unmarshal resp err: %s", rsp.Error.Message)
	}
	result, err := json.Marshal(rsp.Result)
	if err != nil {
		return nil, fmt.Errorf("GetProof, Marshal result err: %s", err)
	}
	//fmt.Printf("proof res is:%s\n", string(result))
	return result, nil
}

func EncodeBigInt(b *big.Int) string {
	if b.Uint64() == 0 {
		return "00"
	}
	return hex.EncodeToString(b.Bytes())
}

func GetExplorerUrl(chainId uint64) string {
	switch chainId {
	case params.MainnetChainConfig.ChainID.Uint64():
		return "https://etherscan.io/tx/"
	case params.RopstenChainConfig.ChainID.Uint64():
		return "https://ropsten.etherscan.io/tx/"
	default:
		return "no url"
	}
}
