# Podman / Quadlets deployment
Note: This is very much WIP, this README currently serving as more of a notes dump until further refinement.

Booted images can be accessed with `flightctl` / `flightctlpass` creds.

## Embedded Install
1. Run
```
make build-embedded-image
make build-qcow
make qemu
```
2. Login to VM
3. Containers should be running without user interventions and after a short while visible with `podman ps`

## Non-Embedded install
1. Run
```
make build-installer-image
make build-qcow
make qemu
```
2. Login to VM
3. Run the following in the VM to template and install the quadlets files
```
/usr/bin/flightctl-installer -s ~/.config/containers/systemd -u ~/.config/flightctl
systemctl --user daemon-reload
systemctl --user start flightctl.slice
```
4. Containers should start and be visible with `podman ps`

## Access via host after deployment

If running `make qemu` ports 3443 and 8080 are currently forwarded to the host
### CLI
```
bin/flightctl login https://localhost:3443 -k
```

### UI
Visit http://localhost:8080
