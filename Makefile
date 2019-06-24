#
# Copyright © 2019 Jimmi Dyson <jimmidyson@gmail.com>
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

SHELL := /bin/bash
.SHELLFLAGS = -o pipefail -c

OUTPUT_DIR := _output/local/bin

ROOT_PKG := $(shell go list -m)
ALL_PKGS := $(shell go list ./... | grep -v hack)
UNIT_TEST_PKGS := $(shell go list ./... | grep -Ev 'github.com/mesosphere/protoss/(test/)?e2e')
ALL_SRC_FILES := $(shell find . ! -path '*/vendor/*' -name '*.go')
NON_GENERATED_SRC_FILES := $(shell find . ! -path '*/vendor/*' ! -path '*/pkg/client/*' -name '*.go' | grep -v generated)
export GOPATH := $(shell go env GOPATH)

GOOS ?= $(shell go env GOOS)
GOARCH ?= $(shell go env GOARCH)
# Use the native vendor/ dependency system
export GO111MODULE := on
export CGO_ENABLED := 0

DOCKER_IMAGE_PREFIX ?= mesosphere/
DOCKER_IMAGE_TAG ?= latest

GIT_COMMIT := $(shell git rev-parse "HEAD^{commit}")
ifneq ($(shell git status --porcelain 2>/dev/null; echo $$?), 0)
	GIT_TREE_STATE := dirty
endif

GIT_TAG := $(shell git describe --tags --abbrev=7 "$(GIT_COMMIT)^{commit}" --exact-tags 2>/dev/null)
ifeq ($(GIT_TAG),)
	GIT_VERSION := $(shell git describe --tags --abbrev=7 --always --dirty)
else
	GIT_VERSION := $(GIT_TAG)$(if $(GIT_TREE_STATE),-$(GIT_TREE_STATE))
endif

SOURCE_DATE_EPOCH := $(shell git show -s --format=format:%ct HEAD)
SOURCE_DATE_FORMATTED := $(shell $(SOURCE_DATE_CMD))

LDFLAGS := -s -w -extldflags '-static'

include ./make/build.mk

.PHONY: all
all: format test vet

.PHONY: format
format: .goimports

.goimports: .bin/goimports $(NON_GENERATED_SRC_FILES)
	@echo "Running goimports"
	@.bin/goimports -local $(ROOT_PKG) -w $(NON_GENERATED_SRC_FILES)
	@touch $@

.bin/goimports:
	@GO111MODULE=off GOPATH=/tmp go get -u golang.org/x/tools/cmd/goimports
	@mkdir -p $(dir $@)
	@cp /tmp/bin/goimports $@

out/configmap-reload: out/configmap-reload-$(GOOS)-$(GOARCH)
	cp $(BUILD_DIR)/configmap-reload-$(GOOS)-$(GOARCH) $(BUILD_DIR)/configmap-reload

out/configmap-reload-linux-ppc64le: $(SRCFILES)
	GOARCH=ppc64le GOOS=linux go build --installsuffix cgo -ldflags="$(LDFLAGS)" -a -o $(BUILD_DIR)/configmap-reload-linux-ppc64le configmap-reload.go

out/configmap-reload-darwin-amd64: $(SRCFILES)
	GOARCH=amd64 GOOS=darwin go build --installsuffix cgo -ldflags="$(LDFLAGS)" -a -o $(BUILD_DIR)/configmap-reload-darwin-amd64 configmap-reload.go

out/configmap-reload-linux-amd64: $(SRCFILES)
	GOARCH=amd64 GOOS=linux go build --installsuffix cgo -ldflags="$(LDFLAGS)" -a -o $(BUILD_DIR)/configmap-reload-linux-amd64 configmap-reload.go

out/configmap-reload-windows-amd64.exe: $(SRCFILES)
	GOARCH=amd64 GOOS=windows go build --installsuffix cgo -ldflags="$(LDFLAGS)" -a -o $(BUILD_DIR)/configmap-reload-windows-amd64.exe configmap-reload.go

.PHONY: cross
cross: out/configmap-reload-linux-amd64 out/configmap-reload-darwin-amd64 out/configmap-reload-windows-amd64.exe

.PHONY: checksum
checksum:
	for f in out/configmap-reload-linux-amd64 out/configmap-reload-darwin-amd64 out/configmap-reload-windows-amd64.exe ; do \
		if [ -f "$${f}" ]; then \
			openssl sha256 "$${f}" | awk '{print $$2}' > "$${f}.sha256" ; \
		fi ; \
	done

.PHONY: clean
clean:
	rm -rf $(BUILD_DIR)

.PHONY: docker
docker: out/configmap-reload Dockerfile
	docker build -t $(DOCKER_IMAGE_NAME):$(DOCKER_IMAGE_TAG) .
