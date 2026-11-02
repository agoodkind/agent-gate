# Select the reviewed analyzer only when the caller has not selected another tool.
_AGATE_ANALYZER_MODULE := goodkind.io/go-makefile/staticcheck
_AGATE_ANALYZER_VERSION := v0.1.1-0.20261102070521-03318cdb2aa5
_AGATE_ANALYZER_SUM := h1:NMUL7i0K96pxZi8MV45G7O6PSW3pj8vS+whM2oatuNg=
_AGATE_ANALYZER_INSTALL := $(_AGATE_ANALYZER_MODULE)/cmd/staticcheck-extra@$(_AGATE_ANALYZER_VERSION)
_AGATE_ANALYZER_BIN := $(CURDIR)/.make/verified-tools/staticcheck-extra/$(_AGATE_ANALYZER_VERSION)/staticcheck-extra
_AGATE_ANALYZER_SOURCE := $(dir $(abspath $(lastword $(MAKEFILE_LIST))))prepare-analyzer
_AGATE_ANALYZER_BIN_ORIGIN := $(origin STATICCHECK_EXTRA_BIN)
_AGATE_ANALYZER_INSTALL_ORIGIN := $(origin STATICCHECK_EXTRA_INSTALL)
_AGATE_ANALYZER_INHERITED := $(and $(filter environment,$(_AGATE_ANALYZER_BIN_ORIGIN)),$(filter $(_AGATE_ANALYZER_BIN),$(STATICCHECK_EXTRA_BIN)),$(filter $(_AGATE_ANALYZER_INSTALL),$(_AGATE_ANALYZER_DEFAULT)))
_AGATE_ANALYZER_INSTALL_INHERITED := $(and $(_AGATE_ANALYZER_INHERITED),$(filter environment,$(_AGATE_ANALYZER_INSTALL_ORIGIN)),$(filter $(_AGATE_ANALYZER_INSTALL),$(STATICCHECK_EXTRA_INSTALL)))

STATICCHECK_EXTRA_INSTALL ?= $(_AGATE_ANALYZER_INSTALL)

include bootstrap.mk

# Discard only inherited project defaults when a recursive caller selects source
# or an install specification. Explicit executable selections keep their priority.
ifneq ($(_AGATE_ANALYZER_INHERITED),)
ifneq ($(strip $(STATICCHECK_EXTRA_BUILD_REPO)),)
undefine STATICCHECK_EXTRA_BIN
else ifeq ($(_AGATE_ANALYZER_INSTALL_INHERITED),)
undefine STATICCHECK_EXTRA_BIN
endif
endif

ifeq ($(strip $(STATICCHECK_EXTRA_BUILD_REPO)),)
ifneq ($(or $(filter undefined,$(_AGATE_ANALYZER_BIN_ORIGIN)),$(_AGATE_ANALYZER_INHERITED)),)
ifneq ($(or $(filter undefined,$(_AGATE_ANALYZER_INSTALL_ORIGIN)),$(_AGATE_ANALYZER_INSTALL_INHERITED)),)
STATICCHECK_EXTRA_BIN := $(_AGATE_ANALYZER_BIN)
export STATICCHECK_EXTRA_BIN
export _AGATE_ANALYZER_DEFAULT := $(_AGATE_ANALYZER_INSTALL)

.PHONY: prepare-reviewed-analyzer
prepare-reviewed-analyzer:
	@GOOS=$(shell go env GOHOSTOS) GOARCH=$(shell go env GOHOSTARCH) GOWORK=off go run "$(_AGATE_ANALYZER_SOURCE)"/main.go -root "$(CURDIR)" -module "$(_AGATE_ANALYZER_MODULE)" -version "$(_AGATE_ANALYZER_VERSION)" -sum "$(_AGATE_ANALYZER_SUM)"

build install build-gate build-check staticcheck-extra staticcheck-extra-bin \
lint-files lint-diff go-mk-notice \
staticcheck-extra-baseline staticcheck-extra-baseline-prune-fixed \
staticcheck-extra-baseline-accept-new baseline baseline-prune-fixed \
baseline-accept-new: | prepare-reviewed-analyzer
endif
endif
endif
