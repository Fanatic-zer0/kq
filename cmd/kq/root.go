package main

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Fanatic-zer0/kq/internal/k8s"
	"github.com/Fanatic-zer0/kq/internal/query"
	"github.com/Fanatic-zer0/kq/internal/view"
	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

type flags struct {
	contextName    string
	kubeconfig     string
	namespace      string
	allNamespaces  bool
	depth          int
	since          time.Duration
	events         bool
	noEvents       bool
	exclude        []string
	output         string
	tree           bool
	maxEvents      int
	timeout        time.Duration
	qps            float32
	burst          int
	showSecretKeys bool
	rbac           bool
	as             string
	asGroups       []string
	color          string
}

type resourceKind struct {
	use           string
	aliases       []string
	kind          string
	resource      schema.GroupVersionResource
	clusterScoped bool
}

func newRootCommand() *cobra.Command {
	options := &flags{}
	root := &cobra.Command{
		Use:           "kq",
		Short:         "Relationship-aware Kubernetes viewer: kubectl get, plus everything it references or owns",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.Args = cobra.MaximumNArgs(2)
	root.RunE = func(command *cobra.Command, args []string) error {
		if len(args) == 0 {
			return command.Help()
		}
		name := ""
		if len(args) == 2 {
			name = args[1]
		}
		return runCustomResource(command, options, args[0], name)
	}
	persistent := root.PersistentFlags()
	persistent.StringVar(&options.contextName, "context", "", "Kubernetes context")
	persistent.StringVar(&options.kubeconfig, "kubeconfig", "", "Kubeconfig path")
	persistent.StringVarP(&options.namespace, "namespace", "n", "", "Namespace for namespaced targets")
	persistent.BoolVarP(&options.allNamespaces, "all-namespaces", "A", false, "List across all namespaces (list view only)")
	persistent.IntVar(&options.depth, "depth", 5, "Association traversal depth (detail view only)")
	persistent.DurationVar(&options.since, "since", 30*time.Minute, "Event lookback window")
	persistent.BoolVar(&options.events, "events", false, "Include Events and resolver warnings in detail output")
	persistent.BoolVar(&options.noEvents, "no-events", false, "Disable Events and resolver warnings (overrides --events)")
	persistent.StringSliceVar(&options.exclude, "exclude", nil, "Resource kind to exclude from association resolution (repeatable)")
	persistent.StringVarP(&options.output, "output", "o", "table", "Output format: table, wide, json, or yaml")
	persistent.BoolVar(&options.tree, "tree", false, "Render a detail view as an ownership/association tree")
	persistent.IntVar(&options.maxEvents, "max-events", 200, "Maximum Events collected for a detail view")
	persistent.DurationVar(&options.timeout, "timeout", 30*time.Second, "Maximum query duration")
	persistent.Float32Var(&options.qps, "qps", 20, "Maximum Kubernetes API requests per second")
	persistent.IntVar(&options.burst, "burst", 40, "Maximum burst of Kubernetes API requests")
	persistent.BoolVar(&options.showSecretKeys, "show-secret-keys", false, "Show Secret key names, never values")
	persistent.BoolVar(&options.rbac, "rbac", false, "Resolve ServiceAccount RoleBindings/ClusterRoleBindings in a Pod detail view")
	persistent.StringVar(&options.as, "as", "", "Impersonate a Kubernetes user")
	persistent.StringSliceVar(&options.asGroups, "as-group", nil, "Impersonation group (repeatable)")
	persistent.StringVar(&options.color, "color", "auto", "Colorize output: auto (default, on unless NO_COLOR is set), always, or never")
	root.PersistentPreRun = func(command *cobra.Command, args []string) {
		view.SetColorMode(options.color, command.OutOrStdout())
	}

	kinds := []resourceKind{
		{use: "pod", aliases: []string{"po"}, kind: "Pod", resource: schema.GroupVersionResource{Version: "v1", Resource: "pods"}},
		{use: "deployment", aliases: []string{"deploy"}, kind: "Deployment", resource: schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}},
		{use: "replicaset", aliases: []string{"rs"}, kind: "ReplicaSet", resource: schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "replicasets"}},
		{use: "statefulset", aliases: []string{"sts"}, kind: "StatefulSet", resource: schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "statefulsets"}},
		{use: "daemonset", aliases: []string{"ds"}, kind: "DaemonSet", resource: schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "daemonsets"}},
		{use: "job", kind: "Job", resource: schema.GroupVersionResource{Group: "batch", Version: "v1", Resource: "jobs"}},
		{use: "cronjob", kind: "CronJob", resource: schema.GroupVersionResource{Group: "batch", Version: "v1", Resource: "cronjobs"}},
		{use: "service", aliases: []string{"svc"}, kind: "Service", resource: schema.GroupVersionResource{Version: "v1", Resource: "services"}},
		{use: "ingress", aliases: []string{"ing"}, kind: "Ingress", resource: schema.GroupVersionResource{Group: "networking.k8s.io", Version: "v1", Resource: "ingresses"}},
		{use: "pvc", kind: "PersistentVolumeClaim", resource: schema.GroupVersionResource{Version: "v1", Resource: "persistentvolumeclaims"}},
		{use: "pv", kind: "PersistentVolume", resource: schema.GroupVersionResource{Version: "v1", Resource: "persistentvolumes"}, clusterScoped: true},
		{use: "configmap", aliases: []string{"cm"}, kind: "ConfigMap", resource: schema.GroupVersionResource{Version: "v1", Resource: "configmaps"}},
		{use: "secret", kind: "Secret", resource: schema.GroupVersionResource{Version: "v1", Resource: "secrets"}},
		{use: "serviceaccount", aliases: []string{"sa"}, kind: "ServiceAccount", resource: schema.GroupVersionResource{Version: "v1", Resource: "serviceaccounts"}},
		{use: "node", kind: "Node", resource: schema.GroupVersionResource{Version: "v1", Resource: "nodes"}, clusterScoped: true},
		{use: "namespace", kind: "Namespace", resource: schema.GroupVersionResource{Version: "v1", Resource: "namespaces"}, clusterScoped: true},
		{use: "crd", kind: "CustomResourceDefinition", resource: schema.GroupVersionResource{Group: "apiextensions.k8s.io", Version: "v1", Resource: "customresourcedefinitions"}, clusterScoped: true},
	}
	for _, kind := range kinds {
		root.AddCommand(newResourceCommand(kind, options))
	}
	root.AddCommand(&cobra.Command{
		Use:     "event",
		Aliases: []string{"events", "ev"},
		Short:   "List Events in a namespace",
		Args:    cobra.NoArgs,
		RunE: func(command *cobra.Command, args []string) error {
			return runEventList(command, options)
		},
	})
	root.AddCommand(&cobra.Command{
		Use:   "resource <plural.group|Kind> [name]",
		Short: "List or query a built-in or custom resource by resource name or Kind",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(command *cobra.Command, args []string) error {
			name := ""
			if len(args) == 2 {
				name = args[1]
			}
			return runCustomResource(command, options, args[0], name)
		},
	})
	return root
}

