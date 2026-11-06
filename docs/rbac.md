# Read-only RBAC

The base role below is a starting point for the built-in resolvers. Reduce resources and API groups to match the commands and relationships users need. It contains no `watch` or write verbs.

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: kq-read-only
rules:
  - apiGroups: [""]
    resources: ["pods", "services", "endpoints", "nodes", "namespaces", "persistentvolumes", "persistentvolumeclaims", "configmaps", "serviceaccounts", "events"]
    verbs: ["get", "list"]
  - apiGroups: ["apps"]
    resources: ["deployments", "replicasets", "statefulsets", "daemonsets"]
    verbs: ["get", "list"]
  - apiGroups: ["batch"]
    resources: ["jobs", "cronjobs"]
    verbs: ["get", "list"]
  - apiGroups: ["networking.k8s.io"]
    resources: ["ingresses", "networkpolicies"]
    verbs: ["get", "list"]
  - apiGroups: ["discovery.k8s.io"]
    resources: ["endpointslices"]
    verbs: ["get", "list"]
  - apiGroups: ["policy"]
    resources: ["poddisruptionbudgets"]
    verbs: ["get", "list"]
  - apiGroups: ["autoscaling"]
    resources: ["horizontalpodautoscalers"]
    verbs: ["get", "list"]
  - apiGroups: ["storage.k8s.io"]
    resources: ["storageclasses"]
    verbs: ["get", "list"]
  - apiGroups: ["rbac.authorization.k8s.io"]
    resources: ["rolebindings", "clusterrolebindings", "roles", "clusterroles"]
    verbs: ["get", "list"]
  - apiGroups: ["apiextensions.k8s.io"]
    resources: ["customresourcedefinitions"]
    verbs: ["get", "list"]
  - nonResourceURLs: ["/api", "/api/*", "/apis", "/apis/*"]
    verbs: ["get"]
```

Bind this ClusterRole to a dedicated ServiceAccount or user using a `ClusterRoleBinding`. To query custom resources, add `get` and `list` rules for the specific custom-resource API groups and plural resource names; avoid wildcard permissions where possible.

PVC/PV `USED BY` results require `list` access to Pods. A namespaced PVC query/list needs Pod access in that namespace; `pvc -A` and PV queries/lists inspect Pods across namespaces. Without access, the resource still loads, but consumer lookup is unavailable: list tables show `unavailable` and a warning, while detail resolver warnings are displayed with `--events`. Do not treat an empty detail consumer list as definitive when Pod-list access is restricted. The ClusterRole above grants cluster-wide Pod list access; a namespace-scoped Role can be used for PVC-only workflows.

## Optional Secret key-name access

Secret reads are disabled unless `--show-secret-keys` is supplied. For a namespaced application, grant `get` only on specifically approved Secret names:

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: kq-secret-key-names
  namespace: payments
rules:
  - apiGroups: [""]
    resources: ["secrets"]
    resourceNames: ["payments-config"]
    verbs: ["get"]
```

Reading a Secret returns its data to the client even though `kq` discards values immediately and emits only key names. Secret access is sensitive and should be granted only when key-name inspection is necessary. Image-pull Secrets are never fetched.
