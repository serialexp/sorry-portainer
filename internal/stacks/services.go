package stacks

import (
	"context"
	"fmt"
	"sort"

	"github.com/serialexp/sorry-portainer/internal/podman"
	"github.com/serialexp/sorry-portainer/internal/protocol"
	"gopkg.in/yaml.v3"
)

// ContainerLister lists the compose containers on this agent.
type ContainerLister interface {
	ComposeContainers(ctx context.Context) (map[string][]podman.ProjectContainer, error)
}

// EnableRuntimeStatus makes List and Inspect report each stack's services with
// the state of their containers. Without it, services report state "unknown".
func (m *Manager) EnableRuntimeStatus(l ContainerLister) {
	m.runtime = l
}

// containersByProject asks Podman once for every compose container. known is
// false when runtime status is not enabled.
func (m *Manager) containersByProject(ctx context.Context) (all map[string][]podman.ProjectContainer, known bool, err error) {
	if m.runtime == nil {
		return nil, false, nil
	}
	all, err = m.runtime.ComposeContainers(ctx)
	if err != nil {
		return nil, true, fmt.Errorf("list containers: %w", err)
	}
	return all, true, nil
}

// declaredService is a service the Compose file declares, in file order.
type declaredService struct {
	name  string
	image string
}

// declaredServices reads the services of a Compose document. The document
// must parse; callers decide what a stack that does not parse reports.
func declaredServices(source []byte) ([]declaredService, error) {
	doc, err := parseCompose(source)
	if err != nil {
		return nil, err
	}
	services := mappingValue(doc.root, "services")
	if services == nil {
		return nil, nil
	}
	out := make([]declaredService, 0, len(services.Content)/2)
	for i := 0; i+1 < len(services.Content); i += 2 {
		service := services.Content[i+1]
		if service.Kind == yaml.AliasNode && service.Alias != nil {
			service = service.Alias
		}
		ds := declaredService{name: services.Content[i].Value}
		if image := mappingValue(service, "image"); image != nil && image.Kind == yaml.ScalarNode {
			ds.image = image.Value
		}
		out = append(out, ds)
	}
	return out, nil
}

// serviceStates lists each declared service with the state of its containers.
// A service with no container is "absent" (a stopped stack's containers are
// removed); with known false every service is "unknown".
func serviceStates(declared []declaredService, containers []podman.ProjectContainer, known bool) []protocol.StackService {
	byService := make(map[string][]podman.ProjectContainer, len(declared))
	for _, c := range containers {
		byService[c.Service] = append(byService[c.Service], c)
	}
	out := make([]protocol.StackService, 0, len(declared))
	for _, d := range declared {
		if !known {
			out = append(out, protocol.StackService{Service: d.name, Image: d.image, State: "unknown"})
			continue
		}
		list := byService[d.name]
		if len(list) == 0 {
			out = append(out, protocol.StackService{Service: d.name, Image: d.image, State: "absent"})
			continue
		}
		sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
		for _, c := range list {
			image := c.Image
			if image == "" {
				image = d.image
			}
			out = append(out, protocol.StackService{Service: d.name, Container: c.Name, Image: image, State: c.State,
				Status: c.Status, Health: c.Health, Restarts: c.Restarts, Recent: c.Recent})
		}
	}
	return out
}