func newResourceCommand(kind resourceKind, options *flags) *cobra.Command {
	return &cobra.Command{
		Use:     kind.use + " [name]",
		Aliases: kind.aliases,
		Short:   "List or query " + kind.kind + " resources and their associations",
		Args:    cobra.MaximumNArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			if len(args) == 0 {
				return runList(command, options, kind)
			}
			return runDetail(command, options, kind, args[0])
		},
	}
}

func resolveClient(command *cobra.Command, options *flags) (*k8s.Client, context.Context, context.CancelFunc, string, error) {
	if options.qps <= 0 || options.burst <= 0 {
		return nil, nil, nil, "", fmt.Errorf("--qps and --burst must be positive")
	}
	ctx, cancel := context.WithTimeout(command.Context(), options.timeout)
	config, configuredNamespace, err := loadConfig(options, options.timeout)
	if err != nil {
		cancel()
		return nil, nil, nil, "", err
	}
	client, err := k8s.New(config)
	if err != nil {
		cancel()
		return nil, nil, nil, "", err
	}
	return client, ctx, cancel, configuredNamespace, nil
}

func resolveNamespace(command *cobra.Command, options *flags, configuredNamespace string, clusterScoped bool) (string, error) {
	if clusterScoped {
		if command.Flags().Changed("namespace") {
			return "", fmt.Errorf("--namespace cannot be used with a cluster-scoped resource")
		}
		return "", nil
	}
	if options.namespace != "" {
		return options.namespace, nil
	}
	if configuredNamespace != "" {
		return configuredNamespace, nil
	}
	return "default", nil
}

