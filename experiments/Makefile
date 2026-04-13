.PHONY: build keys image calibrate run single shell results clean clean-all help compare compare-run compare-analyze experiment-full test test-quick test-auction test-hydra test-smoke

# === Configuration ===
# Experiment settings
NODE_COUNTS      ?= 24
PRODUCER_COUNTS  ?= 1 2 4 8 16 24
RF               ?= 3
COMMAND_COUNT    ?= 1
COMMAND_TYPE     ?= insert
JOIN_RATIO       ?= 0.5
TIMEOUT          ?= 120
DISTRIBUTION     ?= hydra

# Calibration settings
CALIBRATE_NODES  ?= 24
CALIBRATE_ITER   ?= 3

# Single experiment settings
NODES            ?= 24
PRODUCERS        ?= 1

# Docker resources
DOCKER_MEM       ?= 8g
DOCKER_CPUS      ?= 4

# Debug
DEBUG            ?= false

# Failure simulation settings
FAILURE_COUNT    ?= 0
FAILURE_NODES    ?=
FAILURE_WAIT     ?= 0
FAILURE_RECOVERY_TIMEOUT ?= 30

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

# Generate a list of individual iteration targets (e.g., cal_iter_1, cal_iter_2)
CAL_ITERATIONS = $(shell seq 1 $(CALIBRATE_ITER))
CAL_TARGETS = $(foreach i,$(CAL_ITERATIONS),cal_iter_$(i))

calibrate: post_calibrate

pre_calibrate:
	@echo "=== Timeout Calibration ==="
	@echo "Node count: $(CALIBRATE_NODES)"
	@echo "Iterations: $(CALIBRATE_ITER)"
	@echo "Distribution: $(DISTRIBUTION)"
	@echo ""
	@mkdir -p $(RESULTS_DIR)/calibration/run_$(TIMESTAMP)_$(DISTRIBUTION)

# Pattern rule to execute each iteration dynamically
cal_iter_%: pre_calibrate
	@i=$*; \
	echo "--- Starting Iteration $$i/$(CALIBRATE_ITER) ---"; \
	iter_dir="$(RESULTS_DIR)/calibration/run_$(TIMESTAMP)_$(DISTRIBUTION)/iter_$$i"; \
	mkdir -p "$$iter_dir"; \
	routing_wait=$$((30 + ($(CALIBRATE_NODES) - 5) * 5)); \
	docker run --rm --privileged \
		-m $(DOCKER_MEM) --cpus $(DOCKER_CPUS) \
		-v /lib/modules:/lib/modules \
		-v "$$(cd $$iter_dir && pwd)":/results \
		--entrypoint /bin/sh mini-ndn-integration \
		-c "python3 /usr/local/bin/runner.py --node-count $(CALIBRATE_NODES) --timeout 120 --replication-factor $(RF) --routing-wait $$routing_wait --results-dir /results --producer-count 1 --command-count 1 --command-rate 1 --distribution $(DISTRIBUTION)" \
		> "$$iter_dir/docker.log" 2>&1; \
	if [ -f "$$iter_dir/metadata.json" ]; then \
		echo "  ✓ Iteration $$i complete"; \
	else \
		echo "  ✗ Iteration $$i failed (no metadata.json)"; \
	fi

# Collect and analyze after all iteration targets finish
post_calibrate: $(CAL_TARGETS)
	@echo ""
	@echo "Analyzing calibration results..."
	@python3 $(HELPERS_PY) calibrate $(RESULTS_DIR)/calibration/run_$(TIMESTAMP)_$(DISTRIBUTION) \
		--node-count $(CALIBRATE_NODES) \
		--distribution $(DISTRIBUTION) \
		--output $(RESULTS_DIR)/calibration/run_$(TIMESTAMP)_$(DISTRIBUTION)/calibration.json

# === Experiments ===

