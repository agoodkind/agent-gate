package regex

/*
#cgo pkg-config: libpcre2-8
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <stdint.h>
#define PCRE2_CODE_UNIT_WIDTH 8
#include <pcre2.h>

typedef struct pcre2_regex_handle {
	pcre2_code* code;
	pcre2_match_data* match_data;
	pcre2_match_context* match_context;
} pcre2_regex_handle;

static pcre2_regex_handle* pcre2_compile_handle(
	const char* pattern,
	PCRE2_SIZE pattern_len,
	uint32_t match_limit,
	uint32_t depth_limit,
	char* errbuf,
	PCRE2_SIZE errbuf_len
) {
	int error_code;
	PCRE2_SIZE error_offset;
	pcre2_regex_handle* h = (pcre2_regex_handle*)calloc(1, sizeof(pcre2_regex_handle));

	if (h == NULL) {
		return NULL;
	}

	h->code = pcre2_compile(
		(PCRE2_SPTR)pattern,
		pattern_len,
		PCRE2_UTF | PCRE2_UCP,
		&error_code,
		&error_offset,
		NULL
	);
	if (h->code == NULL) {
		if (errbuf != NULL && errbuf_len > 0) {
			errbuf[0] = '\0';
			pcre2_get_error_message(error_code, (PCRE2_UCHAR*)errbuf, errbuf_len);
		}
		free(h);
		return NULL;
	}

	h->match_data = pcre2_match_data_create_from_pattern(h->code, NULL);
	if (h->match_data == NULL) {
		pcre2_code_free(h->code);
		free(h);
		return NULL;
	}

	h->match_context = pcre2_match_context_create(NULL);
	if (h->match_context == NULL) {
		pcre2_match_data_free(h->match_data);
		pcre2_code_free(h->code);
		free(h);
		return NULL;
	}

	pcre2_set_match_limit_8(h->match_context, match_limit);
	pcre2_set_depth_limit_8(h->match_context, depth_limit);

	{
		int jit_rc = pcre2_jit_compile(h->code, PCRE2_JIT_COMPLETE);

		if (jit_rc < 0 && jit_rc != PCRE2_ERROR_JIT_BADOPTION && jit_rc != PCRE2_ERROR_BADDATA) {
			// JIT is optional; the interpreter still runs without it.
			(void)jit_rc;
		}
	}

	return h;
}

static void pcre2_free_handle(pcre2_regex_handle* h) {
	if (h == NULL) {
		return;
	}
	if (h->match_context != NULL) {
		pcre2_match_context_free(h->match_context);
	}
	if (h->match_data != NULL) {
		pcre2_match_data_free(h->match_data);
	}
	if (h->code != NULL) {
		pcre2_code_free(h->code);
	}
	free(h);
}

// Returns 1 matched, 0 no match, or a negative PCRE2 error code.
static int pcre2_match_handle(
	pcre2_regex_handle* h,
	const char* subject,
	PCRE2_SIZE subject_len,
	PCRE2_SIZE start_offset
) {
	int rc;

	if (h == NULL || h->code == NULL || h->match_data == NULL) {
		return -2;
	}

	rc = pcre2_match(
		h->code,
		(PCRE2_SPTR)subject,
		subject_len,
		start_offset,
		0,
		h->match_data,
		h->match_context
	);
	if (rc == PCRE2_ERROR_NOMATCH) {
		return 0;
	}
	if (rc < 0) {
		return rc;
	}
	return 1;
}

static uint32_t pcre2_handle_capture_count(pcre2_regex_handle* h) {
	uint32_t cap = 0;

	if (h == NULL || h->code == NULL) {
		return 0;
	}
	(void)pcre2_pattern_info(h->code, PCRE2_INFO_CAPTURECOUNT, &cap);
	return cap;
}

static int pcre2_handle_group_bounds(
	pcre2_regex_handle* h,
	uint32_t group,
	PCRE2_SIZE* out_start,
	PCRE2_SIZE* out_end
) {
	PCRE2_SIZE* ovec;
	uint32_t ovec_count;

	if (h == NULL || h->match_data == NULL || out_start == NULL || out_end == NULL) {
		return -2;
	}

	ovec = pcre2_get_ovector_pointer(h->match_data);
	ovec_count = pcre2_get_ovector_count(h->match_data);
	// ovec_count is the number of (start,end) pairs, not PCRE2_SIZE slots.
	if (group >= ovec_count) {
		return -1;
	}

	*out_start = ovec[group * 2];
	*out_end = ovec[group * 2 + 1];
	return 0;
}

static int pcre2_group_is_unset(PCRE2_SIZE start, PCRE2_SIZE end) {
	return (start == PCRE2_UNSET) || (end == PCRE2_UNSET);
}

static void pcre2_format_match_error(int rc, char* buf, PCRE2_SIZE buf_len) {
	if (buf == NULL || buf_len == 0) {
		return;
	}
	buf[0] = '\0';
	pcre2_get_error_message(rc, (PCRE2_UCHAR*)buf, buf_len);
}
*/
import "C"

