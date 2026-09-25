package stacks

import (
	"bytes"
	"errors"
	"fmt"
	"path"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/serialexp/sorry-portainer/internal/protocol"
	"github.com/serialexp/sorry-portainer/internal/secrets"
)

// secretPlan is what a compose file asks of stack secrets.
type secretPlan struct {
	// Services maps each secret-bearing service to its secret files.
	Services map[string][]secrets.Mount
	// Names lists the stack secrets services use, sorted.
	Names []string
}

func (p secretPlan) empty() bool { return len(p.Services) == 0 }

// composeDocument is a parsed compose file with its secret plan.
type composeDocument struct {
	root *yaml.Node // the top-level mapping
	doc  *yaml.Node
	plan secretPlan
	// hasSecrets is true when the file has a top-level secrets section.
	hasSecrets bool
}

// parseCompose validates the parts of a compose file that stack secrets
// depend on. Everything else is left to podman-compose.
//
// Standard compose secrets are supported with a stack-secret source only:
// top-level entries must be empty or `external: true`, because `file`,
// `environment` and `content` sources would put the value on the agent's disk
// or in Podman's secret store.
func parseCompose(source []byte) (*composeDocument, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(source, &doc); err != nil {
		return nil, fmt.Errorf("invalid compose YAML: %w", err)
	}
	if doc.Kind == 0 {
		return nil, errors.New("compose file is empty")
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, errors.New("compose file must be a YAML mapping")
	}
	if err := rejectReserved(&doc); err != nil {
		return nil, err
	}
	c := &composeDocument{root: doc.Content[0], doc: &doc, plan: secretPlan{Services: map[string][]secrets.Mount{}}}

	declared := map[string]bool{}
	if top := mappingValue(c.root, "secrets"); top != nil {
		c.hasSecrets = true
		if top.Kind != yaml.MappingNode {
			return nil, errors.New("top-level secrets must be a mapping")
		}
		for i := 0; i < len(top.Content); i += 2 {
			name, body := top.Content[i].Value, top.Content[i+1]
			if !protocol.ValidSecretName(name) {
				return nil, fmt.Errorf("invalid secret name %q: use letters, digits, '.', '_' or '-'", name)
			}
			if err := checkSecretDeclaration(name, body); err != nil {
				return nil, err
			}
			declared[name] = true
		}
	}

	services := mappingValue(c.root, "services")
	if services == nil {
		if c.hasSecrets {
			return nil, errors.New("compose file declares secrets but has no services")
		}
		return c, nil
	}
	if services.Kind != yaml.MappingNode {
		return nil, errors.New("services must be a mapping")
	}
	used := map[string]bool{}
	for i := 0; i < len(services.Content); i += 2 {
		name, service := services.Content[i].Value, services.Content[i+1]
		if service.Kind == yaml.AliasNode && c.hasSecrets {
			return nil, fmt.Errorf("service %s: YAML aliases for whole services are not supported in stacks with secrets", name)
		}
		if service.Kind != yaml.MappingNode {
			continue // podman-compose reports malformed services
		}
		if c.hasSecrets {
			if err := checkMerges(name, service); err != nil {
				return nil, err
			}
		}
		if extends := mappingValue(service, "extends"); extends != nil && extends.Kind == yaml.MappingNode && mappingValue(extends, "file") != nil {
			return nil, fmt.Errorf("service %s: extends with file is not supported; stacks are a single compose file", name)
		}
		list := mappingValue(service, "secrets")
		if list == nil {
			continue
		}
		mounts, err := serviceMounts(name, list, declared)
		if err != nil {
			return nil, err
		}
		if len(mounts) == 0 {
			continue
		}
		if err := checkSecretsDirFree(name, service); err != nil {
			return nil, err
		}
		c.plan.Services[name] = mounts
		for _, m := range mounts {
			used[m.Source] = true
		}
	}
	if len(used) > protocol.MaxSecretsPerStack {
		return nil, fmt.Errorf("a stack may use at most %d secrets", protocol.MaxSecretsPerStack)
	}
	for name := range used {
		c.plan.Names = append(c.plan.Names, name)
	}
	sort.Strings(c.plan.Names)
	return c, nil
}

