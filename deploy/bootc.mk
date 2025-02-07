build-image:
	sudo podman build -f deploy/Containerfile -t quadlet-bootc-image:latest

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
		-snapshot deploy/bootc/output/qcow2/disk.qcow2

virt:
	sudo virt-install \
		--name bootc \
		--cpu host \
		--vcpus 4 \
		--memory 4096 \
		--import --disk ./deploy/bootc/output/qcow2/disk.qcow2,format=qcow2 \
		--os-variant centos-stream9
