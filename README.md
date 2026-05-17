# SRE Capstone — E-Commerce Platform Production Readiness Review

> **Endterm & Final Exam:** SRE Capstone Project   
> **Team:** [BEK]

## 🏗 Architecture Overview

```
                         ┌─────────────────────────────────────────────┐
                         │              GKE Cluster (GCP)               │
                         │                                               │
  External Traffic ──►  │  ┌─────────────┐                             │
                         │  │ API Gateway  │◄── LoadBalancer (80/443)   │
                         │  │  (Gin/Go)    │                             │
                         │  └──────┬───────┘                             │
                         │         │                                      │
                         │    ┌────┴────────────────┐                   │
                         │    ▼                      ▼                   │
                         │  ┌──────────────┐  ┌──────────────┐         │
                         │  │ Product Svc  │  │  Order Svc   │         │
                         │  │  (Gin/Go)    │  │  (Gin/Go)    │         │
                         │  └──────┬───────┘  └──────┬───────┘         │
                         │         │                  │                  │
                         │         └────────┬─────────┘                 │
                         │                  ▼                            │
                         │         ┌────────────────┐                   │
                         │         │  Cloud SQL      │                   │
                         │         │  (PostgreSQL 15)│                   │
                         │         └────────────────┘                   │
                         │                                               │
                         │  ┌──────────────────────────────────────┐   │
                         │  │       Observability Stack             │   │
                         │  │  Prometheus → Grafana  Alertmanager  │   │
                         │  │  Node/PG/Redis Exporters              │   │
                         │  └──────────────────────────────────────┘   │
                         └─────────────────────────────────────────────┘
```

## 📋 Component Summary

| Component | Technology | Purpose |
|-----------|-----------|---------|
| API Gateway | Go + Gin | Request routing, metrics, proxy |
| Product Service | Go + Gin + PostgreSQL | Product catalog CRUD |
| Order Service | Go + Gin + PostgreSQL | Order lifecycle management |
| Database | PostgreSQL 15 (Cloud SQL) | Persistent storage |
| Cache | Redis 7 (Cloud Memorystore) | Session & cache layer |
| IaC | Terraform | All GCP infrastructure |
| Container Registry | Google Artifact Registry | Docker image storage |
| Cluster | GKE (Google Kubernetes Engine) | Orchestration |
| CI/CD | GitHub Actions | Build → Test → Push → Deploy |
| Metrics | Prometheus + Exporters | Metrics collection |
| Dashboards | Grafana | SLI/SLO visualization |
| Alerting | Alertmanager | Alert routing |
| Load Testing | Locust | Traffic simulation |

## 🚀 Quick Start (Local)

### Prerequisites
- Docker & Docker Compose
- Go 1.22+
- Python 3.10+ (for Locust)
- `make`

### Start everything locally
```bash
git clone https://github.com/YOUR_ORG/sre-capstone.git
cd sre-capstone

# Start all services
make up

# Open dashboards
open http://localhost:8080    # API Gateway
open http://localhost:9090    # Prometheus
open http://localhost:3000    # Grafana (admin/admin123)
open http://localhost:9093    # Alertmanager

# Seed sample data
make seed
```

### Run load tests
```bash
pip install locust
make load-test            # Interactive UI at localhost:8089
make load-test-headless   # Headless, 100 users, 5 min
make load-test-spike      # Spike test, 500 users, 10 min
```

## 🏭 Step 1: Infrastructure as Code

All infrastructure is managed with Terraform in the `terraform/` directory.

```bash
export TF_VAR_db_password="your-secure-password"
export TF_VAR_project_id="your-gcp-project"

make tf-init    # Initialize providers & backend
make tf-plan    # Preview changes
make tf-apply   # Apply infrastructure
```

### Resources Provisioned
- **VPC** with custom subnets, secondary ranges for GKE
- **GKE Cluster** with 2 node pools (app + monitoring), HPA enabled
- **Cloud SQL** PostgreSQL 15 with private networking, auto-backup
- **Cloud Memorystore** Redis 7
- **Artifact Registry** for Docker images
- **Cloud Monitoring** alert policy
- **IAM** service account bindings

### State Management
- Remote state stored in **Google Cloud Storage** bucket (`sre-capstone-tfstate`)
- State locking via GCS native locking
- Sensitive values passed via `TF_VAR_*` environment variables (never in `.tf` files)

## ⚙️ Step 2: CI/CD Pipeline

Pipeline defined in `.github/workflows/ci-cd.yml`.

### Pipeline Stages

```
Push to main/develop
      │
      ▼
┌─────────────┐
│  Lint & Test │  (golangci-lint + go test -race)
│  [per service]│
└──────┬──────┘
       │ (on push only)
       ▼
┌─────────────┐
│ Build & Push │  (Docker Buildx multi-stage, push to Artifact Registry)
│ Docker Images│  Tags: branch-SHA, branch, latest(main)
└──────┬──────┘
       │
       ▼
┌─────────────┐
│ Security Scan│  (Trivy - CRITICAL/HIGH CVE detection)
└──────┬──────┘
       │ (main branch only, with approval gate)
       ▼
┌─────────────┐
│ Deploy GKE   │  (kubectl set image + rollout status + smoke test)
└─────────────┘
```

