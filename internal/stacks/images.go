package stacks

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/serialexp/sorry-portainer/internal/imagecheck"
	"github.com/serialexp/sorry-portainer/internal/podman"
	"github.com/serialexp/sorry-portainer/internal/protocol"
)

// ImageSource is what image checks and updates need from Podman.
type ImageSource interface {
	LocalImages(ctx context.Context) ([]podman.LocalImage, error)
	Pull(ctx context.Context, ref string) (string, error)
}

// RemoteChecker answers what image tags point to in their registries;
// imagecheck.Checker is the implementation.
type RemoteChecker interface {
	Latest(ctx context.Context, refs []string, force bool) map[string]imagecheck.Remote
	Built(ctx context.Context, ref, digest string) (time.Time, error)
	Forget(ref string)
}

var errImageChecksUnavailable = errors.New("this agent was started without image checks")

// pullTimeout bounds one `podman pull`.
const pullTimeout = 5 * time.Minute

// EnableImageChecks turns on Images and Update. Call it before serving requests.
func (m *Manager) EnableImageChecks(images ImageSource, checker RemoteChecker) {
	m.images = images
	m.checker = checker
}

// serviceImage is one service's image reference and the containers running it.
type serviceImage struct {
	service    string
	ref        string
	containers []podman.ProjectContainer
	// status is set when the reference is not checked against a registry.
	status, reason string
}

// resolveImages picks each declared service's image reference: the image of
// its containers as Podman names it (fully qualified, with Compose's
// variables resolved), or the Compose file's for a service without one.
func resolveImages(declared []declaredService, containers []podman.ProjectContainer) []serviceImage {
	byService := make(map[string][]podman.ProjectContainer, len(declared))
	for _, c := range containers {
		byService[c.Service] = append(byService[c.Service], c)
	}
	out := make([]serviceImage, 0, len(declared))
	for _, d := range declared {
		s := serviceImage{service: d.name, ref: d.image, containers: byService[d.name]}
		for _, c := range s.containers {
			if c.Image != "" {
				s.ref = c.Image
				break
			}
		}
		switch {
		case s.ref == "":
			s.status, s.reason = protocol.ImageLocal, "The service has no image; Compose builds it on the host."
		case strings.Contains(s.ref, "$"):
			s.status, s.reason = protocol.ImageUnchecked, "The image name uses variables, which Compose resolves when the stack starts."
		default:
			kind, err := imagecheck.Classify(s.ref)
			switch {
			case err != nil:
				s.status, s.reason = protocol.ImageUnchecked, err.Error()
			case kind == imagecheck.Pinned:
				s.status = protocol.ImagePinned
			case kind == imagecheck.Local:
				s.status = protocol.ImageLocal
			}
		}
		out = append(out, s)
	}
	return out
}

// readServiceImages reads the stack's Compose file and containers under the
// stack's lock.
func (m *Manager) readServiceImages(ctx context.Context, n string) ([]serviceImage, error) {
	source, err := os.ReadFile(filepath.Join(m.dir(n), "compose.yaml"))
	if err != nil {
		return nil, err
	}
	declared, err := declaredServices(source)
	if err != nil {
		return nil, fmt.Errorf("the Compose file does not parse: %w", err)
	}
	containers, known, err := m.containersByProject(ctx)
	if err != nil {
		return nil, err
	}
	if !known {
		return nil, errImageChecksUnavailable
	}
	return resolveImages(declared, containers[m.project(n)]), nil
}

// localIndex finds local images by ID and by canonical tag.
type localIndex struct {
	byID map[string]*podman.LocalImage
	// byTag is nil unless a service without a container needs it: parsing
	// every tag on the host is most of a check's own work.
	byTag map[string]*podman.LocalImage
}

func indexLocal(images []podman.LocalImage, byTag bool) localIndex {
	index := localIndex{byID: make(map[string]*podman.LocalImage, len(images))}
	if byTag {
		index.byTag = make(map[string]*podman.LocalImage, len(images))
	}
	for i := range images {
		image := &images[i]
		index.byID[image.ID] = image
		if !byTag {
			continue
		}
		for _, tag := range image.Names {
			if canonical, err := imagecheck.Canonical(tag); err == nil {
				index.byTag[canonical] = image
			}
		}
	}
	return index
}

