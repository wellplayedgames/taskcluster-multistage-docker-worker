package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/alecthomas/kong"
	"github.com/go-logr/logr"
	"github.com/go-logr/zapr"
	"go.uber.org/zap"
)

var cli struct {
	Run Run `cmd help:"Run the worker"`

	Verbose bool `help:"Run with verbose logging."`
}

type commandContext struct {
	context.Context
	logr.Logger
	StopCh <-chan struct{}
}

func main() {
	zapLog, err := zap.NewProduction()
	if err != nil {
		panic(err)
	}
	log := zapr.NewLogger(zapLog)
	args := kong.Parse(&cli)

	ctx, cancelCtx := context.WithCancel(context.Background())
	defer cancelCtx()

	stopCh := make(chan struct{})
	signalCh := make(chan os.Signal)
	signal.Notify(signalCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-signalCh
		log.Info("Shutting down")
		close(stopCh)
		<-signalCh
		log.Info("Shutting down immediately")
		cancelCtx()
	}()

	cmdCtx := &commandContext{
		Context: ctx,
		Logger:  log,
		StopCh:  stopCh,
	}
	err = args.Run(cmdCtx)
	if err != nil {
		log.Error(err, "failed to run command")
		os.Exit(1)
	}
}
