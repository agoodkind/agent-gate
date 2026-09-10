#include <stdint.h>

struct process_usage {
    uint64_t cpu_nanoseconds;
    uint64_t disk_bytes_written;
    uint32_t timebase_numer;
    uint32_t timebase_denom;
};

int read_process_usage(struct process_usage *result);
