package podman

import (
	"github.com/flightctl/flightctl/pkg/executer"
)

const (
	podmanCmd = "podman"
)

type Client struct {
	exec executer.Executer
}

func NewClient(exec executer.Executer) *Client {
	return &Client{
		exec: exec,
	}
}
