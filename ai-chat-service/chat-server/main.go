package main

import (
	chat_context "ai-chat-service/chat-server/chat-context"
	"ai-chat-service/chat-server/data"
	metrics_app "ai-chat-service/chat-server/metrics-app"
	metrics_bus "ai-chat-service/chat-server/metrics-bus"
	"ai-chat-service/chat-server/qa"
	"ai-chat-service/chat-server/server"
	vector_data "ai-chat-service/chat-server/vector-data"
	"ai-chat-service/interceptor"
	"ai-chat-service/pkg/config"
	"ai-chat-service/pkg/db/mysql"
	"ai-chat-service/pkg/db/redis"
	"ai-chat-service/pkg/db/vector"
	"ai-chat-service/pkg/log"
	"ai-chat-service/proto"
	"flag"
	"fmt"
	"net/http"

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
	registry := prometheus.NewRegistry()
	registry.MustRegister(collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	busMetrics := metrics_bus.NewBusMetrics(registry)

	http.Handle("/metrics", promhttp.HandlerFor(registry, promhttp.HandlerOpts{}))
	go http.ListenAndServe(":8080", nil)

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
	// 初始化向量数据库
	vector.InitDB(cnf)
	qaStore, err := qa.NewKVLiteStore(cnf)
	if err != nil {
		logger.Warning(err)
		qaStore = qa.NewNoopRetriever().(qa.Store)
	}

	recordsData := data.NewChatRecordsData(mysql.GetDB())

	lis, err := net.Listen("tcp", fmt.Sprintf("%s:%d", cnf.Server.IP, cnf.Server.Port))
	if err != nil {
		log.Fatal(err)
	}
	s := grpc.NewServer(grpc.UnaryInterceptor(interceptor.UnaryAuthInterceptor), grpc.StreamInterceptor(metrics_app.NewStreamMiddleware(registry).WrapHandler()))
	service := server.NewChatService(recordsData, vector_data.NewChatRecordsData(cnf, vector.GetVdb()), cnf, logger, busMetrics, contextStore, qaStore) //创建grpc服务，并注册服务实现
	proto.RegisterChatServer(s, service)

	healthCheckSrv := health.NewServer()
	grpc_health_v1.RegisterHealthServer(s, healthCheckSrv)

	if err = s.Serve(lis); err != nil {
		log.Fatal(err)
	}
}
