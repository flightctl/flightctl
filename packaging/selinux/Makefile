TARGET ?= flightctl_agent
MODULES ?= ${TARGET:=.pp.bz2}
SHAREDIR ?= /usr/share
CI_RPM_IMAGE ?= quay.io/flightctl-tests/packit-builder:base

.PHONY: all clean man install-policy install

all: ${MODULES}

# Containerized build
ifeq ($(USE_CONTAINER),1)
%.pp.bz2: %.pp
	@echo "Compressing $^ -> $@ in container"
	podman run --rm -v $(CURDIR):/work:Z $(CI_RPM_IMAGE) bash -c "cd /work && bzip2 -9 -f $^"

%.pp: %.te %.fc
	@echo "Building $@ in container"
	podman run --rm -v $(CURDIR):/work:Z $(CI_RPM_IMAGE) bash -c "cd /work && make -f /usr/share/selinux/devel/Makefile $@"

else
%.pp.bz2: %.pp
	rm -f $@ || true
	@echo Compressing $^ -> $@
	bzip2 -9 -f $^

%.pp: %.te %.fc
	make -f ${SHAREDIR}/selinux/devel/Makefile $@

endif

clean:
	rm -f *~ *.tc *.pp *.mod *.pp.bz2
	rm -rf tmp *.tar.gz

man: install-policy
	sepolicy manpage --path . --domain ${TARGET}_t

install-policy: all
	sudo semodule -i ${TARGET}.pp.bz2

install: man
	sudo install -D -m 644 ${TARGET}.pp.bz2    ${DESTDIR}${SHAREDIR}/selinux/packages/
	sudo install -D -m 644 ${TARGET}_selinux.8 ${DESTDIR}${SHAREDIR}/man/man8/
