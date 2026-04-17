// Package client wraps the Gitea SDK for both Gitea (source) and Forgejo
// (target). The REST shape under /api/v1/ is shared, so one client type
// suffices; the distinction is semantic.
package client

import (
	"crypto/tls"
	"fmt"
	"net/http"

	"code.gitea.io/sdk/gitea"

	"github.com/pacnpal/gitea2forgejo/internal/config"
)

// Kind tags whether this client targets the source (Gitea) or target (Forgejo).
type Kind string

const (
	KindSource Kind = "source"
	KindTarget Kind = "target"
)

type Client struct {
	*gitea.Client
	Kind Kind
	URL  string
}

// New constructs a client with the instance's admin token and TLS settings.
func New(inst *config.Instance, kind Kind) (*Client, error) {
	opts := []gitea.ClientOption{
		gitea.SetToken(inst.AdminToken),
	}
	if inst.InsecureTLS {
		opts = append(opts, gitea.SetHTTPClient(&http.Client{
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
			},
		}))
	}
	c, err := gitea.NewClient(inst.URL, opts...)
	if err != nil {
		return nil, fmt.Errorf("new %s client: %w", kind, err)
	}
	return &Client{Client: c, Kind: kind, URL: inst.URL}, nil
}
