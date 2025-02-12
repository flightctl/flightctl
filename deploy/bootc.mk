build-image:
	sudo podman build -f deploy/Containerfile.embedded -t quadlet-bootc-image:latest --network=host

build-qcow:
	sudo podman run \
		--rm \
		-it \
		--privileged \
		--pull=newer \
		--security-opt label=type:unconfined_t \
		-v ./deploy/bootc/config.toml:/config.toml:ro \
		-v ./deploy/bootc/output:/output \
		-v /var/lib/containers/storage:/var/lib/containers/storage \
		quay.io/centos-bootc/bootc-image-builder:latest \
		--type qcow2 \
		--local \
		localhost/quadlet-bootc-image:latest

# Press ctrl + A then X to exit qemu
qemu:
	qemu-system-x86_64 \
		-M accel=kvm \
		-cpu host \
		-smp 2 \
		-m 4096 \
		-bios /usr/share/OVMF/OVMF_CODE.fd \
		-nographic \
		-snapshot deploy/bootc/output/qcow2/disk.qcow2 \
    	-netdev user,id=net0,hostfwd=tcp::8080-:8080,hostfwd=tcp::3443-:3443 \
    	-device e1000,netdev=net0

virt:
	sudo virt-install \
		--name bootc \
		--cpu host \
		--vcpus 4 \
		--memory 4096 \
		--import --disk ./deploy/bootc/output/qcow2/disk.qcow2,format=qcow2 \
		--os-variant centos-stream9

build-installer:
	go build -o deploy/podman/installer/bin/flightctl-installer deploy/podman/installer/flightctl-installer.go

test-template:
	rm -rf deploy/podman/installer/test-systemd-output
	rm -rf deploy/podman/installer/test-config-output
	./deploy/podman/installer/bin/flightctl-installer \
		-c deploy/podman \
		-s deploy/podman/installer/test-systemd-output \
		-u deploy/podman/installer/test-config-output

deploy-new-quadlets:
	./deploy/podman/installer/bin/flightctl-installer \
		-c deploy/podman \
		-s ~/.config/containers/systemd \
		-u ~/.config/flightctl
