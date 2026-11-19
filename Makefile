LABEL := com.bondario.ambient-light-bridge
HOST := 127.0.0.1
PORT := 5890
REPO := $(CURDIR)
export REPO
PLIST := $(HOME)/Library/LaunchAgents/$(LABEL).plist
LOG_DIR := $(HOME)/Library/Logs/ambient-light-bridge
DOMAIN := gui/$(shell id -u)

.PHONY: build test run install uninstall restart status logs
.NOTPARALLEL:

build:
	mkdir -p bin
	@# Rename into place so the running executable is never overwritten.
	@tmp=$$(mktemp bin/ambient-light-bridge.XXXXXX) && \
		trap 'rm -f "$$tmp"' EXIT && \
		go build -o "$$tmp" . && \
		mv -f "$$tmp" bin/ambient-light-bridge

test:
	go vet ./...
	go test ./...

run: build
	bin/ambient-light-bridge -addr "$(HOST):$(PORT)"

install: test build
	@set -e; \
	tmp="$(PLIST).tmp"; \
	trap 'rm -f "$$tmp"' EXIT; \
	mkdir -p "$(LOG_DIR)" "$$(dirname "$(PLIST)")"; \
	cp "$$REPO/launchd/$(LABEL).plist.in" "$$tmp"; \
	plutil -insert Label -string "$(LABEL)" "$$tmp"; \
	plutil -insert ProgramArguments -array "$$tmp"; \
	plutil -insert ProgramArguments -string "$$REPO/bin/ambient-light-bridge" -append "$$tmp"; \
	plutil -insert ProgramArguments -string "-addr" -append "$$tmp"; \
	plutil -insert ProgramArguments -string "$(HOST):$(PORT)" -append "$$tmp"; \
	plutil -insert StandardOutPath -string "$(LOG_DIR)/bridge.log" "$$tmp"; \
	plutil -insert StandardErrorPath -string "$(LOG_DIR)/bridge.log" "$$tmp"; \
	plutil -lint "$$tmp"; \
	launchctl bootout "$(DOMAIN)/$(LABEL)" 2>/dev/null || true; \
	remaining=10; \
	while launchctl print "$(DOMAIN)/$(LABEL)" >/dev/null 2>&1; do \
		if [ "$$remaining" -eq 0 ]; then \
			echo "error: $(LABEL) did not unload" >&2; \
			exit 1; \
		fi; \
		sleep 1; \
		remaining=$$((remaining - 1)); \
	done; \
	if listeners=$$(lsof -nP -iTCP:$(PORT) -sTCP:LISTEN); then \
		printf 'error: port $(PORT) is in use\n%s\n' "$$listeners" >&2; \
		exit 1; \
	fi; \
	mv -f "$$tmp" "$(PLIST)"
	@if ! launchctl bootstrap "$(DOMAIN)" "$(PLIST)"; then \
		rm -f "$(PLIST)"; \
		exit 1; \
	fi

uninstall:
	@if ! launchctl bootout "$(DOMAIN)/$(LABEL)" 2>/dev/null && \
		launchctl print "$(DOMAIN)/$(LABEL)" >/dev/null 2>&1; then \
		echo "error: could not unload $(LABEL)" >&2; \
		exit 1; \
	fi
	rm -f "$(PLIST)"

restart:
	launchctl kickstart -k "$(DOMAIN)/$(LABEL)"

status:
	@launchctl print "$(DOMAIN)/$(LABEL)" | grep -E '^[[:space:]]*(state|pid|last exit code) =' || true
	@curl -s --max-time 3 http://$(HOST):$(PORT)/sensor/ambient_light

logs:
	tail -f "$(LOG_DIR)/bridge.log"