// rejectReserved refuses any mention of the reserved annotation prefix, so a
// stack cannot pose as another stack's secret-bearing container, whatever
// compose feature (annotations, labels, podman arguments) it would use.
func rejectReserved(node *yaml.Node) error {
	if node.Kind == yaml.ScalarNode && strings.Contains(node.Value, secrets.AnnotationPrefix) {
		return fmt.Errorf("line %d: %q is reserved for sorry-portainer", node.Line, secrets.AnnotationPrefix)
	}
	for _, child := range node.Content {
		if err := rejectReserved(child); err != nil {
			return err
		}
	}
	return nil
}

func checkSecretDeclaration(name string, body *yaml.Node) error {
	if body.Kind == yaml.ScalarNode && body.ShortTag() == "!!null" {
		return nil
	}
	if body.Kind != yaml.MappingNode {
		return fmt.Errorf("secret %s: declaration must be empty or `external: true`", name)
	}
	for i := 0; i < len(body.Content); i += 2 {
		key, value := body.Content[i].Value, body.Content[i+1]
		switch {
		case key == "external" && value.Kind == yaml.ScalarNode && value.Value == "true":
		case key == "file" || key == "environment" || key == "content":
			return fmt.Errorf("secret %s: %s sources are not supported; set the value in sorry-portainer instead (it never touches the host's disk)", name, key)
		default:
			return fmt.Errorf("secret %s: %q is not supported; declare it empty or `external: true`", name, key)
		}
	}
	return nil
}

// checkMerges refuses merge keys that would bring in keys the rewrite
// manages, since the rewrite only sees the service's own keys.
func checkMerges(service string, mapping *yaml.Node) error {
	for i := 0; i < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value != "<<" {
			continue
		}
		sources := []*yaml.Node{mapping.Content[i+1]}
		if sources[0].Kind == yaml.SequenceNode {
			sources = sources[0].Content
		}
		for _, source := range sources {
			for source.Kind == yaml.AliasNode {
				source = source.Alias
			}
			if source.Kind != yaml.MappingNode {
				continue
			}
			for _, key := range []string{"secrets", "annotations", "tmpfs", "volumes", "<<"} {
				if mappingValue(source, key) != nil {
					return fmt.Errorf("service %s: a YAML merge brings in %q, which is not supported in stacks with secrets; write it on the service", service, key)
				}
			}
		}
	}
	return nil
}

func serviceMounts(service string, list *yaml.Node, declared map[string]bool) ([]secrets.Mount, error) {
	if list.Kind != yaml.SequenceNode {
		return nil, fmt.Errorf("service %s: secrets must be a list", service)
	}
	mounts := make([]secrets.Mount, 0, len(list.Content))
	for _, item := range list.Content {
		m := secrets.Mount{Mode: secrets.DefaultMode}
		switch item.Kind {
		case yaml.ScalarNode:
			m.Source, m.Target = item.Value, item.Value
		case yaml.MappingNode:
			for i := 0; i < len(item.Content); i += 2 {
				key, value := item.Content[i].Value, item.Content[i+1]
				if value.Kind != yaml.ScalarNode {
					return nil, fmt.Errorf("service %s: secret %s must be a scalar", service, key)
				}
				var err error
				switch key {
				case "source":
					m.Source = value.Value
				case "target":
					m.Target = value.Value
				case "uid":
					m.UID, err = parseID(value.Value)
				case "gid":
					m.GID, err = parseID(value.Value)
				case "mode":
					m.Mode, err = parseMode(value)
				default:
					return nil, fmt.Errorf("service %s: secret option %q is not supported", service, key)
				}
				if err != nil {
					return nil, fmt.Errorf("service %s: secret %s: %w", service, key, err)
				}
			}
			if m.Target == "" {
				m.Target = m.Source
			}
		default:
			return nil, fmt.Errorf("service %s: invalid secret entry", service)
		}
		if !declared[m.Source] {
			return nil, fmt.Errorf("service %s uses secret %q, which is not declared in the top-level secrets", service, m.Source)
		}
		target, err := secretTarget(m.Target)
		if err != nil {
			return nil, fmt.Errorf("service %s: %w", service, err)
		}
		m.Target = target
		mounts = append(mounts, m)
	}
	if len(mounts) == 0 {
		return mounts, nil
	}
	if _, err := secrets.EncodeMounts(mounts); err != nil {
		return nil, fmt.Errorf("service %s: %w", service, err)
	}
	return mounts, nil
}

