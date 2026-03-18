package main

import (
	"context"
	"encoding/binary"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"runtime"
	"runtime/pprof"
	"sync"
	"sync/atomic"
	"syscall"
)

type searchFunc func(f *os.File, target uint64, blockSize int) bool

func findSequential(f *os.File, target uint64, blockSize int) bool {
	buf := make([]byte, blockSize)
	for {
		n, err := io.ReadFull(f, buf)
		for i := 0; i+8 <= n; i += 8 {
			if binary.LittleEndian.Uint64(buf[i:]) == target {
				return true
			}
		}
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			return false
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "read error: %v\n", err)
			os.Exit(1)
		}
	}
}

// findMmap scans the file using a pipelined sliding mmap window.
// A producer goroutine maps windows and calls madvise(MADV_SEQUENTIAL) to
// trigger async kernel readahead, then sends each region over a buffered
// channel. This lets the OS fetch window N+1 from disk while the scanner
// is still processing window N, maximising disk throughput.
// After scanning each window, MADV_DONTNEED releases the page-cache pages
// before Munmap so memory pressure stays bounded on large files.
func findMmap(f *os.File, target uint64, blockSize int) bool {
	fi, err := f.Stat()
	if err != nil {
		fmt.Fprintf(os.Stderr, "stat error: %v\n", err)
		os.Exit(1)
	}
	fileSize := fi.Size()
	if fileSize == 0 {
		return false
	}

	pageSize := int64(os.Getpagesize())
	windowSize := (int64(blockSize) + pageSize - 1) &^ (pageSize - 1)

	type mappedWindow struct {
		data []byte
		err  error
	}

	// Buffer depth 2: producer maps and prefetches window N+1 while the
	// scanner is still working on window N.
	const pipelineDepth = 2
	windows := make(chan mappedWindow, pipelineDepth)
	cancel := make(chan struct{})

	go func() {
		defer close(windows)
		for offset := int64(0); offset < fileSize; offset += windowSize {
			select {
			case <-cancel:
				return
			default:
			}
			mapLen := windowSize
			if offset+mapLen > fileSize {
				mapLen = fileSize - offset
			}
			data, err := syscall.Mmap(int(f.Fd()), offset, int(mapLen), syscall.PROT_READ, syscall.MAP_SHARED)
			if err != nil {
				windows <- mappedWindow{err: err}
				return
			}
			// Hint the kernel to read this window ahead aggressively.
			syscall.Madvise(data, syscall.MADV_SEQUENTIAL)
			windows <- mappedWindow{data: data}
		}
	}()

	for w := range windows {
		if w.err != nil {
			fmt.Fprintf(os.Stderr, "mmap error: %v\n", w.err)
			os.Exit(1)
		}
		found := false
		for i := 0; i+8 <= len(w.data); i += 8 {
			if binary.LittleEndian.Uint64(w.data[i:]) == target {
				found = true
				break
			}
		}
		// Release page-cache pages before unmapping to bound memory pressure.
		syscall.Madvise(w.data, syscall.MADV_DONTNEED)
		syscall.Munmap(w.data)
		if found {
			close(cancel)
			// Drain buffered windows so the producer goroutine can exit.
			for remaining := range windows {
				if remaining.data != nil {
					syscall.Madvise(remaining.data, syscall.MADV_DONTNEED)
					syscall.Munmap(remaining.data)
				}
			}
			return true
		}
	}
	return false
}


