# build_binary builds a go binary, attempting to be as reproducible as possible by trimming
# paths to ensure portabilty and using SOURCE_DATE_EPOCH as defined in
# https://reproducible-builds.org/docs/source-date-epoch/.
# This also strips the buildid from the golang binary if GOOS is linux - after the above steps, this
# is the only thing that differs between builds of same content on different hosts/platforms.
define build_binary
	@echo "Building $(1)"
	@export GOOS=$(shell basename $$(dirname $$(dirname $(1)))) \
		GOARCH=$(shell basename $$(dirname $(1))) \
		GOROOT_FINAL=/go \
		&& \
		go build -v \
			-ldflags " \
				-s -w \
				-extldflags \"-static\" \
				-X $(ROOT_PKG)/pkg/version.gitVersion=$(GIT_VERSION) \
				-X $(ROOT_PKG)/pkg/version.gitCommit=$(GIT_COMMIT) \
				-X $(ROOT_PKG)/pkg/version.gitTreeState=$(GIT_TREE_STATE) \
				-X '$(ROOT_PKG)/pkg/version.buildDate=$(SOURCE_DATE_FORMATTED))' \
			" \
			-gcflags all=-trimpath=$(GOPATH)/src \
 			-asmflags all=-trimpath=$(GOPATH)/src \
			-o $(1) \
			$(2) \
		&& \
		([ "$${GOOS}" != linux ] || $(MAKE) docker.dev WHAT="strip --remove-section .note.go.buildid $(1)")

	@if [ -n "$(RUN_UPX)" ]; then \
		$(UPX_BINARY) -q --best $(1); \
	fi
endef
