package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	httpRequestsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "http_requests_total",
		Help: "Total number of HTTP requests processed by the API gateway",
	}, []string{"method", "path", "status_code", "service"})

	httpRequestDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "http_request_duration_seconds",
		Help:    "HTTP request latency distribution",
		Buckets: prometheus.DefBuckets,
	}, []string{"method", "path", "service"})

	httpRequestsInFlight = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "http_requests_in_flight",
		Help: "Current number of HTTP requests being served",
	})

	upstreamErrors = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "upstream_errors_total",
		Help: "Total number of upstream service errors",
	}, []string{"service"})
)

type Config struct {
	Port           string
	ProductService string
	OrderService   string
	ReadTimeout    time.Duration
	WriteTimeout   time.Duration
}

func configFromEnv() Config {
	return Config{
		Port:           getEnv("PORT", "8080"),
		ProductService: getEnv("PRODUCT_SERVICE_URL", "http://product-service:8081"),
		OrderService:   getEnv("ORDER_SERVICE_URL", "http://order-service:8082"),
		ReadTimeout:    30 * time.Second,
		WriteTimeout:   30 * time.Second,
	}
}

func getEnv(key, defaultVal string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultVal
}

func prometheusMiddleware(service string) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		path := c.FullPath()
		if path == "" {
			path = c.Request.URL.Path
		}
		httpRequestsInFlight.Inc()
		defer httpRequestsInFlight.Dec()

		c.Next()

		duration := time.Since(start).Seconds()
		statusCode := fmt.Sprintf("%d", c.Writer.Status())

		httpRequestsTotal.WithLabelValues(c.Request.Method, path, statusCode, service).Inc()
		httpRequestDuration.WithLabelValues(c.Request.Method, path, service).Observe(duration)
	}
}

func reverseProxy(target string, service string) gin.HandlerFunc {
	targetURL, err := url.Parse(target)
	if err != nil {
		log.Fatalf("invalid upstream URL %s: %v", target, err)
	}
	proxy := httputil.NewSingleHostReverseProxy(targetURL)
	proxy.ErrorHandler = func(rw http.ResponseWriter, req *http.Request, err error) {
		log.Printf("[proxy] upstream %s error: %v", service, err)
		upstreamErrors.WithLabelValues(service).Inc()
		rw.WriteHeader(http.StatusBadGateway)
		fmt.Fprintf(rw, `{"error":"upstream service unavailable","service":"%s"}`, service)
	}
	return func(c *gin.Context) {
		c.Request.Header.Set("X-Forwarded-For", c.ClientIP())
		c.Request.Header.Set("X-Request-ID", c.GetHeader("X-Request-ID"))
		proxy.ServeHTTP(c.Writer, c.Request)
	}
}

func main() {
	cfg := configFromEnv()

	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Recovery())
	r.Use(prometheusMiddleware("api-gateway"))

	// Health & readiness
	r.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"status":    "ok",
			"service":   "api-gateway",
			"timestamp": time.Now().UTC(),
		})
	})
	r.GET("/ready", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ready"})
	})

	// Metrics endpoint
	r.GET("/metrics", gin.WrapH(promhttp.Handler()))

	// Product service routes
	r.Any("/api/v1/products", reverseProxy(cfg.ProductService, "product-service"))
	r.Any("/api/v1/products/*path", reverseProxy(cfg.ProductService, "product-service"))

	// Order service routes
	r.Any("/api/v1/orders", reverseProxy(cfg.OrderService, "order-service"))
	r.Any("/api/v1/orders/*path", reverseProxy(cfg.OrderService, "order-service"))

	srv := &http.Server{
		Addr:         ":" + cfg.Port,
		Handler:      r,
		ReadTimeout:  cfg.ReadTimeout,
		WriteTimeout: cfg.WriteTimeout,
	}

	// Graceful shutdown
	go func() {
		log.Printf("API Gateway listening on :%s", cfg.Port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("listen: %v", err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	log.Println("Shutting down API Gateway...")

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Fatalf("forced shutdown: %v", err)
	}
	log.Println("API Gateway exited")
}
