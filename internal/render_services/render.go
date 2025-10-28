package renderservices

import "fmt"

const configFile = "/etc/flightctl/service-config.yaml"

func RenderServicesConfig() error {
	fmt.Println("Rendering services config file")

	config, err := unmarshalServicesConfig(configFile)
	if err != nil {
		return err
	}
	fmt.Println(config.Global.BaseDomain)
	fmt.Println(config.Global.Auth.Type)
	fmt.Println(config.Global.Auth.InsecureSkipTlsVerify)
	return nil
}
