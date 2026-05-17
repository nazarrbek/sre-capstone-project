package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	_ "github.com/lib/pq"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// ─── Metrics ─────────────────────────────────────────────────────────────────

var (
	requestsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: "product_service",
		Name:      "http_requests_total",
		Help:      "Total HTTP requests handled by product-service",
	}, []string{"method", "endpoint", "status"})

	requestDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: "product_service",
		Name:      "http_request_duration_seconds",
		Help:      "Request latency histogram",
		Buckets:   []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5},
	}, []string{"method", "endpoint"})

	dbQueryDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: "product_service",
		Name:      "db_query_duration_seconds",
		Help:      "Database query latency",
		Buckets:   []float64{.001, .005, .01, .05, .1, .5, 1},
	}, []string{"operation"})

	productsInStock = promauto.NewGauge(prometheus.GaugeOpts{
		Namespace: "product_service",
		Name:      "products_in_stock_total",
		Help:      "Current number of products with stock > 0",
	})
)

// ─── Domain ───────────────────────────────────────────────────────────────────

type Product struct {
	ID          int64     `json:"id"`
	Name        string    `json:"name" binding:"required,min=1,max=255"`
	Description string    `json:"description"`
	Price       float64   `json:"price" binding:"required,gt=0"`
	Stock       int       `json:"stock" binding:"gte=0"`
	Category    string    `json:"category"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// ─── Repository ───────────────────────────────────────────────────────────────

type ProductRepository struct {
	db *sql.DB
}

func NewProductRepository(db *sql.DB) *ProductRepository {
	return &ProductRepository{db: db}
}

func (r *ProductRepository) Create(ctx context.Context, p *Product) error {
	timer := prometheus.NewTimer(dbQueryDuration.WithLabelValues("insert"))
	defer timer.ObserveDuration()

	query := `INSERT INTO products (name, description, price, stock, category, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, NOW(), NOW()) RETURNING id, created_at, updated_at`
	return r.db.QueryRowContext(ctx, query, p.Name, p.Description, p.Price, p.Stock, p.Category).
		Scan(&p.ID, &p.CreatedAt, &p.UpdatedAt)
}

func (r *ProductRepository) GetByID(ctx context.Context, id int64) (*Product, error) {
	timer := prometheus.NewTimer(dbQueryDuration.WithLabelValues("select_by_id"))
	defer timer.ObserveDuration()

	p := &Product{}
	query := `SELECT id, name, description, price, stock, category, created_at, updated_at
		FROM products WHERE id = $1`
	err := r.db.QueryRowContext(ctx, query, id).
		Scan(&p.ID, &p.Name, &p.Description, &p.Price, &p.Stock, &p.Category, &p.CreatedAt, &p.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return p, err
}

func (r *ProductRepository) List(ctx context.Context, limit, offset int) ([]*Product, int, error) {
	timer := prometheus.NewTimer(dbQueryDuration.WithLabelValues("list"))
	defer timer.ObserveDuration()

	var total int
	r.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM products").Scan(&total)

	rows, err := r.db.QueryContext(ctx,
		`SELECT id, name, description, price, stock, category, created_at, updated_at
		FROM products ORDER BY created_at DESC LIMIT $1 OFFSET $2`, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var products []*Product
	for rows.Next() {
		p := &Product{}
		if err := rows.Scan(&p.ID, &p.Name, &p.Description, &p.Price, &p.Stock,
			&p.Category, &p.CreatedAt, &p.UpdatedAt); err != nil {
			return nil, 0, err
		}
		products = append(products, p)
	}
	return products, total, rows.Err()
}

func (r *ProductRepository) Update(ctx context.Context, p *Product) error {
	timer := prometheus.NewTimer(dbQueryDuration.WithLabelValues("update"))
	defer timer.ObserveDuration()

	query := `UPDATE products SET name=$1, description=$2, price=$3, stock=$4, category=$5, updated_at=NOW()
		WHERE id=$6 RETURNING updated_at`
	return r.db.QueryRowContext(ctx, query, p.Name, p.Description, p.Price, p.Stock, p.Category, p.ID).
		Scan(&p.UpdatedAt)
}

func (r *ProductRepository) Delete(ctx context.Context, id int64) error {
	timer := prometheus.NewTimer(dbQueryDuration.WithLabelValues("delete"))
	defer timer.ObserveDuration()

	_, err := r.db.ExecContext(ctx, "DELETE FROM products WHERE id=$1", id)
	return err
}

func (r *ProductRepository) RefreshStockGauge(ctx context.Context) {
	var count int
	r.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM products WHERE stock > 0").Scan(&count)
	productsInStock.Set(float64(count))
}

// ─── Handlers ─────────────────────────────────────────────────────────────────

type Handler struct {
	repo *ProductRepository
}

func metricsMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()
		d := time.Since(start).Seconds()
		status := strconv.Itoa(c.Writer.Status())
		requestsTotal.WithLabelValues(c.Request.Method, c.FullPath(), status).Inc()
		requestDuration.WithLabelValues(c.Request.Method, c.FullPath()).Observe(d)
	}
}

func (h *Handler) CreateProduct(c *gin.Context) {
	var p Product
	if err := c.ShouldBindJSON(&p); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := h.repo.Create(c.Request.Context(), &p); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create product"})
		return
	}
	go h.repo.RefreshStockGauge(context.Background())
	c.JSON(http.StatusCreated, p)
}

func (h *Handler) GetProduct(c *gin.Context) {
	id, _ := strconv.ParseInt(c.Param("id"), 10, 64)
	p, err := h.repo.GetByID(c.Request.Context(), id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "database error"})
		return
	}
	if p == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "product not found"})
		return
	}
	c.JSON(http.StatusOK, p)
}

func (h *Handler) ListProducts(c *gin.Context) {
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))
	offset, _ := strconv.Atoi(c.DefaultQuery("offset", "0"))
	if limit > 100 {
		limit = 100
	}
	products, total, err := h.repo.List(c.Request.Context(), limit, offset)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list products"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"data":   products,
		"total":  total,
		"limit":  limit,
		"offset": offset,
	})
}

func (h *Handler) UpdateProduct(c *gin.Context) {
	id, _ := strconv.ParseInt(c.Param("id"), 10, 64)
	var p Product
	if err := c.ShouldBindJSON(&p); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	p.ID = id
	if err := h.repo.Update(c.Request.Context(), &p); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update product"})
		return
	}
	go h.repo.RefreshStockGauge(context.Background())
	c.JSON(http.StatusOK, p)
}

func (h *Handler) DeleteProduct(c *gin.Context) {
	id, _ := strconv.ParseInt(c.Param("id"), 10, 64)
	if err := h.repo.Delete(c.Request.Context(), id); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to delete product"})
		return
	}
	c.Status(http.StatusNoContent)
}

// ─── DB Migration ─────────────────────────────────────────────────────────────

func runMigrations(db *sql.DB) error {
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS products (
			id          BIGSERIAL PRIMARY KEY,
			name        VARCHAR(255) NOT NULL,
			description TEXT,
			price       NUMERIC(12,2) NOT NULL CHECK (price > 0),
			stock       INTEGER NOT NULL DEFAULT 0 CHECK (stock >= 0),
			category    VARCHAR(100),
			created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
		);
		CREATE INDEX IF NOT EXISTS idx_products_category ON products(category);
		CREATE INDEX IF NOT EXISTS idx_products_created_at ON products(created_at DESC);
	`)
	return err
}

