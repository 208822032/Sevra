package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"github.com/gin-gonic/gin"
	"net/http"
	"openai-api-proxy/pkg/config"
	"openai-api-proxy/pkg/log"
	"openai-api-proxy/routers"
	"os"
	"os/signal"
	"syscall"
	"time"
)

var (
	configFile = flag.String("config", "dev.config.yaml", "")
)

func main() {
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	config.InitConfig(*configFile)
	conf := config.GetConfig()

	gin.SetMode(conf.Http.Mode)

	//初始化日志组件
	log.SetOutput(log.GetRotateWriter(conf.Log.LogPath))
	log.SetLevel(conf.Log.Level)
	log.SetPrintCaller(true)

	r := gin.Default()
	routers.InitRouters(r)
	server := &http.Server{Addr: fmt.Sprintf("%s:%d", conf.Http.Host, conf.Http.Port), Handler: r}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil && !errors.Is(err, context.Canceled) {
			log.Error(err)
		}
	}()
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Error(err)
	}
}
