// +build js

package main

import (
	"context"
	"crypto/tls"
	"errors"
	"net/http"
	"os"
	"runtime/debug"

	// "runtime/debug"

	"github.com/jessevdk/go-flags"
	"github.com/lightninglabs/loop/looprpc"
	"github.com/lightninglabs/pool/poolrpc"
	"github.com/lightningnetwork/lnd/build"
	"github.com/lightningnetwork/lnd/lnrpc"
	"github.com/lightningnetwork/lnd/lnrpc/autopilotrpc"
	"github.com/lightningnetwork/lnd/lnrpc/chainrpc"
	"github.com/lightningnetwork/lnd/lnrpc/invoicesrpc"
	"github.com/lightningnetwork/lnd/lnrpc/routerrpc"
	"github.com/lightningnetwork/lnd/lnrpc/signrpc"
	"github.com/lightningnetwork/lnd/lnrpc/verrpc"
	"github.com/lightningnetwork/lnd/lnrpc/walletrpc"
	"github.com/lightningnetwork/lnd/lnrpc/watchtowerrpc"
	"github.com/lightningnetwork/lnd/lnrpc/wtclientrpc"
	"github.com/lightningnetwork/lnd/signal"
	"github.com/teamortix/golang-wasm/wasm"
	"google.golang.org/grpc"
)

type stubPackageRegistration func(map[string]func(context.Context,
	*grpc.ClientConn, string, func(string, error)))

var (
	cfg     = config{}
	lndConn *grpc.ClientConn

	registry = make(map[string]func(context.Context, *grpc.ClientConn,
		string, func(string, error)))

	registrations = []stubPackageRegistration{
		lnrpc.RegisterLightningJSONCallbacks,
		lnrpc.RegisterStateJSONCallbacks,
		autopilotrpc.RegisterAutopilotJSONCallbacks,
		chainrpc.RegisterChainNotifierJSONCallbacks,
		invoicesrpc.RegisterInvoicesJSONCallbacks,
		routerrpc.RegisterRouterJSONCallbacks,
		signrpc.RegisterSignerJSONCallbacks,
		verrpc.RegisterVersionerJSONCallbacks,
		walletrpc.RegisterWalletKitJSONCallbacks,
		watchtowerrpc.RegisterWatchtowerJSONCallbacks,
		wtclientrpc.RegisterWatchtowerClientJSONCallbacks,
		looprpc.RegisterSwapClientJSONCallbacks,
		poolrpc.RegisterTraderJSONCallbacks,
	}
)

func main() {
	defer func() {
		if r := recover(); r != nil {
			log.Debugf("Recovered in f: %v", r)
			debug.PrintStack()
		}
	}()

	wasm.Expose("isReady", wasmClientIsReady)
	wasm.Expose("connectServer", wasmClientConnectServer)
	wasm.Expose("isConnected", wasmClientIsConnected)
	wasm.Expose("disconnect", wasmClientDisconnect)
	wasm.Expose("invokeRPC", wasmClientInvokeRPC)

	wasm.Ready()

	for _, registration := range registrations {
		registration(registry)
	}

	// Parse command line flags.
	parser := flags.NewParser(&cfg, flags.Default)
	parser.SubcommandsOptional = true

	_, err := parser.Parse()
	if e, ok := err.(*flags.Error); ok && e.Type == flags.ErrHelp {
		exit(err)
	}
	if err != nil {
		exit(err)
	}
	//
	// 	// Hook interceptor for os signals.
	shutdownInterceptor, err := signal.Intercept()
	if err != nil {
		exit(err)
	}

	logWriter := build.NewRotatingLogWriter()
	SetupLoggers(logWriter, shutdownInterceptor)

	err = build.ParseAndSetDebugLevels(cfg.DebugLevel, logWriter)
	if err != nil {
		exit(err)
	}

	log.Debugf("WASM client ready for connecting")

	select {
	case <-shutdownInterceptor.ShutdownChannel():
		log.Debugf("Shutting down WASM client")
		wasmClientDisconnect()
		log.Debugf("Shutdown of WASM client complete")
	}
	<-make(chan bool) // To use anything from Go WASM, the program may not exit.
}

func wasmClientIsReady() bool {
	// This will always return true. So as soon as this method is called
	// successfully the JS part knows the WASM instance is fully started up
	// and ready to connect.
	return true
}

func wasmClientConnectServer(mailboxServer string, isDevServer bool, pairingPhrase string) {
	// Disable TLS verification for the REST connections if this is a dev
	// server.
	if isDevServer {
		defaultHttpTransport := http.DefaultTransport.(*http.Transport)
		defaultHttpTransport.TLSClientConfig = &tls.Config{
			InsecureSkipVerify: true,
		}
	}

	var err error
	lndConn, err = mailboxRPCConnection(mailboxServer, pairingPhrase)
	if err != nil {
		exit(err)
	}
}

func wasmClientIsConnected() bool {
	return lndConn != nil
}

func wasmClientDisconnect() {
	if lndConn != nil {
		if err := lndConn.Close(); err != nil {
			log.Errorf("Error closing RPC connection: %v", err)
		}
	}
}

func wasmClientInvokeRPC(rpcName string, requestJSON string, jsCallback func(resultJSON string, err error)) interface{} {
	if lndConn == nil {
		return wasm.NewPromise(func() (interface{}, error) {
			return nil, errors.New("RPC connection not ready")
		})
	}

	method, ok := registry[rpcName]
	if !ok {
		return wasm.NewPromise(func() (interface{}, error) {
			return nil, errors.New("rpc with name " + rpcName + " not found")
		})
	}

	go func() {
		log.Infof("Calling '%s' on RPC with request %s", rpcName, requestJSON)
		cb := func(resultJSON string, err error) {
			if err != nil {
				jsCallback("", err)
				//jsCallback.Invoke(js.ValueOf(err.Error()))
			} else {
				jsCallback(resultJSON, nil)
				//jsCallback.Invoke(js.ValueOf(resultJSON))
			}
		}
		ctx := context.Background()
		method(ctx, lndConn, requestJSON, cb)
		<-ctx.Done()
	}()
	return nil

}

func exit(err error) {
	log.Debugf("Error running wasm client: %v", err)
	os.Exit(1)
}
