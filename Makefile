.PHONY: test test-short test-unit test-integration test-failure test-timing test-concurrent test-multi test-edge test-all-local build clean help

test:
	go test -timeout 5m ./...

test-short:
	go test -short -timeout 30s ./...

test-unit:
	go test -v -run 'Test(EventLogger|CountingFace|Repo_|TLV_)' -timeout 30s ./repo/...

test-integration:
	go test -v -run 'TestLocal|TestCommand|TestEvent' -timeout 3m ./repo/...

test-failure:
	go test -v -run 'TestFailureRecovery' -timeout 5m ./repo/...

test-concurrent:
	go test -v -run 'TestConcurrentCommands' -timeout 3m ./repo/...

test-multi:
	go test -v -run 'TestMultipleCommands' -timeout 3m ./repo/...

test-edge:
	go test -v -run 'TestReplicationFactor_EdgeCases|TestNodeJoin' -timeout 5m ./repo/...

test-all-local: test-unit test-integration test-concurrent test-multi test-edge test-failure

test-timing:
	go test -v -run 'TestConfiguration_Timing' -timeout 10m ./repo/... -args -timing-enable=true

test-mini-ndn:
	go test -v -run 'TestMiniNDN' -timeout 15m ./repo/...

build:
	go build -o bin/repo ./repo
	go build -o bin/producer ./producer

clean:
	rm -rf bin/

help:
	@echo "Available targets:"
	@echo "  test             - Run all tests (5m timeout)"
	@echo "  test-short       - Run tests in short mode (skips integration/failure tests)"
	@echo "  test-unit        - Run only unit tests (no NFD required)"
	@echo "  test-integration - Run integration tests (requires NFD)"
	@echo "  test-concurrent  - Run concurrent command tests (requires NFD)"
	@echo "  test-multi       - Run multiple sequential command tests (requires NFD)"
	@echo "  test-edge        - Run edge case tests: RF=1, RF=nodes, node join (requires NFD)"
	@echo "  test-failure     - Run failure recovery tests (requires NFD)"
	@echo "  test-all-local   - Run all local NFD tests (no Docker)"
	@echo "  test-timing      - Run timing calibration tests (requires Docker/mini-ndn)"
	@echo "  test-mini-ndn    - Run mini-ndn Docker tests (requires Docker)"
	@echo "  build            - Build repo and producer binaries"
	@echo "  clean            - Remove built binaries"
