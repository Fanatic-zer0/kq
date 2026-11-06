# kq

`kq` is a query-only personal kubectl for people who want answers without a chain of `kubectl` commands. Run `kq <kind>` to list resources or `kq <kind> <name>` to see a resource and its relationships in one view: owners, Pods using a PVC, Services, Ingresses, configuration references, RBAC, and more. Custom resources can be selected by Kind or `plural.group`. Events and warnings are available with `--events`; Secret values are never shown. `kq` helps you find and explore resources—it does not perform health checks or remediation.


<img width="1222" height="203" alt="image" src="https://github.com/user-attachments/assets/8ef67eeb-6640-485f-86ee-8a7c57e949d4" />

<img width="1222" height="502" alt="image" src="https://github.com/user-attachments/assets/42c8e64c-3a27-4327-8d4e-d6b0bb6c265a" />


## Build

Requires Go 1.22 or newer.

```sh
go build -o kq ./cmd/kq
```

## Quick start

```sh
kq pod -n payments                          # list view: Pods + owner + key associations
kq pod payments-api-66f7c -n payments        # detail view: one Pod + everything it's connected to
```

Omitting the name shows a list view; providing a name shows a detail view with full associations.

## Command examples

```sh
kq pod -n payments -o wide
kq deployment payments-api -n payments
kq service payments-api -n payments
kq pvc payments-data -n payments
kq configmap payments-config -n payments
kq secret payments-secret -n payments --show-secret-keys
kq serviceaccount payments-api-sa -n payments
kq node worker-3
kq pod payments-api-66f7c -n payments --tree
kq pod payments-api-66f7c -n payments -o json
kq resource widgets.example.io sample -n default
kq po -A                                     # alias + all-namespaces list
```

## Supported resources

`pod` (`po`), `deployment` (`deploy`), `replicaset` (`rs`), `statefulset` (`sts`), `daemonset` (`ds`), `job`, `cronjob`, `service` (`svc`), `ingress` (`ing`), `pvc`, `pv`, `configmap` (`cm`), `secret`, `serviceaccount` (`sa`), `node`, `namespace`, `crd`, `event`, and `resource <plural.group|Kind> [name]` for any other built-in or custom resource discovered through the Kubernetes API. A uniquely discovered custom Kind can also be run directly, for example `kq Widget [name]`.

## Flags

`--context`, `--kubeconfig`, `-n/--namespace`, `-A/--all-namespaces` (list view only), `--since`, `--events` (opt in to Events and warnings), `--no-events`, `--rbac` (include RBAC expansion in Pod detail), `--depth` (detail view traversal depth), `-o/--output table|wide|json|yaml`, `--tree` (detail view only), `--show-secret-keys`, `--exclude`, `--timeout`, `--qps` (default `20`), `--burst` (default `40`), `--as`, `--as-group`, `--color auto|always|never` (colored by default; set `NO_COLOR` or `--color=never` to disable).

See [usage](docs/usage.md), [architecture](docs/architecture.md), and [read-only RBAC](docs/rbac.md) for full details.

## Read-only behavior

Kubernetes access is limited to GET and LIST requests, enforced by the HTTP transport itself. Raw Secret data is never added to output or cached. Secret references do not trigger Secret reads unless `--show-secret-keys` is enabled; a direct `kq secret <name>` detail query requires that flag because Kubernetes has no metadata-only GET for Secrets.

## Development

```sh
make build   # build ./kq
make test    # unit tests
make vet     # go vet
make fmt     # gofmt
make plugin  # build ./kubectl-kq for use as `kubectl kq ...`
```

## Roadmap

- Search resources across namespaces by kind, name, and labels, then open a result with its relationships in one step.
- Schema-aware CRD and custom-resource summaries, with readable field names and types instead of generic field output.
- Reusable user-defined views that collect selected resource kinds and relationships into one command.
- Optional live refresh for resource lists and detail views, while keeping Kubernetes access read-only.
- RBAC-aware effective-permissions summaries for ServiceAccounts; current output shows Role and ClusterRole rules without resolving the final permissions.
- Krew and Homebrew distribution. Manifests are included in [.krew.yaml](.krew.yaml) and [Formula/kq.rb](Formula/kq.rb), but are not yet published.