// findAsync keeps queueDepth read requests perpetually in flight by using a
// counting semaphore that is acquired before issuing a ReadAt and released by
// the searcher only after the chunk is scanned. This means a new read starts
// the instant a scan slot frees up, not when the results channel drains,
// keeping the disk queue saturated regardless of scan speed.
func findAsync(f *os.File, target uint64, blockSize int) bool {
	const queueDepth = 32

	fi, err := f.Stat()
	if err != nil {
		fmt.Fprintf(os.Stderr, "stat error: %v\n", err)
		os.Exit(1)
	}
	fileSize := fi.Size()
	if fileSize == 0 {
		return false
	}

	pageSize := int64(os.Getpagesize())
	windowSize := (int64(blockSize) + pageSize - 1) &^ (pageSize - 1)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var nextOffset atomic.Int64

	bufPool := &sync.Pool{New: func() any {
		b := make([]byte, windowSize)
		return &b
	}}

	type chunk struct {
		bufPtr *[]byte
		n      int
	}

	// sem tracks how many reads are in-flight or waiting in the chunks channel.
	// Acquired before ReadAt; released by the searcher after scanning.
	// This guarantees a new read is issued the moment a scan slot opens,
	// keeping exactly queueDepth reads outstanding at all times.
	sem := make(chan struct{}, queueDepth)

	// Sized to queueDepth: at most queueDepth items can ever be buffered here
	// (one per held semaphore slot), so readers never block on this send.
	chunks := make(chan chunk, queueDepth)

	var readerWg sync.WaitGroup
	for i := 0; i < queueDepth; i++ {
		readerWg.Add(1)
		go func() {
			defer readerWg.Done()
			for {
				// Acquire a slot before claiming an offset so goroutines
				// cannot race ahead and pre-consume offsets without reading.
				select {
				case sem <- struct{}{}:
				case <-ctx.Done():
					return
				}
				offset := nextOffset.Add(windowSize) - windowSize
				if offset >= fileSize {
					<-sem // no read will be issued; release the slot
					return
				}
				readLen := windowSize
				if offset+readLen > fileSize {
					readLen = fileSize - offset
				}
				bufPtr := bufPool.Get().(*[]byte)
				n, err := f.ReadAt((*bufPtr)[:readLen], offset)
				if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
					fmt.Fprintf(os.Stderr, "read error: %v\n", err)
					os.Exit(1)
				}
				select {
				case chunks <- chunk{bufPtr: bufPtr, n: n}:
				case <-ctx.Done():
					bufPool.Put(bufPtr)
					<-sem
					return
				}
			}
		}()
	}

	go func() {
		readerWg.Wait()
		close(chunks)
	}()

	var found atomic.Bool
	var searchWg sync.WaitGroup
	log.Printf("CPU used: %d", runtime.NumCPU())
	for i := 0; i < runtime.NumCPU(); i++ {
		searchWg.Add(1)
		go func() {
			defer searchWg.Done()
			for c := range chunks {
				data := (*c.bufPtr)[:c.n]
				localFound := false
				for j := 0; j+8 <= len(data); j += 8 {
					if binary.LittleEndian.Uint64(data[j:]) == target {
						localFound = true
						break
					}
				}
				bufPool.Put(c.bufPtr)
				<-sem // release slot — immediately unblocks the next ReadAt
				if localFound {
					found.Store(true)
					cancel()
					return
				}
			}
		}()
	}

	searchWg.Wait()
	return found.Load()
}

var methods = map[string]searchFunc{
	"sequential": findSequential,
	"mmap":       findMmap,
	"async":      findAsync,
}

func main() {
	cpuprofile := flag.String("cpuprofile", "", "write cpu profile to `file`")
	memprofile := flag.String("memprofile", "", "write memory profile to `file`")

	file := flag.String("file", "", "path to the binary file to search (required)")
	value := flag.Uint64("value", 0, "uint64 value to search for (required)")
	blockSize := flag.Int("block-size", 512, "read block size in bytes (must be a multiple of 8)")
	method := flag.String("method", "sequential", "search method to use (sequential, mmap, async)")
	flag.Parse()

	if *file == "" {
		fmt.Fprintf(os.Stderr, "error: -file is required\n")
		flag.Usage()
		os.Exit(1)
	}
	if *blockSize < 8 || *blockSize%8 != 0 {
		fmt.Fprintf(os.Stderr, "error: -block-size must be a multiple of 8 and >= 8\n")
		os.Exit(1)
	}
	search, ok := methods[*method]
	if !ok {
		fmt.Fprintf(os.Stderr, "error: unknown method %q\n", *method)
		os.Exit(1)
	}
	if *cpuprofile != "" {
		f, err := os.Create(*cpuprofile)
		if err != nil {
			log.Fatal("could not create CPU profile: ", err)
		}
		defer f.Close()
		if err := pprof.StartCPUProfile(f); err != nil {
			log.Fatal("could not start CPU profile: ", err)
		}
		defer pprof.StopCPUProfile()
	}

	f, err := os.Open(*file)
	if err != nil {
		fmt.Print(err)
		os.Exit(1)
	}
	defer f.Close()

	if search(f, *value, *blockSize) {
		fmt.Println("found value!")
	}
	if *memprofile != "" {
		f, err := os.Create(*memprofile)
		if err != nil {
			log.Fatal("could not create memory profile: ", err)
		}
		defer f.Close()
		runtime.GC()    // get up-to-date statistics
		if err := pprof.Lookup("allocs").WriteTo(f, 0); err != nil {
			log.Fatal("could not write memory profile: ", err)
		}
	}
}