// secretTarget accepts "name" or "/run/secrets/name".
func secretTarget(target string) (string, error) {
	if strings.HasPrefix(target, "/") {
		clean := path.Clean(target)
		if path.Dir(clean) != secrets.Dir || clean != target {
			return "", fmt.Errorf("secret target %q must be a file directly in %s", target, secrets.Dir)
		}
		target = path.Base(clean)
	}
	if !protocol.ValidSecretName(target) {
		return "", fmt.Errorf("invalid secret target %q", target)
	}
	return target, nil
}

func parseID(value string) (int, error) {
	id, err := strconv.ParseUint(value, 10, 31)
	if err != nil {
		return 0, fmt.Errorf("invalid id %q", value)
	}
	return int(id), nil
}

// parseMode reads an octal mode: a YAML integer (0440, 0o440, or decimal) or
// a string of octal digits, as Docker Compose accepts.
func parseMode(node *yaml.Node) (uint32, error) {
	base := 0
	if node.ShortTag() == "!!str" {
		base = 8
	}
	mode, err := strconv.ParseUint(node.Value, base, 32)
	if err != nil || mode > 0o777 {
		return 0, fmt.Errorf("invalid mode %q", node.Value)
	}
	return uint32(mode), nil
}

// checkSecretsDirFree refuses services that already mount something at or
// under /run/secrets, which would hide or replace the secrets tmpfs, and
// annotations or tmpfs written in a form the rewrite cannot extend.
func checkSecretsDirFree(service string, mapping *yaml.Node) error {
	if a := mappingValue(mapping, "annotations"); a != nil && a.Kind != yaml.MappingNode && a.Kind != yaml.SequenceNode {
		return fmt.Errorf("service %s: annotations must be a mapping or list in a service with secrets", service)
	}
	if t := mappingValue(mapping, "tmpfs"); t != nil && t.Kind != yaml.ScalarNode && t.Kind != yaml.SequenceNode {
		return fmt.Errorf("service %s: tmpfs must be a string or list in a service with secrets", service)
	}
	conflict := func(target string) bool {
		target = path.Clean(target)
		return target == secrets.Dir || strings.HasPrefix(target, secrets.Dir+"/")
	}
	if tmpfs := mappingValue(mapping, "tmpfs"); tmpfs != nil {
		entries := []*yaml.Node{tmpfs}
		if tmpfs.Kind == yaml.SequenceNode {
			entries = tmpfs.Content
		}
		for _, entry := range entries {
			target, _, _ := strings.Cut(entry.Value, ":")
			if entry.Kind == yaml.ScalarNode && conflict(target) {
				return fmt.Errorf("service %s: tmpfs at %s conflicts with its secrets", service, target)
			}
		}
	}
	if volumes := mappingValue(mapping, "volumes"); volumes != nil && volumes.Kind == yaml.SequenceNode {
		for _, volume := range volumes.Content {
			target := ""
			switch volume.Kind {
			case yaml.ScalarNode:
				parts := strings.Split(volume.Value, ":")
				target = parts[0]
				if len(parts) > 1 {
					target = parts[1]
				}
			case yaml.MappingNode:
				if t := mappingValue(volume, "target"); t != nil {
					target = t.Value
				}
			}
			if target != "" && conflict(target) {
				return fmt.Errorf("service %s: volume at %s conflicts with its secrets", service, target)
			}
		}
	}
	return nil
}

