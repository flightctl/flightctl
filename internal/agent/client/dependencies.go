package client

// CLIClients provides access to CLI-based clients for container and Kubernetes operations.
type CLIClients interface {
	Podman() *Podman
	Skopeo() *Skopeo
	Kube() *Kube
	Helm() *Helm
	CRI() *CRI
}

type cliClients struct {
	podman *Podman
	skopeo *Skopeo
	kube   *Kube
	helm   *Helm
	cri    *CRI
}

// CLIClientsOption is a functional option for configuring CLIClients.
type CLIClientsOption func(*cliClients)

// WithPodmanClient sets the Podman client.
func WithPodmanClient(p *Podman) CLIClientsOption {
	return func(c *cliClients) { c.podman = p }
}

// WithSkopeoClient sets the Skopeo client.
func WithSkopeoClient(s *Skopeo) CLIClientsOption {
	return func(c *cliClients) { c.skopeo = s }
}

// WithKubeClient sets the Kubernetes CLI client.
func WithKubeClient(k *Kube) CLIClientsOption {
	return func(c *cliClients) { c.kube = k }
}

// WithHelmClient sets the Helm client.
func WithHelmClient(h *Helm) CLIClientsOption {
	return func(c *cliClients) { c.helm = h }
}

// WithCRIClient sets the CRI client.
func WithCRIClient(cri *CRI) CLIClientsOption {
	return func(c *cliClients) { c.cri = cri }
}

// NewCLIClients creates a new CLIClients instance with the provided options.
func NewCLIClients(opts ...CLIClientsOption) CLIClients {
	c := &cliClients{}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

func (c *cliClients) Podman() *Podman {
	return c.podman
}

func (c *cliClients) Skopeo() *Skopeo {
	return c.skopeo
}

func (c *cliClients) Kube() *Kube {
	return c.kube
}

func (c *cliClients) Helm() *Helm {
	return c.helm
}

func (c *cliClients) CRI() *CRI {
	return c.cri
}
