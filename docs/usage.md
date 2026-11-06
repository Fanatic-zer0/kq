# Usage

## Global syntax

```sh
kq <resource-or-kind> [-n namespace] [name]
```

- No name: list view in the current kubeconfig namespace (or `default` if none is set); `-A` lists namespaced resources across namespaces.
- Name provided: detail view showing the object plus everything it references or owns.

Built-in resource names and aliases are available directly. A discovered custom Kind can also be used as a top-level command (`kq Widget [name]`). If a Kind is ambiguous, use `kq resource <plural.group> [name]` to select the API resource explicitly.

## Commands

| Command | Aliases |
|---|---|
| `pod` | `po` |
| `deployment` | `deploy` |
| `replicaset` | `rs` |
| `statefulset` | `sts` |
| `daemonset` | `ds` |
| `job` | |
| `cronjob` | |
| `service` | `svc` |
| `ingress` | `ing` |
| `pvc` | |
| `pv` | |
| `configmap` | `cm` |
| `secret` | |
| `serviceaccount` | `sa` |
| `node` | |
| `namespace` | |
| `crd` | |
| `event` | `events`, `ev` |
| `resource <plural.group|Kind> [name]` | |

`node`, `namespace`, `crd`, and `pv` are cluster-scoped; `-n`/`--namespace` is rejected for them.

`kq crd` lists the installed API definitions with group, kind, scope, versions, establishment status, and age. `kq crd <name>` shows the same catalog details plus CRD conditions. To list or inspect custom-resource instances, use `kq resource <plural.group> [name]`, or use the CRD Kind when it resolves uniquely, for example `kq resource widgets.example.io sample -n default` or `kq resource Widget`. If a Kind is registered by multiple API resources, specify `plural.group`. CRD queries do not automatically list instances.

Custom-resource detail output includes metadata plus bounded top-level `config`, `spec`, and `status` fields rendered as nested YAML. Sensitive-looking fields are redacted, nesting and value lengths are capped, and output is limited to 100 fields. JSON/YAML detail output includes these sanitized fields in the resource projection.

PV and PVC list views include a `USED BY` column. PVC usage is matched by namespace and claim name; PV usage is resolved through its bound PVC. Detail views show the bound PV/PVC and consuming Pods. These relationships come from Pod volume specs, so `USED BY` means a Pod declares the claim; it does not prove that the Pod successfully mounted the volume. PV usage needs permission to list Pods across namespaces. If access is denied, the list displays `unavailable` and table output includes a warning.

## Flags

- `--context` — Kubernetes context.
- `--kubeconfig` — kubeconfig file path.
- `-n, --namespace` — namespace for a namespaced resource.
- `-A, --all-namespaces` — list across all namespaces (list view only; rejected for detail queries and cluster-scoped resources).
- `--since` — Event lookback window; default `30m`.
- `--max-events` — maximum Events collected for a detail query; default `200`.
- `--events` — opt in to Events and resolver warning output for detail views; off by default.
- `--no-events` — disable Events and warnings, overriding `--events`.
- `--depth` — association traversal depth for detail views; default `5`.
- `-o, --output` — `table` (default), `wide` (Pod list only; adds NODE/POD IP/QOS/CONFIG/SECRET/STORAGE/SA/EVENTS columns), `json`, or `yaml`. JSON/YAML detail output contains the relationship graph; custom-resource fields are included in sanitized nested form.
- `--tree` — render a detail view as an ownership/association tree instead of the curated text sections.
- `--show-secret-keys` — include Secret key names (never values). Required for `kq secret <name>` detail queries.
- `--rbac` — include RoleBinding/ClusterRoleBinding traversal in a Pod detail query. ServiceAccount detail queries always include RBAC. This is off by default for Pods to avoid a cluster-wide ClusterRoleBinding scan on every Pod query.
- `--exclude` — exclude a related resource kind from detail-view traversal; repeatable.
- `--timeout` — overall query timeout; default `30s`.
- `--qps` — client-side API request rate limit; default `20` requests/second.
- `--burst` — client-side API request burst; default `40`. Explicitly lower values are honored.
- `--as`, `--as-group` — Kubernetes impersonation.
- `--color` — `auto` (default; colored unless the `NO_COLOR` environment variable is set), `always` (force color, overriding `NO_COLOR`), or `never`. JSON/YAML output is never colorized.

## Examples

```sh
kq pod -n payments                            # list view
kq pod -n payments -o wide                    # list view with association counts
kq pod payments-api-66f7c -n payments         # detail view
kq pod payments-api-66f7c -n payments --tree  # ownership/association tree
kq pod payments-api-66f7c -n payments -o json # machine-readable detail
kq deployment payments-api -n payments
kq statefulset payments-db -n payments
kq job nightly-migration -n payments
kq cronjob nightly-migration -n payments
kq service payments-api -n payments
kq pvc payments-data -n payments
kq configmap payments-config -n payments
kq secret payments-secret -n payments --show-secret-keys
kq serviceaccount payments-api-sa -n payments
kq node worker-3
kq crd                                      # list installed custom APIs
kq crd widgets.example.io                   # inspect one custom API definition
kq resource widgets.example.io              # list custom resources by plural.group
kq resource Widget                          # list a uniquely discovered Kind
kq Widget sample -n default                 # custom Kind as a top-level command
kq event -n payments
kq po -A                                      # alias + cluster-wide list
kq resource widgets.example.io sample -n default
kq pv pv-data                                # PV and its bound PVC/using Pods
kq pvc data -n payments                      # PVC and its bound PV/using Pods
```

Job detail includes active/succeeded/failed counts, completions, parallelism, backoff limit, start/completion times, template container images and commands/args, owned Pods, and their Events. Sensitive-looking argument values such as password/token/secret flags are redacted.

CronJob detail includes its schedule, suspend state, concurrency policy, last scheduled time, calculated next run and time remaining, template containers, owned Jobs/Pods, and their Events. Next-run time uses the CronJob timezone when set and is omitted while suspended or when the schedule is invalid.