// runtimeCompose returns the file podman-compose runs. Secret-bearing
// services lose their secrets list and gain the hook annotations and the
// /run/secrets tmpfs; the top-level secrets section is dropped so Podman never
// creates its own, disk-backed secrets. A file without secrets is returned
// unchanged.
func (c *composeDocument) runtimeCompose(original []byte, hostID, stack string) ([]byte, error) {
	if !c.hasSecrets && c.plan.empty() {
		return original, nil
	}
	services := mappingValue(c.root, "services")
	for name, mounts := range c.plan.Services {
		service := mappingValue(services, name)
		encoded, err := secrets.EncodeMounts(mounts)
		if err != nil {
			return nil, err
		}
		removeKey(service, "secrets")
		addAnnotations(service, [][2]string{
			{secrets.AnnotationAgent, hostID},
			{secrets.AnnotationStack, stack},
			{secrets.AnnotationSecrets, encoded},
		})
		addTmpfs(service, secrets.Dir+":"+secrets.TmpfsOptions)
	}
	// Services with an empty secrets list keep nothing.
	if services != nil && services.Kind == yaml.MappingNode {
		for i := 1; i < len(services.Content); i += 2 {
			if services.Content[i].Kind == yaml.MappingNode {
				removeKey(services.Content[i], "secrets")
			}
		}
	}
	removeKey(c.root, "secrets")
	var out bytes.Buffer
	encoder := yaml.NewEncoder(&out)
	encoder.SetIndent(2)
	if err := encoder.Encode(c.doc); err != nil {
		return nil, err
	}
	if err := encoder.Close(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// deployedMounts reads the secret mounts back from a runtime compose file.
func deployedMounts(runtime []byte) (map[string][]secrets.Mount, error) {
	var doc struct {
		Services map[string]struct {
			Annotations yaml.Node `yaml:"annotations"`
		} `yaml:"services"`
	}
	if err := yaml.Unmarshal(runtime, &doc); err != nil {
		return nil, err
	}
	out := map[string][]secrets.Mount{}
	for name, service := range doc.Services {
		value := ""
		switch service.Annotations.Kind {
		case yaml.MappingNode:
			if v := mappingValue(&service.Annotations, secrets.AnnotationSecrets); v != nil {
				value = v.Value
			}
		case yaml.SequenceNode:
			for _, item := range service.Annotations.Content {
				if v, ok := strings.CutPrefix(item.Value, secrets.AnnotationSecrets+"="); ok {
					value = v
				}
			}
		}
		if value == "" {
			continue
		}
		mounts, err := secrets.DecodeMounts(value)
		if err != nil {
			return nil, fmt.Errorf("service %s: %w", name, err)
		}
		out[name] = mounts
	}
	return out, nil
}

func mappingValue(mapping *yaml.Node, key string) *yaml.Node {
	if mapping == nil || mapping.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			return mapping.Content[i+1]
		}
	}
	return nil
}

func removeKey(mapping *yaml.Node, key string) {
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			mapping.Content = append(mapping.Content[:i], mapping.Content[i+2:]...)
			return
		}
	}
}

func scalar(value string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value}
}

func addAnnotations(service *yaml.Node, pairs [][2]string) {
	annotations := mappingValue(service, "annotations")
	if annotations == nil {
		annotations = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		service.Content = append(service.Content, scalar("annotations"), annotations)
	}
	for _, pair := range pairs {
		if annotations.Kind == yaml.SequenceNode {
			annotations.Content = append(annotations.Content, scalar(pair[0]+"="+pair[1]))
		} else {
			annotations.Content = append(annotations.Content, scalar(pair[0]), scalar(pair[1]))
		}
	}
}

func addTmpfs(service *yaml.Node, entry string) {
	tmpfs := mappingValue(service, "tmpfs")
	switch {
	case tmpfs == nil:
		service.Content = append(service.Content, scalar("tmpfs"), &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Content: []*yaml.Node{scalar(entry)}})
	case tmpfs.Kind == yaml.SequenceNode:
		tmpfs.Content = append(tmpfs.Content, scalar(entry))
	default:
		existing := *tmpfs
		*tmpfs = yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Content: []*yaml.Node{&existing, scalar(entry)}}
	}
}
