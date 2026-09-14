package transport

import (
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// cookieJar is a minimal in-memory jar that keeps the Perplexity session
// semantics the reference client needs: host-only cookies plus reassembly of
// chunked cookies (__Secure-next-auth.session-token.0, .1, ... joined in
// numeric order). http.CookieJar's interface has no way to read values back,
// so internal/transport uses this jar directly.
type cookieJar struct {
	mu      sync.Mutex
	host    string
	cookies map[string]string
}

func newCookieJar(host string) *cookieJar {
	return &cookieJar{host: host, cookies: make(map[string]string)}
}

func (j *cookieJar) SetCookies(u *url.URL, cookies []*http.Cookie) {
	j.mu.Lock()
	defer j.mu.Unlock()
	for _, c := range cookies {
		if c.MaxAge < 0 {
			delete(j.cookies, c.Name)
			continue
		}
		j.cookies[c.Name] = c.Value
	}
}

func (j *cookieJar) Cookies(u *url.URL) []*http.Cookie {
	j.mu.Lock()
	defer j.mu.Unlock()
	var out []*http.Cookie
	for name, value := range j.cookies {
		out = append(out, &http.Cookie{Name: name, Value: value, Path: "/", Secure: true, HttpOnly: true})
	}
	return out
}

// get returns the cookie value for name, reassembling chunked cookies when
// the whole cookie is absent. Empty string when not found.
func (j *cookieJar) get(u *url.URL, name string) string {
	j.mu.Lock()
	defer j.mu.Unlock()
	if v, ok := j.cookies[name]; ok {
		return v
	}
	prefix := name + "."
	type chunk struct {
		idx int
		val string
	}
	var chunks []chunk
	for k, v := range j.cookies {
		if !strings.HasPrefix(k, prefix) {
			continue
		}
		idx, err := strconv.Atoi(strings.TrimPrefix(k, prefix))
		if err != nil {
			continue
		}
		chunks = append(chunks, chunk{idx, v})
	}
	if chunks == nil {
		return ""
	}
	sort.Slice(chunks, func(a, b int) bool { return chunks[a].idx < chunks[b].idx })
	var b strings.Builder
	for _, c := range chunks {
		b.WriteString(c.val)
	}
	return b.String()
}
