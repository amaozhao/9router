// Package redisx wraps github.com/redis/go-redis/v9 for shared use.
package redisx

import (
	"context"
	"crypto/tls"
	"net/url"
	"strconv"
	"sync"

	"github.com/redis/go-redis/v9"
)

var (
	client *redis.Client
	once   sync.Once
)

// Init parses a Node-style URL (redis://user:pw@host:port/db) and dials.
// Idempotent.
func Init(rawURL string) error {
	var err error
	once.Do(func() {
		opts, parseErr := parseURL(rawURL)
		if parseErr != nil {
			err = parseErr
			return
		}
		client = redis.NewClient(opts)
	})
	return err
}

// C returns the global client; panics if Init was not called.
func C() *redis.Client {
	if client == nil {
		panic("redisx.Init has not been called")
	}
	return client
}

// Close releases the connection pool.
func Close() {
	if client != nil {
		_ = client.Close()
		client = nil
	}
}

// Ping returns the PONG from server or an error.
func Ping(ctx context.Context) (string, error) {
	return C().Ping(ctx).Result()
}

func parseURL(raw string) (*redis.Options, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, err
	}
	opts := &redis.Options{Addr: u.Host}
	if u.User != nil {
		opts.Username = u.User.Username()
		if pw, ok := u.User.Password(); ok {
			opts.Password = pw
		}
	}
	if u.Path != "" && len(u.Path) > 1 {
		if n, err := strconv.Atoi(u.Path[1:]); err == nil {
			opts.DB = n
		}
	}
	if u.Scheme == "rediss" {
		opts.TLSConfig = &tls.Config{ServerName: u.Hostname()}
	}
	return opts, nil
}
