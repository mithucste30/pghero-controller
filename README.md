# PgHero Controller

A Kubernetes controller for managing PgHero database connections using Custom Resource Definitions (CRDs).

## Overview

The PgHero Controller allows you to declaratively manage database connections for [PgHero](https://github.com/ankane/pghero) using Kubernetes resources. It watches for `Database` custom resources and automatically creates and manages ConfigMaps containing the database configurations that can be mounted into PgHero deployments.

## Features

- 🎯 Declarative database connection management
- 🔐 Support for referencing database URLs from Kubernetes Secrets
- 📊 Built-in Prometheus metrics support
- 🔄 Automatic ConfigMap synchronization
- 🏷️ Resource ownership and cleanup with finalizers
- 🎛️ Comprehensive Helm chart with best practices
- 🔒 Security-first design with non-root containers and read-only filesystems

## Installation

### Using Helm

```bash
# Add the Helm repository (once published)
helm repo add pghero-controller https://mithucste30.github.io/pghero-controller

# Install the controller
helm install pghero-controller pghero-controller/pghero-controller \
  --namespace pghero-system \
  --create-namespace
```

### From Source

```bash
# Clone the repository
git clone https://github.com/mithucste30/pghero-controller.git
cd pghero-controller

# Install CRDs
kubectl apply -f config/crd/

# Install using Helm
helm install pghero-controller ./helm/pghero-controller \
  --namespace pghero-system \
  --create-namespace
```

## Usage

### Creating a Database Resource

#### With Direct URL

```yaml
apiVersion: pghero.mithucste30.io/v1alpha1
kind: Database
metadata:
  name: production-db
  namespace: default
spec:
  name: production
  url: postgres://user:password@postgres.example.com:5432/mydb
  databaseType: postgresql
  enabled: true
```

#### With Secret Reference

First, create a secret with your database URL:

```bash
kubectl create secret generic postgres-credentials \
  --from-literal=database-url='postgres://user:password@postgres.example.com:5432/mydb'
```

Then reference it in your Database resource:

```yaml
apiVersion: pghero.mithucste30.io/v1alpha1
kind: Database
metadata:
  name: production-db
  namespace: default
spec:
  name: production
  databaseType: postgresql
  enabled: true
  urlFromSecret:
    name: postgres-credentials
    key: database-url
```

### Checking Database Status

```bash
# List all databases
kubectl get databases

# Get detailed information
kubectl describe database production-db

# Check the generated ConfigMap
kubectl get configmap pghero-database-production-db -o yaml
```

## Helm Chart Configuration

The Helm chart supports extensive configuration options. Here are some key values:

```yaml
# values.yaml

# Controller configuration
controller:
  replicas: 1
  leaderElection:
    enabled: true

# Resource limits
resources:
  limits:
    cpu: 500m
    memory: 256Mi
  requests:
    cpu: 100m
    memory: 128Mi

# Metrics and monitoring
metrics:
  enabled: true
  serviceMonitor:
    enabled: true
    interval: 30s
    labels:
      release: prometheus

# Network policy
networkPolicy:
  enabled: true

# Pod disruption budget
podDisruptionBudget:
  enabled: true
  minAvailable: 1
```

For a complete list of configuration options, see [values.yaml](helm/pghero-controller/values.yaml).

## Architecture

The controller watches for `Database` custom resources and:

1. Retrieves the database URL (either directly or from a Secret)
2. Creates/updates a ConfigMap with the database configuration
3. Updates the Database resource status with the current state
4. Handles cleanup when Database resources are deleted

### CRD Specification

```yaml
spec:
  name: string                 # Friendly name for the database
  url: string                  # Direct database URL (optional if urlFromSecret is set)
  urlFromSecret:              # Reference to secret containing URL (optional)
    name: string               # Secret name
    key: string                # Secret key
    namespace: string          # Secret namespace (optional, defaults to resource namespace)
  databaseType: string         # postgresql or mysql (default: postgresql)
  enabled: boolean             # Enable/disable this database connection (default: true)
```

## Development

### Prerequisites

- Go 1.21+
- kubectl
- Access to a Kubernetes cluster (for testing)
- Docker (for building images)

### Building

```bash
# Build the controller binary
go build -o bin/manager cmd/controller/main.go

# Build Docker image
docker build -t pghero-controller:latest .

# Run tests
go test ./...
```

### Local Development

```bash
# Install CRDs
kubectl apply -f config/crd/

# Run the controller locally
go run cmd/controller/main.go
```

## Monitoring

The controller exposes Prometheus metrics on port 8080 by default. When using the Prometheus Operator, enable the ServiceMonitor:

```yaml
metrics:
  enabled: true
  serviceMonitor:
    enabled: true
    labels:
      release: prometheus  # Match your Prometheus Operator's label selector
```

## Security

The controller follows security best practices:

- Runs as non-root user (UID 65532)
- Read-only root filesystem
- Dropped all capabilities
- Seccomp profile enabled
- Supports Network Policies for network segmentation

## License

MIT License - see [LICENSE](LICENSE) file for details.

## Contributing

Contributions are welcome! Please feel free to submit a Pull Request.

## Credits

This controller is designed to work with [PgHero](https://github.com/ankane/pghero) by Andrew Kane.
