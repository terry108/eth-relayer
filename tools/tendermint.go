package tools

import (
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/cosmos/cosmos-sdk/codec"
	"github.com/polynetwork/cosmos-poly-module/ccm"
	"github.com/tendermint/tendermint/crypto/merkle"
	"github.com/tendermint/tendermint/rpc/client"
	"github.com/tendermint/tendermint/rpc/client/http"
	rpctypes "github.com/tendermint/tendermint/rpc/core/types"
	"github.com/tendermint/tendermint/types"
)

type BridgeHeader struct {
	Header  types.Header
	Commit  *types.Commit
	Valsets []*types.Validator
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

func GetBridgeHdr(rpcCli *http.HTTP, height int64) (*BridgeHeader, error) {
	rc, err := rpcCli.Commit(&height)
	if err != nil {
		return nil, fmt.Errorf("failed to get Commit of height %d: %v", height, err)
	}
	vSet, err := GetValidators(rpcCli, height)
	if err != nil {
		return nil, fmt.Errorf("failed to get Validators of height %d: %v", height, err)
	}
	return &BridgeHeader{
		Header:  *rc.Header,
		Commit:  rc.Commit,
		Valsets: vSet,
	}, nil
}

func GetValidators(rpcCli *http.HTTP, height int64) ([]*types.Validator, error) {
	p := 1
	vSet := make([]*types.Validator, 0)
	for {
		res, err := rpcCli.Validators(&height, p, 100)
		if err != nil {
			if strings.Contains(err.Error(), "page should be within") {
				return vSet, nil
			}
			return nil, err
		}
		// In case tendermint don't give relayer the right error
		if len(res.Validators) == 0 {
			return vSet, nil
		}
		vSet = append(vSet, res.Validators...)
		p++
	}
}

func GetBridgeTxs(rpcCli *http.HTTP, height int64) ([]*BridgeTx, error) {
	txs := make([]*BridgeTx, 0)
	txQuery := fmt.Sprintf("tx.height=%d AND make_from_cosmos_proof.status='1'", height)
	res, err := rpcCli.TxSearch(txQuery, true, 1, PerPage, "asc")
	if err != nil {
		return nil, err
	}

	pages := ((res.TotalCount - 1) / PerPage) + 1
	for p := 1; p <= pages; p++ {
		// already have page 1
		if p > 1 {
			if res, err = rpcCli.TxSearch(txQuery, true, p, PerPage, "asc"); err != nil {
				return nil, err
			}
		}
		// get proof for every tx, and add them to txArr prepared to commit
		for _, tx := range res.Txs {
			hash := getKeyHash(tx)
			res, _ := rpcCli.ABCIQueryWithOptions(ProofPath, ccm.GetCrossChainTxKey(hash),
				client.ABCIQueryOptions{Prove: true, Height: height})
			proof, _ := res.Response.Proof.Marshal()
			kp := merkle.KeyPath{}
			kp = kp.AppendKey([]byte(CosmosCrossChainModName), merkle.KeyEncodingURL)
			kp = kp.AppendKey(res.Response.Key, merkle.KeyEncodingURL)
			cmCdc := codec.New()
			pv, _ := cmCdc.MarshalBinaryBare(&CosmosProofValue{
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
