package main

import (
	chat_context "ai-chat-service/chat-server/chat-context"
	"ai-chat-service/chat-server/data"
	metrics_app "ai-chat-service/chat-server/metrics-app"
	metrics_bus "ai-chat-service/chat-server/metrics-bus"
	"ai-chat-service/chat-server/retrieval/kvlite"
	"ai-chat-service/chat-server/server"
	"ai-chat-service/interceptor"
	"ai-chat-service/pkg/config"
	"ai-chat-service/pkg/db/mysql"
	"ai-chat-service/pkg/db/redis"
	"ai-chat-service/pkg/log"
	"ai-chat-service/proto"
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	"google.golang.org/grpc/health/grpc_health_v1"

	"net"
)

var (
	configFile = flag.String("config", "dev.config.yaml", "")
)

// 核心服务的启动函数，负责初始化配置、日志、数据库连接，并启动gRPC服务器。
func main() {
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	registry := prometheus.NewRegistry()
	registry.MustRegister(collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	busMetrics := metrics_bus.NewBusMetrics(registry)

	metricsServer := &http.Server{Addr: ":8080", Handler: promhttp.HandlerFor(registry, promhttp.HandlerOpts{})}
	go func() {
		if err := metricsServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error(err)
		}
	}()

	//初始化配置文件
	config.InitConfig(*configFile)
	cnf := config.GetConfig()
	//初始化日志
	log.SetLevel(cnf.Log.Level)
	log.SetOutput(log.GetRotateWriter(cnf.Log.LogPath))
	log.SetPrintCaller(true)

	logger := log.NewLogger()
	logger.SetLevel(cnf.Log.Level)
	logger.SetOutput(log.GetRotateWriter(cnf.Log.LogPath))
	logger.SetPrintCaller(true)

	// 初始化Mysql
	mysql.InitMysql(cnf)
	// 初始化redis
	redis.InitRedisPool(cnf)
	contextStore, err := chat_context.NewStore(cnf, logger, busMetrics)
	if err != nil {
		log.Fatal(err)
	}
	vectorStore, retrievalDisabledReason, err := newVectorStore(cnf, logger)
	if err != nil {
		log.Fatal(err)
	}
	if vectorStore != nil {
		defer vectorStore.Close()
	}
	defer contextStore.Close()
	recordsData := data.NewChatRecordsData(mysql.GetDB())

	lis, err := net.Listen("tcp", fmt.Sprintf("%s:%d", cnf.Server.IP, cnf.Server.Port))
	if err != nil {
		log.Fatal(err)
	}
	s := grpc.NewServer(grpc.UnaryInterceptor(interceptor.UnaryAuthInterceptor), grpc.StreamInterceptor(metrics_app.NewStreamMiddleware(registry).WrapHandler()))
	service := server.NewChatService(recordsData, cnf, logger, busMetrics, contextStore, vectorStore, retrievalDisabledReason) //创建grpc服务，并注册服务实现
	proto.RegisterChatServer(s, service)

	healthCheckSrv := health.NewServer()
	grpc_health_v1.RegisterHealthServer(s, healthCheckSrv)

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		stopped := make(chan struct{})
		go func() {
			s.GracefulStop()
			close(stopped)
		}()
		select {
		case <-stopped:
		case <-shutdownCtx.Done():
			s.Stop()
		}
		if shutdownErr := metricsServer.Shutdown(shutdownCtx); shutdownErr != nil && !errors.Is(shutdownErr, context.Canceled) {
			log.Error(shutdownErr)
		}
	}()

	if err = s.Serve(lis); err != nil && !errors.Is(err, grpc.ErrServerStopped) {
		log.Fatal(err)
	}
}

func newVectorStore(cnf *config.Config, logger log.ILogger) (kvlite.Store, string, error) {
	if cnf == nil || !cnf.Retrieval.KVLite.Enabled {
		return nil, "kvlite_retrieval_disabled", nil
	}
	retrieval := cnf.Retrieval.KVLite
	clientConfig := kvlite.DefaultConfig(retrieval.Address)
	clientConfig.ConnectTimeout = time.Duration(retrieval.TimeoutMS) * time.Millisecond
	clientConfig.ReadTimeout = time.Duration(retrieval.TimeoutMS) * time.Millisecond
	clientConfig.WriteTimeout = time.Duration(retrieval.TimeoutMS) * time.Millisecond
	clientConfig.PoolSize = retrieval.PoolSize
	clientConfig.MaxIdle = retrieval.MaxIdle
	store, err := kvlite.NewStore(clientConfig)
	if err != nil {
		return nil, "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(retrieval.TimeoutMS)*time.Millisecond)
	defer cancel()
	if err := store.Health(ctx); err == nil {
		logger.InfoF("KVLite vector retrieval ready at %s", retrieval.Address)
		return store, "", nil
	} else if strings.EqualFold(strings.TrimSpace(retrieval.StartupPolicy), "fallback_llm") {
		_ = store.Close()
		reason := kvlite.FailureReason(err)
		logger.WarningF("KVLite vector retrieval unavailable at startup; using LLM fallback: reason=%s err=%v", reason, err)
		return nil, reason, nil
	} else {
		_ = store.Close()
		return nil, "", fmt.Errorf("KVLite vector retrieval startup health check failed: %w", err)
	}
}
