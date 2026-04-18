.PHONY: build help

# === Configuration ===
# Fixed settings for all experiments
NODES            := 24
RF               := 3
ROUTING_WAIT     := 115
REPLICATION_TIMEOUT := 90
COMMAND_RATE     := 1

# Docker resources
DOCKER_MEM       ?= 8g
DOCKER_CPUS      ?= 8

# Paths
MAKEFILE_DIR     := $(dir $(lastword $(MAKEFILE_LIST)))
RESULTS_DIR      ?= $(MAKEFILE_DIR)results
REPO_DIR         := $(MAKEFILE_DIR)/..
ANALYZE_PY       := $(MAKEFILE_DIR)/analyze_traces.py

# Timestamp for run directory
TIMESTAMP        := $(shell date +%Y%m%d_%H%M%S)

# === Build Setup ===

build:
	@echo "=== Building Go binaries ==="
	@mkdir -p $(REPO_DIR)/bin
	@cd $(REPO_DIR) && go build -o bin/repo ./repo && go build -o bin/producer ./producer
	@echo "Binaries built."
	@echo "=== Copying NDN keys ==="
	@rm -rf $(REPO_DIR)/keys
	@cp -r ~/.ndn/keys $(REPO_DIR)/keys 2>/dev/null || echo "Warning: No keys found at ~/.ndn/keys"
	@echo "Keys copied."
	@echo "=== Building Docker image ==="
	@cd $(REPO_DIR) && docker build -t mini-ndn-integration -f experiments/Dockerfile.integration . > /dev/null 2>&1 || { echo "Failed to build Docker image"; exit 1; }
	@echo "Docker image built."

# === Ground Truth RTTs ===
# Generate ground truth RTT measurements from topology
# Use: make generate-rtts TOPOLOGY=testbed_topology.conf
GROUND_TRUTH_RTT_PY := $(MAKEFILE_DIR)/ground_truth_rtt.py
TOPOLOGY ?= $(MAKEFILE_DIR)/testbed_topology.conf
RTT_OUTPUT ?= $(MAKEFILE_DIR)/ground_truth_rtts.json

generate-rtts:
	@echo "=== Generating ground truth RTTs ==="
	@echo "Topology: $(TOPOLOGY)"
	@echo "Output: $(RTT_OUTPUT)"
	@python3 $(GROUND_TRUTH_RTT_PY) --topology $(TOPOLOGY) --output $(RTT_OUTPUT) --theoretical
	@echo "Ground truth RTTs saved to: $(RTT_OUTPUT)"

# === Helper Macro ===

define run_experiment
	@TS=$$(date +%Y%m%d_%H%M%S); \
	mkdir -p $(RESULTS_DIR)/$(1)_$$TS; \
	echo "Running $(1) experiment..."; \
	echo "Timestamp: $$TS"; \
	echo "Results: $(RESULTS_DIR)/$(1)_$$TS"; \
	docker run --rm --privileged \
		-m $(DOCKER_MEM) --cpus $(DOCKER_CPUS) \
		-v /lib/modules:/lib/modules \
		-v "$(RESULTS_DIR)/$(1)_$$TS":/results \
		--entrypoint /bin/sh mini-ndn-integration \
		-c "python3 /usr/local/bin/runner.py \
			--node-count $(NODES) \
			--timeout $(REPLICATION_TIMEOUT) \
			--replication-factor $(RF) \
			--routing-wait $(ROUTING_WAIT) \
			--results-dir /results \
			--producer-count $(2) \
			--command-count $(3) \
			--command-rate $(COMMAND_RATE) \
			--distribution $(4)" \
		> "$(RESULTS_DIR)/$(1)_$$TS/docker.log" 2>&1; \
	if grep -q "TEST PASSED" "$(RESULTS_DIR)/$(1)_$$TS/docker.log"; then \
		echo "  ✓ $(1) ($(4)) passed"; \
	else \
		echo "  ✗ $(1) ($(4)) failed"; \
		tail -20 "$(RESULTS_DIR)/$(1)_$$TS/docker.log"; \
	fi; \
	echo ""; \
	echo "=== Analyzing $(1) Results ==="; \
	python3 $(ANALYZE_PY) \
		--experiments "$(RESULTS_DIR)/$(1)_$$TS" \
		--output-dir "$(RESULTS_DIR)/$(1)_$$TS" \
		--format json csv markdown 2>/dev/null || true; \
	echo "Results saved to: $(RESULTS_DIR)/$(1)_$$TS"
