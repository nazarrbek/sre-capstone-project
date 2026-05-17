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
		Namespace: "order_service",
		Name:      "http_requests_total",
		Help:      "Total HTTP requests",
	}, []string{"method", "endpoint", "status"})

	requestDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: "order_service",
		Name:      "http_request_duration_seconds",
		Help:      "Request latency",
		Buckets:   prometheus.DefBuckets,
	}, []string{"method", "endpoint"})

	ordersCreatedTotal = promauto.NewCounter(prometheus.CounterOpts{
		Namespace: "order_service",
		Name:      "orders_created_total",
		Help:      "Total orders created",
	})

	ordersRevenue = promauto.NewCounter(prometheus.CounterOpts{
		Namespace: "order_service",
		Name:      "revenue_total",
		Help:      "Total revenue from completed orders",
	})

	pendingOrders = promauto.NewGauge(prometheus.GaugeOpts{
		Namespace: "order_service",
		Name:      "pending_orders",
		Help:      "Current number of pending orders",
	})
)

// ─── Domain ───────────────────────────────────────────────────────────────────

type OrderStatus string

const (
	StatusPending    OrderStatus = "pending"
	StatusProcessing OrderStatus = "processing"
	StatusShipped    OrderStatus = "shipped"
	StatusDelivered  OrderStatus = "delivered"
	StatusCancelled  OrderStatus = "cancelled"
)

type OrderItem struct {
	ID        int64   `json:"id"`
	OrderID   int64   `json:"order_id"`
	ProductID int64   `json:"product_id" binding:"required"`
	Quantity  int     `json:"quantity" binding:"required,min=1"`
	Price     float64 `json:"price"`
}

type Order struct {
	ID          int64       `json:"id"`
	CustomerID  int64       `json:"customer_id" binding:"required"`
	Status      OrderStatus `json:"status"`
	TotalAmount float64     `json:"total_amount"`
	Items       []OrderItem `json:"items" binding:"required,min=1"`
	CreatedAt   time.Time   `json:"created_at"`
	UpdatedAt   time.Time   `json:"updated_at"`
}

// ─── Repository ───────────────────────────────────────────────────────────────

type OrderRepository struct {
	db *sql.DB
}

func NewOrderRepository(db *sql.DB) *OrderRepository {
	return &OrderRepository{db: db}
}

func (r *OrderRepository) Create(ctx context.Context, o *Order) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	err = tx.QueryRowContext(ctx,
		`INSERT INTO orders (customer_id, status, total_amount, created_at, updated_at)
		VALUES ($1, $2, $3, NOW(), NOW()) RETURNING id, created_at, updated_at`,
		o.CustomerID, StatusPending, 0,
	).Scan(&o.ID, &o.CreatedAt, &o.UpdatedAt)
	if err != nil {
		return err
	}

	var total float64
	for i := range o.Items {
		item := &o.Items[i]
		item.OrderID = o.ID
		err = tx.QueryRowContext(ctx,
			`INSERT INTO order_items (order_id, product_id, quantity, price)
			VALUES ($1, $2, $3, $4) RETURNING id`,
			item.OrderID, item.ProductID, item.Quantity, item.Price,
		).Scan(&item.ID)
		if err != nil {
			return err
		}
		total += item.Price * float64(item.Quantity)
	}

	_, err = tx.ExecContext(ctx, "UPDATE orders SET total_amount=$1 WHERE id=$2", total, o.ID)
	if err != nil {
		return err
	}
	o.TotalAmount = total
	o.Status = StatusPending

	return tx.Commit()
}

