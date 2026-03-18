| Command | Mean [s] | Min [s] | Max [s] | Relative |
|:---|---:|---:|---:|---:|
| `fio --name=int-search --filename=sample.bin --direct=1 --rw=read --ioengine=libaio --bs=1M --numjobs=1 --group_reporting --iodepth=8` | 14.520 ± 0.137 | 14.424 | 14.617 | 1.27 ± 0.01 |
| `fio --name=int-search --filename=sample.bin --direct=1 --rw=read --ioengine=libaio --bs=1M --numjobs=1 --group_reporting --iodepth=16` | 11.434 ± 0.040 | 11.406 | 11.462 | 1.00 |
| `fio --name=int-search --filename=sample.bin --direct=1 --rw=read --ioengine=libaio --bs=1M --numjobs=1 --group_reporting --iodepth=24` | 11.679 ± 0.173 | 11.557 | 11.802 | 1.02 ± 0.02 |
| `fio --name=int-search --filename=sample.bin --direct=1 --rw=read --ioengine=libaio --bs=1M --numjobs=1 --group_reporting --iodepth=32` | 12.058 ± 0.031 | 12.037 | 12.080 | 1.05 ± 0.00 |