endef

# === Steady-State Experiments ===
# These test normal operation: 24 nodes, RF=3, commands replicate to 2*RF=6 nodes

steady-hydra-1x1: build
	$(call run_experiment,steady-hydra-1x1,1,1,hydra)

steady-hydra-1x8: build
	$(call run_experiment,steady-hydra-1x8,1,8,hydra)

steady-hydra-4x1: build
	$(call run_experiment,steady-hydra-4x1,4,1,hydra)

steady-hydra-24x1: build
	$(call run_experiment,steady-hydra-24x1,24,1,hydra)

# 24x24: 24 producers, 1 command each, synchronized start
steady-hydra-24x24: build
	$(call run_experiment,steady-hydra-24x24,24,24,hydra)

steady-auction-1x1: build
	$(call run_experiment,steady-auction-1x1,1,1,auction)

steady-auction-1x8: build
	$(call run_experiment,steady-auction-1x8,1,8,auction)

steady-auction-4x1: build
	$(call run_experiment,steady-auction-4x1,4,1,auction)

steady-auction-24x1: build
	$(call run_experiment,steady-auction-24x1,24,1,auction)

# 24x24: 24 producers, 24 commands each, synchronized start
steady-auction-24x24: build
	$(call run_experiment,steady-auction-24x24,24,24,auction)

# === Failure Experiments ===
# Test one node failure: wait for replication, kill one node, verify recovery

failure-hydra-1x8: build
	@TS=$$(date +%Y%m%d_%H%M%S); \
	mkdir -p $(RESULTS_DIR)/failure-hydra-1x8_$$TS; \
	echo "Running failure-hydra-1x8 experiment..."; \
	echo "Timestamp: $$TS"; \
	docker run --rm --privileged \
		-m $(DOCKER_MEM) --cpus $(DOCKER_CPUS) \
		-v /lib/modules:/lib/modules \
		-v "$(RESULTS_DIR)/failure-hydra-1x8_$$TS":/results \
		--entrypoint /bin/sh mini-ndn-integration \
		-c "python3 /usr/local/bin/runner.py \
			--node-count $(NODES) \
			--timeout $(REPLICATION_TIMEOUT) \
			--replication-factor $(RF) \
			--routing-wait $(ROUTING_WAIT) \
			--results-dir /results \
			--producer-count 1 \
			--command-count 8 \
			--command-rate $(COMMAND_RATE) \
			--distribution hydra \
			--failure-count 1 \
			--failure-wait 30 \
			--failure-recovery-timeout 60" \
		> "$(RESULTS_DIR)/failure-hydra-1x8_$$TS/docker.log" 2>&1; \
	if grep -q "TEST PASSED" "$(RESULTS_DIR)/failure-hydra-1x8_$$TS/docker.log"; then \
		echo "  ✓ failure-hydra-1x8 passed"; \
	else \
		echo "  ✗ failure-hydra-1x8 failed"; \
		tail -30 "$(RESULTS_DIR)/failure-hydra-1x8_$$TS/docker.log"; \
	fi; \
	echo ""; \
	echo "=== Analyzing Results ==="; \
	python3 $(ANALYZE_PY) \
		--experiments "$(RESULTS_DIR)/failure-hydra-1x8_$$TS" \
		--output-dir "$(RESULTS_DIR)/failure-hydra-1x8_$$TS" \
		--format json csv markdown 2>/dev/null || true; \
	echo "Results saved to: $(RESULTS_DIR)/failure-hydra-1x8_$$TS"

