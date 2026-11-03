//go:build auditqualification

#include "usage_darwin.h"
#include <errno.h>
#include <libproc.h>
#include <mach/mach_time.h>
#include <sys/resource.h>
#include <unistd.h>

int read_process_usage(int pid, struct process_usage *result) {
    struct rusage_info_v4 usage;
    mach_timebase_info_data_t timebase;
    if (proc_pid_rusage(pid, RUSAGE_INFO_V4, (rusage_info_t *)&usage) != 0) {
        return errno;
    }
    if (mach_timebase_info(&timebase) != KERN_SUCCESS) {
        return EINVAL;
    }
    if (timebase.denom == 0) {
        return EINVAL;
    }
    result->cpu_nanoseconds = (uint64_t)(((__uint128_t)usage.ri_user_time +
        usage.ri_system_time) * timebase.numer / timebase.denom);
    result->disk_bytes_written = usage.ri_diskio_byteswritten;
    result->resident_bytes = usage.ri_resident_size;
    result->physical_footprint_bytes = usage.ri_phys_footprint;
    result->timebase_numer = timebase.numer;
    result->timebase_denom = timebase.denom;
    return 0;
}