// localDigests are the digests Podman knows image by in ref's repository,
// the digest it pulled the image by first.
func localDigests(ref string, image *podman.LocalImage) []string {
	if image == nil {
		return nil
	}
	digests := imagecheck.DigestsFor(ref, image.RepoDigests)
	if i := slices.Index(digests, image.Digest); i > 0 {
		digests[0], digests[i] = digests[i], digests[0]
	}
	return digests
}

// Images reports for each service whether the image it runs is what its tag
// points to now. The registry is asked only for references whose cached
// answer is older than the checker's TTL, or for every one with force. The
// stack's lock is held only while its file and containers are read, not
// during registry requests.
func (m *Manager) Images(ctx context.Context, n string, force bool) (protocol.StackImages, error) {
	if !ValidName(n) {
		return protocol.StackImages{}, errors.New("invalid stack name")
	}
	if m.images == nil || m.checker == nil {
		return protocol.StackImages{}, errImageChecksUnavailable
	}
	l := m.lock(n)
	l.Lock()
	services, err := m.readServiceImages(ctx, n)
	l.Unlock()
	if err != nil {
		return protocol.StackImages{}, err
	}
	out := protocol.StackImages{Name: n, Services: make([]protocol.ServiceImage, len(services))}
	var refs []string
	for i, s := range services {
		out.Services[i] = protocol.ServiceImage{Service: s.service, Image: s.ref, Status: s.status, Error: s.reason}
		if s.status == "" {
			out.Services[i].Registry = imagecheck.RegistryName(s.ref)
			refs = append(refs, s.ref)
		}
	}
	if len(refs) == 0 {
		return out, nil
	}
	local, err := m.images.LocalImages(ctx)
	if err != nil {
		return protocol.StackImages{}, fmt.Errorf("list images: %w", err)
	}
	needTags := slices.ContainsFunc(services, func(s serviceImage) bool { return s.status == "" && len(s.containers) == 0 })
	index := indexLocal(local, needTags)
	remotes := m.checker.Latest(ctx, refs, force)
	for i, s := range services {
		if s.status != "" {
			continue
		}
		entry := &out.Services[i]
		remote := remotes[s.ref]
		entry.LatestDigest = remote.Digest
		if !remote.CheckedAt.IsZero() {
			entry.CheckedAt = remote.CheckedAt.Unix()
		}
		entry.Error = remote.Err
		entry.Status, entry.RunningDigest = compareImage(s, index, remote.Digest)
		if remote.Err != "" || remote.Digest == "" {
			entry.Status, entry.Failure = protocol.ImageUnknown, failureKind(remote.Failure)
			continue
		}
		if entry.Status == protocol.ImageBehind {
			if created, err := m.checker.Built(ctx, s.ref, remote.Digest); err == nil {
				entry.LatestCreated = created.Unix()
			} else if m.Logf != nil {
				m.Logf("stack %s: build time of %s@%s: %v", n, s.ref, remote.Digest, err)
			}
		}
	}
	return out, nil
}

// failureKind is the protocol's name for a checker failure.
func failureKind(f imagecheck.Failure) string {
	switch f {
	case imagecheck.Denied:
		return protocol.ImageFailureDenied
	case imagecheck.NotFound:
		return protocol.ImageFailureNotFound
	case "":
		return ""
	default:
		return protocol.ImageFailureUnreachable
	}
}

// compareImage says whether a service runs latest, and the digest it runs.
// A service with containers is behind when any of them runs another image;
// one without is compared through the local image its reference names, and
// is behind when there is none.
func compareImage(s serviceImage, index localIndex, latest string) (status, running string) {
	var images []*podman.LocalImage
	if len(s.containers) > 0 {
		for _, c := range s.containers {
			images = append(images, index.byID[c.ImageID])
		}
	} else if canonical, err := imagecheck.Canonical(s.ref); err == nil {
		images = append(images, index.byTag[canonical])
	}
	if len(images) == 0 {
		return protocol.ImageBehind, ""
	}
	status = protocol.ImageCurrent
	for _, image := range images {
		digests := localDigests(s.ref, image)
		if latest != "" && slices.Contains(digests, latest) {
			continue
		}
		// The digest shown is the first container's that is behind.
		if status == protocol.ImageCurrent && len(digests) > 0 {
			running = digests[0]
		}
		status = protocol.ImageBehind
	}
	if status == protocol.ImageCurrent {
		running = latest
	}
	return status, running
}