# Generate a list of individual experiment targets (e.g., exp_24_1, exp_24_4)
EXP_TARGETS = $(foreach n,$(NODE_COUNTS),$(foreach p,$(PRODUCER_COUNTS),exp_$(n)_$(p)))

run: post_run

pre_run:
	@echo "=== NDN Repository Experiment Runner ==="
	@echo "Node counts: $(NODE_COUNTS)"
	@echo "Producer counts: $(PRODUCER_COUNTS)"
	@echo "Replication factor: $(RF)"
	@echo "Command count per producer: $(COMMAND_COUNT)"
	@echo ""
	@mkdir -p $(RESULTS_DIR)/run_$(TIMESTAMP)

# Pattern rule to execute each experiment dynamically
exp_%: pre_run
	@nodes="$$(echo $* | cut -d_ -f1)"; \
	prods="$$(echo $* | cut -d_ -f2)"; \
	echo "Starting experiment: $$nodes nodes, $$prods producers"; \
	$(MAKE) single NODES=$$nodes PRODUCERS=$$prods TIMESTAMP=$(TIMESTAMP) > /dev/null 2>&1 && \
		echo "  ✓ $$nodes nodes, $$prods producers" || \
		echo "  ✗ $$nodes nodes, $$prods producers (failed)"

# Collect results after all dynamic targets finish
post_run: $(EXP_TARGETS)
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
	routing_wait=$$((30 + ($(NODES) - 5) * 5)); \
	cal_file="$(RESULTS_DIR)/calibration/latest_calibration_$(DISTRIBUTION).json"; \
	if [ -f "$$cal_file" ]; then \
		safe_interval=$$(python3 -c "import json; d=json.load(open('$$cal_file')); mv=d.get('max_values',{}); print(mv.get('safe_interval_ms', mv.get('replication_time_p99_ms', mv.get('replication_time_ms', 10))))" 2>/dev/null || echo "10"); \
		cmd_rate=$$(python3 -c "import math; print(max(1, int(1000 / ($$safe_interval * 1.25))))" 2>/dev/null || echo "80"); \
	else \
		cmd_rate=80; \
	fi; \
	per_producer_rate=$$((cmd_rate / $(PRODUCERS))); \
	[ "$$per_producer_rate" -lt 1 ] && per_producer_rate=1; \
	debug_arg=""; \
	[ "$(DEBUG)" = "true" ] && debug_arg=" --debug"; \
	failure_args=""; \
	[ "$(FAILURE_COUNT)" -gt 0 ] && failure_args=" --failure-count $(FAILURE_COUNT) --failure-wait $(FAILURE_WAIT) --failure-recovery-timeout $(FAILURE_RECOVERY_TIMEOUT)"; \
	[ -n "$(FAILURE_NODES)" ] && failure_args="$$failure_args --failure-nodes '$(FAILURE_NODES)'"; \
	docker run --rm --privileged \
		-m $(DOCKER_MEM) --cpus $(DOCKER_CPUS) \
		-v /lib/modules:/lib/modules \
		-v "$$result_dir":/results \
		--entrypoint /bin/sh mini-ndn-integration \
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
			--join-ratio $(JOIN_RATIO) \
			--distribution $(DISTRIBUTION)$${debug_arg}$${failure_args}" \
   		> "$$result_dir/docker.log" 2>&1; \
	echo ""; \
	echo "=== Analyzing Results ==="; \
	python3 $(HELPERS_PY) collect "$$result_dir" --show

# === Debugging ===

shell:
	@echo "Starting interactive Docker shell with $(NODES) nodes..."
	@tmp_dir=$$(mktemp -d); \
	routing_wait=$$((30 + ($(NODES) - 5) * 5)); \
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

# === Fix Verification Tests ===

TEST_RESULTS_DIR := $(RESULTS_DIR)/test_fix

test: test-smoke test-auction test-hydra
	@echo ""
	@echo "=== All Fix Tests Passed ==="

