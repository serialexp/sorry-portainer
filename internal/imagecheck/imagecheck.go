// Package imagecheck finds out what an image tag points to in its registry
// now, for comparing it with the image a container runs. It asks with HEAD
// requests, which Docker Hub does not count toward its pull limit, and keeps
// the answers for a while so reopening a stack page asks nothing.
package imagecheck

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/google/go-containerregistry/pkg/v1/remote/transport"
)

// Defaults for Options fields left zero.
const (
	DefaultTTL         = 15 * time.Minute
	DefaultMaxEntries  = 1024
	DefaultConcurrency = 4
	DefaultTimeout     = 15 * time.Second
)

// Registry is the network side of a check. The production implementation is
// NewRemoteRegistry; tests use an in-process registry or a fake.
type Registry interface {
	// Head returns the digest the reference (a tag) points to now.
	Head(ctx context.Context, ref string) (string, error)
	// Created returns the build time of the image the reference's repository
	// holds under digest, for this host's platform.
	Created(ctx context.Context, ref, digest string) (time.Time, error)
}

// Remote is what the registry said about one reference.
type Remote struct {
	// Digest is the last digest the registry answered, empty if it never did.
	Digest string
	// CheckedAt is when the registry last answered Digest; zero if it never did.
	CheckedAt time.Time
	// Err is why the latest attempt failed, empty when it succeeded. Digest
	// and CheckedAt then still hold the last good answer.
	Err string
	// Failure is the kind of the latest failure, empty when it succeeded.
	Failure Failure
}

// Failure is what kind of failure a check ran into.
type Failure string

const (
	// Denied: the registry refused the credentials, or asked for some
	// (HTTP 401 or 403). Docker Hub also answers 401 for a repository that
	// does not exist.
	Denied Failure = "denied"
	// NotFound: the registry has no such tag (HTTP 404).
	NotFound Failure = "not_found"
	// Unreachable: anything else; no answer, a timeout, or a server error.
	Unreachable Failure = "unreachable"
)

// failureOf classifies a failed request by the registry's HTTP status.
func failureOf(err error) Failure {
	var answered *transport.Error
	if errors.As(err, &answered) {
		switch answered.StatusCode {
		case http.StatusUnauthorized, http.StatusForbidden:
			return Denied
		case http.StatusNotFound:
			return NotFound
		}
	}
	return Unreachable
}

// describe is the text kept and shown for a failed request. A registry's
// error body can echo request details, so an HTTP answer is reduced to its
// status; other errors (DNS, connection, TLS) come from this host's network
// stack and carry no registry text.
func describe(err error) string {
	var answered *transport.Error
	if errors.As(err, &answered) {
		return fmt.Sprintf("the registry answered %d %s", answered.StatusCode, http.StatusText(answered.StatusCode))
	}
	return err.Error()
}

type Options struct {
	TTL         time.Duration
	MaxEntries  int
	Concurrency int
	// Timeout bounds each registry request.
	Timeout time.Duration
	Now     func() time.Time
}

type entry struct {
	remote Remote
	// attempted is when the latest request finished, good or not; a failure
	// is cached as long as a success, so a down registry is not hammered.
	attempted time.Time
}

type call struct {
	done   chan struct{}
	remote Remote
	// generation is the Checker's generation when the request started.
	generation uint64
}

// Checker caches registry answers per reference. It is safe for concurrent use.
type Checker struct {
	registry Registry
	options  Options

	mu       sync.Mutex
	entries  map[string]*entry
	inflight map[string]*call
	built    map[string]time.Time
	// builtOrder is the insertion order of built, oldest first, for eviction.
	builtOrder []string
	// generation changes on every Flush. A request records it when it starts
	// and only caches its answer if it is still current, so a request made
	// with logins that have since changed cannot refill the cache.
	generation uint64
}

