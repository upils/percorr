# finder

A tool for searching a `uint64` value in a binary file containing an unsorted array of integers that doesn't fit in memory. The goal is to approach the theoretical maximum sequential read throughput of the underlying storage device.

## Usage

```
./finder -file <path> -value <uint64> [-block-size <bytes>] [-method <name>]
```

| Flag | Default | Description |
|---|---|---|
| `-file` | _(required)_ | Path to the binary file to search |
| `-value` | _(required)_ | `uint64` value to search for |
| `-block-size` | `512` | Read window size in bytes (must be a multiple of 8) |
| `-method` | `sequential` | Search method: `sequential`, `mmap`, or `async` |
| `-cpuprofile` | | Write a CPU pprof profile to this file |
| `-memprofile` | | Write a memory pprof profile to this file |

Exit prints `found value!` if the value is present; nothing otherwise.

## Search methods

### `sequential`

Single-threaded buffered read using `io.ReadFull`. Reads one block at a time and scans it before issuing the next read. Simple baseline.

### `mmap`

Sliding `mmap` window with a two-slot pipeline. A producer goroutine maps each window and calls `madvise(MADV_SEQUENTIAL)` to trigger async kernel readahead, then pushes it into a channel. This allows the OS to prefetch window N+1 from disk while the scanner processes window N. `madvise(MADV_DONTNEED)` + `munmap` after each window bounds page-cache memory usage regardless of file size.

### `async`

Designed to keep the disk queue saturated at all times.

- **32 reader goroutines** each call `ReadAt` (→ `pread(2)`) concurrently, so up to 32 I/O requests are always in flight at the kernel level.
- A **counting semaphore** (`cap = 32`) is acquired *before* issuing a read and released *after* the chunk is scanned. This ensures a new read starts the instant a CPU scan slot opens — not when the results channel drains — so the disk queue never empties due to scan latency.
- **`NumCPU` search workers** drain the results channel in parallel. The scan is memory-bandwidth-bound, so one worker per logical core is optimal.
- A `sync.Pool` of fixed-size buffers avoids per-chunk heap allocations under concurrency.

## Benchmarking

All benchmark targets drop the OS page cache before each run via `--prepare` so measurements reflect actual disk I/O, not cache hits.

```sh
# Theoretical maximum: sweep I/O queue depth with fio (O_DIRECT, libaio)
make fio

# Per-method benchmarks (exports Markdown results table)
make bench-sequential
make bench-mmap
make bench-async
```

Sweep block sizes to find the optimal value:

```sh
hyperfine \
  --prepare 'sync && echo 3 | sudo tee /proc/sys/vm/drop_caches > /dev/null' \
  --parameter-list bs 65536,131072,524288,1048576,4194304 \
  './finder -file sample.bin -value 12345678 -block-size {bs} -method async'
```

## Generating a test file

```sh
# Large realistic file (60 GiB of random data, value appended at the end)
make gen-sample
```

## Build

```sh
make build   # produces ./finder
make run     # build + drop cache + run
```

Requires Go 1.21+, Linux (uses `syscall.Mmap` / `madvise`).
