// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

// Package rbac owns provider-neutral RBAC lifecycle reconciliation.
package rbac

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/gitstore-dev/gitstore/controller-manager/internal/graphqlclient"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/listwatch"
	namespacecontroller "github.com/gitstore-dev/gitstore/controller-manager/internal/namespace"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/status"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/types"
)

const LifecycleFinalizer = "rbac.authorization.gitstore.dev/lifecycle"

var Kinds = []string{"Role", "ClusterRole", "RoleBinding", "ClusterRoleBinding"}

type Metadata struct {
	UID               string            `json:"uid"`
	Name              string            `json:"name"`
	Namespace         string            `json:"namespace"`
	ResourceVersion   string            `json:"resourceVersion"`
	Generation        int64             `json:"generation"`
	Revision          string            `json:"revision"`
	Finalizers        []string          `json:"finalizers"`
	DeletionTimestamp *time.Time        `json:"deletionTimestamp"`
	Annotations       map[string]string `json:"annotations"`
}

type RoleRef struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
}

type Subject struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
}

type Spec struct {
	RoleRef  *RoleRef  `json:"roleRef"`
	Subjects []Subject `json:"subjects"`
}

type ResolvedRole struct {
	RoleUID  string `json:"roleUID"`
	RoleKind string `json:"roleKind"`
	RoleName string `json:"roleName"`
}

type ResourceStatus struct {
	ObservedGeneration int64               `json:"observedGeneration"`
	Conditions         []*status.Condition `json:"conditions"`
	Resolved           *ResolvedRole       `json:"resolved"`
}

type Resource struct {
	ID       string         `json:"id"`
	Kind     string         `json:"kind"`
	Metadata Metadata       `json:"metadata"`
	Spec     Spec           `json:"spec"`
	Status   ResourceStatus `json:"status"`
}

func (r *Resource) UnmarshalJSON(data []byte) error {
	type resource Resource
	var decoded resource
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	// Keep checkpoint identities in manifest form, including older checkpoints.
	kinds := map[string]string{
		"ROLE": "Role", "CLUSTER_ROLE": "ClusterRole",
		"USER": "User", "GROUP": "Group", "SERVICE_ACCOUNT": "ServiceAccount",
	}
	if ref := decoded.Spec.RoleRef; ref != nil {
		if kind, ok := kinds[ref.Kind]; ok {
			ref.Kind = kind
		}
	}
	for i := range decoded.Spec.Subjects {
		if kind, ok := kinds[decoded.Spec.Subjects[i].Kind]; ok {
			decoded.Spec.Subjects[i].Kind = kind
		}
	}
	if resolved := decoded.Status.Resolved; resolved != nil {
		if kind, ok := kinds[resolved.RoleKind]; ok {
			resolved.RoleKind = kind
		}
	}
	*r = Resource(decoded)
	return nil
}

func (r Resource) Key() types.WorkItemKey {
	return types.WorkItemKey{Kind: r.Kind, Namespace: r.Metadata.Namespace, Name: r.Metadata.Name}
}

type StatusUpdate struct {
	Resolved   *ResolvedRole
	Conditions []*status.Condition
	Finalizers []string
}

type GraphQLClient struct{ client *graphqlclient.Client }

type GraphQLListWatcher struct {
	client     *GraphQLClient
	kind       string
	namespaces listwatch.PagedListWatcher[namespacecontroller.Namespace]
}

func NewGraphQLListWatcher(client *GraphQLClient, kind string, namespaces listwatch.PagedListWatcher[namespacecontroller.Namespace]) *GraphQLListWatcher {
	return &GraphQLListWatcher{client: client, kind: kind, namespaces: namespaces}
}

func (l *GraphQLListWatcher) List(ctx context.Context) (listwatch.ListResponse[Resource], error) {
	var result listwatch.ListResponse[Resource]
	cursor, err := l.ListPages(ctx, func(page []Resource) error {
		result.Items = append(result.Items, page...)
		return nil
	})
	if err != nil {
		return listwatch.ListResponse[Resource]{}, err
	}
	result.ResourceVersion = cursor
	return result, nil
}