failure-auction-1x8: build
	@TS=$$(date +%Y%m%d_%H%M%S); \
	mkdir -p $(RESULTS_DIR)/failure-auction-1x8_$$TS; \
	echo "Running failure-auction-1x8 experiment..."; \
	echo "Timestamp: $$TS"; \
	docker run --rm --privileged \
		-m $(DOCKER_MEM) --cpus $(DOCKER_CPUS) \
		-v /lib/modules:/lib/modules \
		-v "$(RESULTS_DIR)/failure-auction-1x8_$$TS":/results \
		--entrypoint /bin/sh mini-ndn-integration \
		-c "python3 /usr/local/bin/runner.py \
			--node-count $(NODES) \
			--timeout $(REPLICATION_TIMEOUT) \
			--replication-factor $(RF) \
			--routing-wait $(ROUTING_WAIT) \
			--results-dir /results \
			--producer-count 1 \
			--command-count 8 \
			--command-rate $(COMMAND_RATE) \
			--distribution auction \
			--failure-count 1 \
			--failure-wait 30 \
			--failure-recovery-timeout 60" \
		> "$(RESULTS_DIR)/failure-auction-1x8_$$TS/docker.log" 2>&1; \
	if grep -q "TEST PASSED" "$(RESULTS_DIR)/failure-auction-1x8_$$TS/docker.log"; then \
		echo "  ✓ failure-auction-1x8 passed"; \
	else \
		echo "  ✗ failure-auction-1x8 failed"; \
		tail -30 "$(RESULTS_DIR)/failure-auction-1x8_$$TS/docker.log"; \
	fi; \
	echo ""; \
	echo "=== Analyzing Results ==="; \
	python3 $(ANALYZE_PY) \
		--experiments "$(RESULTS_DIR)/failure-auction-1x8_$$TS" \
		--output-dir "$(RESULTS_DIR)/failure-auction-1x8_$$TS" \
		--format json csv markdown 2>/dev/null || true; \
	echo "Results saved to: $(RESULTS_DIR)/failure-auction-1x8_$$TS"

# === Partition Experiments ===
# Test network partition: commands replicate to RF, then partition, then verify 2*RF after timeout

partition-hydra-1x8: build
	@TS=$$(date +%Y%m%d_%H%M%S); \
	mkdir -p $(RESULTS_DIR)/partition-hydra-1x8_$$TS; \
	echo "Running partition-hydra-1x8 experiment..."; \
	echo "Timestamp: $$TS"; \
	docker run --rm --privileged \
		-m $(DOCKER_MEM) --cpus $(DOCKER_CPUS) \
		-v /lib/modules:/lib/modules \
		-v "$(RESULTS_DIR)/partition-hydra-1x8_$$TS":/results \
		--entrypoint /bin/sh mini-ndn-integration \
		-c "python3 /usr/local/bin/runner.py \
			--node-count $(NODES) \
			--timeout $(REPLICATION_TIMEOUT) \
			--replication-factor $(RF) \
			--routing-wait $(ROUTING_WAIT) \
			--results-dir /results \
			--producer-count 1 \
			--command-count 8 \
			--command-rate $(COMMAND_RATE) \
			--distribution hydra \
			--partition-links UCLA:WASEDA,OSAKA:DELFT,ANYANG:ARIZONA,MML1:IIITH,SINGAPORE:FRANKFURT \
			--partition-after 65 \
			--partition-timeout 180" \
		> "$(RESULTS_DIR)/partition-hydra-1x8_$$TS/docker.log" 2>&1; \
	if grep -q "TEST PASSED" "$(RESULTS_DIR)/partition-hydra-1x8_$$TS/docker.log"; then \
		echo "  ✓ partition-hydra-1x8 passed"; \
	else \
		echo "  ✗ partition-hydra-1x8 failed"; \
		tail -30 "$(RESULTS_DIR)/partition-hydra-1x8_$$TS/docker.log"; \
	fi; \
	echo ""; \
	echo "=== Analyzing Results ==="; \
	python3 $(ANALYZE_PY) \
		--experiments "$(RESULTS_DIR)/partition-hydra-1x8_$$TS" \
		--output-dir "$(RESULTS_DIR)/partition-hydra-1x8_$$TS" \
		--format json csv markdown 2>/dev/null || true; \
	echo "Results saved to: $(RESULTS_DIR)/partition-hydra-1x8_$$TS"

