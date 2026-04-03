package main

import (
	"ai-chat-backend/pkg/config"
	"ai-chat-backend/pkg/log"
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"time"

	"ai-chat-backend/pkg/controllers"
	"ai-chat-backend/pkg/middlewares"

	"github.com/gin-gonic/gin"
)

type ChatGPTWebServer struct {
	config *config.Config
	log    log.ILogger
}

func (r *ChatGPTWebServer) Run(ctx context.Context) error {
	go r.httpServer(ctx)
	return nil
}

func (r *ChatGPTWebServer) httpServer(ctx context.Context) {
	//创建ChatService实例
	chatService, err := controllers.NewChatService(r.config, r.log)
	if err != nil {
		r.log.Fatal(err)
	}

	addr := fmt.Sprintf("%s:%d", r.config.Http.IP, r.config.Http.Port)
	r.log.InfoF("ChatGPT Web Server on: %s", addr)
	server := &http.Server{
		Addr: addr,
	}
	//创建Gin引擎
	entry := gin.Default()
	//挂载中间件和路由
	entry.Use(middlewares.Cors())
	//注册路由 /api/chat-process /api/config /api/session /api/health
	chat := entry.Group("/api")
	chat.POST("/chat-process", chatService.ChatProcess)
	chat.POST("/config", func(ctx *gin.Context) {
		ctx.JSON(200, gin.H{
			"status": "Success",
			"data": map[string]string{
				"apiModel":   "ChatGPTAPI",
				"socksProxy": "",
			},
		})
	})
	chat.POST("/session", func(ctx *gin.Context) {
		ctx.JSON(200, gin.H{
			"status":  "Success",
			"message": "",
			"data": gin.H{
				"auth": false,
			},
		})
	})
	//健康检查
	chat.GET("/health", func(c *gin.Context) {})
	// 静态文件服务
	fs := http.FileServer(http.Dir("www"))
	entry.NoRoute(func(ctx *gin.Context) {
		// 如果路径没有命中当前定义的路由，则尝试访问静态文件
		fs.ServeHTTP(ctx.Writer, ctx.Request)
	})
	//设置根路由，访问根路径时返回index.html
	entry.GET("/", func(ctx *gin.Context) {
		// 定向到html
		http.ServeFile(ctx.Writer, ctx.Request, "www/index.html")
	})

	// 将Gin引擎作为HTTP服务器的处理器
	server.Handler = entry
	go func(ctx context.Context) {
		<-ctx.Done()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		// 优雅关闭服务器
		if err := server.Shutdown(ctx); err != nil {
			r.log.InfoF("Server shutdown with error %v", err)
		}
	}(ctx)
	//开始监听
	if err := server.ListenAndServe(); err != http.ErrServerClosed {
		r.log.FatalF("Server listen and serve error %v", err)
	}
}

var (
	configFile = flag.String("config", "dev.config.yaml", "")
)

func main() {
	flag.Parse() //读取config参数
	// 监听系统信号，优雅关闭服务
	ctx, stop := signal.NotifyContext(context.Background(), os.Kill, os.Interrupt)
	defer stop()

	//读取yaml配置文件
	config.InitConfig(*configFile)
	cnf := config.GetConfig()
	fmt.Printf("%+v\n", cnf)

	//初始化日志
	log.SetLevel(cnf.Log.Level)
	log.SetOutput(log.GetRotateWriter(cnf.Log.LogPath))
	log.SetPrintCaller(true)

	logger := log.NewLogger()
	logger.SetLevel(cnf.Log.Level)
	logger.SetOutput(log.GetRotateWriter(cnf.Log.LogPath))
	logger.SetPrintCaller(true)

	//创建ChatGPTWebServer实例并运行
	webServer := &ChatGPTWebServer{
		config: cnf,
		log:    logger,
	}
	//启动HTTP服务协程
	webServer.Run(ctx)
	<-ctx.Done()
}
