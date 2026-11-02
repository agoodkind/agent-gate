-include config.mk
BINARY := fixture
CMD := .
GO_MK_MODULES := go-build.mk
include $(AGATE_ANALYZER_PROJECT)/scripts/prepare-analyzer.mk

.PHONY: recursive-analyzer selection
recursive-analyzer:
	$(MAKE) staticcheck-extra-bin

selection:
	@printf '%s\n' 'BIN=$(STATICCHECK_EXTRA_BIN)' 'INSTALL=$(STATICCHECK_EXTRA_INSTALL)' 'SOURCE=$(STATICCHECK_EXTRA_BUILD_REPO)' 'FLAGS=$(STATICCHECK_EXTRA_FLAGS)' 'TEST_FLAGS=$(STATICCHECK_EXTRA_TEST_FLAGS)' 'BASELINE=$(STATICCHECK_EXTRA_BASELINE)' 'GOOS=$(GOOS)' 'GOARCH=$(GOARCH)'