partition-auction-1x8: build
	@TS=$$(date +%Y%m%d_%H%M%S); \
	mkdir -p $(RESULTS_DIR)/partition-auction-1x8_$$TS; \
	echo "Running partition-auction-1x8 experiment..."; \
	echo "Timestamp: $$TS"; \
	docker run --rm --privileged \
		-m $(DOCKER_MEM) --cpus $(DOCKER_CPUS) \
		-v /lib/modules:/lib/modules \
		-v "$(RESULTS_DIR)/partition-auction-1x8_$$TS":/results \
		--entrypoint /bin/sh mini-ndn-integration \
		-c "python3 /usr/local/bin/runner.py \
			--node-count $(NODES) \
			--timeout $(REPLICATION_TIMEOUT) \
			--replication-factor $(RF) \
			--routing-wait $(ROUTING_WAIT) \
			--results-dir /results \
			--producer-count 1 \
			--command-count 8 \
			--command-rate $(COMMAND_RATE) \
			--distribution auction \
			--partition-links UCLA:WASEDA,OSAKA:DELFT,ANYANG:ARIZONA,MML1:IIITH,SINGAPORE:FRANKFURT \
			--partition-after 65 \
			--partition-timeout 180" \
		> "$(RESULTS_DIR)/partition-auction-1x8_$$TS/docker.log" 2>&1; \
	if grep -q "TEST PASSED" "$(RESULTS_DIR)/partition-auction-1x8_$$TS/docker.log"; then \
		echo "  ✓ partition-auction-1x8 passed"; \
	else \
		echo "  ✗ partition-auction-1x8 failed"; \
		tail -30 "$(RESULTS_DIR)/partition-auction-1x8_$$TS/docker.log"; \
	fi; \
	echo ""; \
	echo "=== Analyzing Results ==="; \
	python3 $(ANALYZE_PY) \
		--experiments "$(RESULTS_DIR)/partition-auction-1x8_$$TS" \
		--output-dir "$(RESULTS_DIR)/partition-auction-1x8_$$TS" \
		--format json csv markdown 2>/dev/null || true; \
	echo "Results saved to: $(RESULTS_DIR)/partition-auction-1x8_$$TS"

# === Link Loss Experiments ===
# Test behavior under increasing network degradation

loss-hydra-1x8-1: build
	@TS=$$(date +%Y%m%d_%H%M%S); \
	mkdir -p $(RESULTS_DIR)/loss-hydra-1x8-1_$$TS; \
	echo "Running loss-hydra-1x8-1 (1% loss) experiment..."; \
	docker run --rm --privileged \
		-m $(DOCKER_MEM) --cpus $(DOCKER_CPUS) \
		-v /lib/modules:/lib/modules \
		-v "$(RESULTS_DIR)/loss-hydra-1x8-1_$$TS":/results \
		--entrypoint /bin/sh mini-ndn-integration \
		-c "python3 /usr/local/bin/runner.py \
			--node-count $(NODES) \
			--timeout $(REPLICATION_TIMEOUT) \
			--replication-factor $(RF) \
			--routing-wait $(ROUTING_WAIT) \
			--results-dir /results \
			--producer-count 1 \
			--command-count 8 \
			--command-rate $(COMMAND_RATE) \
			--distribution hydra \
			--link-loss-rate 1" \
		> "$(RESULTS_DIR)/loss-hydra-1x8-1_$$TS/docker.log" 2>&1; \
	if grep -q "TEST PASSED" "$(RESULTS_DIR)/loss-hydra-1x8-1_$$TS/docker.log"; then \
		echo "  ✓ loss-hydra-1x8-1 passed"; \
	else \
		echo "  ✗ loss-hydra-1x8-1 failed"; \
		tail -20 "$(RESULTS_DIR)/loss-hydra-1x8-1_$$TS/docker.log"; \
	fi

