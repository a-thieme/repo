.PHONY: build keys image calibrate run single shell results clean clean-all help

# === Configuration ===
# Experiment settings
NODE_COUNTS      ?= 24
PRODUCER_COUNTS  ?= 1 2 4 8 16 24
RF               ?= 3
COMMAND_COUNT    ?= 1
COMMAND_TYPE     ?= insert
JOIN_RATIO       ?= 0.5
TIMEOUT          ?= 120

# Calibration settings
CALIBRATE_NODES  ?= 24
CALIBRATE_ITER   ?= 3

# Single experiment settings
NODES            ?= 24
PRODUCERS        ?= 1

# Docker resources
DOCKER_MEM       ?= 16g
DOCKER_CPUS      ?= 8

# Debug
DEBUG            ?= false

# Paths
MAKEFILE_DIR     := $(dir $(lastword $(MAKEFILE_LIST)))
RESULTS_DIR      ?= $(MAKEFILE_DIR)results
REPO_DIR         := $(MAKEFILE_DIR)/..
HELPERS_PY       := $(MAKEFILE_DIR)/helpers.py

# Timestamp for run directory
TIMESTAMP        := $(shell date +%Y%m%d_%H%M%S)

# === Setup ===

build: keys image
	@echo "Build complete."

keys:
	@echo "Copying NDN keys..."
	@rm -rf $(REPO_DIR)/keys
	@cp -r ~/.ndn/keys $(REPO_DIR)/keys
	@echo "Keys copied to $(REPO_DIR)/keys"

image:
	@echo "Building Go binaries..."
	@mkdir -p $(REPO_DIR)/bin
	@cd $(REPO_DIR) && go build -o bin/repo ./repo && go build -o bin/producer ./producer
	@echo "Binaries built."
	@echo "Building Docker image..."
	@cd $(REPO_DIR) && docker build -t mini-ndn-integration -f experiments/Dockerfile.integration . > /dev/null 2>&1 || { echo "Failed to build Docker image"; exit 1; }
	@echo "Docker image built."

# === Calibration ===

calibrate:
	@echo "=== Timeout Calibration ==="
	@echo "Node count: $(CALIBRATE_NODES)"
	@echo "Iterations: $(CALIBRATE_ITER)"
	@echo ""
	@mkdir -p $(RESULTS_DIR)/calibration/run_$(TIMESTAMP)
	@for i in $$(seq 1 $(CALIBRATE_ITER)); do \
		echo "--- Iteration $$i/$(CALIBRATE_ITER) ---"; \
		iter_dir="$(RESULTS_DIR)/calibration/run_$(TIMESTAMP)/iter_$$i"; \
		mkdir -p "$$iter_dir"; \
		routing_wait=$$((30 + ($(CALIBRATE_NODES) - 5) * 2)); \
		docker run --rm --privileged \
			-m $(DOCKER_MEM) --cpus $(DOCKER_CPUS) \
			-v /lib/modules:/lib/modules \
			-v "$$(cd $$iter_dir && pwd)":/results \
			mini-ndn-integration \
			-c "python3 /usr/local/bin/runner.py --node-count $(CALIBRATE_NODES) --timeout 120 --replication-factor $(RF) --routing-wait $$routing_wait --results-dir /results --producer-count 1 --command-count 1 --command-rate 1 --svs-timeout 60 --replication-timeout 60" \
			> "$$iter_dir/docker.log" 2>&1; \
		if [ -f "$$iter_dir/metadata.json" ]; then \
			echo "  Iteration $$i complete"; \
		else \
			echo "  Iteration $$i failed (no metadata.json)"; \
		fi; \
	done
	@echo ""
	@python3 $(HELPERS_PY) calibrate $(RESULTS_DIR)/calibration/run_$(TIMESTAMP) \
		--node-count $(CALIBRATE_NODES) \
		--output $(RESULTS_DIR)/calibration/run_$(TIMESTAMP)/calibration.json

# === Experiments ===

run:
	@echo "=== NDN Repository Experiment Runner ==="
	@echo "Node counts: $(NODE_COUNTS)"
	@echo "Producer counts: $(PRODUCER_COUNTS)"
	@echo "Replication factor: $(RF)"
	@echo "Command count per producer: $(COMMAND_COUNT)"
	@echo ""
	@mkdir -p $(RESULTS_DIR)/run_$(TIMESTAMP)
	@for nodes in $(NODE_COUNTS); do \
		for producers in $(PRODUCER_COUNTS); do \
			echo "Starting experiment: $$nodes nodes, $$producers producers"; \
			$(MAKE) single NODES=$$nodes PRODUCERS=$$producers TIMESTAMP=$(TIMESTAMP) > /dev/null 2>&1 && \
				echo "  ✓ $$nodes nodes, $$producers producers" || \
				echo "  ✗ $$nodes nodes, $$producers producers (failed)"; \
		done; \
	done
	@echo ""
	@echo "Collecting results..."
	@python3 $(HELPERS_PY) collect $(RESULTS_DIR)/run_$(TIMESTAMP) \
		--csv $(RESULTS_DIR)/run_$(TIMESTAMP)/summary.csv \
		--md $(RESULTS_DIR)/run_$(TIMESTAMP)/RESULTS.md \
		--show
	@echo ""
	@echo "Results directory: $(RESULTS_DIR)/run_$(TIMESTAMP)"