test-smoke:
	@echo "=== Running Smoke Test (4 nodes, auction) ==="
	@mkdir -p $(TEST_RESULTS_DIR)/smoke
	@docker run --rm --privileged -m 4g --cpus 2 \
		-v /lib/modules:/lib/modules \
		-v "$(shell pwd)/$(TEST_RESULTS_DIR)/smoke":/results \
		--entrypoint /bin/sh mini-ndn-integration \
		-c "python3 /usr/local/bin/runner.py \
			--node-count 4 \
			--timeout 60 \
			--replication-factor 3 \
			--routing-wait 30 \
			--results-dir /results \
			--producer-count 1 \
			--command-count 10 \
			--command-rate 4 \
			--command-type insert \
			--join-ratio 0.5 \
			--distribution auction" \
		> $(TEST_RESULTS_DIR)/smoke/docker.log 2>&1
	@if grep -q "TEST PASSED" $(TEST_RESULTS_DIR)/smoke/docker.log; then \
		echo "  ✓ Smoke test passed"; \
		grep -h "heartbeat_received" $(TEST_RESULTS_DIR)/smoke/events-*.jsonl 2>/dev/null | wc -l | xargs -I {} echo "    Heartbeat events: {}"; \
		grep -h "node_failure_detected" $(TEST_RESULTS_DIR)/smoke/events-*.jsonl 2>/dev/null | wc -l | xargs -I {} echo "    Node failures: {}"; \
		echo ""; \
		echo "=== Analyzing Results ==="; \
		python3 $(HELPERS_PY) collect $(TEST_RESULTS_DIR)/smoke --show; \
	else \
		echo "  ✗ Smoke test failed"; \
		tail -20 $(TEST_RESULTS_DIR)/smoke/docker.log; \
		exit 1; \
	fi

test-quick: test-smoke
	@echo "=== Quick test passed ==="

test-auction:
	@echo "=== Running Auction Test (24 nodes, 24 producers) ==="
	@mkdir -p $(TEST_RESULTS_DIR)/auction_24
	@docker run --rm --privileged -m 8g --cpus 4 \
		-v /lib/modules:/lib/modules \
		-v "$(shell pwd)/$(TEST_RESULTS_DIR)/auction_24":/results \
		--entrypoint /bin/sh mini-ndn-integration \
		-c "python3 /usr/local/bin/runner.py \
			--node-count 24 \
			--timeout 120 \
			--replication-factor 3 \
			--routing-wait 125 \
			--results-dir /results \
			--producer-count 24 \
			--command-count 20 \
			--command-rate 4 \
			--command-type insert \
			--join-ratio 0.5 \
			--distribution auction" \
		> $(TEST_RESULTS_DIR)/auction_24/docker.log 2>&1
	@if grep -q "TEST PASSED" $(TEST_RESULTS_DIR)/auction_24/docker.log; then \
		echo "  ✓ Auction test passed"; \
		echo ""; \
		echo "=== Analyzing Results ==="; \
		python3 $(HELPERS_PY) collect $(TEST_RESULTS_DIR)/auction_24 --show; \
	else \
		echo "  ✗ Auction test failed"; \
		tail -20 $(TEST_RESULTS_DIR)/auction_24/docker.log; \
		exit 1; \
	fi