loss-auction-1x8-1: build
	@TS=$$(date +%Y%m%d_%H%M%S); \
	mkdir -p $(RESULTS_DIR)/loss-auction-1x8-1_$$TS; \
	echo "Running loss-auction-1x8-1 (1% loss) experiment..."; \
	docker run --rm --privileged \
		-m $(DOCKER_MEM) --cpus $(DOCKER_CPUS) \
		-v /lib/modules:/lib/modules \
		-v "$(RESULTS_DIR)/loss-auction-1x8-1_$$TS":/results \
		--entrypoint /bin/sh mini-ndn-integration \
		-c "python3 /usr/local/bin/runner.py \
			--node-count $(NODES) \
			--timeout $(REPLICATION_TIMEOUT) \
			--replication-factor $(RF) \
			--routing-wait $(ROUTING_WAIT) \
			--results-dir /results \
			--producer-count 1 \
			--command-count 8 \
			--command-rate $(COMMAND_RATE) \
			--distribution auction \
			--link-loss-rate 1" \
		> "$(RESULTS_DIR)/loss-auction-1x8-1_$$TS/docker.log" 2>&1; \
	if grep -q "TEST PASSED" "$(RESULTS_DIR)/loss-auction-1x8-1_$$TS/docker.log"; then \
		echo "  ✓ loss-auction-1x8-1 passed"; \
	else \
		echo "  ✗ loss-auction-1x8-1 failed"; \
		tail -20 "$(RESULTS_DIR)/loss-auction-1x8-1_$$TS/docker.log"; \
	fi

loss-hydra-1x8-5: build
	@TS=$$(date +%Y%m%d_%H%M%S); \
	mkdir -p $(RESULTS_DIR)/loss-hydra-1x8-5_$$TS; \
	echo "Running loss-hydra-1x8-5 (5% loss) experiment..."; \
	docker run --rm --privileged \
		-m $(DOCKER_MEM) --cpus $(DOCKER_CPUS) \
		-v /lib/modules:/lib/modules \
		-v "$(RESULTS_DIR)/loss-hydra-1x8-5_$$TS":/results \
		--entrypoint /bin/sh mini-ndn-integration \
		-c "python3 /usr/local/bin/runner.py \
			--node-count $(NODES) \
			--timeout $(REPLICATION_TIMEOUT) \
			--replication-factor $(RF) \
			--routing-wait $(ROUTING_WAIT) \
			--results-dir /results \
			--producer-count 1 \
			--command-count 8 \
			--command-rate $(COMMAND_RATE) \
			--distribution hydra \
			--link-loss-rate 5" \
		> "$(RESULTS_DIR)/loss-hydra-1x8-5_$$TS/docker.log" 2>&1; \
	if grep -q "TEST PASSED" "$(RESULTS_DIR)/loss-hydra-1x8-5_$$TS/docker.log"; then \
		echo "  ✓ loss-hydra-1x8-5 passed"; \
	else \
		echo "  ✗ loss-hydra-1x8-5 failed"; \
		tail -20 "$(RESULTS_DIR)/loss-hydra-1x8-5_$$TS/docker.log"; \
	fi

loss-auction-1x8-5: build
	@TS=$$(date +%Y%m%d_%H%M%S); \
	mkdir -p $(RESULTS_DIR)/loss-auction-1x8-5_$$TS; \
	echo "Running loss-auction-1x8-5 (5% loss) experiment..."; \
	docker run --rm --privileged \
		-m $(DOCKER_MEM) --cpus $(DOCKER_CPUS) \
		-v /lib/modules:/lib/modules \
		-v "$(RESULTS_DIR)/loss-auction-1x8-5_$$TS":/results \
		--entrypoint /bin/sh mini-ndn-integration \
		-c "python3 /usr/local/bin/runner.py \
			--node-count $(NODES) \
			--timeout $(REPLICATION_TIMEOUT) \
			--replication-factor $(RF) \
			--routing-wait $(ROUTING_WAIT) \
			--results-dir /results \
			--producer-count 1 \
			--command-count 8 \
			--command-rate $(COMMAND_RATE) \
			--distribution auction \
			--link-loss-rate 5" \
		> "$(RESULTS_DIR)/loss-auction-1x8-5_$$TS/docker.log" 2>&1; \
	if grep -q "TEST PASSED" "$(RESULTS_DIR)/loss-auction-1x8-5_$$TS/docker.log"; then \
		echo "  ✓ loss-auction-1x8-5 passed"; \
	else \
		echo "  ✗ loss-auction-1x8-5 failed"; \
		tail -20 "$(RESULTS_DIR)/loss-auction-1x8-5_$$TS/docker.log"; \
	fi

