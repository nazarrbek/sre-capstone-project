terraform {
  required_version = ">= 1.5.0"

  required_providers {
    google = {
      source  = "hashicorp/google"
      version = "~> 5.0"
    }
    kubernetes = {
      source  = "hashicorp/kubernetes"
      version = "~> 2.25"
    }
    helm = {
      source  = "hashicorp/helm"
      version = "~> 2.12"
    }
  }

  backend "gcs" {
    bucket = "sre-capstone-tfstate"
    prefix = "terraform/state"
  }
}

provider "google" {
  project = var.project_id
  region  = var.region
}

provider "kubernetes" {
  host                   = "https://${module.gke.endpoint}"
  token                  = data.google_client_config.default.access_token
  cluster_ca_certificate = base64decode(module.gke.ca_certificate)
}

provider "helm" {
  kubernetes {
    host                   = "https://${module.gke.endpoint}"
    token                  = data.google_client_config.default.access_token
    cluster_ca_certificate = base64decode(module.gke.ca_certificate)
  }
}

data "google_client_config" "default" {}

# ─── VPC Network ────────────────────────────────────────────────────────────────
resource "google_compute_network" "main" {
  name                    = "${var.project_name}-vpc"
  auto_create_subnetworks = false
}

resource "google_compute_subnetwork" "main" {
  name          = "${var.project_name}-subnet"
  ip_cidr_range = var.subnet_cidr
  region        = var.region
  network       = google_compute_network.main.id

  secondary_ip_range {
    range_name    = "pods"
    ip_cidr_range = var.pods_cidr
  }

  secondary_ip_range {
    range_name    = "services"
    ip_cidr_range = var.services_cidr
  }
}

# ─── GKE Cluster ────────────────────────────────────────────────────────────────
module "gke" {
  source  = "terraform-google-modules/kubernetes-engine/google"
  version = "~> 30.0"

  project_id              = var.project_id
  name                    = "${var.project_name}-cluster"
  region                  = var.region
  zones                   = var.zones
  network                 = google_compute_network.main.name
  subnetwork              = google_compute_subnetwork.main.name
  ip_range_pods           = "pods"
  ip_range_services       = "services"
  http_load_balancing     = true
  horizontal_pod_autoscaling = true
  network_policy          = true
  remove_default_node_pool = true
  initial_node_count      = 1

  node_pools = [
    {
      name               = "app-pool"
      machine_type       = "e2-medium"
      min_count          = var.min_nodes
      max_count          = var.max_nodes
      disk_size_gb       = 50
      disk_type          = "pd-standard"
      auto_upgrade       = true
      auto_repair        = true
      preemptible        = false
    },
    {
      name               = "monitoring-pool"
      machine_type       = "e2-medium"
      min_count          = 1
      max_count          = 2
      disk_size_gb       = 100
      disk_type          = "pd-standard"
      auto_upgrade       = true
      auto_repair        = true
      preemptible        = false
    }
  ]

  node_pools_labels = {
    app-pool = {
      role = "application"
    }
    monitoring-pool = {
      role = "monitoring"
    }
  }

  node_pools_taints = {
    monitoring-pool = [
      {
        key    = "dedicated"
        value  = "monitoring"
        effect = "NO_SCHEDULE"
      }
    ]
  }
}

# ─── Cloud SQL (PostgreSQL) ──────────────────────────────────────────────────────
resource "google_sql_database_instance" "main" {
  name             = "${var.project_name}-postgres"
  database_version = "POSTGRES_15"
  region           = var.region
  deletion_protection = false

  settings {
    tier              = "db-f1-micro"
    availability_type = "REGIONAL"
    disk_autoresize   = true
    disk_size         = 20
    disk_type         = "PD_SSD"

    backup_configuration {
      enabled    = true
      start_time = "02:00"
      backup_retention_settings {
        retained_backups = 7
      }
    }

    ip_configuration {
      ipv4_enabled    = false
      private_network = google_compute_network.main.id
    }

    database_flags {
      name  = "max_connections"
      value = "100"
    }
  }

  depends_on = [google_service_networking_connection.private_vpc_connection]
}

resource "google_sql_database" "ecommerce" {
  name     = "ecommerce"
  instance = google_sql_database_instance.main.name
}

resource "google_sql_user" "app" {
  name     = var.db_user
  instance = google_sql_database_instance.main.name
  password = var.db_password
}

# ─── Private Service Access ──────────────────────────────────────────────────────
resource "google_compute_global_address" "private_ip" {
  name          = "${var.project_name}-private-ip"
  purpose       = "VPC_PEERING"
  address_type  = "INTERNAL"
  prefix_length = 16
  network       = google_compute_network.main.id
}

resource "google_service_networking_connection" "private_vpc_connection" {
  network                 = google_compute_network.main.id
  service                 = "servicenetworking.googleapis.com"
  reserved_peering_ranges = [google_compute_global_address.private_ip.name]
}

# ─── Cloud Memorystore (Redis) ───────────────────────────────────────────────────
resource "google_redis_instance" "cache" {
  name           = "${var.project_name}-redis"
  tier           = "BASIC"
  memory_size_gb = 1
  region         = var.region

  authorized_network = google_compute_network.main.id
  connect_mode       = "PRIVATE_SERVICE_ACCESS"

  redis_version     = "REDIS_7_0"
  display_name      = "SRE Capstone Cache"

  depends_on = [google_service_networking_connection.private_vpc_connection]
}

# ─── Artifact Registry ───────────────────────────────────────────────────────────
resource "google_artifact_registry_repository" "main" {
  location      = var.region
  repository_id = "${var.project_name}-registry"
  format        = "DOCKER"
  description   = "Docker registry for SRE Capstone microservices"
}

# ─── IAM: GKE → Artifact Registry ───────────────────────────────────────────────
resource "google_artifact_registry_repository_iam_member" "gke_reader" {
  location   = google_artifact_registry_repository.main.location
  repository = google_artifact_registry_repository.main.name
  role       = "roles/artifactregistry.reader"
  member     = "serviceAccount:${module.gke.service_account}"
}

# ─── Cloud Monitoring Alert Policy ──────────────────────────────────────────────
resource "google_monitoring_alert_policy" "high_cpu" {
  display_name = "High CPU Utilization"
  combiner     = "OR"

  conditions {
    display_name = "CPU > 80%"
    condition_threshold {
      filter          = "resource.type = \"k8s_container\" AND metric.type = \"kubernetes.io/container/cpu/core_usage_time\""
      duration        = "60s"
      comparison      = "COMPARISON_GT"
      threshold_value = 0.8
      aggregations {
        alignment_period   = "60s"
        per_series_aligner = "ALIGN_RATE"
      }
    }
  }

  notification_channels = []
  alert_strategy {
    auto_close = "1800s"
  }
}