test-hydra:
	@echo "=== Running Hydra Test (24 nodes, 24 producers) ==="
	@mkdir -p $(TEST_RESULTS_DIR)/hydra_24
	@docker run --rm --privileged -m 8g --cpus 4 \
		-v /lib/modules:/lib/modules \
		-v "$(shell pwd)/$(TEST_RESULTS_DIR)/hydra_24":/results \
		--entrypoint /bin/sh mini-ndn-integration \
		-c "python3 /usr/local/bin/runner.py \
			--node-count 24 \
			--timeout 120 \
			--replication-factor 3 \
			--routing-wait 125 \
			--results-dir /results \
			--producer-count 24 \
			--command-count 20 \
			--command-rate 4 \
			--command-type insert \
			--join-ratio 0.5 \
			--distribution hydra" \
		> $(TEST_RESULTS_DIR)/hydra_24/docker.log 2>&1
	@if grep -q "TEST PASSED" $(TEST_RESULTS_DIR)/hydra_24/docker.log; then \
		echo "  ✓ Hydra test passed"; \
		echo ""; \
		echo "=== Analyzing Results ==="; \
		python3 $(HELPERS_PY) collect $(TEST_RESULTS_DIR)/hydra_24 --show; \
	else \
		echo "  ✗ Hydra test failed"; \
		tail -20 $(TEST_RESULTS_DIR)/hydra_24/docker.log; \
		exit 1; \
	fi

# === Help ===

help:
	@echo "NDN Repository Experiments"
	@echo ""
	@echo "Usage: make [target] [VARIABLE=value ...]"
	@echo ""
	@echo "Targets:"
	@echo "  build           Build Go binaries + copy keys + build Docker image"
	@echo "  keys            Copy NDN keys (~/.ndn/keys -> ./keys)"
	@echo "  image           Build Docker image only"
	@echo "  calibrate       Run timeout calibration"
	@echo "  run             Run experiment suite (use -j for parallel)"
	@echo "  single          Run single experiment"
	@echo "  shell           Interactive Docker shell for debugging"
	@echo "  results         Show summary of last run"
	@echo "  compare         Full comparison: build + run both distributions + analyze"
	@echo "  compare-run     Run both hydra and auction experiments in parallel (-j4)"
	@echo "  compare-analyze Analyze comparison results"
	@echo "  clean           Remove results (keep calibration)"
	@echo "  clean-all       Remove all results + Docker cleanup"
	@echo "  test            Run all fix verification tests (smoke + auction + hydra)"
	@echo "  test-quick      Run quick smoke test only (4 nodes, auction)"
	@echo "  test-smoke      Run smoke test (4 nodes, auction)"
	@echo "  test-auction    Run auction test (24 nodes, 24 producers)"
	@echo "  test-hydra      Run hydra test (24 nodes, 24 producers)"
	@echo "  experiment-full Run full 24-node suite: hydra + auction with 1/2/4/8/16/24 producers (includes analysis)"
	@echo "  help            Show this help"
	@echo ""
	@echo "Variables:"
	@echo "  NODE_COUNTS      Node counts for 'run' (default: 24)"
	@echo "  PRODUCER_COUNTS  Producer counts for 'run' (default: 1 2 4 8 16 24)"
	@echo "  RF               Replication factor (default: 3)"
	@echo "  COMMAND_COUNT    Commands per producer (default: 1)"
	@echo "  COMMAND_TYPE     Command type: insert, join, both (default: insert)"
	@echo "  TIMEOUT          Timeout per experiment in seconds (default: 120)"
	@echo "  DISTRIBUTION     Distribution mechanism: hydra, auction (default: hydra)"
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
	@echo ""
	@echo "Comparison targets (Hydra vs Auction):"
	@echo "  make compare                    # Full workflow: build + run + analyze"
	@echo "  make compare-run               # Run both distributions in parallel"
	@echo "  make compare-analyze           # Analyze existing comparison results"
	@echo "  make compare COMPARE_PRODUCER_COUNTS=\"1 2 4\"  # Custom producer counts"

# === Hydra vs Auction Comparison ===

COMPARE_PRODUCER_COUNTS ?= 1 2 4 8 16 24
COMPARE_DIR := $(RESULTS_DIR)/compare_$(TIMESTAMP)

compare: build compare-run compare-analyze
	@echo ""
	@echo "=== Comparison complete ==="
	@echo "Results saved to: $(RESULTS_DIR)/compare_$(TIMESTAMP)"

