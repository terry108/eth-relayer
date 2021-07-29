package tools

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/core/types"
	"github.com/terry108/eth-relayer/config"
)

func TestETHSigner_SignTransaction(t *testing.T) {
	cfg := config.NewServiceConfig("../config-debug.json")
	ethsigner := NewEthKeyStore(cfg.ETHConfig, big.NewInt(10))
	accounts := ethsigner.GetAccounts()
	err := ethsigner.UnlockKeys(cfg.ETHConfig)
	if err != nil {
		t.Fatal(err)
	}
	tx := &types.Transaction{}
	tx, err = ethsigner.SignTransaction(tx, accounts[0])
	if err != nil {
		t.Fatal(err)
	}
	v, r, s := tx.RawSignatureValues()
	if v.BitLen()+r.BitLen()+s.BitLen() <= 0 {
		t.Fatal("failed to sign")
	}
}
