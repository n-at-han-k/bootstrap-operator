# bootstrap-operator

A Kubernetes operator for bootstrapping git repositories, container registries, and Nix-based image builds.

## Custom Resources

| CRD | Description |
|-----|-------------|
| **Repo** | Provisions a bare git repository served over HTTP (StatefulSet + Service) |
| **Registry** | Provisions a Docker Distribution container image registry (StatefulSet + Service) |
| **Image** | Builds a Nix flake output from a Repo and pushes the resulting OCI image to a Registry |

## Examples

All three CRDs require a Secret with `username` and `password` keys for HTTP basic auth. Create one first:

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: my-credentials
  namespace: default
type: Opaque
stringData:
  username: admin
  password: changeme
```

### Repo

Provisions a bare git repository served over HTTP.

```yaml
apiVersion: bootstrap.cia.net/v1alpha1
kind: Repo
metadata:
  name: my-repo
  namespace: default
spec:
  credentialsSecretRef:
    name: my-credentials
  storage:
    size: 5Gi
    storageClassName: standard
  image: alpine:3.21  # optional, this is the default
```

### Registry

Provisions a Docker Distribution container image registry.

```yaml
apiVersion: bootstrap.cia.net/v1alpha1
kind: Registry
metadata:
  name: my-registry
  namespace: default
spec:
  credentialsSecretRef:
    name: my-credentials
  storage:
    size: 10Gi
    storageClassName: standard
  image: registry:2  # optional, this is the default
```

### Image

Builds a Nix flake output from a Repo and pushes the OCI image to a Registry. The Repo and Registry must exist in the same namespace.

```yaml
apiVersion: bootstrap.cia.net/v1alpha1
kind: Image
metadata:
  name: my-image
  namespace: default
spec:
  repoRef:
    name: my-repo
  registryRef:
    name: my-registry
  flakeOutput: image
  destination: apps/my-app:v1
  branch: main  # optional, defaults to main
```

## Install

### From the Helm repo

```bash
helm repo add bootstrap-operator https://n-at-han-k.github.io/bootstrap-operator
helm repo update
helm install bootstrap-operator bootstrap-operator/bootstrap-operator \
  --namespace bootstrap-operator-system \
  --create-namespace
```

### From source

```bash
git clone https://github.com/n-at-han-k/bootstrap-operator.git
cd bootstrap-operator
helm install bootstrap-operator charts/bootstrap-operator \
  --namespace bootstrap-operator-system \
  --create-namespace
```

## Configuration

Override values with `--set` or a values file:

```bash
helm install bootstrap-operator bootstrap-operator/bootstrap-operator \
  --namespace bootstrap-operator-system \
  --create-namespace \
  -f my-values.yaml
```

### Values

| Key | Default | Description |
|-----|---------|-------------|
| `image.repository` | `ghcr.io/n-at-han-k/bootstrap-operator` | Operator container image |
| `image.tag` | `latest` | Image tag |
| `image.pullPolicy` | `IfNotPresent` | Image pull policy |
| `replicas` | `1` | Number of operator replicas |
| `resources.limits.cpu` | `500m` | CPU limit |
| `resources.limits.memory` | `128Mi` | Memory limit |
| `resources.requests.cpu` | `10m` | CPU request |
| `resources.requests.memory` | `64Mi` | Memory request |
| `leaderElect` | `true` | Enable leader election |
| `serviceAccount.create` | `true` | Create a service account |
| `serviceAccount.name` | `bootstrap-operator` | Service account name |
| `namespace` | `bootstrap-operator-system` | Namespace to deploy into |
| `crds.install` | `true` | Install CRDs with the chart |

## Upgrade

```bash
helm repo update
helm upgrade bootstrap-operator bootstrap-operator/bootstrap-operator \
  --namespace bootstrap-operator-system
```

## Uninstall

```bash
helm uninstall bootstrap-operator --namespace bootstrap-operator-system
```

Note: CRDs are not removed by `helm uninstall`. To remove them manually:

```bash
kubectl delete crd images.bootstrap.cia.net registries.bootstrap.cia.net repos.bootstrap.cia.net
```