import (
	"fmt"
	"unsafe"
)

func compileHandle(pattern string, matchLimit, depthLimit uint32) (*C.pcre2_regex_handle, error) {
	cPattern := C.CString(pattern)
	defer C.free(unsafe.Pointer(cPattern))

	var errBuf [256]C.char

	handle := C.pcre2_compile_handle(
		cPattern,
		C.PCRE2_SIZE(len(pattern)),
		C.uint32_t(matchLimit),
		C.uint32_t(depthLimit),
		(*C.char)(unsafe.Pointer(&errBuf[0])),
		C.PCRE2_SIZE(len(errBuf)),
	)
	if handle == nil {
		msg := C.GoString((*C.char)(unsafe.Pointer(&errBuf[0])))
		if msg == "" {
			msg = "unknown compile error"
		}

		return nil, fmt.Errorf("compile pattern %q: %s", pattern, msg)
	}

	return handle, nil
}

func freeHandle(handle *C.pcre2_regex_handle) {
	if handle == nil {
		return
	}

	C.pcre2_free_handle(handle)
}

// pcre2Match runs pcre2_match against subject with startOffset in bytes.
// It returns 1 for match, 0 for no match, and a negative value for a PCRE2 error code.
func pcre2Match(handle *C.pcre2_regex_handle, subject string, startOffset int) int {
	if handle == nil {
		return -2
	}

	cSubject := C.CString(subject)
	defer C.free(unsafe.Pointer(cSubject))

	rc := C.pcre2_match_handle(
		handle,
		cSubject,
		C.PCRE2_SIZE(len(subject)),
		C.PCRE2_SIZE(startOffset),
	)

	return int(rc)
}

func pcre2CaptureCount(handle *C.pcre2_regex_handle) uint32 {
	if handle == nil {
		return 0
	}

	return uint32(C.pcre2_handle_capture_count(handle))
}

func pcre2GroupBounds(handle *C.pcre2_regex_handle, group uint32) (start, end int, unset bool, ok bool) {
	if handle == nil {
		return 0, 0, false, false
	}

	var cStart, cEnd C.PCRE2_SIZE

	rc := C.pcre2_handle_group_bounds(handle, C.uint32_t(group), &cStart, &cEnd)
	if rc != 0 {
		return 0, 0, false, false
	}

	if int(C.pcre2_group_is_unset(cStart, cEnd)) != 0 {
		return 0, 0, true, true
	}

	return int(cStart), int(cEnd), false, true
}

func pcre2MatchError(rc int) error {
	if rc >= 0 {
		return nil
	}

	var buf [256]C.char

	C.pcre2_format_match_error(C.int(rc), (*C.char)(unsafe.Pointer(&buf[0])), C.PCRE2_SIZE(len(buf)))
	msg := C.GoString((*C.char)(unsafe.Pointer(&buf[0])))
	if msg == "" {
		msg = fmt.Sprintf("pcre2_match error %d", rc)
	}

	return fmt.Errorf("pcre2_match: %s", msg)
}

// matchWithLimits compiles pattern for one shot, matches subject, then frees.
// Prefer a compiled Regexp for hot paths.
func matchWithLimits(pattern, subject string, matchLimit, depthLimit uint32) (bool, error) {
	handle, err := compileHandle(pattern, matchLimit, depthLimit)
	if err != nil {
		return false, err
	}

	defer freeHandle(handle)

	rc := pcre2Match(handle, subject, 0)
	if rc == 1 {
		return true, nil
	}

	if rc == 0 {
		return false, nil
	}

	return false, pcre2MatchError(rc)
}

// HandleMatch runs pcre2_match for a compiled handle (opaque unsafe.Pointer).
func HandleMatch(handle unsafe.Pointer, subject string, startOffset int) int {
	if handle == nil {
		return -2
	}

	return pcre2Match((*C.pcre2_regex_handle)(handle), subject, startOffset)
}

// HandleFree releases a compiled handle.
func HandleFree(handle unsafe.Pointer) {
	if handle == nil {
		return
	}

	freeHandle((*C.pcre2_regex_handle)(handle))
}

// HandleCaptureCount returns the number of capturing subpatterns (not including group 0).
func HandleCaptureCount(handle unsafe.Pointer) uint32 {
	if handle == nil {
		return 0
	}

	return pcre2CaptureCount((*C.pcre2_regex_handle)(handle))
}

// HandleGroupBounds reads ovector pair for group after a successful HandleMatch.
func HandleGroupBounds(handle unsafe.Pointer, group uint32) (start, end int, unset bool, ok bool) {
	if handle == nil {
		return 0, 0, false, false
	}

	return pcre2GroupBounds((*C.pcre2_regex_handle)(handle), group)
}

// HandleCompile allocates a compiled PCRE2 program and match context with the given limits.
func HandleCompile(pattern string, matchLimit, depthLimit uint32) (unsafe.Pointer, error) {
	handle, err := compileHandle(pattern, matchLimit, depthLimit)
	if err != nil {
		return nil, err
	}

	return unsafe.Pointer(handle), nil
}
