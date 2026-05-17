variable "project_id" {
  description = "GCP Project ID"
  type        = string
}

variable "project_name" {
  description = "Short name used as prefix for all resources"
  type        = string
  default     = "sre-capstone"
}

variable "region" {
  description = "GCP region"
  type        = string
  default     = "us-central1"
}

variable "zones" {
  description = "GCP zones within the region"
  type        = list(string)
  default     = ["us-central1-a", "us-central1-b", "us-central1-c"]
}

variable "subnet_cidr" {
  description = "Primary subnet CIDR"
  type        = string
  default     = "10.0.0.0/24"
}

variable "pods_cidr" {
  description = "Secondary CIDR for GKE Pods"
  type        = string
  default     = "10.1.0.0/16"
}

variable "services_cidr" {
  description = "Secondary CIDR for GKE Services"
  type        = string
  default     = "10.2.0.0/16"
}

variable "min_nodes" {
  description = "Minimum nodes per zone in the app node pool"
  type        = number
  default     = 1
}

variable "max_nodes" {
  description = "Maximum nodes per zone in the app node pool"
  type        = number
  default     = 5
}

variable "db_user" {
  description = "PostgreSQL application username"
  type        = string
  default     = "sre_app"
  sensitive   = true
}

variable "db_password" {
  description = "PostgreSQL application password"
  type        = string
  sensitive   = true
}