func (r *OrderRepository) GetByID(ctx context.Context, id int64) (*Order, error) {
	o := &Order{}
	err := r.db.QueryRowContext(ctx,
		`SELECT id, customer_id, status, total_amount, created_at, updated_at FROM orders WHERE id=$1`, id,
	).Scan(&o.ID, &o.CustomerID, &o.Status, &o.TotalAmount, &o.CreatedAt, &o.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	rows, err := r.db.QueryContext(ctx,
		`SELECT id, order_id, product_id, quantity, price FROM order_items WHERE order_id=$1`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var item OrderItem
		rows.Scan(&item.ID, &item.OrderID, &item.ProductID, &item.Quantity, &item.Price)
		o.Items = append(o.Items, item)
	}
	return o, nil
}

func (r *OrderRepository) UpdateStatus(ctx context.Context, id int64, status OrderStatus) error {
	_, err := r.db.ExecContext(ctx,
		"UPDATE orders SET status=$1, updated_at=NOW() WHERE id=$2", status, id)
	return err
}

func (r *OrderRepository) ListByCustomer(ctx context.Context, customerID int64, limit, offset int) ([]*Order, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, customer_id, status, total_amount, created_at, updated_at
		FROM orders WHERE customer_id=$1 ORDER BY created_at DESC LIMIT $2 OFFSET $3`,
		customerID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var orders []*Order
	for rows.Next() {
		o := &Order{}
		rows.Scan(&o.ID, &o.CustomerID, &o.Status, &o.TotalAmount, &o.CreatedAt, &o.UpdatedAt)
		orders = append(orders, o)
	}
	return orders, nil
}

func (r *OrderRepository) RefreshPendingGauge(ctx context.Context) {
	var count int
	r.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM orders WHERE status='pending'").Scan(&count)
	pendingOrders.Set(float64(count))
}

// ─── Handlers ─────────────────────────────────────────────────────────────────

type Handler struct {
	repo *OrderRepository
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

func (h *Handler) CreateOrder(c *gin.Context) {
	var o Order
	if err := c.ShouldBindJSON(&o); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := h.repo.Create(c.Request.Context(), &o); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create order"})
		return
	}
	ordersCreatedTotal.Inc()
	ordersRevenue.Add(o.TotalAmount)
	go h.repo.RefreshPendingGauge(context.Background())
	c.JSON(http.StatusCreated, o)
}

func (h *Handler) GetOrder(c *gin.Context) {
	id, _ := strconv.ParseInt(c.Param("id"), 10, 64)
	o, err := h.repo.GetByID(c.Request.Context(), id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "database error"})
		return
	}
	if o == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "order not found"})
		return
	}
	c.JSON(http.StatusOK, o)
}

func (h *Handler) UpdateOrderStatus(c *gin.Context) {
	id, _ := strconv.ParseInt(c.Param("id"), 10, 64)
	var body struct {
		Status OrderStatus `json:"status" binding:"required"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := h.repo.UpdateStatus(c.Request.Context(), id, body.Status); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update order"})
		return
	}
	go h.repo.RefreshPendingGauge(context.Background())
	c.JSON(http.StatusOK, gin.H{"id": id, "status": body.Status})
}

func (h *Handler) ListCustomerOrders(c *gin.Context) {
	customerID, _ := strconv.ParseInt(c.Param("customer_id"), 10, 64)
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))
	offset, _ := strconv.Atoi(c.DefaultQuery("offset", "0"))
	orders, err := h.repo.ListByCustomer(c.Request.Context(), customerID, limit, offset)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list orders"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": orders, "limit": limit, "offset": offset})
}

// ─── Migration ────────────────────────────────────────────────────────────────

func runMigrations(db *sql.DB) error {
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS orders (
			id           BIGSERIAL PRIMARY KEY,
			customer_id  BIGINT NOT NULL,
			status       VARCHAR(20) NOT NULL DEFAULT 'pending',
			total_amount NUMERIC(12,2) NOT NULL DEFAULT 0,
			created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			updated_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
		);
		CREATE TABLE IF NOT EXISTS order_items (
			id          BIGSERIAL PRIMARY KEY,
			order_id    BIGINT NOT NULL REFERENCES orders(id) ON DELETE CASCADE,
			product_id  BIGINT NOT NULL,
			quantity    INTEGER NOT NULL CHECK (quantity > 0),
			price       NUMERIC(12,2) NOT NULL
		);
		CREATE INDEX IF NOT EXISTS idx_orders_customer ON orders(customer_id);
		CREATE INDEX IF NOT EXISTS idx_orders_status ON orders(status);
		CREATE INDEX IF NOT EXISTS idx_order_items_order ON order_items(order_id);
	`)
	return err
}

// ─── Main ─────────────────────────────────────────────────────────────────────

func getEnv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

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

	for i := 0; i < 10; i++ {
		if err := db.Ping(); err == nil {
			break
		}
		log.Printf("Waiting for DB... (%d/10)", i+1)
		time.Sleep(3 * time.Second)
	}

	if err := runMigrations(db); err != nil {
		log.Fatalf("migrations: %v", err)
	}

	repo := NewOrderRepository(db)
	go repo.RefreshPendingGauge(context.Background())
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		for range ticker.C {
			repo.RefreshPendingGauge(context.Background())
		}
	}()

	h := &Handler{repo: repo}

	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Recovery())
	r.Use(metricsMiddleware())

	r.GET("/health", func(c *gin.Context) {
		if err := db.PingContext(c.Request.Context()); err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"status": "unhealthy"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "ok", "service": "order-service"})
	})
	r.GET("/metrics", gin.WrapH(promhttp.Handler()))

	v1 := r.Group("/api/v1/orders")
	{
		v1.POST("", h.CreateOrder)
		v1.GET("/:id", h.GetOrder)
		v1.PATCH("/:id/status", h.UpdateOrderStatus)
		v1.GET("/customer/:customer_id", h.ListCustomerOrders)
	}

	port := getEnv("PORT", "8082")
	srv := &http.Server{Addr: ":" + port, Handler: r, ReadTimeout: 30 * time.Second, WriteTimeout: 30 * time.Second}
	go func() {
		log.Printf("Order Service on :%s", port)
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
}
