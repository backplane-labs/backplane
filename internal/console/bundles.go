package console

import (
	"bytes"
	"container/list"
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"path"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/gopherex/xlog"

	backplanev1 "github.com/backplane-labs/backplane/backplanepb/v1"
	"github.com/backplane-labs/backplane/pkg/backplane/config"
)

const (
	// SecretHTTPHeader carries the internal secret to a platform port.
	SecretHTTPHeader = "Bp-Internal-Secret" //nolint:gosec // a header name, not a credential
	// uiPath is where the SDK serves the bundle on the platform port.
	uiPath = "/_backplane/ui"
	// maxBundleFile bounds one file of a bundle.
	maxBundleFile = 32 << 20
	// fetchTimeout bounds one fetch from an instance.
	fetchTimeout = 30 * time.Second
	// immutable: a path with the content hash never changes content.
	immutable = "public, max-age=31536000, immutable"
	// cacheShare: a file larger than this share of the cap is served but
	// not cached.
	cacheShare = 4
)

var (
	errBundleNotFound = errors.New("console: no such bundle file")
	errBundleMismatch = errors.New("console: instance serves another bundle")
	errBundleTooLarge = errors.New("console: bundle file too large")
	errBundleStatus   = errors.New("console: bundle fetch")
	errNoInstance     = errors.New("console: no serving instance has the bundle")
)

// plugin serves /plugins/<service>/<hash>/<path>: from the cache, else
// from a live instance serving that hash.
func (c *Console) plugin(w http.ResponseWriter, r *http.Request) {
	c.serveBundle(w, r, uiPath, uiHash(r.PathValue("hash")), "", immutable)
}

func (c *Console) apiFile(w http.ResponseWriter, r *http.Request) {
	hash := r.PathValue("hash")
	c.serveBundle(w, r, "/_backplane/api/"+hash, func(m *backplanev1.Manifest) bool {
		for _, route := range m.GetApis() {
			if route.GetBundle().GetHash() == hash && hash != "" {
				return true
			}
		}

		return false
	}, "api/", "private, no-store")
}

func (c *Console) serveBundle(w http.ResponseWriter, r *http.Request, prefix string,
	match func(*backplanev1.Manifest) bool, namespace, cacheControl string,
) {
	if _, ok := c.authorized(w, r); !ok {
		return
	}

	name, hash, file := r.PathValue("service"), r.PathValue("hash"), r.PathValue("path")

	clean := path.Clean("/" + file)
	if file == "" || clean == "/" || strings.Contains(file, "..") {
		http.NotFound(w, r)

		return
	}

	key := namespace + name + "/" + hash + clean

	f, ok := c.bundles.cached(key)
	if !ok {
		svc, found := c.src.Current().Services[name]
		if !found {
			http.NotFound(w, r)

			return
		}

		preferred, fallback := serving(svc, match)

		var err error

		f, err = c.bundles.fetchAt(r.Context(), name, key, hash, clean, prefix, platformAddrs(append(preferred, fallback...)))

		switch {
		case errors.Is(err, errBundleNotFound), errors.Is(err, errNoInstance):
			http.NotFound(w, r)

			return
		case err != nil:
			c.Log().Warn("console: plugin bundle", xlog.String("service", name), xlog.String("path", clean), xlog.Err(err))
			http.Error(w, "bundle unavailable", http.StatusBadGateway)

			return
		}
	}

	h := w.Header()
	h.Set("Cache-Control", cacheControl)
	h.Set("ETag", `"`+hash+`"`)
	h.Set("Content-Type", f.contentType)

	if namespace != "" {
		h.Set("Content-Type", "application/octet-stream")
		h.Set("Content-Disposition", "attachment")
	}
	// ServeContent answers If-None-Match with 304 against the ETag.
	http.ServeContent(w, r, clean, time.Time{}, bytes.NewReader(f.body))
}

// bundleFile is one cached file.
type bundleFile struct {
	key         string
	contentType string
	body        []byte
}

// bundles fetch plugin files from platform ports and keep them in an LRU
// capped in bytes. A bundle is addressed by its content hash, so a cached
// file never goes stale.
//
// bundles are shared by pointer: they hold the cache.
type bundles struct {
	secret   config.Secret
	client   *http.Client
	platform Platform

	tlsMu   sync.Mutex
	clients map[string]*http.Client // mutual TLS, by service
	group   singleflight.Group

	mu    sync.Mutex
	max   int64
	size  int64
	order *list.List               // front: most recent
	items map[string]*list.Element // of bundleFile
}