func runList(command *cobra.Command, options *flags, kind resourceKind) error {
	if options.allNamespaces && kind.clusterScoped {
		return fmt.Errorf("--all-namespaces is not valid for cluster-scoped resource %s", kind.kind)
	}
	client, ctx, cancel, configuredNamespace, err := resolveClient(command, options)
	if err != nil {
		return err
	}
	defer cancel()
	namespace, err := resolveNamespace(command, options, configuredNamespace, kind.clusterScoped)
	if err != nil {
		return err
	}
	request := view.ListRequest{
		Resource: kind.resource, Kind: kind.kind, Namespace: namespace, AllNamespaces: options.allNamespaces,
		Wide: options.output == "wide", IncludeEvents: (options.events || options.output == "wide") && !options.noEvents, Since: options.since,
	}
	var result *view.ListResult
	if kind.kind == "Pod" {
		result, err = view.ListPods(ctx, client, request)
	} else {
		result, err = view.ListGeneric(ctx, client, request)
	}
	if err != nil {
		return err
	}
	return view.RenderList(command.OutOrStdout(), result, options.output, options.output == "wide")
}

func runDetail(command *cobra.Command, options *flags, kind resourceKind, name string) error {
	client, ctx, cancel, configuredNamespace, err := resolveClient(command, options)
	if err != nil {
		return err
	}
	defer cancel()
	namespace, err := resolveNamespace(command, options, configuredNamespace, kind.clusterScoped)
	if err != nil {
		return err
	}
	return executeDetail(command, options, client, ctx, kind.resource, kind.kind, namespace, name)
}

func executeDetail(command *cobra.Command, options *flags, client *k8s.Client, ctx context.Context, resource schema.GroupVersionResource, kind, namespace, name string) error {
	excluded := make(map[string]bool, len(options.exclude))
	for _, excludedKind := range options.exclude {
		excluded[strings.ToLower(excludedKind)] = true
		excluded[excludedKind] = true
	}
	request := query.Request{
		Resource: resource, Kind: kind, Namespace: namespace, Name: name, Depth: options.depth,
		Since: options.since, IncludeEvents: options.events && !options.noEvents, MaxEvents: options.maxEvents,
		ShowSecretKeys: options.showSecretKeys, IncludeRBAC: options.rbac, ExcludedKinds: excluded,
	}
	result, err := query.New(client).Run(ctx, request)
	if err != nil {
		return err
	}
	if !options.events || options.noEvents {
		result.Warnings = nil
	}
	format := options.output
	if options.tree {
		format = "tree"
	} else if format == "wide" {
		format = "table"
	}
	return view.RenderDetail(command.OutOrStdout(), result, format, view.DetailOptions{Since: options.since, ShowWarnings: options.events && !options.noEvents})
}

func runEventList(command *cobra.Command, options *flags) error {
	client, ctx, cancel, configuredNamespace, err := resolveClient(command, options)
	if err != nil {
		return err
	}
	defer cancel()
	namespace, err := resolveNamespace(command, options, configuredNamespace, false)
	if err != nil {
		return err
	}
	result, err := view.ListEvents(ctx, client, namespace, options.allNamespaces)
	if err != nil {
		return err
	}
	return view.RenderList(command.OutOrStdout(), result, options.output, false)
}

