package tools

import (
	"time"

	"github.com/polynetwork/cosmos-poly-module/ccm"
	"github.com/polynetwork/cosmos-poly-module/headersync"
)

const (
	PerPage                 = 100 // (0, 100]
	HdrLimitPerBatch        = 50
	QueryConsensusPath      = "/custom/" + headersync.ModuleName + "/" + headersync.QueryConsensusPeers
	CosmosCrossChainModName = ccm.ModuleName
	RightHeightUpdate       = "update latest height"
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

var (
	SleepSecs = func(n int) {
		time.Sleep(time.Duration(n) * time.Second)
	}
)
