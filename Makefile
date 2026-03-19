BINARY := finder
BENCH_VALUE := 12345678
BENCH_FILE  := sample.bin
BLOCK_SIZE := 1048576

.PHONY: fio build run drop-cache gen-sample bench-sequential bench-mmap bench-async

FIO_CMD = fio --name=int-search --filename=$(BENCH_FILE) --direct=1 --rw=read --ioengine=libaio --bs=1M --numjobs=1 --group_reporting --iodepth={depth}

fio:
	hyperfine \
		--prepare 'sync && echo 3 | sudo tee /proc/sys/vm/drop_caches > /dev/null' \
		--max-runs 10 \
		--export-markdown bench-fio.md \
 		--parameter-scan depth 8 32 \
 		--parameter-step-size 8 \
		'$(FIO_CMD)'

drop-cache:
	sync && echo 3 | sudo tee /proc/sys/vm/drop_caches

build:
	go build -o $(BINARY) .

gen-sample: 
	dd if=/dev/urandom of=$(BENCH_FILE) bs=1M count=60000 status=progress

run: build drop-cache
	./$(BINARY) -file $(BENCH_FILE) -value $(BENCH_VALUE) -block-size $(BLOCK_SIZE) -method async

run-profile: build drop-cache
	GODEBUG=gctrace=1 ./$(BINARY) -file $(BENCH_FILE) -value $(BENCH_VALUE) -block-size $(BLOCK_SIZE) -method async -cpuprofile cpu.prof -memprofile mem.prof -traceprofile trace.out

bench-sequential: build
	hyperfine \
		--prepare 'sync && echo 3 | sudo tee /proc/sys/vm/drop_caches > /dev/null' \
		--max-runs 5 \
		--export-markdown bench-sequential.md \
		'./$(BINARY) -file $(BENCH_FILE) -value $(BENCH_VALUE) -block-size $(BLOCK_SIZE) -method sequential'

bench-mmap: build
	hyperfine \
		--prepare 'sync && echo 3 | sudo tee /proc/sys/vm/drop_caches > /dev/null' \
		--max-runs 5 \
		--export-markdown bench-mmap.md \
		'./$(BINARY) -file $(BENCH_FILE) -value $(BENCH_VALUE) -block-size $(BLOCK_SIZE) -method mmap'
		
		
bench-async: build
	hyperfine \
		--prepare 'sync && echo 3 | sudo tee /proc/sys/vm/drop_caches > /dev/null' \
		--max-runs 5 \
		--export-markdown bench-async.md \
		'./$(BINARY) -file $(BENCH_FILE) -value $(BENCH_VALUE) -block-size $(BLOCK_SIZE) -method async'