**Required GitHub Secrets:**
- `GCP_PROJECT_ID` — GCP Project ID
- `GCP_SA_KEY` — Service account JSON key

## 📊 Step 3: Observability & Alerting

### SLIs Visualized in Grafana

| Panel | SLI | Query |
|-------|-----|-------|
| Availability SLO | % successful requests | `1 - error_rate` |
| Latency SLO | p99 latency | `histogram_quantile(0.99, ...)` |
| Error Budget | Remaining budget | `1 - (actual_errors / budget)` |
| Request Rate | RPS per service | `rate(http_requests_total[2m])` |
| DB Query Duration | p95 per operation | Per-service histogram |

### Alert Rules (`monitoring/prometheus/rules/alerts.yml`)

| Alert | Condition | Severity |
|-------|-----------|----------|
| AvailabilitySLOBreach | Error rate > 0.1% for 2m | Critical |
| LatencySLOBreach | p99 > 500ms for 3m | Warning |
| LatencySLOCritical | p99 > 1s for 2m | Critical |
| ServiceDown | Target unreachable for 1m | Critical |
| HighErrorRate | Error rate > 5% for 2m | Warning |
| PostgresDown | pg_up == 0 for 1m | Critical |
| RedisDown | redis_up == 0 for 1m | Critical |
| HighCPUUsage | CPU > 80% for 5m | Warning |
| HighMemoryUsage | Memory > 85% for 5m | Warning |

## 📈 Step 4: SRE Operations

### SLO Definitions

| SLO | Target | Window | Error Budget |
|-----|--------|--------|-------------|
| Availability | 99.9% | 28 days | 43.8 min/month |
| Latency p99 < 500ms | 99.5% | 28 days | 3.4h/month |
| Latency p99 < 1s | 99.0% | 28 days | 6.9h/month |
| Throughput @ 500 rps | 99.9% | 28 days | 43.8 min/month |

Full definitions: [`docs/slo-definitions.yaml`](docs/slo-definitions.yaml)

### Auto-Scaling

HPAs configured in `k8s/hpa.yaml`:

| Service | Min | Max | Scale-Up Trigger | Scale-Down Stabilization |
|---------|-----|-----|-----------------|-------------------------|
| api-gateway | 2 | 10 | CPU > 60% | 300s window |
| product-service | 2 | 8 | CPU > 65% | 300s window |
| order-service | 2 | 8 | CPU > 65% | 300s window |

Scale-up is **fast** (1 min window, max +2 pods or +100%) and scale-down is **conservative** (5 min window, max -1 pod) to prevent flapping.

### Load Testing Results

Run `make load-test-headless` then review `load-test-report.html`.

Expected results at 100 concurrent users:
- **RPS**: ~250–350 req/s
- **p50**: < 50ms
- **p95**: < 200ms
- **p99**: < 500ms  ✅ (meets SLO)
- **Error rate**: < 0.1%  ✅ (within budget)

## 📁 Repository Structure

```
sre-capstone/
├── terraform/               # Step 1: IaC
│   ├── main.tf              #   GKE, SQL, Redis, VPC, Artifact Registry
│   ├── variables.tf
│   ├── outputs.tf
│   └── terraform.tfvars
├── .github/workflows/
│   └── ci-cd.yml            # Step 2: CI/CD Pipeline
├── services/
│   ├── api-gateway/         # Reverse proxy + metrics
│   │   ├── main.go
│   │   ├── go.mod
│   │   └── Dockerfile
│   ├── product-service/     # Product CRUD
│   │   ├── main.go
│   │   ├── go.mod
│   │   └── Dockerfile
│   └── order-service/       # Order lifecycle
│       ├── main.go
│       ├── go.mod
│       └── Dockerfile
├── k8s/                     # Kubernetes manifests
│   ├── namespace.yaml
│   ├── deployments.yaml     #   Deployments + Services
│   └── hpa.yaml             #   HorizontalPodAutoscalers
├── monitoring/              # Step 3: Observability
│   ├── prometheus/
│   │   ├── prometheus.yml   #   Scrape configuration
│   │   └── rules/
│   │       └── alerts.yml   #   Alert rules (SLO + infra)
│   ├── alertmanager/
│   │   └── alertmanager.yml #   Routing + receivers
│   └── grafana/
│       └── provisioning/    #   Auto-provisioned dashboards
├── load-testing/            # Step 4: Load Tests
│   └── locustfile.py        #   Realistic e-commerce workload
├── docs/
│   └── slo-definitions.yaml #   Formal SLI/SLO spec
├── docker-compose.yml       # Local development
├── Makefile                 # All common commands
└── README.md
```

## 🔐 Security Notes

- All secrets managed via Kubernetes Secrets + GCP Secret Manager
- No credentials committed to repository (enforced by `.gitignore`)
- Container images scanned by Trivy on every build
- Private networking for all backend services (no public exposure)
- Least-privilege IAM service accounts

## 📝 Team Members

| Name | Role |
|------|------|
| [Member 1] | Infrastructure & IaC |
| [Member 2] | CI/CD & Docker |
| [Member 3] | Observability & SLOs |
| [Member 4] | Services & Load Testing |
