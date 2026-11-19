package consts

const (
	ContainersAuthRelativePath   = ".config/containers/auth.json"
	RootContainersAuthPath       = "/root/" + ContainersAuthRelativePath
	HelmRegistryConfigPath       = "/root/.config/helm/registry/config.json"
	HelmRepositoryConfigPath     = "/root/.config/helm/repositories.yaml"
	OSTreeAuthPath               = "/etc/ostree/auth.json"
	ContainersRegistriesConfPath = "/etc/containers/registries.conf"
	ContainersRegistriesConfDir  = "/etc/containers/registries.conf.d"
	ContainersCertsDir           = "/etc/containers/certs.d"
)
