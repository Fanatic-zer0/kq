# Architecture

`kq` splits into two request paths depending on whether a name is given:

```text
Cobra built-in command (0 args: list, 1 arg: detail)
    -> kubeconfig and timeout
    -> read-only dynamic/discovery client (internal/k8s)
    -> list engine (internal/view)         OR   query engine + graph builder (internal/query)
    -> per-kind column renderer                 per-kind curated/tree renderer
    -> table/wide/JSON/YAML                     text/tree/JSON/YAML
```

Unknown top-level commands are resolved as custom resource Kinds through API discovery. The explicit `resource <plural.group|Kind> [name]` command is available when a Kind is ambiguous.

The Kubernetes client exposes only `Get`, `List`, and contextual API discovery. An HTTP transport rejects non-GET requests. Objects are cached per command by GVR/namespace/name and UID, excluding Secrets.

## Typed details (Resource View Registry)

`internal/query.project()` converts built-in objects into a `model.ResourceNode`: a stable `ResourceRef` plus typed `Details`. Built-in details use Kubernetes API types and curated allowlists; Secret values are never captured, and Secret key names are populated only with `--show-secret-keys`. Custom resources retain sanitized labels/annotations and bounded `config`, `spec`, and `status` trees. Sensitive-looking fields are redacted, nesting and values are capped, and the flattened display index is limited to 100 fields. `internal/view` renders dedicated views for workloads, Services, Ingresses, CRDs, PVs/PVCs, and other commonly used built-ins; remaining kinds use generic metadata and relationship sections.

## List view

`internal/view.ListPods` and `ListGeneric` list a resource kind (optionally across all namespaces with `-A`) and render kind-specific columns. Pod list owner resolution (`Deployment/x`, `CronJob/y`) uses at most two bulk `List` calls (ReplicaSets, Jobs) regardless of Pod count — no per-Pod Gets. `-o wide` is Pod-specific and adds ConfigMap/Secret/PVC reference counts derived from each Pod's spec plus a namespace-scoped Warning-Event count. PV/PVC lists make one Pod list to populate `USED BY`; PV listings inspect Pods across namespaces and associate them through each PV's claim reference. This is a declared volume relationship, not a mount-health check.

## Detail view (graph model)

For a named target, the query engine fetches the object, traverses owner references by UID both upward and downward, and runs per-kind resolvers (ConfigMap/Secret/PVC/PV/ServiceAccount/RBAC/Service/Ingress/NetworkPolicy/PDB/HPA/Node) bounded by `--depth`. Pod queries create Pod→PVC→PV edges. Direct PVC queries reverse-list namespace Pods and match `spec.volumes[].persistentVolumeClaim.claimName`; direct PV queries fetch the bound PVC and find its consuming Pods. Results form a `model.Graph` of typed nodes and relationship edges; `internal/view` renders the graph. Optional lookup failures become structured warnings rather than aborting the whole query; warnings are shown with `--events` for detail queries.

## Custom-resource enrichment

Only a named custom target with positive traversal depth is enriched, before concurrent traversal starts. Its requested GVR identifies the exact CRD (`plural.group`), fetched through the existing GET cache. The matching version's schema is summarized with bounded traversal. Explicit API-version/kind/name references are deduplicated and capped at eight, with at most one additional discovery call and bounded parallel GETs. Referenced objects are graph leaves rather than recursively expanded custom references. Built-in detail paths and list commands do not perform these enrichment reads. Failures are preserved as visible warnings for custom targets, independently of Event collection.

## Events

Detail Events are opt-in with `--events`. They are listed with an involved-object UID field selector first; if that selector is rejected, lookup falls back to kind/name/namespace. Results are filtered by `--since`, deduplicated by involved-object identity/reason/message, sorted newest-first, and capped by `--max-events`. `kq event` is a separate Event list command.

## Performance

A detail query makes many small, independent Kubernetes reads (owner chain, ConfigMaps, Secrets, PVCs, ServiceAccount/RBAC, Services, policies, HPAs). These controls keep the work bounded and avoid unnecessary payloads:

- **One shared client-side token bucket** governs the dynamic, discovery, and metadata clients. QPS/Burst default to 20/40 only when unset and can be overridden with `--qps`/`--burst`; explicit lower limits are preserved. A client-wide semaphore separately caps in-flight requests at 8.
- **Built-in owner references resolve from a static GVK map**, so common Pod/Deployment/Job queries skip discovery. Unknown/custom owner GVKs trigger discovery lazily; full group-version discovery is parallelized and cached on disk for 10 minutes for generic resource queries.
- **BFS and per-Pod associations use fixed worker pools** bounded by `traversalConcurrency`; they do not create one goroutine per graph node/task. Identical concurrent namespace Lists are coalesced and successful results are memoized per command.
- **Pod list owner lookup uses PartialObjectMetadata** for ReplicaSets and Jobs, since only names, UIDs, and owner references are needed; the two metadata Lists run in parallel and avoid downloading full controller specs/status.
- **Generic custom-resource lookups use API discovery**, with a 10-minute disk cache; unqualified Kinds that map to multiple APIs return an explicit ambiguity error and list the plural.group choices.

`internal/graph.Builder` and the query engine's list cache are mutex-protected to support this; `go test -race` covers the concurrent paths.