compare-run:
	@echo "=== Hydra vs Auction Comparison Run ==="
	@echo "Producer counts: $(COMPARE_PRODUCER_COUNTS)"
	@echo ""
	$(eval BASE_TS := $(shell date +%Y%m%d_%H%M%S))
	$(eval HYDRA_TS := $(BASE_TS)_hydra)
	$(eval AUCTION_TS := $(BASE_TS)_auction)
	@mkdir -p $(COMPARE_DIR)
	@echo "$(HYDRA_TS)" > $(COMPARE_DIR)/.hydra_ts
	@echo "$(AUCTION_TS)" > $(COMPARE_DIR)/.auction_ts
	@echo "Starting Hydra experiments (timestamp: $(HYDRA_TS))..."
	@$(MAKE) run DISTRIBUTION=hydra PRODUCER_COUNTS="$(COMPARE_PRODUCER_COUNTS)" TIMESTAMP=$(HYDRA_TS) -j4 &
	@echo "Starting Auction experiments (timestamp: $(AUCTION_TS))..."
	@$(MAKE) run DISTRIBUTION=auction PRODUCER_COUNTS="$(COMPARE_PRODUCER_COUNTS)" TIMESTAMP=$(AUCTION_TS) -j4 &
	@wait
	@echo ""
	@echo "=== Both distribution runs complete ==="

compare-analyze:
	@echo "=== Analyzing Comparison Results ==="
	@if [ ! -f $(COMPARE_DIR)/.hydra_ts ] || [ ! -f $(COMPARE_DIR)/.auction_ts ]; then \
		echo "Error: Run 'make compare-run' first, or specify TIMESTAMP=<run_timestamp>"; \
		echo "Example: make compare-analyze TIMESTAMP=20260330_120000"; \
		exit 1; \
	fi
	@HYDRA_TS=$$(cat $(COMPARE_DIR)/.hydra_ts) && \
	AUCTION_TS=$$(cat $(COMPARE_DIR)/.auction_ts) && \
	echo "Hydra timestamp: $$HYDRA_TS" && \
	echo "Auction timestamp: $$AUCTION_TS" && \
	echo "" && \
	echo "Collecting Hydra results..." && \
	python3 $(MAKEFILE_DIR)/helpers.py collect $(RESULTS_DIR)/run_$$HYDRA_TS \
		--csv $(COMPARE_DIR)/hydra_summary.csv \
		--md $(COMPARE_DIR)/hydra_results.md && \
	echo "Collecting Auction results..." && \
	python3 $(MAKEFILE_DIR)/helpers.py collect $(RESULTS_DIR)/run_$$AUCTION_TS \
		--csv $(COMPARE_DIR)/auction_summary.csv \
		--md $(COMPARE_DIR)/auction_results.md && \
	echo "" && \
	python3 -c "import pandas as pd; from datetime import datetime; \
hydra=pd.read_csv('$(COMPARE_DIR)/hydra_summary.csv'); \
auction=pd.read_csv('$(COMPARE_DIR)/auction_summary.csv'); \
target=[1,2,4,8,16,24]; \
hydra=hydra[hydra['producers'].isin(target)].sort_values('producers'); \
auction=auction[auction['producers'].isin(target)].sort_values('producers'); \
print('='*80); \
print('HYDRA vs AUCTION COMPARISON'); \
print(f'Generated: {datetime.now().strftime(\"%Y-%m-%d %H:%M:%S\")}'); \
print('='*80); \
print(); \
print(f'{\"Producers\":>10} | {\"Dist\":>8} | {\"RepMed(ms)\":>10} | {\"RepMed(RTT)\":>10} | {\"Sync/Cmd\":>8} | {\"AtRF\":>6}'); \
print('-'*80); \
for p in target: \
  h=hydra[hydra['producers']==p].iloc[0] if p in hydra['producers'].values else None; \
  a=auction[auction['producers']==p].iloc[0] if p in auction['producers'].values else None; \
  if h is not None: print(f'{p:>10} | {\"HYDRA\":>8} | {h[\"rep_med_ms\"]:>10.1f} | {h[\"rep_med_rtt\"]:>10.2f} | {h[\"sync_per_cmd\"]:>8.1f} | {h[\"commands_at_rf\"]:>6}'); \
  if a is not None: print(f'{p:>10} | {\"AUCTION\":>8} | {a[\"rep_med_ms\"]:>10.1f} | {a[\"rep_med_rtt\"]:>10.2f} | {a[\"sync_per_cmd\"]:>8.1f} | {a[\"commands_at_rf\"]:>6}'); \
  print()"
	@echo ""
	@echo "Analysis complete. Results saved to: $(COMPARE_DIR)"