loss-hydra-1x8-10: build
	@TS=$$(date +%Y%m%d_%H%M%S); \
	mkdir -p $(RESULTS_DIR)/loss-hydra-1x8-10_$$TS; \
	echo "Running loss-hydra-1x8-10 (10% loss) experiment..."; \
	docker run --rm --privileged \
		-m $(DOCKER_MEM) --cpus $(DOCKER_CPUS) \
		-v /lib/modules:/lib/modules \
		-v "$(RESULTS_DIR)/loss-hydra-1x8-10_$$TS":/results \
		--entrypoint /bin/sh mini-ndn-integration \
		-c "python3 /usr/local/bin/runner.py \
			--node-count $(NODES) \
			--timeout $(REPLICATION_TIMEOUT) \
			--replication-factor $(RF) \
			--routing-wait $(ROUTING_WAIT) \
			--results-dir /results \
			--producer-count 1 \
			--command-count 8 \
			--command-rate $(COMMAND_RATE) \
			--distribution hydra \
			--link-loss-rate 10" \
		> "$(RESULTS_DIR)/loss-hydra-1x8-10_$$TS/docker.log" 2>&1; \
	if grep -q "TEST PASSED" "$(RESULTS_DIR)/loss-hydra-1x8-10_$$TS/docker.log"; then \
		echo "  ✓ loss-hydra-1x8-10 passed"; \
	else \
		echo "  ✗ loss-hydra-1x8-10 failed"; \
		tail -20 "$(RESULTS_DIR)/loss-hydra-1x8-10_$$TS/docker.log"; \
	fi

loss-auction-1x8-10: build
	@TS=$$(date +%Y%m%d_%H%M%S); \
	mkdir -p $(RESULTS_DIR)/loss-auction-1x8-10_$$TS; \
	echo "Running loss-auction-1x8-10 (10% loss) experiment..."; \
	docker run --rm --privileged \
		-m $(DOCKER_MEM) --cpus $(DOCKER_CPUS) \
		-v /lib/modules:/lib/modules \
		-v "$(RESULTS_DIR)/loss-auction-1x8-10_$$TS":/results \
		--entrypoint /bin/sh mini-ndn-integration \
		-c "python3 /usr/local/bin/runner.py \
			--node-count $(NODES) \
			--timeout $(REPLICATION_TIMEOUT) \
			--replication-factor $(RF) \
			--routing-wait $(ROUTING_WAIT) \
			--results-dir /results \
			--producer-count 1 \
			--command-count 8 \
			--command-rate $(COMMAND_RATE) \
			--distribution auction \
			--link-loss-rate 10" \
		> "$(RESULTS_DIR)/loss-auction-1x8-10_$$TS/docker.log" 2>&1; \
	if grep -q "TEST PASSED" "$(RESULTS_DIR)/loss-auction-1x8-10_$$TS/docker.log"; then \
		echo "  ✓ loss-auction-1x8-10 passed"; \
	else \
		echo "  ✗ loss-auction-1x8-10 failed"; \
		tail -20 "$(RESULTS_DIR)/loss-auction-1x8-10_$$TS/docker.log"; \
	fi

# === Steady State Cost Experiments ===
# Measure cost to reach and maintain steady state (longer running)

cost-hydra-1x8-60s: build
	@TS=$$(date +%Y%m%d_%H%M%S); \
	mkdir -p $(RESULTS_DIR)/cost-hydra-1x8-60s_$$TS; \
	echo "Running cost-hydra-1x8-60s (60s steady state) experiment..."; \
	docker run --rm --privileged \
		-m $(DOCKER_MEM) --cpus $(DOCKER_CPUS) \
		-v /lib/modules:/lib/modules \
		-v "$(RESULTS_DIR)/cost-hydra-1x8-60s_$$TS":/results \
		--entrypoint /bin/sh mini-ndn-integration \
		-c "python3 /usr/local/bin/runner.py \
			--node-count $(NODES) \
			--timeout 60 \
			--replication-factor $(RF) \
			--routing-wait $(ROUTING_WAIT) \
			--results-dir /results \
			--producer-count 1 \
			--command-count 8 \
			--command-rate $(COMMAND_RATE) \
			--distribution hydra" \
		> "$(RESULTS_DIR)/cost-hydra-1x8-60s_$$TS/docker.log" 2>&1; \
	if grep -q "TEST PASSED" "$(RESULTS_DIR)/cost-hydra-1x8-60s_$$TS/docker.log"; then \
		echo "  ✓ cost-hydra-1x8-60s passed"; \
	else \
		echo "  ✗ cost-hydra-1x8-60s failed"; \
		tail -20 "$(RESULTS_DIR)/cost-hydra-1x8-60s_$$TS/docker.log"; \
	fi

