"""
SRE Capstone Load Testing Suite
================================
Uses Locust to simulate realistic e-commerce traffic with:
  - Browse products (GET /api/v1/products)
  - View single product
  - Create orders
  - Check order status

Usage:
  # Install: pip install locust
  # Run interactive UI:
  locust -f locustfile.py --host=http://localhost:8080
  
  # Run headless (CI mode):
  locust -f locustfile.py --host=http://localhost:8080 \
    --users=100 --spawn-rate=10 --run-time=5m --headless \
    --html=load-test-report.html --csv=load-test-results
"""

import random
import json
from locust import HttpUser, TaskSet, task, between, events
from locust.contrib.fasthttp import FastHttpUser


# ─── Sample Data ──────────────────────────────────────────────────────────────

SAMPLE_PRODUCTS = [
    {"name": "Laptop Pro 15", "description": "High-performance laptop", "price": 1299.99, "stock": 50, "category": "electronics"},
    {"name": "Wireless Headphones", "description": "Noise-cancelling headphones", "price": 299.99, "stock": 100, "category": "electronics"},
    {"name": "Running Shoes", "description": "Lightweight running shoes", "price": 89.99, "stock": 200, "category": "footwear"},
    {"name": "Coffee Maker", "description": "Automatic coffee machine", "price": 149.99, "stock": 75, "category": "appliances"},
    {"name": "Desk Chair", "description": "Ergonomic office chair", "price": 449.99, "stock": 30, "category": "furniture"},
]

PRODUCT_IDS = list(range(1, 50))  # Assume seeded products


# ─── Task Sets ────────────────────────────────────────────────────────────────

class BrowseCatalog(TaskSet):
    """Read-heavy workload simulating product catalog browsing."""

    @task(5)
    def list_products(self):
        offset = random.randint(0, 5) * 10
        with self.client.get(
            f"/api/v1/products?limit=20&offset={offset}",
            name="/api/v1/products [list]",
            catch_response=True
        ) as resp:
            if resp.status_code == 200:
                resp.success()
            else:
                resp.failure(f"Unexpected status: {resp.status_code}")

    @task(3)
    def view_product(self):
        product_id = random.choice(PRODUCT_IDS)
        with self.client.get(
            f"/api/v1/products/{product_id}",
            name="/api/v1/products/{id} [view]",
            catch_response=True
        ) as resp:
            if resp.status_code in (200, 404):
                resp.success()
            else:
                resp.failure(f"Unexpected status: {resp.status_code}")

    @task(1)
    def check_health(self):
        self.client.get("/health", name="/health")


class PlaceOrders(TaskSet):
    """Write-heavy workload simulating order creation."""

    product_ids_created = []

    def on_start(self):
        """Seed a product on start if needed."""
        product = random.choice(SAMPLE_PRODUCTS)
        resp = self.client.post(
            "/api/v1/products",
            json=product,
            name="/api/v1/products [seed]",
        )
        if resp.status_code == 201:
            data = resp.json()
            self.product_ids_created.append(data.get("id", 1))

    @task(3)
    def create_order(self):
        product_id = random.choice(self.product_ids_created or PRODUCT_IDS)
        order = {
            "customer_id": random.randint(1, 1000),
            "items": [
                {
                    "product_id": product_id,
                    "quantity": random.randint(1, 3),
                    "price": round(random.uniform(10, 500), 2)
                }
            ]
        }
        with self.client.post(
            "/api/v1/orders",
            json=order,
            name="/api/v1/orders [create]",
            catch_response=True
        ) as resp:
            if resp.status_code == 201:
                resp.success()
            elif resp.status_code == 400:
                resp.success()  # Expected validation errors
            else:
                resp.failure(f"Order creation failed: {resp.status_code} {resp.text[:200]}")

    @task(1)
    def get_order(self):
        order_id = random.randint(1, 100)
        with self.client.get(
            f"/api/v1/orders/{order_id}",
            name="/api/v1/orders/{id} [get]",
            catch_response=True
        ) as resp:
            if resp.status_code in (200, 404):
                resp.success()
            else:
                resp.failure(f"Unexpected: {resp.status_code}")


# ─── User Classes ─────────────────────────────────────────────────────────────

class BrowserUser(HttpUser):
    """Simulates a customer browsing the catalog — read-heavy."""
    tasks = [BrowseCatalog]
    wait_time = between(1, 3)
    weight = 7  # 70% of users


class ShopperUser(HttpUser):
    """Simulates a customer placing orders — write-heavy."""
    tasks = [PlaceOrders]
    wait_time = between(2, 5)
    weight = 3  # 30% of users


# ─── Custom Metrics Reporting ─────────────────────────────────────────────────

@events.request.add_listener
def on_request(request_type, name, response_time, response_length, exception, **kwargs):
    """Log slow requests for analysis."""
    if response_time > 1000:  # > 1 second
        print(f"[SLOW] {request_type} {name}: {response_time:.0f}ms")


@events.test_start.add_listener
def on_test_start(environment, **kwargs):
    print("=" * 60)
    print("SRE Capstone Load Test Starting")
    print(f"Target: {environment.host}")
    print("=" * 60)


@events.test_stop.add_listener
def on_test_stop(environment, **kwargs):
    stats = environment.stats
    print("\n" + "=" * 60)
    print("Load Test Summary")
    print("=" * 60)
    for name, stat in stats.entries.items():
        print(f"{name[0]} {name[1]}")
        print(f"  Requests: {stat.num_requests}")
        print(f"  Failures: {stat.num_failures} ({stat.fail_ratio*100:.1f}%)")
        print(f"  Avg (ms): {stat.avg_response_time:.1f}")
        print(f"  p95 (ms): {stat.get_response_time_percentile(0.95):.1f}")
        print(f"  p99 (ms): {stat.get_response_time_percentile(0.99):.1f}")
