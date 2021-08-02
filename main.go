package main

import (
	"fmt"
	"os"
	"os/signal"
	"runtime"
	"syscall"

	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/tendermint/tendermint/rpc/client/http"
	"github.com/terry108/eth-relayer/cmd"
	"github.com/terry108/eth-relayer/config"
	"github.com/terry108/eth-relayer/db"
	"github.com/terry108/eth-relayer/log"
	"github.com/terry108/eth-relayer/manager"
	"github.com/urfave/cli"
)

var ConfigPath string
var LogDir string
var StartHeight uint64
var BridgeStartHeight uint64
var StartForceHeight uint64

func setupApp() *cli.App {
	app := cli.NewApp()
	app.Usage = "ETH relayer Service"
	app.Action = startServer
	app.Version = config.Version
	app.Copyright = "Copyright in 2021 The Pi Bridge Authors"
	app.Flags = []cli.Flag{
		cmd.LogLevelFlag,
		cmd.ConfigPathFlag,
		cmd.EthStartFlag,
		cmd.EthStartForceFlag,
		// cmd.PolyStartFlag,
		cmd.LogDir,
	}
	app.Commands = []cli.Command{}
	app.Before = func(context *cli.Context) error {
		runtime.GOMAXPROCS(runtime.NumCPU())
		return nil
	}
	return app
}

func startServer(ctx *cli.Context) {
	// get all cmd flag
	logLevel := ctx.GlobalInt(cmd.GetFlagName(cmd.LogLevelFlag))

	ld := ctx.GlobalString(cmd.GetFlagName(cmd.LogDir))
	log.InitLog(logLevel, ld, log.Stdout)

	ConfigPath = ctx.GlobalString(cmd.GetFlagName(cmd.ConfigPathFlag))
	ethstart := ctx.GlobalUint64(cmd.GetFlagName(cmd.EthStartFlag))
	if ethstart > 0 {
		StartHeight = ethstart
	}

	StartForceHeight = 0
	ethstartforce := ctx.GlobalUint64(cmd.GetFlagName(cmd.EthStartForceFlag))
	if ethstartforce > 0 {
		StartForceHeight = ethstartforce
	}
	bridgeStart := ctx.GlobalUint64(cmd.GetFlagName(cmd.BridgeStartFlag))
	if bridgeStart > 0 {
		BridgeStartHeight = bridgeStart
	}

	// read config
	servConfig := config.NewServiceConfig(ConfigPath)
	if servConfig == nil {
		log.Errorf("startServer - create config failed!")
		return
	}

	// create pi bridge sdk
	bridgeSdk, err := http.New(servConfig.BridgeConfig.BridgeRpcAddr, "/websocket")
	if err != nil {
		log.Errorf("startServer - failed to new Tendermint Cli: %v", err)
		return
	}

	// create ethereum sdk
	ethereumSdk, err := ethclient.Dial(servConfig.ETHConfig.RestURL)
	if err != nil {
		log.Errorf("startServer - cannot dial sync node, err: %s", err)
		return
	}

	var boltDB *db.BoltDB
	if servConfig.BoltDbPath == "" {
		boltDB, err = db.NewBoltDB("boltdb")
	} else {
		boltDB, err = db.NewBoltDB(servConfig.BoltDbPath)
	}
	if err != nil {
		log.Fatalf("db.NewWaitingDB error:%s", err)
		return
	}

	initBridgeServer(servConfig, bridgeSdk, ethereumSdk, boltDB)
	initETHServer(servConfig, bridgeSdk, ethereumSdk, boltDB)
	waitToExit()
}

// func setUpPoly(poly *sdk.PolySdk, RpcAddr string) error {
// 	poly.NewRpcClient().SetAddress(RpcAddr)
// 	hdr, err := poly.GetHeaderByHeight(0)
// 	if err != nil {
// 		return err
// 	}
// 	poly.SetChainId(hdr.ChainID)
// 	return nil
// }

func waitToExit() {
	exit := make(chan bool)
	sc := make(chan os.Signal, 1)
	signal.Notify(sc, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	go func() {
		for sig := range sc {
			log.Infof("waitToExit - ETH relayer received exit signal:%v.", sig.String())
			close(exit)
			break
		}
	}()
	<-exit
}

func initETHServer(servConfig *config.ServiceConfig, bridgeSDK *http.HTTP, ethereumsdk *ethclient.Client, boltDB *db.BoltDB) {
	mgr, err := manager.NewEthereumManager(servConfig, StartHeight, StartForceHeight, bridgeSDK, ethereumsdk, boltDB)
	if err != nil {
		log.Error("initETHServer - eth service start err: %s", err.Error())
		return
	}
	_ = mgr
	go mgr.MonitorChain()
	go mgr.MonitorDeposit()
	go mgr.CheckDeposit()
}

func initBridgeServer(servConfig *config.ServiceConfig, bridgeSDK *http.HTTP, ethereumsdk *ethclient.Client, boltDB *db.BoltDB) {
	mgr, err := manager.NewBridgeManager(servConfig, BridgeStartHeight, bridgeSDK, ethereumsdk, boltDB)
	if err != nil {
		log.Error("initPolyServer - PolyServer service start failed: %v", err)
		return
	}
	go mgr.MonitorChain()
}

func main() {
	log.Infof("main - ETH relayer starting...")
	if err := setupApp().Run(os.Args); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
