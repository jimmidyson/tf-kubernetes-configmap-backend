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

SHELL := /bin/bash -o pipefail -c

include ./make/help.mk
include ./make/platform.mk

OUTPUT_DIR := _output/local/bin
BINARY_NAMES := $(subst cmd/,,$(wildcard cmd/*))

ROOT_PKG := $(shell go list -m)
ALL_PKGS := $(shell go list ./... | grep -v hack)
UNIT_TEST_PKGS := $(shell go list ./... | grep -Ev '$(ROOT_PKG)/(test/)?e2e')
ALL_SRC_FILES := $(shell find . ! -path '*/.git/*' -name '*.go')
NON_GENERATED_SRC_FILES := $(shell find . -name '*.go' | grep -v generated)
export GOPATH := $(shell go env GOPATH)

# Use the native vendor/ dependency system
export GO111MODULE := on
export CGO_ENABLED := 0

DOCKER_IMAGE_TAG ?= latest
DOCKERFILE_DEV_SHA := $(shell $(SHA1) Dockerfile.dev | awk '{ print $$1 }')

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
all: test binaries

## run the unit tests
.PHONY: test
test: .bin/go-junit-report
	@CGO_ENABLED=1 go test --race -v $(UNIT_TEST_PKGS) | tee >(.bin/go-junit-report > unit-test-report.xml)

## generate test coverage report in ./coverage.zip
.PHONY: test.coverage
test.coverage:
	@CGO_ENABLED=1 go test -cover -coverprofile=coverage.out $(UNIT_TEST_PKGS)
	@export COVERTMP=$$(mktemp -d) && \
		go tool cover -html=coverage.out -o $${COVERTMP}/index.html && \
		cd $${COVERTMP} && \
		zip $(CURDIR)/coverage.zip * && \
		cd $(CURDIR) && \
		rm -rf $${COVERTMP}

.bin/go-junit-report:
	@GO111MODULE=off GOPATH=/tmp go get -u github.com/jstemmer/go-junit-report
	@mkdir -p $(dir $@)
	@cp /tmp/bin/go-junit-report $@

PHONY: binaries
binaries: $(addprefix $(OUTPUT_DIR)/linux/amd64/,$(BINARY_NAMES))

$(OUTPUT_DIR)/linux/amd64/%: \
								$(ALL_SRC_FILES) \
								$(if $(value RUN_UPX),$(UPX_BINARY))
	$(call build_binary,$@,$(ROOT_PKG)/cmd/$(notdir $@))

.PHONY: docker.dev
docker.dev: docker.build.dev
	$(call run_docker_dev,$(WHAT))

.PHONY: docker.build.dev
docker.build.dev: .docker.build.dev.$(DOCKERFILE_DEV_SHA)

.docker.build.dev.$(DOCKERFILE_DEV_SHA): Dockerfile.dev
	@$(RM) .docker.build.dev.*
	@docker build \
			--build-arg=OS_PLATFORM=$(PLATFORM) \
			$(if $(filter-out darwin,$(PLATFORM)),--build-arg=DOCKER_GID=$(shell getent group docker 2> /dev/null | cut -d: -f3)) \
			-t mesosphere/tf-kubernetes-configmap-backend-dev:$(DOCKERFILE_DEV_SHA) \
			-f Dockerfile.dev .
	@touch $@

## build all docker images
.PHONY: docker.build
docker.build: $(addprefix docker.build.,$(BINARY_NAMES))

.PHONY: docker.build.%
docker.build.%: .docker.build.%.$(GIT_VERSION)
	@printf ''

.PRECIOUS: .docker.build.%.$(GIT_VERSION)
.docker.build.%.$(GIT_VERSION): Dockerfile.go-binary $(ALL_SRC_FILES) $(OUTPUT_DIR)/linux/amd64/%
	@echo Building Docker image: mesosphere/$*:$(GIT_VERSION)
	@$(RM) $(subst $(GIT_VERSION),*,$@)
	@sed 's/$${BINARY_NAME}/$*/g' Dockerfile.go-binary | docker build \
		-t mesosphere/$*:$(GIT_VERSION) \
		-f - .
	@touch $@