cost-auction-1x8-60s: build
	@TS=$$(date +%Y%m%d_%H%M%S); \
	mkdir -p $(RESULTS_DIR)/cost-auction-1x8-60s_$$TS; \
	echo "Running cost-auction-1x8-60s (60s steady state) experiment..."; \
	docker run --rm --privileged \
		-m $(DOCKER_MEM) --cpus $(DOCKER_CPUS) \
		-v /lib/modules:/lib/modules \
		-v "$(RESULTS_DIR)/cost-auction-1x8-60s_$$TS":/results \
		--entrypoint /bin/sh mini-ndn-integration \
		-c "python3 /usr/local/bin/runner.py \
			--node-count $(NODES) \
			--timeout 60 \
			--replication-factor $(RF) \
			--routing-wait $(ROUTING_WAIT) \
			--results-dir /results \
			--producer-count 1 \
			--command-count 8 \
			--command-rate $(COMMAND_RATE) \
			--distribution auction" \
		> "$(RESULTS_DIR)/cost-auction-1x8-60s_$$TS/docker.log" 2>&1; \
	if grep -q "TEST PASSED" "$(RESULTS_DIR)/cost-auction-1x8-60s_$$TS/docker.log"; then \
		echo "  ✓ cost-auction-1x8-60s passed"; \
	else \
		echo "  ✗ cost-auction-1x8-60s failed"; \
		tail -20 "$(RESULTS_DIR)/cost-auction-1x8-60s_$$TS/docker.log"; \
	fi

cost-hydra-1x8-120s: build
	@TS=$$(date +%Y%m%d_%H%M%S); \
	mkdir -p $(RESULTS_DIR)/cost-hydra-1x8-120s_$$TS; \
	echo "Running cost-hydra-1x8-120s (120s steady state) experiment..."; \
	docker run --rm --privileged \
		-m $(DOCKER_MEM) --cpus $(DOCKER_CPUS) \
		-v /lib/modules:/lib/modules \
		-v "$(RESULTS_DIR)/cost-hydra-1x8-120s_$$TS":/results \
		--entrypoint /bin/sh mini-ndn-integration \
		-c "python3 /usr/local/bin/runner.py \
			--node-count $(NODES) \
			--timeout 120 \
			--replication-factor $(RF) \
			--routing-wait $(ROUTING_WAIT) \
			--results-dir /results \
			--producer-count 1 \
			--command-count 8 \
			--command-rate $(COMMAND_RATE) \
			--distribution hydra" \
		> "$(RESULTS_DIR)/cost-hydra-1x8-120s_$$TS/docker.log" 2>&1; \
	if grep -q "TEST PASSED" "$(RESULTS_DIR)/cost-hydra-1x8-120s_$$TS/docker.log"; then \
		echo "  ✓ cost-hydra-1x8-120s passed"; \
	else \
		echo "  ✗ cost-hydra-1x8-120s failed"; \
		tail -20 "$(RESULTS_DIR)/cost-hydra-1x8-120s_$$TS/docker.log"; \
	fi

cost-auction-1x8-120s: build
	@TS=$$(date +%Y%m%d_%H%M%S); \
	mkdir -p $(RESULTS_DIR)/cost-auction-1x8-120s_$$TS; \
	echo "Running cost-auction-1x8-120s (120s steady state) experiment..."; \
	docker run --rm --privileged \
		-m $(DOCKER_MEM) --cpus $(DOCKER_CPUS) \
		-v /lib/modules:/lib/modules \
		-v "$(RESULTS_DIR)/cost-auction-1x8-120s_$$TS":/results \
		--entrypoint /bin/sh mini-ndn-integration \
		-c "python3 /usr/local/bin/runner.py \
			--node-count $(NODES) \
			--timeout 120 \
			--replication-factor $(RF) \
			--routing-wait $(ROUTING_WAIT) \
			--results-dir /results \
			--producer-count 1 \
			--command-count 8 \
			--command-rate $(COMMAND_RATE) \
			--distribution auction" \
		> "$(RESULTS_DIR)/cost-auction-1x8-120s_$$TS/docker.log" 2>&1; \
	if grep -q "TEST PASSED" "$(RESULTS_DIR)/cost-auction-1x8-120s_$$TS/docker.log"; then \
		echo "  ✓ cost-auction-1x8-120s passed"; \
	else \
		echo "  ✗ cost-auction-1x8-120s failed"; \
		tail -20 "$(RESULTS_DIR)/cost-auction-1x8-120s_$$TS/docker.log"; \
	fi