func newBundles(secret config.Secret, maxBytes int64, platform Platform) *bundles {
	return &bundles{
		secret: secret, platform: platform, clients: map[string]*http.Client{},
		client: &http.Client{
			Timeout:       fetchTimeout,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		max:   maxBytes,
		order: list.New(),
		items: map[string]*list.Element{},
	}
}

// schemePlain is the scheme of a platform port without TLS.
const schemePlain = "http"

// clientOf is the client of service's platform port and its scheme: mutual
// TLS when the platform serves it, plain otherwise.
func (b *bundles) clientOf(service string) (*http.Client, string) {
	if b.platform == nil {
		return b.client, schemePlain
	}

	cfg := b.platform.PlatformTLS(service)
	if cfg == nil {
		return b.client, schemePlain
	}

	b.tlsMu.Lock()
	defer b.tlsMu.Unlock()

	client, ok := b.clients[service]
	if !ok {
		client = &http.Client{
			Timeout: fetchTimeout, CheckRedirect: b.client.CheckRedirect,
			Transport: &http.Transport{TLSClientConfig: cfg},
		}
		b.clients[service] = client
	}

	return client, "https"
}

func (b *bundles) cached(key string) (bundleFile, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()

	el, ok := b.items[key]
	if !ok {
		return bundleFile{}, false
	}

	b.order.MoveToFront(el)

	f, _ := el.Value.(bundleFile)

	return f, true
}

func (b *bundles) store(f bundleFile) {
	n := int64(len(f.body))
	if n > b.max/cacheShare {
		return
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	if _, ok := b.items[f.key]; ok {
		return
	}

	b.items[f.key] = b.order.PushFront(f)
	b.size += n

	for b.size > b.max {
		last := b.order.Back()
		old, _ := last.Value.(bundleFile)
		b.order.Remove(last)
		delete(b.items, old.key)
		b.size -= int64(len(old.body))
	}
}

// fetch gets one file from the first instance of addrs that has it,
// once for concurrent requests of the same key, and caches it.
func (b *bundles) fetchAt(
	ctx context.Context, service, key, hash, file, prefix string, addrs []string,
) (bundleFile, error) {
	if len(addrs) == 0 {
		return bundleFile{}, errNoInstance
	}

	v, err, _ := b.group.Do(key, func() (any, error) {
		// Shared by every waiter: not bound to the first one's request.
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), fetchTimeout)
		defer cancel()

		var errs []error

		for _, addr := range addrs {
			f, err := b.fetchFromAt(ctx, service, addr, hash, file, prefix)
			if err == nil {
				f.key = key
				b.store(f)

				return f, nil
			}

			if errors.Is(err, errBundleNotFound) {
				return nil, err
			}

			errs = append(errs, err)
		}

		return nil, errors.Join(errs...)
	})
	if err != nil {
		return bundleFile{}, err //nolint:wrapcheck // fetchFrom's errors
	}

	f, _ := v.(bundleFile)

	return f, nil
}

// fetchFrom gets the file from one platform port; the ETag must be the
// hash (the instance serves that bundle).
func (b *bundles) fetchFromAt(ctx context.Context, service, addr, hash, file, prefix string) (bundleFile, error) {
	client, scheme := b.clientOf(service)
	u := url.URL{Scheme: scheme, Host: addr, Path: prefix + file}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), http.NoBody)
	if err != nil {
		return bundleFile{}, fmt.Errorf("console: bundle request: %w", err)
	}

	if s := b.secret.Reveal(); s != "" {
		req.Header.Set(SecretHTTPHeader, s)
	}

	res, err := client.Do(req)
	if err != nil {
		return bundleFile{}, fmt.Errorf("console: bundle from %s: %w", addr, err)
	}
	defer res.Body.Close()

	switch {
	case res.StatusCode == http.StatusNotFound:
		return bundleFile{}, errBundleNotFound
	case res.StatusCode != http.StatusOK:
		return bundleFile{}, fmt.Errorf("%w: %s answered %s", errBundleStatus, addr, res.Status)
	case res.Header.Get("ETag") != `"`+hash+`"`:
		return bundleFile{}, fmt.Errorf("%w: %s has %s", errBundleMismatch, addr, res.Header.Get("ETag"))
	}

	body, err := io.ReadAll(io.LimitReader(res.Body, maxBundleFile+1))
	if err != nil {
		return bundleFile{}, fmt.Errorf("console: bundle from %s: %w", addr, err)
	}

	if len(body) > maxBundleFile {
		return bundleFile{}, errBundleTooLarge
	}

	typ := res.Header.Get("Content-Type")
	if typ == "" {
		typ = mime.TypeByExtension(path.Ext(file))
	}

	if typ == "" {
		typ = http.DetectContentType(body)
	}

	return bundleFile{contentType: typ, body: body}, nil
}