# === Full 24-Node Experiment Suite ===

experiment-full: build
	@echo "=== Full 24-Node Experiment Suite ==="
	@echo "Producers: 1 2 4 8 16 24"
	@echo "Distributions: hydra, auction"
	@echo "Parallel jobs: 8 (4 per distribution)"
	@echo ""
	@BASE_TS=$$(date +%Y%m%d_%H%M%S) && \
	HYDRA_TS=$${BASE_TS}_hydra && \
	AUCTION_TS=$${BASE_TS}_auction && \
	COMPARE_DIR="$(RESULTS_DIR)/compare_$${BASE_TS}" && \
	ANALYSIS_DIR="$(RESULTS_DIR)/analysis_$${BASE_TS}" && \
	echo "Hydra timestamp: $$HYDRA_TS" && \
	echo "Auction timestamp: $$AUCTION_TS" && \
	echo "" && \
	echo "Starting Hydra experiments..." && \
	$(MAKE) run DISTRIBUTION=hydra NODE_COUNTS=24 PRODUCER_COUNTS="1 2 4 8 16 24" TIMESTAMP=$$HYDRA_TS -j4 & \
	echo "Starting Auction experiments..." && \
	$(MAKE) run DISTRIBUTION=auction NODE_COUNTS=24 PRODUCER_COUNTS="1 2 4 8 16 24" TIMESTAMP=$$AUCTION_TS -j4 & \
	wait && \
	echo "" && \
	echo "=== Running Trace Analysis ===" && \
	mkdir -p "$$ANALYSIS_DIR" && \
	python3 $(MAKEFILE_DIR)/analyze_traces.py \
		--experiments \
			$(RESULTS_DIR)/run_$$HYDRA_TS/nodes_24_producers_1 \
			$(RESULTS_DIR)/run_$$HYDRA_TS/nodes_24_producers_2 \
			$(RESULTS_DIR)/run_$$HYDRA_TS/nodes_24_producers_4 \
			$(RESULTS_DIR)/run_$$HYDRA_TS/nodes_24_producers_8 \
			$(RESULTS_DIR)/run_$$HYDRA_TS/nodes_24_producers_16 \
			$(RESULTS_DIR)/run_$$HYDRA_TS/nodes_24_producers_24 \
			$(RESULTS_DIR)/run_$$AUCTION_TS/nodes_24_producers_1 \
			$(RESULTS_DIR)/run_$$AUCTION_TS/nodes_24_producers_2 \
			$(RESULTS_DIR)/run_$$AUCTION_TS/nodes_24_producers_4 \
			$(RESULTS_DIR)/run_$$AUCTION_TS/nodes_24_producers_8 \
			$(RESULTS_DIR)/run_$$AUCTION_TS/nodes_24_producers_16 \
			$(RESULTS_DIR)/run_$$AUCTION_TS/nodes_24_producers_24 \
		--output-dir "$$ANALYSIS_DIR" \
		--format json csv markdown && \
	echo "" && \
	echo "=== Experiment Suite Complete ===" && \
	echo "Hydra results: $(RESULTS_DIR)/run_$$HYDRA_TS" && \
	echo "Auction results: $(RESULTS_DIR)/run_$$AUCTION_TS" && \
	echo "Analysis: $$ANALYSIS_DIR"
