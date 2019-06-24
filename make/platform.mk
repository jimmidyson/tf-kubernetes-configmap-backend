# detect what platform we're running in so we can use proper command flavors and
# download proper binaries (dep and kubectl).
OS := $(shell uname -s)
override RUN_UPX := $(subst false,,$(RUN_UPX))
ifeq ($(OS),Linux)
PLATFORM := linux
SHA1 := sha1sum
BASE64 := base64 -w 0
UPX_BINARY := .bin/upx
# sed in-place edit is different on Linux and OSX so create a variable to handle this.
SEDI := sed -i
SOURCE_DATE_CMD = date --date=@$(SOURCE_DATE_EPOCH) -u +'%Y-%m-%dT%H:%M:%SZ'
endif
ifeq ($(OS),Darwin)
PLATFORM := darwin
SHA1 := shasum -a1
BASE64 := base64
# On OSX sed's -i flag requires an argument separated by a space. This sets the argument to the empty string to
# effectively overwrite the existing file, duplicating sed -i behaviour on Linux.
SEDI := sed -i ''
SOURCE_DATE_CMD = date -r $(SOURCE_DATE_EPOCH) -u +'%Y-%m-%dT%H:%M:%SZ'
ifneq (,$(RUN_UPX))
UPX_BINARY := $(shell command -v upx || command -v upx-ucl)
ifeq (,$(UPX_BINARY))
$(error "UPX packing has been requested by setting RUN_UPX=$(RUN_UPX) but the upx binary cannot be found - please install with `brew install upx`")
endif
endif
endif