// Update pulls the image of one service and, when any container of the
// stack runs, recreates the service with `up -d --force-recreate`, which in
// podman-compose 1.0.6 also recreates the services it depends on. A stopped
// stack only pulls; the service uses the image when the stack next starts.
// The desired state does not change. progress, when not nil, is told each
// step as it starts: protocol.UpdatePulling, then UpdateRecreating.
func (m *Manager) Update(ctx context.Context, n, service string, progress func(step string)) (protocol.StackOperation, error) {
	if progress == nil {
		progress = func(string) {}
	}
	if !ValidName(n) {
		return protocol.StackOperation{}, errors.New("invalid stack name")
	}
	if m.images == nil || m.checker == nil {
		return protocol.StackOperation{}, errImageChecksUnavailable
	}
	l := m.lock(n)
	l.Lock()
	defer l.Unlock()
	stack, err := m.inspect(n)
	if err != nil {
		return protocol.StackOperation{}, err
	}
	services, err := m.readServiceImages(ctx, n)
	if err != nil {
		return protocol.StackOperation{}, err
	}
	index := slices.IndexFunc(services, func(s serviceImage) bool { return s.service == service })
	if index < 0 {
		return protocol.StackOperation{}, fmt.Errorf("stack %s has no service %q", n, service)
	}
	target := services[index]
	if target.status != "" {
		reason := target.reason
		if reason == "" {
			reason = "its image is " + target.status
		}
		return protocol.StackOperation{}, fmt.Errorf("service %s cannot be updated: %s", service, reason)
	}
	result := protocol.StackOperation{Name: n, Operation: "update"}
	var output strings.Builder
	progress(protocol.UpdatePulling)
	fmt.Fprintf(&output, "$ podman pull %s\n", target.ref)
	pullCtx, cancel := context.WithTimeout(ctx, pullTimeout)
	id, err := m.images.Pull(pullCtx, target.ref)
	cancel()
	// The cached answer predates the pull either way.
	m.checker.Forget(target.ref)
	if err != nil {
		output.WriteString(err.Error())
		result.Output = output.String()
		return result, err
	}
	output.WriteString(id + "\n")
	if !stackRuns(services) {
		output.WriteString("The stack is stopped; " + service + " uses the new image when the stack starts.\n")
		result.Output, result.Success = output.String(), true
		return result, nil
	}
	plan, err := m.prepareRuntime(n, true)
	if err != nil {
		output.WriteString(err.Error())
		result.Output = output.String()
		return result, err
	}
	args := []string{"-p", m.project(n), "-f", runtimeComposeFile, "up", "-d", "--force-recreate", service}
	progress(protocol.UpdateRecreating)
	fmt.Fprintf(&output, "$ podman-compose %s\n", strings.Join(args, " "))
	runCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	out, err := m.executor.Run(runCtx, args, m.dir(n), stack.Environment)
	output.WriteString(out)
	if err == nil && !plan.empty() {
		if err = m.checkInjected(runCtx, n, plan.Services); err != nil {
			output.WriteString("\n" + err.Error())
		}
	}
	// podman-compose 1.0.6 exits 0 even when `podman run` failed, so the
	// update succeeded only if the service now runs the pulled image.
	if err == nil {
		if err = m.checkRunsImage(runCtx, n, service, id); err != nil {
			output.WriteString("\n" + err.Error())
		}
	}
	result.Output, result.Success = output.String(), err == nil
	return result, err
}

// checkRunsImage verifies that every container of service runs and was
// created from image, and that there is at least one.
func (m *Manager) checkRunsImage(ctx context.Context, n, service, image string) error {
	all, _, err := m.containersByProject(ctx)
	if err != nil {
		return err
	}
	found := false
	for _, c := range all[m.project(n)] {
		if c.Service != service {
			continue
		}
		found = true
		if c.State != "running" {
			return fmt.Errorf("%s is %s after the update", c.Name, c.State)
		}
		if c.ImageID != image {
			return fmt.Errorf("%s runs image %.12s, not the pulled %.12s", c.Name, c.ImageID, image)
		}
	}
	if !found {
		return fmt.Errorf("%s has no container after the update", service)
	}
	return nil
}

// stackRuns is whether any container of the stack runs.
func stackRuns(services []serviceImage) bool {
	for _, s := range services {
		for _, c := range s.containers {
			if c.State == "running" {
				return true
			}
		}
	}
	return false
}
