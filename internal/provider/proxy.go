package provider

import (
	"net/http"
	"net/url"
	"os"
	"sync"
)

// proxyClient returns an *http.Client that honors per-connection metadata.proxy_url
// (preferred) or HTTPS_PROXY-style env (dev fallback, disabled by DISALLOW_ENV_PROXY=1).
// One client is cached per resolved proxy URL via the supplied mutex + map; the
// caller owns both. Both ClaudeSub and CodexSub share this helper rather than
// each holding their own copy of the resolve+cache logic.
func proxyClient(mu *sync.Mutex, cache map[string]*http.Client, metadata map[string]any) *http.Client {
	proxy := stringFrom(metadata, "proxy_url")
	if proxy == "" && os.Getenv("DISALLOW_ENV_PROXY") != "1" {
		for _, k := range []string{"HTTPS_PROXY", "https_proxy", "ALL_PROXY", "all_proxy"} {
			if v := os.Getenv(k); v != "" {
				proxy = v
				break
			}
		}
	}
	if proxy == "" {
		return http.DefaultClient
	}
	mu.Lock()
	defer mu.Unlock()
	if cli, ok := cache[proxy]; ok {
		return cli
	}
	u, err := url.Parse(proxy)
	if err != nil {
		return http.DefaultClient
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.Proxy = http.ProxyURL(u)
	cli := &http.Client{Transport: tr}
	cache[proxy] = cli
	return cli
}