func runCustomResource(command *cobra.Command, options *flags, qualifiedName, name string) error {
	client, ctx, cancel, configuredNamespace, err := resolveClient(command, options)
	if err != nil {
		return err
	}
	defer cancel()
	resource, kind, clusterScoped, err := resolveResource(ctx, client, qualifiedName)
	if err != nil {
		return err
	}
	namespace, err := resolveNamespace(command, options, configuredNamespace, clusterScoped)
	if err != nil {
		return err
	}
	if name == "" {
		request := view.ListRequest{Resource: resource, Kind: kind, Namespace: namespace, AllNamespaces: options.allNamespaces}
		result, err := view.ListGeneric(ctx, client, request)
		if err != nil {
			return err
		}
		return view.RenderList(command.OutOrStdout(), result, options.output, options.output == "wide")
	}
	return executeDetail(command, options, client, ctx, resource, kind, namespace, name)
}

func loadConfig(options *flags, timeout time.Duration) (*rest.Config, string, error) {
	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	if options.kubeconfig != "" {
		loadingRules.ExplicitPath = options.kubeconfig
	}
	overrides := &clientcmd.ConfigOverrides{CurrentContext: options.contextName}
	if options.as != "" {
		overrides.AuthInfo.Impersonate = options.as
		overrides.AuthInfo.ImpersonateGroups = options.asGroups
	}
	clientConfig := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, overrides)
	config, err := clientConfig.ClientConfig()
	if err != nil {
		return nil, "", fmt.Errorf("load Kubernetes config: %w", err)
	}
	config.Timeout = timeout
	config.QPS = options.qps
	config.Burst = options.burst
	namespace, _, err := clientConfig.Namespace()
	if err != nil {
		return nil, "", fmt.Errorf("resolve default namespace: %w", err)
	}
	return config, namespace, nil
}

func resolveResource(ctx context.Context, client k8s.Reader, qualifiedName string) (schema.GroupVersionResource, string, bool, error) {
	lists, err := client.APIResources(ctx)
	if err != nil {
		return schema.GroupVersionResource{}, "", false, fmt.Errorf("discover API resources: %w", err)
	}
	return resolveDiscoveredResource(lists, qualifiedName)
}

func resolveDiscoveredResource(lists []*metav1.APIResourceList, qualifiedName string) (schema.GroupVersionResource, string, bool, error) {
	parts := strings.SplitN(qualifiedName, ".", 2)
	resourceName := parts[0]
	group := ""
	if len(parts) == 2 {
		group = parts[1]
	}

	kindMatches := make(map[string]schema.GroupVersionResource)
	kindNames := make(map[string]string)
	kindScopes := make(map[string]bool)
	for _, list := range lists {
		if list == nil {
			continue
		}
		gv, parseErr := schema.ParseGroupVersion(list.GroupVersion)
		if parseErr != nil {
			continue
		}
		for _, apiResource := range list.APIResources {
			if strings.Contains(apiResource.Name, "/") {
				continue
			}
			if apiResource.Name == resourceName && gv.Group == group {
				return gv.WithResource(apiResource.Name), apiResource.Kind, !apiResource.Namespaced, nil
			}
			if !strings.EqualFold(apiResource.Kind, resourceName) || (len(parts) == 2 && gv.Group != group) {
				continue
			}
			key := gv.Group + "/" + apiResource.Name
			if _, found := kindMatches[key]; found {
				continue
			}
			kindMatches[key] = gv.WithResource(apiResource.Name)
			kindNames[key] = apiResource.Kind
			kindScopes[key] = !apiResource.Namespaced
		}
	}
	if len(kindMatches) == 1 {
		for key, resource := range kindMatches {
			return resource, kindNames[key], kindScopes[key], nil
		}
	}
	if len(kindMatches) > 1 {
		candidates := make([]string, 0, len(kindMatches))
		for key := range kindMatches {
			parts := strings.SplitN(key, "/", 2)
			candidates = append(candidates, parts[1]+"."+parts[0])
		}
		sort.Strings(candidates)
		return schema.GroupVersionResource{}, "", false, fmt.Errorf("Kind %q is ambiguous; use one of: %s", qualifiedName, strings.Join(candidates, ", "))
	}
	return schema.GroupVersionResource{}, "", false, fmt.Errorf("resource %q was not found in API discovery", qualifiedName)
}
