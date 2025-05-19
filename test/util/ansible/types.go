package ansible

type AnsibleOutput struct {
	Result AnsibleResult `json:"result"`
}

type AnsibleResult struct {
	Changed bool     `json:"changed"`
	Devices []Device `json:"devices"`
}

type Device struct {
	Name        string            `json:"name"`
	Status      string            `json:"status"`
	Annotations map[string]string `json:"annotations"`
}