// ─── Main ─────────────────────────────────────────────────────────────────────

func main() {
	dsn := fmt.Sprintf("host=%s port=%s user=%s password=%s dbname=%s sslmode=disable",
		getEnv("DB_HOST", "localhost"),
		getEnv("DB_PORT", "5432"),
		getEnv("DB_USER", "sre_app"),
		getEnv("DB_PASSWORD", "password"),
		getEnv("DB_NAME", "ecommerce"),
	)

	db, err := sql.Open("postgres", dsn)
	if err != nil {
		log.Fatalf("db open: %v", err)
	}
	defer db.Close()

	db.SetMaxOpenConns(25)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(5 * time.Minute)

	// Wait for DB to be ready
	for i := 0; i < 10; i++ {
		if err := db.Ping(); err == nil {
			break
		}
		log.Printf("Waiting for database... (%d/10)", i+1)
		time.Sleep(3 * time.Second)
	}

	if err := runMigrations(db); err != nil {
		log.Fatalf("migrations: %v", err)
	}

	repo := NewProductRepository(db)
	h := &Handler{repo: repo}

	// Refresh gauge on startup
	go repo.RefreshStockGauge(context.Background())

	// Periodically refresh stock gauge
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		for range ticker.C {
			repo.RefreshStockGauge(context.Background())
		}
	}()

	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Recovery())
	r.Use(metricsMiddleware())

	r.GET("/health", func(c *gin.Context) {
		if err := db.PingContext(c.Request.Context()); err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"status": "unhealthy", "error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "ok", "service": "product-service"})
	})
	r.GET("/metrics", gin.WrapH(promhttp.Handler()))

	v1 := r.Group("/api/v1/products")
	{
		v1.POST("", h.CreateProduct)
		v1.GET("", h.ListProducts)
		v1.GET("/:id", h.GetProduct)
		v1.PUT("/:id", h.UpdateProduct)
		v1.DELETE("/:id", h.DeleteProduct)
	}

	port := getEnv("PORT", "8081")
	srv := &http.Server{
		Addr:         ":" + port,
		Handler:      r,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
	}

	go func() {
		log.Printf("Product Service listening on :%s", port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("listen: %v", err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	srv.Shutdown(ctx)
	log.Println("Product Service exited")
}

func getEnv(key, defaultVal string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultVal
}
