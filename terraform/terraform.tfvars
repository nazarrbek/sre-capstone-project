project_id   = "YOUR_GCP_PROJECT_ID"
project_name = "sre-capstone"
region       = "us-central1"
zones        = ["us-central1-a", "us-central1-b", "us-central1-c"]

subnet_cidr   = "10.0.0.0/24"
pods_cidr     = "10.1.0.0/16"
services_cidr = "10.2.0.0/16"

min_nodes = 1
max_nodes = 5

db_user     = "sre_app"
# db_password is set via TF_VAR_db_password environment variable
