## Run in VM
```
/usr/bin/flightctl-installer -s ~/.config/containers/systemd -u ~/.config/flightctl
systemctl --user daemon-reload
systemctl --user start flightctl.slice
```

## Access via host after deployment

If running `make qemu`
### CLI
```
bin/flightctl login https://localhost:3443 -k
```

### UI
Visit http://localhost:8080