func New(registry Registry, options Options) *Checker {
	if options.TTL <= 0 {
		options.TTL = DefaultTTL
	}
	if options.MaxEntries <= 0 {
		options.MaxEntries = DefaultMaxEntries
	}
	if options.Concurrency <= 0 {
		options.Concurrency = DefaultConcurrency
	}
	if options.Timeout <= 0 {
		options.Timeout = DefaultTimeout
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	return &Checker{
		registry: registry,
		options:  options,
		entries:  map[string]*entry{},
		inflight: map[string]*call{},
		built:    map[string]time.Time{},
	}
}

// Latest answers each reference, from the cache when its entry is younger
// than the TTL and force is false, otherwise from the registry. At most
// Options.Concurrency requests run at once, and a reference already being
// asked about is not asked again.
func (c *Checker) Latest(ctx context.Context, refs []string, force bool) map[string]Remote {
	out := make(map[string]Remote, len(refs))
	var pending []string
	for _, ref := range refs {
		if _, seen := out[ref]; seen {
			continue
		}
		if remote, ok := c.fresh(ref, force); ok {
			out[ref] = remote
			continue
		}
		// Reserve the key so a duplicate in refs is skipped above.
		out[ref] = Remote{}
		pending = append(pending, ref)
	}
	if len(pending) == 0 {
		return out
	}
	results := make([]Remote, len(pending))
	slots := make(chan struct{}, c.options.Concurrency)
	var wg sync.WaitGroup
	for i, ref := range pending {
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case slots <- struct{}{}:
				defer func() { <-slots }()
			case <-ctx.Done():
			}
			// select picks at random when both are ready; a cancelled check
			// must not start a request.
			if err := ctx.Err(); err != nil {
				results[i] = c.failed(ref, err)
				return
			}
			results[i] = c.ask(ctx, ref)
		}()
	}
	wg.Wait()
	for i, ref := range pending {
		out[ref] = results[i]
	}
	return out
}

// fresh returns the cached answer when it may be used.
func (c *Checker) fresh(ref string, force bool) (Remote, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e := c.entries[ref]
	if e == nil || force || c.options.Now().Sub(e.attempted) >= c.options.TTL {
		return Remote{}, false
	}
	return e.remote, true
}

// failed is the cached answer with err as the latest failure, without
// recording it: the request was never made.
func (c *Checker) failed(ref string, err error) Remote {
	c.mu.Lock()
	defer c.mu.Unlock()
	remote := Remote{}
	if e := c.entries[ref]; e != nil {
		remote = e.remote
	}
	remote.Err, remote.Failure = describe(err), failureOf(err)
	return remote
}

// ask makes one HEAD request for ref, or waits for the one already running.
func (c *Checker) ask(ctx context.Context, ref string) Remote {
	c.mu.Lock()
	if running := c.inflight[ref]; running != nil && running.generation == c.generation {
		c.mu.Unlock()
		select {
		case <-running.done:
			return running.remote
		case <-ctx.Done():
			return c.failed(ref, ctx.Err())
		}
	}
	current := &call{done: make(chan struct{}), generation: c.generation}
	c.inflight[ref] = current
	c.mu.Unlock()

	requestCtx, cancel := context.WithTimeout(ctx, c.options.Timeout)
	digest, err := c.registry.Head(requestCtx, ref)
	cancel()

	c.mu.Lock()
	now := c.options.Now()
	var remote Remote
	if e := c.entries[ref]; e != nil {
		remote = e.remote
	}
	if err != nil {
		remote.Err, remote.Failure = describe(err), failureOf(err)
	} else {
		remote = Remote{Digest: digest, CheckedAt: now}
	}
	if current.generation == c.generation {
		c.entries[ref] = &entry{remote: remote, attempted: now}
		c.evictLocked()
	}
	current.remote = remote
	if c.inflight[ref] == current {
		delete(c.inflight, ref)
	}
	c.mu.Unlock()
	close(current.done)
	return current.remote
}

// Flush drops every cached answer and makes requests already running unable
// to cache theirs. The agent calls it when its registry logins change.
func (c *Checker) Flush() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.generation++
	clear(c.entries)
}

// evictLocked drops the least recently attempted entries beyond MaxEntries.
func (c *Checker) evictLocked() {
	for len(c.entries) > c.options.MaxEntries {
		var oldest string
		var oldestAt time.Time
		for ref, e := range c.entries {
			if oldest == "" || e.attempted.Before(oldestAt) {
				oldest, oldestAt = ref, e.attempted
			}
		}
		delete(c.entries, oldest)
	}
}

// Forget drops the cached answer for ref, so the next check asks again.
func (c *Checker) Forget(ref string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.entries, ref)
}

// Built returns the build time of the image ref's repository holds under
// digest. Digests are immutable, so a good answer is kept until evicted;
// failures are not kept.
func (c *Checker) Built(ctx context.Context, ref, digest string) (time.Time, error) {
	key := ref + "@" + digest
	c.mu.Lock()
	if created, ok := c.built[key]; ok {
		c.mu.Unlock()
		return created, nil
	}
	c.mu.Unlock()
	requestCtx, cancel := context.WithTimeout(ctx, c.options.Timeout)
	defer cancel()
	created, err := c.registry.Created(requestCtx, ref, digest)
	if err != nil {
		return time.Time{}, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.built[key]; !ok {
		c.built[key] = created
		c.builtOrder = append(c.builtOrder, key)
		for len(c.builtOrder) > c.options.MaxEntries {
			delete(c.built, c.builtOrder[0])
			c.builtOrder = c.builtOrder[1:]
		}
	}
	return created, nil
}
