SERVICES_VM_NAME ?= flightctl-services-default
SERVICES_VM_RAM ?= 2048
SERVICES_VM_CPUS ?= 2
VSERVICES_VM_DISK = /var/lib/libvirt/images/$(SERVICES_VM_NAME).qcow2
SERVICES_VM_WAIT ?= 0

# Can set the image tag to a specific version by using the PACKIT_CURRENT_VERSION variable
# which builds the rpm with the specified version.
#
# Example cmd:
# PACKIT_CURRENT_VERSION=0.7.0 make services-container
services-container: rpm
	sudo podman build -t flightctl-services:latest -f test/scripts/services-images/Containerfile.services .

run-services-container: services-container
	sudo podman run -d --privileged --replace \
	--name flightctl-services \
	-p 8080:443 \
	-p 3443:3443 \
	-p 8090:8090 \
	localhost/flightctl-services:latest

clean-services-container:
	sudo podman stop flightctl-services || true
	sudo podman rm flightctl-services || true

bin/services-output/qcow2/disk.qcow2: services-container
	mkdir -p bin/services-output && \
	sudo podman run --rm -it --privileged --pull=newer \
		--security-opt label=type:unconfined_t \
		-v "${PWD}/bin/services-output":/output \
		-v /var/lib/containers/storage:/var/lib/containers/storage \
		quay.io/centos-bootc/bootc-image-builder:latest \
		--type qcow2 \
		localhost/flightctl-services:latest

services-vm: bin/services-output/qcow2/disk.qcow2 bin/services-output/init-data.iso
	@echo "Booting Services VM from $(VSERVICES_VM_DISK)"
	sudo cp bin/services-output/qcow2/disk.qcow2 $(VSERVICES_VM_DISK)
	sudo chown libvirt:libvirt $(VSERVICES_VM_DISK) 2>/dev/null || true
	sudo virt-install \
		--name $(SERVICES_VM_NAME) \
		--memory $(SERVICES_VM_RAM) \
		--vcpus $(SERVICES_VM_CPUS) \
		--import \
		--disk path=$(VSERVICES_VM_DISK),format=qcow2,bus=virtio \
		--disk path=bin/services-output/init-data.iso,device=cdrom \
		--os-variant centos-stream9 \
		--graphics none \
		--console pty,target_type=serial \
		--wait $(SERVICES_VM_WAIT) \
		--transient || true

bin/services-output/init-data.iso:
	@echo "Creating cloud-init ISO for Services VM"
	mkdir -p bin/services-output
	rm -f bin/services-output/init-data.iso
	genisoimage -output bin/services-output/init-data.iso -V cidata -r -J \
		test/scripts/services-images/user-data \
		test/scripts/services-images/meta-data

clean-services-vm:
	sudo virsh destroy $(SERVICES_VM_NAME) || true
	sudo rm -f $(VSERVICES_VM_DISK)

.PHONY: services-container run-services-container clean-services-container services-vm clean-services-vm services-vm-console
