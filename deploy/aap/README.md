# Running FCTL as an AAP Gateway service

In order to run FCTL as AAP Gateway service, you need to:
 
 - start FCTL 
 - start AAP Gateway
 - install `ansible.platform` collection

Then run the `register_fctl.yml` playbook

Example:
```console
$ ansible-playbook register_fctl.yml -e "gateway_username=admin gateway_password=adminPass gateway_hostname=https://localhost:8000 fctl_address=https://api.foo.com fctl_port=3443"
```

After successfull registration, the FlightControl API will be accessible at `<gateway_hostname>/api/fctl`