func (l *GraphQLListWatcher) ListPages(ctx context.Context, visit func([]Resource) error) (string, error) {
	if visit == nil {
		return "", errors.New("rbac: missing page visitor")
	}
	watch, err := l.Watch(ctx, "__resource_watch_bootstrap__")
	if err != nil {
		return "", err
	}
	var cursor string
	select {
	case event, open := <-watch.Events():
		if !open {
			return "", errors.Join(errors.New("rbac: missing pre-list bookmark"), watch.Err())
		}
		watch.Stop()
		if event.Type != listwatch.Bookmark || event.ResourceVersion == "" {
			return "", errors.New("rbac: missing pre-list bookmark")
		}
		cursor = event.ResourceVersion
	case <-ctx.Done():
		watch.Stop()
		return "", ctx.Err()
	}
	if !namespaced(l.kind) {
		return cursor, l.client.ListPages(ctx, l.kind, "", visit)
	}
	if l.namespaces == nil {
		return "", errors.New("rbac: missing namespace page source")
	}
	_, err = l.namespaces.ListPages(ctx, func(page []namespacecontroller.Namespace) error {
		for _, namespace := range page {
			if namespace.DeletionTimestamp != nil || slices.Contains(namespace.Finalizers, namespacecontroller.ForegroundDeletionFinalizer) {
				continue
			}
			if err := l.client.ListPages(ctx, l.kind, namespace.Name, visit); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return cursor, nil
}

func (l *GraphQLListWatcher) Watch(ctx context.Context, cursor string) (listwatch.Watcher[Resource], error) {
	if !slices.Contains(Kinds, l.kind) {
		return nil, errors.New("rbac: unsupported watch kind")
	}
	subscription, err := l.client.client.Subscribe(ctx, watchSubscription, map[string]any{"kind": l.kind, "resourceVersion": cursor})
	if err != nil {
		return nil, watchError(err)
	}
	return newResourceWatcher(ctx, l.kind, subscription), nil
}

type resourceWatcher struct {
	ctx          context.Context
	cancel       context.CancelFunc
	subscription graphqlclient.Subscription
	once         sync.Once
	events       chan listwatch.WatchEvent[Resource]
	err          error
}

func newResourceWatcher(ctx context.Context, kind string, subscription graphqlclient.Subscription) *resourceWatcher {
	ctx, cancel := context.WithCancel(ctx)
	watcher := &resourceWatcher{ctx: ctx, cancel: cancel, subscription: subscription,
		events: make(chan listwatch.WatchEvent[Resource], 16)}
	go watcher.run(kind)
	return watcher
}

func (w *resourceWatcher) Events() <-chan listwatch.WatchEvent[Resource] { return w.events }
func (w *resourceWatcher) Err() error                                    { return w.err }
func (w *resourceWatcher) Stop() {
	w.once.Do(func() {
		w.cancel()
		w.subscription.Stop()
	})
}

func watchError(err error) error {
	var structured *graphqlclient.Error
	if errors.As(err, &structured) && structured.Extensions["code"] == "WATCH_EXPIRED" {
		return fmt.Errorf("%w: %w", listwatch.ErrWatchExpired, err)
	}
	return err
}

func (w *resourceWatcher) run(kind string) {
	defer close(w.events)
	defer w.Stop()
	for {
		select {
		case <-w.ctx.Done():
			return
		case data, open := <-w.subscription.Next():
			if !open {
				w.err = watchError(w.subscription.Err())
				return
			}
			var envelope struct {
				WatchResources struct {
					Type, Kind, Namespace, Name, ResourceVersion string
					Object                                       *Resource
				}
			}
			if err := json.Unmarshal(data, &envelope); err != nil {
				w.err = err
				return
			}
			event := envelope.WatchResources
			if event.Kind != kind || event.ResourceVersion == "" {
				w.err = errors.New("rbac: watch crossed its kind or omitted its cursor")
				return
			}
			if event.Type != "BOOKMARK" && namespaced(kind) != (event.Namespace != "") {
				w.err = errors.New("rbac: watch has an invalid resource scope")
				return
			}
			update := listwatch.WatchEvent[Resource]{ResourceVersion: event.ResourceVersion,
				Object: Resource{Kind: kind, Metadata: Metadata{Name: event.Name, Namespace: event.Namespace}}}
			switch event.Type {
			case "BOOKMARK":
				update.Type = listwatch.Bookmark
				if event.Object != nil {
					w.err = errors.New("rbac: bookmark contained a resource")
					return
				}
			case "DELETED":
				update.Type = listwatch.Deleted
				if event.Object != nil || event.Name == "" {
					w.err = errors.New("rbac: invalid deletion event")
					return
				}
			case "ADDED", "MODIFIED":
				if event.Object == nil || event.Object.Kind != kind || event.Object.Metadata.Name != event.Name ||
					event.Object.Metadata.Namespace != event.Namespace || event.Object.Metadata.UID == "" || event.Object.Metadata.ResourceVersion == "" {
					w.err = errors.New("rbac: watch resource identity is inconsistent")
					return
				}
				update.Object = *event.Object
				update.Type = listwatch.Added
				if event.Type == "MODIFIED" {
					update.Type = listwatch.Modified
				}
			default:
				w.err = errors.New("rbac: unsupported watch event")
				return
			}
			select {
			case w.events <- update:
			case <-w.ctx.Done():
				return
			}
		}
	}
}

func NewGraphQLClient(client *graphqlclient.Client) *GraphQLClient {
	return &GraphQLClient{client: client}
}

const metadataFields = `id kind metadata {
  uid name namespace resourceVersion generation revision annotations finalizers deletionTimestamp
}`

func resourceFields(kind string) string {
	fields := metadataFields + ` status { observedGeneration conditions {
  type status observedGeneration lastTransitionTime reason message
}`
	if strings.HasSuffix(kind, "Binding") {
		fields += ` resolved { roleUID roleKind roleName }`
	}
	fields += ` }`
	if strings.HasSuffix(kind, "Binding") {
		fields += ` spec { roleRef { kind name } subjects { kind name } }`
	}
	return fields
}

func namespaced(kind string) bool { return kind == "Role" || kind == "RoleBinding" }

func fieldName(kind string) string { return strings.ToLower(kind[:1]) + kind[1:] }

func getQuery(kind string) string {
	return fmt.Sprintf(`query($by: %sBy!) { result: %s(by: $by) { %s } }`, kind, fieldName(kind), resourceFields(kind))
}

func listQuery(kind string) string {
	declaration, arguments := "", ""
	if namespaced(kind) {
		declaration, arguments = "$namespace: String!, ", "namespace: $namespace, "
	}
	return fmt.Sprintf(`query(%s$after: String) {
  result: %ss(%sfirst: 100, after: $after) {
    edges { node { %s } } pageInfo { hasNextPage endCursor }
  }
}`, declaration, fieldName(kind), arguments, resourceFields(kind))
}

func completionMutation(kind string) string {
	return fmt.Sprintf(`mutation($input: Complete%sDeletionInput!) {
  result: complete%sDeletion(input: $input) { id }
}`, kind, kind)
}

const statusMutation = `mutation($input: UpdateResourceStatusInput!) {
  updateResourceStatus(input: $input) { object }
}`

const watchSubscription = `subscription($kind: String!, $resourceVersion: String) {
  watchResources(kind: $kind, resourceVersion: $resourceVersion) {
    type kind namespace name resourceVersion object
  }
}`

const ownerBindingMutation = `mutation($input: CreateRoleBindingInput!) {
  result: createRoleBinding(input: $input) { roleBinding {
` + metadataFields + `
    spec { roleRef { kind name } subjects { kind name } }
    status { observedGeneration conditions { type status observedGeneration lastTransitionTime reason message } resolved { roleUID roleKind roleName } }
  } }
}`

func clientError(err error) error {
	var structured *graphqlclient.Error
	if errors.As(err, &structured) && graphqlclient.IsConflictCode(structured.Extensions["code"]) {
		return fmt.Errorf("%w: %w", types.ErrConflict, err)
	}
	return err
}

func isNotFound(err error) bool {
	var structured *graphqlclient.Error
	return errors.As(err, &structured) && structured.Extensions["code"] == "NOT_FOUND"
}

func (c *GraphQLClient) Get(ctx context.Context, key types.WorkItemKey) (Resource, bool, error) {
	if !slices.Contains(Kinds, key.Kind) || namespaced(key.Kind) != (key.Namespace != "") || key.Name == "" {
		return Resource{}, false, errors.New("rbac: invalid scoped key")
	}
	by := map[string]any{"name": key.Name}
	if namespaced(key.Kind) {
		by = map[string]any{"namespacePath": map[string]any{"namespace": key.Namespace, "name": key.Name}}
	}
	var response struct{ Result *Resource }
	err := c.client.Query(ctx, getQuery(key.Kind), map[string]any{"by": by}, &response)
	if isNotFound(err) {
		return Resource{}, false, nil
	}
	if err != nil {
		return Resource{}, false, clientError(err)
	}
	if response.Result == nil {
		return Resource{}, false, nil
	}
	if response.Result.Key() != key || response.Result.ID == "" || response.Result.Metadata.UID == "" || response.Result.Metadata.ResourceVersion == "" {
		return Resource{}, false, errors.New("rbac: resource response has inconsistent identity")
	}
	return *response.Result, true, nil
}

func (c *GraphQLClient) ListPages(ctx context.Context, kind, namespace string, visit func([]Resource) error) error {
	if !slices.Contains(Kinds, kind) || namespaced(kind) != (namespace != "") || visit == nil {
		return errors.New("rbac: invalid list scope or visitor")
	}
	var after *string
	for {
		var response struct {
			Result struct {
				Edges    []struct{ Node Resource }
				PageInfo struct {
					HasNextPage bool
					EndCursor   *string
				}
			}
		}
		vars := map[string]any{"after": after}
		if namespaced(kind) {
			vars["namespace"] = namespace
		}
		if err := c.client.Query(ctx, listQuery(kind), vars, &response); err != nil {
			return clientError(err)
		}
		if len(response.Result.Edges) > 100 {
			return errors.New("rbac: oversized resource page")
		}
		items := make([]Resource, 0, len(response.Result.Edges))
		for _, edge := range response.Result.Edges {
			if edge.Node.Kind != kind || edge.Node.Metadata.Namespace != namespace || edge.Node.ID == "" ||
				edge.Node.Metadata.UID == "" || edge.Node.Metadata.Name == "" || edge.Node.Metadata.ResourceVersion == "" {
				return errors.New("rbac: resource page crossed its declared scope")
			}
			items = append(items, edge.Node)
		}
		if err := visit(items); err != nil {
			return err
		}
		if !response.Result.PageInfo.HasNextPage {
			return nil
		}
		next := response.Result.PageInfo.EndCursor
		if next == nil || *next == "" || after != nil && *next == *after {
			return errors.New("rbac: list cursor did not advance")
		}
		after = next
	}
}

func (c *GraphQLClient) UpdateStatus(ctx context.Context, resource Resource, update StatusUpdate) error {
	if !slices.Contains(Kinds, resource.Kind) || resource.Metadata.ResourceVersion == "" {
		return errors.New("rbac: missing status identity")
	}
	input := map[string]any{"kind": resource.Kind, "namespace": resource.Metadata.Namespace, "name": resource.Metadata.Name,
		"resourceVersion": resource.Metadata.ResourceVersion, "observedGeneration": resource.Metadata.Generation}
	if strings.HasSuffix(resource.Kind, "Binding") && resource.Metadata.DeletionTimestamp == nil &&
		resource.Status.Resolved == nil && update.Resolved != nil {
		input["resolved"] = update.Resolved
	}
	if update.Conditions != nil {
		for _, condition := range update.Conditions {
			if condition == nil {
				return errors.New("rbac: nil condition")
			}
		}
		input["conditions"] = status.ToConditionInputs(update.Conditions)
	}
	if update.Finalizers != nil {
		input["finalizers"] = update.Finalizers
	}
	var response any
	return clientError(c.client.Mutate(ctx, statusMutation, map[string]any{"input": input}, &response))
}

func (c *GraphQLClient) CompleteDeletion(ctx context.Context, resource Resource) error {
	if !slices.Contains(Kinds, resource.Kind) || resource.ID == "" || resource.Metadata.ResourceVersion == "" {
		return errors.New("rbac: missing completion identity")
	}
	input := map[string]any{"id": resource.ID, "name": resource.Metadata.Name, "resourceVersion": resource.Metadata.ResourceVersion}
	if namespaced(resource.Kind) {
		input["namespace"] = resource.Metadata.Namespace
	}
	var response struct{ Result struct{ ID string } }
	err := c.client.Mutate(ctx, completionMutation(resource.Kind), map[string]any{"input": input}, &response)
	if isNotFound(err) {
		return nil
	}
	if err != nil {
		return clientError(err)
	}
	if response.Result.ID != resource.ID {
		return errors.New("rbac: completion did not confirm the requested identity")
	}
	return nil
}
