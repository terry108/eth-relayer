package tools

import (
	"errors"
	"fmt"
	"io/ioutil"

	"github.com/cosmos/cosmos-sdk/codec"
	"github.com/cosmos/cosmos-sdk/crypto/keys/mintkey"
	"github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/x/auth"
	"github.com/cosmos/cosmos-sdk/x/bank"
	"github.com/polynetwork/cosmos-poly-module/btcx"
	"github.com/polynetwork/cosmos-poly-module/ccm"
	"github.com/polynetwork/cosmos-poly-module/ft"
	"github.com/polynetwork/cosmos-poly-module/headersync"
	"github.com/polynetwork/cosmos-poly-module/lockproxy"
	"github.com/tendermint/tendermint/crypto"
)

func GetBridgePrivateKey(path string, pwd []byte) (crypto.PrivKey, types.AccAddress, error) {
	bz, err := ioutil.ReadFile(path)
	if err != nil {
		return nil, types.AccAddress{}, err
	}

	privKey, _, err := mintkey.UnarmorDecryptPrivKey(string(bz), string(pwd))
	if err != nil {
		return nil, types.AccAddress{}, fmt.Errorf("failed to decrypt private key: v", err)
	}

	return privKey, types.AccAddress(privKey.PubKey().Address().Bytes()), nil
}

func CalcCosmosFees(gasPrice types.DecCoins, gas uint64) (types.Coins, error) {
	if gasPrice.IsZero() {
		return types.Coins{}, errors.New("gas price is zero")
	}
	if gas == 0 {
		return types.Coins{}, errors.New("gas is zero")
	}
	glDec := types.NewDec(int64(gas))
	fees := make(types.Coins, len(gasPrice))
	for i, gp := range gasPrice {
		fee := gp.Amount.Mul(glDec)
		fees[i] = types.NewCoin(gp.Denom, fee.Ceil().RoundInt())
	}
	return fees, nil
}

// TODO Should be bridge modules
func NewCodecForRelayer() *codec.Codec {
	cdc := codec.New()
	bank.RegisterCodec(cdc)
	types.RegisterCodec(cdc)
	codec.RegisterCrypto(cdc)
	auth.RegisterCodec(cdc)
	btcx.RegisterCodec(cdc)
	ccm.RegisterCodec(cdc)
	ft.RegisterCodec(cdc)
	headersync.RegisterCodec(cdc)
	lockproxy.RegisterCodec(cdc)
	return cdc
}