single:
	@mkdir -p $(RESULTS_DIR)/run_$(TIMESTAMP)/nodes_$(NODES)_producers_$(PRODUCERS)
	@result_dir="$$(cd $(RESULTS_DIR)/run_$(TIMESTAMP)/nodes_$(NODES)_producers_$(PRODUCERS) && pwd)"; \
	routing_wait=$$((30 + ($(NODES) - 5) * 2)); \
	cal_file="$(RESULTS_DIR)/calibration/latest_calibration.json"; \
	if [ -f "$$cal_file" ]; then \
		safe_interval=$$(python3 -c "import json; d=json.load(open('$$cal_file')); mv=d.get('max_values',{}); print(mv.get('safe_interval_ms', mv.get('replication_time_ms', 10)))" 2>/dev/null || echo "10"); \
		cmd_rate=$$(python3 -c "import math; print(max(1, int(1000 / ($$safe_interval * 1.25))))" 2>/dev/null || echo "80"); \
	else \
		cmd_rate=80; \
	fi; \
	per_producer_rate=$$((cmd_rate / $(PRODUCERS))); \
	[ "$$per_producer_rate" -lt 1 ] && per_producer_rate=1; \
	debug_arg=""; \
	[ "$(DEBUG)" = "true" ] && debug_arg=" --debug"; \
	docker run --rm --privileged \
		-m $(DOCKER_MEM) --cpus $(DOCKER_CPUS) \
		-v /lib/modules:/lib/modules \
		-v "$$result_dir":/results \
		mini-ndn-integration \
		-c "python3 /usr/local/bin/runner.py \
			--node-count $(NODES) \
			--timeout $(TIMEOUT) \
			--replication-factor $(RF) \
			--routing-wait $$routing_wait \
			--results-dir /results \
			--producer-count $(PRODUCERS) \
			--command-count $(COMMAND_COUNT) \
			--command-rate $$per_producer_rate \
			--command-type $(COMMAND_TYPE) \
			--join-ratio $(JOIN_RATIO)$$debug_arg" \
		> "$$result_dir/docker.log" 2>&1

# === Debugging ===

shell:
	@echo "Starting interactive Docker shell with $(NODES) nodes..."
	@tmp_dir=$$(mktemp -d); \
	routing_wait=$$((30 + ($(NODES) - 5) * 2)); \
	docker run --rm -it --privileged \
		-m $(DOCKER_MEM) --cpus $(DOCKER_CPUS) \
		-v /lib/modules:/lib/modules \
		-v "$$tmp_dir":/results \
		mini-ndn-integration \
		-c "python3 /usr/local/bin/runner.py --node-count $(NODES) --timeout 300 --replication-factor $(RF) --routing-wait $$routing_wait --results-dir /results --producer-count 1 --command-count 1 --command-rate 1; bash"; \
	rm -rf $$tmp_dir

# === Results ===

results:
	@latest=$$(ls -td $(RESULTS_DIR)/run_* 2>/dev/null | head -1); \
	if [ -z "$$latest" ]; then \
		echo "No results found."; \
		exit 1; \
	fi; \
	echo "Latest results: $$latest"; \
	python3 $(HELPERS_PY) collect "$$latest" --show

# === Cleanup ===

clean:
	@echo "Cleaning results (keeping calibration)..."
	@find $(RESULTS_DIR) -maxdepth 1 -type d -name 'run_*' -exec rm -rf {} +
	@echo "Results cleaned."

clean-all:
	@echo "Cleaning all results and Docker resources..."
	@rm -rf $(RESULTS_DIR)
	@docker system prune -f 2>/dev/null || true
	@echo "Clean complete."

# === Help ===

help:
	@echo "NDN Repository Experiments"
	@echo ""
	@echo "Usage: make [target] [VARIABLE=value ...]"
	@echo ""
	@echo "Targets:"
	@echo "  build       Build Go binaries + copy keys + build Docker image"
	@echo "  keys        Copy NDN keys (~/.ndn/keys -> ./keys)"
	@echo "  image       Build Docker image only"
	@echo "  calibrate   Run timeout calibration"
	@echo "  run         Run experiment suite (use -j for parallel)"
	@echo "  single      Run single experiment"
	@echo "  shell       Interactive Docker shell for debugging"
	@echo "  results     Show summary of last run"
	@echo "  clean       Remove results (keep calibration)"
	@echo "  clean-all   Remove all results + Docker cleanup"
	@echo "  help        Show this help"
	@echo ""
	@echo "Variables:"
	@echo "  NODE_COUNTS      Node counts for 'run' (default: 24)"
	@echo "  PRODUCER_COUNTS  Producer counts for 'run' (default: 1 2 4 8 16 24)"
	@echo "  RF               Replication factor (default: 3)"
	@echo "  COMMAND_COUNT    Commands per producer (default: 1)"
	@echo "  COMMAND_TYPE     Command type: insert, join, both (default: insert)"
	@echo "  TIMEOUT          Timeout per experiment in seconds (default: 120)"
	@echo "  CALIBRATE_NODES  Node count for calibration (default: 24)"
	@echo "  CALIBRATE_ITER   Calibration iterations (default: 3)"
	@echo "  NODES            Node count for 'single' (default: 24)"
	@echo "  PRODUCERS        Producer count for 'single' (default: 1)"
	@echo "  DOCKER_MEM       Memory per container (default: 16g)"
	@echo "  DOCKER_CPUS      CPUs per container (default: 8)"
	@echo "  DEBUG            Enable debug logging (default: false)"
	@echo ""
	@echo "Examples:"
	@echo "  make build"
	@echo "  make calibrate CALIBRATE_NODES=24 CALIBRATE_ITER=3"
	@echo "  make run NODE_COUNTS=\"5 24\" PRODUCER_COUNTS=\"1 4\" -j2"
	@echo "  make single NODES=5 PRODUCERS=1"
	@echo "  make shell NODES=5"
	@echo "  make results"
