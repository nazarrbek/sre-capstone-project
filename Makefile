.PHONY: help up down build restart logs ps clean \
        tf-init tf-plan tf-apply tf-destroy \
        k8s-deploy k8s-delete k8s-status \
        load-test load-test-headless \
        fmt lint test

# Default target
help: ## Show this help message
	@echo "SRE Capstone — Available Commands"
	@echo "=================================="
	@awk 'BEGIN {FS = ":.*?## "} /^[a-zA-Z_-]+:.*?## / {printf "  \033[36m%-20s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)

# ─── Local Development (Docker Compose) ──────────────────────────────────────
up: ## Start all services locally (docker-compose)
	docker compose up -d --build
	@echo "✅ Services started:"
	@echo "  API Gateway:  http://localhost:8080"
	@echo "  Prometheus:   http://localhost:9090"
	@echo "  Grafana:      http://localhost:3000 (admin/admin123)"
	@echo "  Alertmanager: http://localhost:9093"

down: ## Stop all local services
	docker compose down

build: ## Build all Docker images
	docker compose build --no-cache

restart: ## Restart all services
	docker compose restart

logs: ## Follow logs for all services
	docker compose logs -f

logs-gw: ## Follow API Gateway logs only
	docker compose logs -f api-gateway

logs-products: ## Follow Product Service logs
	docker compose logs -f product-service

logs-orders: ## Follow Order Service logs
	docker compose logs -f order-service

ps: ## Show container status
	docker compose ps

clean: ## Remove containers, networks, volumes
	docker compose down -v --remove-orphans

# ─── Terraform ────────────────────────────────────────────────────────────────
tf-init: ## Initialize Terraform
	cd terraform && terraform init -upgrade

tf-plan: ## Preview Terraform changes
	cd terraform && terraform plan -out=tfplan

tf-apply: ## Apply Terraform changes
	cd terraform && terraform apply tfplan

tf-destroy: ## DANGER: Destroy all infrastructure
	cd terraform && terraform destroy -auto-approve

tf-fmt: ## Format Terraform files
	cd terraform && terraform fmt -recursive

tf-validate: ## Validate Terraform configuration
	cd terraform && terraform validate

# ─── Kubernetes ───────────────────────────────────────────────────────────────
k8s-deploy: ## Deploy all manifests to Kubernetes
	kubectl apply -f k8s/namespace.yaml
	kubectl apply -f k8s/deployments.yaml
	kubectl apply -f k8s/hpa.yaml
	@echo "✅ Kubernetes resources deployed"

k8s-delete: ## Delete all Kubernetes resources
	kubectl delete -f k8s/ --ignore-not-found

k8s-status: ## Show status of all deployments
	@echo "=== Deployments ==="
	kubectl get deployments -n ecommerce
	@echo ""
	@echo "=== Pods ==="
	kubectl get pods -n ecommerce
	@echo ""
	@echo "=== HPA ==="
	kubectl get hpa -n ecommerce
	@echo ""
	@echo "=== Services ==="
	kubectl get svc -n ecommerce

k8s-rollout-status: ## Check rollout status
	kubectl rollout status deployment/api-gateway -n ecommerce
	kubectl rollout status deployment/product-service -n ecommerce
	kubectl rollout status deployment/order-service -n ecommerce

# ─── Load Testing ─────────────────────────────────────────────────────────────
load-test: ## Run Locust load test UI (opens browser)
	@echo "Starting Locust UI at http://localhost:8089"
	locust -f load-testing/locustfile.py --host=http://localhost:8080

load-test-headless: ## Run headless load test (100 users, 5 minutes)
	locust -f load-testing/locustfile.py \
		--host=http://localhost:8080 \
		--users=100 \
		--spawn-rate=10 \
		--run-time=5m \
		--headless \
		--html=load-test-report.html \
		--csv=load-test-results
	@echo "✅ Report saved to load-test-report.html"

load-test-spike: ## Simulate traffic spike (500 users)
	locust -f load-testing/locustfile.py \
		--host=http://localhost:8080 \
		--users=500 \
		--spawn-rate=50 \
		--run-time=10m \
		--headless \
		--html=spike-test-report.html \
		--csv=spike-test-results

# ─── Go Development ───────────────────────────────────────────────────────────
fmt: ## Format all Go code
	gofmt -w services/api-gateway/
	gofmt -w services/product-service/
	gofmt -w services/order-service/

lint: ## Run golangci-lint
	cd services/api-gateway && golangci-lint run
	cd services/product-service && golangci-lint run
	cd services/order-service && golangci-lint run

test: ## Run all Go unit tests
	cd services/api-gateway && go test ./... -v
	cd services/product-service && go test ./... -v
	cd services/order-service && go test ./... -v

tidy: ## Tidy all go.mod files
	cd services/api-gateway && go mod tidy
	cd services/product-service && go mod tidy
	cd services/order-service && go mod tidy

# ─── Monitoring Helpers ───────────────────────────────────────────────────────
prometheus-reload: ## Hot-reload Prometheus config
	curl -X POST http://localhost:9090/-/reload

grafana-open: ## Open Grafana in browser
	open http://localhost:3000

alertmanager-open: ## Open Alertmanager in browser
	open http://localhost:9093

# ─── Seed Data ────────────────────────────────────────────────────────────────
seed: ## Seed sample products via API
	@echo "Seeding products..."
	@for i in 1 2 3 4 5; do \
		curl -s -X POST http://localhost:8080/api/v1/products \
			-H "Content-Type: application/json" \
			-d "{\"name\":\"Product $$i\",\"description\":\"Sample product $$i\",\"price\":$$(echo "$$i * 29.99" | bc),\"stock\":100,\"category\":\"test\"}" \
			| jq .id; \
	done
	@echo "✅ Products seeded"