# === Run All Experiments ===

all: build \
	steady-hydra-1x1 steady-hydra-1x8 steady-hydra-4x1 \
	steady-auction-1x1 steady-auction-1x8 steady-auction-4x1 \
	failure-hydra-1x8 failure-auction-1x8 \
	partition-hydra-1x8 partition-auction-1x8

# === Run Comprehensive Experiments ===

all-comprehensive: build \
	steady-hydra-1x8 steady-auction-1x8 \
	steady-hydra-24x24 steady-auction-24x24 \
	failure-hydra-1x8 failure-auction-1x8 \
	partition-hydra-1x8 partition-auction-1x8 \
	loss-hydra-1x8-1 loss-auction-1x8-1 \
	loss-hydra-1x8-5 loss-auction-1x8-5 \
	loss-hydra-1x8-10 loss-auction-1x8-10 \
	cost-hydra-1x8-60s cost-auction-1x8-60s \
	cost-hydra-1x8-120s cost-auction-1x8-120s

# === Quick Tests (faster, fewer commands) ===

quick-steady-hydra: build
	$(call run_experiment,quick-steady-hydra,1,1,hydra)

quick-steady-auction: build
	$(call run_experiment,quick-steady-auction,1,1,auction)

quick-all: build quick-steady-hydra quick-steady-auction

# === Cleanup ===

clean:
	@echo "Cleaning results..."
	@find $(RESULTS_DIR) -maxdepth 1 -type d -name '*_??????_?????*' -exec rm -rf {} + 2>/dev/null || true
	@echo "Clean complete."

# === Help ===

help:
	@echo "NDN Repository Experiments"
	@echo ""
	@echo "Usage: make [target]"
	@echo ""
	@echo "Targets:"
	@echo "  build                    Build binaries, keys, and Docker image"
	@echo "  generate-rtts            Generate ground truth RTTs from topology"
	@echo "                          (TOPOLOGY=... RTT_OUTPUT=... make generate-rtts)"
	@echo ""
	@echo "  Steady-State Experiments:"
	@echo "  steady-hydra-1x1         Hydra: 1 producer, 1 command"
	@echo "  steady-hydra-1x8         Hydra: 1 producer, 8 commands"
	@echo "  steady-hydra-4x1         Hydra: 4 producers, 1 command each"
	@echo "  steady-auction-1x1       Auction: 1 producer, 1 command"
	@echo "  steady-auction-1x8       Auction: 1 producer, 8 commands"
	@echo "  steady-auction-4x1       Auction: 4 producers, 1 command each"
	@echo ""
	@echo "  Failure Experiments:"
	@echo "  failure-hydra-1x8        Hydra: 1 producer, 8 commands, then kill 1 node"
	@echo "  failure-auction-1x8      Auction: 1 producer, 8 commands, then kill 1 node"
	@echo ""
	@echo "  Partition Experiments:"
	@echo "  partition-hydra-1x8      Hydra: 1 producer, 8 commands, then partition"
	@echo "  partition-auction-1x8     Auction: 1 producer, 8 commands, then partition"
	@echo ""
	@echo "  Test Suites:"
	@echo "  all                      Run all experiments (steady + failure + partition)"
	@echo "  quick-all                Quick smoke test (steady-state only, 1 command each)"
	@echo ""
	@echo "  Cleanup:"
	@echo "  clean                    Remove experiment results"
	@echo ""
	@echo "Settings:"
	@echo "  NODES=$(NODES)"
	@echo "  RF=$(RF)"
	@echo "  DOCKER_MEM=$(DOCKER_MEM)"
	@echo "  DOCKER_CPUS=$(DOCKER_CPUS)"
