package portable

import (
	"bytes"
	"context"
	"encoding/xml"
	"golang.org/x/oauth2"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"math"
	"mime"
	"net"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type assetMaterializer struct {
	s        *Service
	c        Config
	cat      *catalog
	transfer *budget
	index    bool
}

func (m *assetMaterializer) materialize(ctx context.Context, d *captureDraft) (materializedAssets, error) {
	cat := m.cat
	if m.index {
		_, resolved, e := m.s.planModel(ctx, m.c, d.model)
		if e != nil {
			return materializedAssets{}, e
		}
		cat = resolved
		m.cat = cat
	}
	if cat == nil {
		cat = newCatalog()
	}
	a := materializedAssets{attempt: d.attempt}
	cache := map[string]int{}
	identities := map[string]string{}
	var failure error
	position := 0
	walk(d.model, func(n map[string]any) {
		if n["t"] != "Image" || failure != nil {
			return
		}
		target := target(n)
		key := imageIdentity(target, position, identities)
		var asset capturedAsset
		var err error
		if idx, ok := cache[key]; ok {
			asset = a.assets[idx]
		} else {
			if strings.HasPrefix(target, "mdoc-object:") {
				id := strings.TrimPrefix(target, "mdoc-object:")
				capability, ok := d.handles[id]
				if !ok {
					failure = fail("missing_asset", "image object %s is missing", id)
					return
				}
				b, mime, e := m.s.fetchImage(ctx, capability, m.c.Limits.ImageBytes, m.transfer)
				if e != nil {
					failure = e
					return
				}
				ev := d.evidence
				ev.Source.Kind = "embedded"
				ev.Source.ObjectID = id
				ev.MIME = mime
				asset = capturedAsset{evidence: ev, bytes: b, mime: mime, digest: digest(b), objectID: key}
			} else {
				m.s.sourceAccess(target)
				ref, e := m.s.resolveAssetReference(ctx, target, d.evidence, cat)
				if e != nil {
					failure = e
					return
				}
				asset, err = m.s.captureBlob(ctx, ref, m.c, m.transfer, true)
				if err != nil {
					failure = err
					return
				}
				asset.objectID = key
				asset.path = cat.linkPaths[ref.FileID]
			}
			if len(a.assets) >= m.c.Limits.Assets {
				failure = fail("limit_exceeded", "image source count exceeded")
				return
			}
			cache[key] = len(a.assets)
			a.assets = append(a.assets, asset)
		}
		parts := arr(n["c"])
		caption := ""
		if t := arr(parts[2]); len(t) > 1 {
			caption = str(t[1])
		}
		a.occurrences = append(a.occurrences, Occurrence{DocumentID: d.evidence.Source.FileID, SourceDigest: d.evidence.SourceDigest, Position: position, ObjectID: key, Alt: textOf(parts[1]), Caption: caption})
		position++
	})
	if failure != nil {
		if typed, ok := failure.(*Error); ok {
			failure = &Error{Code: typed.Code, Message: typed.Message + " (document " + d.evidence.Source.FileID + ", image occurrence " + strconv.Itoa(position+1) + ")"}
		}
		return materializedAssets{}, failure
	}
	return a, nil
}
func (s *Service) resolveAssetReference(ctx context.Context, target string, source Evidence, cat *catalog) (SourceRef, error) {
	if ref, e := ParseSource(target); e == nil {
		u, _ := url.Parse(target)
		if ref.TabID != "" || u.Fragment != "" {
			return SourceRef{}, fail("unsupported_image", "image selectors are unsupported")
		}
		return ref, nil
	}
	id, e := s.resolveRelative(ctx, target, source, cat)
	if e != nil {
		return SourceRef{}, e
	}
	if _, ok := cat.linkPaths[id]; !ok {
		return SourceRef{}, fail("missing_asset", "relative image is outside declared asset roots")
	}
	return SourceRef{FileID: id, Kind: "drive"}, nil
}
func (s *Service) captureBlob(ctx context.Context, ref SourceRef, c Config, b *budget, asImage bool) (capturedAsset, error) {
	if e := s.requireProvider(ctx); e != nil {
		return capturedAsset{}, e
	}
	start := s.now().UTC()
	f, e := s.provider.File(ctx, ref.FileID)
	if e != nil {
		return capturedAsset{}, safeProviderError(e)
	}
	if e = validateFile(f, ref.FileID); e != nil {
		return capturedAsset{}, e
	}
	if strings.HasPrefix(f.MimeType, "application/vnd.google-apps.") {
		return capturedAsset{}, fail("unsupported_image", "native files cannot be copied as asset bytes")
	}
	max := min(c.Limits.TotalBytes-b.used, b.max-b.used)
	if asImage || strings.HasPrefix(f.MimeType, "image/") {
		max = min(max, c.Limits.ImageBytes)
	}
	if f.Size > max {
		return capturedAsset{}, fail("limit_exceeded", "asset exceeds configured limit")
	}
	raw, e := s.provider.Download(ctx, ref.FileID, max)
	if e != nil {
		return capturedAsset{}, safeProviderError(e)
	}
	if int64(len(raw)) > max {
		return capturedAsset{}, fail("limit_exceeded", "asset exceeds configured limit")
	}
	if e = b.add(int64(len(raw))); e != nil {
		return capturedAsset{}, e
	}
	mimeType := f.MimeType
	if asImage || strings.HasPrefix(mimeType, "image/") {
		mimeType, e = validateImage(raw, mimeType)
		if e != nil {
			return capturedAsset{}, e
		}
	}
	ev := fileEvidence(f, ref)
	ev.CapturedFrom = start
	after, e := s.provider.File(ctx, ref.FileID)
	if e != nil {
		return capturedAsset{}, safeProviderError(e)
	}
	if e = validateFile(after, ref.FileID); e != nil {
		return capturedAsset{}, e
	}
	if !sameEvidence(ev, fileEvidence(after, ref)) {
		return capturedAsset{}, fail("unstable_source", "asset changed during download")
	}
	ev.CapturedTo = s.now().UTC()
	ev.SourceDigest = digest(raw)
	return capturedAsset{evidence: ev, bytes: raw, mime: mimeType, digest: digest(raw)}, nil
}
func (s *Service) stableBlob(ctx context.Context, ref SourceRef, c Config, b *budget) (capturedAsset, error) {
	var e error
	for range c.Limits.Attempts {
		var a capturedAsset
		a, e = s.captureBlob(ctx, ref, c, b, false)
		if e == nil {
			return a, nil
		}
		if !retryCapture(e) {
			break
		}
	}
	return capturedAsset{}, e
}
func imageURL(value string) error {
	u, e := url.Parse(value)
	if e != nil || u.Scheme != "https" || u.User != nil || u.Port() != "" || u.Fragment != "" {
		return fail("unsupported_image", "invalid provider image capability")
	}
	h := strings.ToLower(u.Hostname())
	if !(h == "googleusercontent.com" || strings.HasSuffix(h, ".googleusercontent.com")) {
		return fail("unsupported_image", "image host is outside the provider allowlist")
	}
	return nil
}
func publicImageTransport() *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.Proxy = nil
	d := &net.Dialer{Timeout: 15 * time.Second}
	t.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, e := net.SplitHostPort(address)
		if e != nil {
			return nil, e
		}
		ips, e := net.DefaultResolver.LookupIPAddr(ctx, host)
		if e != nil {
			return nil, e
		}
		for _, a := range ips {
			ip := a.IP
			if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
				return nil, fail("unsupported_image", "nonpublic image host rejected")
			}
		}
		if len(ips) == 0 {
			return nil, fail("transport_failure", "image host unavailable")
		}
		return d.DialContext(ctx, network, net.JoinHostPort(ips[0].IP.String(), port))
	}
	return t
}
func (s *Service) fetchImage(ctx context.Context, capability string, max int64, b *budget) ([]byte, string, error) {
	max = min(max, b.max-b.used)
	if e := imageURL(capability); e != nil {
		return nil, "", e
	}
	client := http.Client{Transport: publicImageTransport(), Timeout: 30 * time.Second}
	if s.options.ImageHTTPClient != nil {
		client = *s.options.ImageHTTPClient
		if _, oauth := client.Transport.(*oauth2.Transport); oauth {
			return nil, "", fail("invalid_config", "image transport cannot carry OAuth credentials")
		}
		client.Jar = nil
	}
	if client.Timeout <= 0 || client.Timeout > 30*time.Second {
		client.Timeout = 30 * time.Second
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	r, _ := http.NewRequestWithContext(ctx, "GET", capability, nil)
	resp, e := client.Do(r)
	if e != nil {
		if ctx.Err() != nil {
			return nil, "", ctx.Err()
		}
		return nil, "", fail("transport_failure", "image download failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode == 401 || resp.StatusCode == 403 || resp.StatusCode == 404 {
		return nil, "", fail("image_unavailable", "native image capability expired or is unavailable")
	}
	if resp.StatusCode != 200 {
		return nil, "", fail("transport_failure", "native image download failed")
	}
	raw, e := readBounded(resp.Body, max)
	if e != nil {
		return nil, "", fail("limit_exceeded", "image download exceeded limits or failed")
	}
	if e = b.add(int64(len(raw))); e != nil {
		return nil, "", e
	}
	m, e := validateImage(raw, resp.Header.Get("Content-Type"))
	return raw, m, e
}

// imageIdentity binds repeated uses within one captured model without retaining
// retrieval URLs. Markdown identities use the first AST occurrence position;
// native objects already have provider-assigned identities.
func imageIdentity(target string, position int, identities map[string]string) string {
	if id, ok := identities[target]; ok {
		return id
	}
	id := "mdoc-image:" + strconv.Itoa(position)
	if strings.HasPrefix(target, "mdoc-object:") {
		id = target
	}
	identities[target] = id
	return id
}
func validateImage(b []byte, declared string) (string, error) {
	m, _, e := mime.ParseMediaType(declared)
	if e != nil {
		return "", fail("unsupported_image", "missing or invalid image MIME")
	}
	if m == "image/svg+xml" {
		if e = validateSVG(b); e != nil {
			return "", e
		}
		return m, nil
	}
	want := map[string]string{"image/png": "png", "image/jpeg": "jpeg", "image/gif": "gif"}[m]
	if want == "" {
		return "", fail("unsupported_image", "unsupported image MIME")
	}
	cfg, kind, e := image.DecodeConfig(bytes.NewReader(b))
	if e != nil || kind != want || cfg.Width < 1 || cfg.Height < 1 || cfg.Width > 16384 || cfg.Height > 16384 || int64(cfg.Width)*int64(cfg.Height) > 40_000_000 {
		return "", fail("unsupported_image", "image MIME, dimensions or bytes are invalid")
	}
	if _, format, e := image.Decode(bytes.NewReader(b)); e != nil || format != want {
		return "", fail("unsupported_image", "image cannot be decoded")
	}
	return m, nil
}
func imageExtension(m string) string {
	return map[string]string{"image/png": ".png", "image/jpeg": ".jpg", "image/gif": ".gif", "image/svg+xml": ".svg"}[m]
}
func imagePathOK(p, m string) bool {
	ext := strings.ToLower(path.Ext(p))
	return ext == imageExtension(m) || (m == "image/jpeg" && ext == ".jpeg")
}
func validateSVG(b []byte) error {
	d := xml.NewDecoder(bytes.NewReader(b))
	depth := 0
	root := false
	allowed := map[string]bool{}
	for _, s := range strings.Fields("svg g defs path rect circle ellipse line polyline polygon text tspan title desc use symbol clipPath mask linearGradient radialGradient stop pattern marker") {
		allowed[s] = true
	}
	for {
		tok, e := d.Token()
		if e == io.EOF {
			break
		}
		if e != nil {
			return fail("unsupported_image", "invalid SVG XML")
		}
		switch x := tok.(type) {
		case xml.Directive:
			return fail("unsupported_image", "SVG directives are unsupported")
		case xml.ProcInst:
			if x.Target != "xml" || root {
				return fail("unsupported_image", "SVG processing instruction unsupported")
			}
		case xml.StartElement:
			depth++
			if depth > 128 || !allowed[x.Name.Local] || x.Name.Space != "" && x.Name.Space != "http://www.w3.org/2000/svg" {
				return fail("unsupported_image", "SVG contains unsupported active content")
			}
			if root && depth == 1 {
				return fail("unsupported_image", "multiple SVG roots")
			}
			if !root {
				if x.Name.Local != "svg" {
					return fail("unsupported_image", "missing SVG root")
				}
				root = true
				dims := map[string]string{}
				for _, a := range x.Attr {
					dims[a.Name.Local] = a.Value
				}
				values := strings.Fields(strings.ReplaceAll(dims["viewBox"], ",", " "))
				if len(values) == 4 {
					for _, v := range values[2:] {
						n, e := strconv.ParseFloat(v, 64)
						if e != nil || math.IsNaN(n) || math.IsInf(n, 0) || n <= 0 || n > 16384 {
							return fail("unsupported_image", "invalid SVG dimensions")
						}
					}
				} else {
					for _, k := range []string{"width", "height"} {
						n, e := strconv.ParseFloat(strings.TrimSuffix(dims[k], "px"), 64)
						if e != nil || math.IsNaN(n) || math.IsInf(n, 0) || n <= 0 || n > 16384 {
							return fail("unsupported_image", "SVG needs bounded dimensions")
						}
					}
				}
			}
			for _, a := range x.Attr {
				v := strings.ToLower(strings.TrimSpace(a.Value))
				if a.Name.Local == "base" || strings.ContainsAny(v, `\@`) || strings.Contains(v, "/*") {
					return fail("unsupported_image", "SVG external base or escaped CSS is unsupported")
				}
				if strings.HasPrefix(strings.ToLower(a.Name.Local), "on") || a.Name.Local == "href" && !strings.HasPrefix(v, "#") || strings.Contains(v, "@import") || strings.Contains(v, "javascript:") || strings.Contains(v, "data:") {
					return fail("unsupported_image", "SVG external or active content is unsupported")
				}
				for _, hit := range regexp.MustCompile(`url\s*\(([^)]*)\)`).FindAllStringSubmatch(v, -1) {
					if !strings.HasPrefix(strings.Trim(hit[1], " \"'"), "#") {
						return fail("unsupported_image", "SVG external resource unsupported")
					}
				}
			}
		case xml.EndElement:
			depth--
			if depth < 0 {
				return fail("unsupported_image", "invalid SVG structure")
			}
		}
	}
	if !root || depth != 0 {
		return fail("unsupported_image", "invalid SVG structure")
	}
	return nil
}
func assetPath(c Config, docPath string, a capturedAsset) (string, error) {
	if a.path != "" {
		if strings.HasPrefix(a.mime, "image/") && !imagePathOK(a.path, a.mime) {
			return "", fail("path_conflict", "asset extension does not match image MIME")
		}
		return a.path, safePath(a.path)
	}
	slug := "image"
	if a.evidence.Source.Kind != "embedded" {
		slug = name(c, strings.TrimSuffix(a.evidence.Name, path.Ext(a.evidence.Name)))
		if slug == "" {
			slug = "image"
		}
	}
	dir := path.Dir(docPath)
	if dir == "." {
		dir = ""
	}
	folder := expand(c.Layout.Assets, map[string]string{"document_dir": dir, "document_stem": strings.TrimSuffix(path.Base(docPath), path.Ext(docPath))})
	folder = strings.TrimPrefix(folder, "/")
	base := expand(c.Naming.AssetPattern, map[string]string{"slug": slug, "digest8": a.digest[:8], "digest": a.digest}) + imageExtension(a.mime)
	p := path.Join(folder, base)
	return p, safePath(p)
}

// coalesceAsset permits sharing an exact path only after full byte identity checks.
// Case-spelling and file/directory conflicts are checked by the reservation set.
func coalesceAsset(existing, incoming capturedAsset) error {
	if existing.mime != incoming.mime || len(existing.bytes) != len(incoming.bytes) || existing.digest != incoming.digest || !bytes.Equal(existing.bytes, incoming.bytes) {
		return fail("path_conflict", "different asset bytes claim %s", incoming.path)
	}
	return nil
}
