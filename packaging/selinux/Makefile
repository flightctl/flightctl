MODULES?=${TARGET:=.pp.bz2}
TARGET ?= flightctl_agent
SHAREDIR ?= /usr/share

all: ${MODULES}

# Compress .pp file to .pp.bz2
%.pp.bz2: %.pp
	rm -f $@ || true
	@echo Compressing $^ -\> $@
	bzip2 -9 $^

# Build .pp from .te and .fc using SELinux development Makefile
%.pp: %.te %.fc
	make -f ${SHAREDIR}/selinux/devel/Makefile $@

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
