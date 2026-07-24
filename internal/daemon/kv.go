package daemon

import (
	"context"
	"errors"
	"strings"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"goodkind.io/agent-gate/api/daemonpb"
	"goodkind.io/agent-gate/internal/hotkv"
)

// KVGet implements the hot KV GET RPC.
func (s *Server) KVGet(
	_ context.Context,
	req *daemonpb.KVGetRequest,
) (*daemonpb.KVGetResponse, error) {
	if err := validatePublicKVNamespace(req.GetNamespace()); err != nil {
		return nil, err
	}
	entry, found, err := s.hotKV.Get(req.GetNamespace(), req.GetKey())
	if err != nil {
		return nil, kvStatusError(err)
	}
	if !found {
		return &daemonpb.KVGetResponse{Found: false, Entry: nil}, nil
	}
	return &daemonpb.KVGetResponse{Found: true, Entry: daemonKVEntry(entry)}, nil
}

// KVSet implements the hot KV SET RPC.
func (s *Server) KVSet(
	_ context.Context,
	req *daemonpb.KVSetRequest,
) (*daemonpb.KVSetResponse, error) {
	if err := validatePublicKVNamespace(req.GetNamespace()); err != nil {
		return nil, err
	}
	mode, err := parseKVSetMode(req.GetMode())
	if err != nil {
		return nil, err
	}
	ttlMs := req.GetTtlMs()
	if ttlMs < 0 {
		return nil, status.Error(codes.InvalidArgument, "ttl_ms must be non-negative")
	}
	entry, stored, err := s.hotKV.Set(
		req.GetNamespace(),
		req.GetKey(),
		req.GetValue(),
		hotkv.SetOptions{
			Mode: mode,
			TTL:  time.Duration(ttlMs) * time.Millisecond,
		},
	)
	if err != nil {
		return nil, kvStatusError(err)
	}
	if !stored {
		return &daemonpb.KVSetResponse{Stored: false, Entry: nil}, nil
	}
	return &daemonpb.KVSetResponse{Stored: true, Entry: daemonKVEntry(entry)}, nil
}

// KVDelete implements the hot KV DEL RPC.
func (s *Server) KVDelete(
	_ context.Context,
	req *daemonpb.KVDeleteRequest,
) (*daemonpb.KVDeleteResponse, error) {
	if err := validatePublicKVNamespace(req.GetNamespace()); err != nil {
		return nil, err
	}
	deleted, err := s.hotKV.Delete(req.GetNamespace(), req.GetKey())
	if err != nil {
		return nil, kvStatusError(err)
	}
	return &daemonpb.KVDeleteResponse{Deleted: deleted}, nil
}

// KVExists implements the hot KV EXISTS RPC.
func (s *Server) KVExists(
	_ context.Context,
	req *daemonpb.KVExistsRequest,
) (*daemonpb.KVExistsResponse, error) {
	if err := validatePublicKVNamespace(req.GetNamespace()); err != nil {
		return nil, err
	}
	exists, err := s.hotKV.Exists(req.GetNamespace(), req.GetKey())
	if err != nil {
		return nil, kvStatusError(err)
	}
	return &daemonpb.KVExistsResponse{Exists: exists}, nil
}

// KVTTL implements the hot KV TTL RPC.
func (s *Server) KVTTL(
	_ context.Context,
	req *daemonpb.KVGetRequest,
) (*daemonpb.KVTTLResponse, error) {
	if err := validatePublicKVNamespace(req.GetNamespace()); err != nil {
		return nil, err
	}
	ttl, err := hotKVTTL(s.hotKV, req.GetNamespace(), req.GetKey(), false)
	if err != nil {
		return nil, err
	}
	return &daemonpb.KVTTLResponse{Ttl: ttl}, nil
}

// KVPTTL implements the hot KV PTTL RPC.
func (s *Server) KVPTTL(
	_ context.Context,
	req *daemonpb.KVGetRequest,
) (*daemonpb.KVPTTLResponse, error) {
	if err := validatePublicKVNamespace(req.GetNamespace()); err != nil {
		return nil, err
	}
	ttl, err := hotKVTTL(s.hotKV, req.GetNamespace(), req.GetKey(), true)
	if err != nil {
		return nil, err
	}
	return &daemonpb.KVPTTLResponse{Pttl: ttl}, nil
}

// KVExpire implements the hot KV EXPIRE RPC.
func (s *Server) KVExpire(
	_ context.Context,
	req *daemonpb.KVExpireRequest,
) (*daemonpb.KVExpireResponse, error) {
	if err := validatePublicKVNamespace(req.GetNamespace()); err != nil {
		return nil, err
	}
	ttlMs := req.GetTtlMs()
	if ttlMs < 0 {
		return nil, status.Error(codes.InvalidArgument, "ttl_ms must be non-negative")
	}
	updated, err := s.hotKV.Expire(
		req.GetNamespace(),
		req.GetKey(),
		time.Duration(ttlMs)*time.Millisecond,
	)
	if err != nil {
		return nil, kvStatusError(err)
	}
	return &daemonpb.KVExpireResponse{Updated: updated}, nil
}

// KVGetDelete implements the hot KV GETDEL RPC.
func (s *Server) KVGetDelete(
	_ context.Context,
	req *daemonpb.KVGetDeleteRequest,
) (*daemonpb.KVGetDeleteResponse, error) {
	if err := validatePublicKVNamespace(req.GetNamespace()); err != nil {
		return nil, err
	}
	entry, found, err := s.hotKV.GetDelete(req.GetNamespace(), req.GetKey())
	if err != nil {
		return nil, kvStatusError(err)
	}
	if !found {
		return &daemonpb.KVGetDeleteResponse{Found: false, Entry: nil}, nil
	}
	return &daemonpb.KVGetDeleteResponse{Found: true, Entry: daemonKVEntry(entry)}, nil
}

// KVList implements the hot KV list RPC used by diagnostics.
func (s *Server) KVList(
	_ context.Context,
	req *daemonpb.KVListRequest,
) (*daemonpb.KVListResponse, error) {
	if err := validatePublicKVNamespace(req.GetNamespace()); err != nil {
		return nil, err
	}
	if req.GetLimit() < 0 {
		return nil, status.Error(codes.InvalidArgument, "limit must be non-negative")
	}
	entries, err := s.hotKV.List(
		req.GetNamespace(),
		req.GetPrefix(),
		int(req.GetLimit()),
		req.GetIncludeValues(),
	)
	if err != nil {
		return nil, kvStatusError(err)
	}
	out := make([]*daemonpb.KVEntry, 0, len(entries))
	for _, entry := range entries {
		out = append(out, daemonKVEntry(entry))
	}
	return &daemonpb.KVListResponse{Entries: out}, nil
}

func validatePublicKVNamespace(namespace string) error {
	if !hotkv.IsInternalNamespace(namespace) {
		return nil
	}
	return status.Error(
		codes.PermissionDenied,
		"internal namespace is not available through public KV RPCs",
	)
}

func parseKVSetMode(mode string) (hotkv.SetMode, error) {
	switch strings.ToUpper(strings.TrimSpace(mode)) {
	case "":
		return hotkv.SetModeAny, nil
	case string(hotkv.SetModeNX):
		return hotkv.SetModeNX, nil
	case string(hotkv.SetModeXX):
		return hotkv.SetModeXX, nil
	default:
		return hotkv.SetModeAny, status.Errorf(
			codes.InvalidArgument,
			"unknown set mode %q",
			mode,
		)
	}
}

func kvStatusError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, hotkv.ErrInvalidNamespace), errors.Is(err, hotkv.ErrInvalidKey):
		return status.Error(codes.InvalidArgument, err.Error())
	case errors.Is(err, hotkv.ErrValueTooLarge):
		return status.Error(codes.ResourceExhausted, err.Error())
	default:
		return status.Error(codes.Internal, err.Error())
	}
}

func hotKVTTL(
	store *hotkv.Store,
	namespace string,
	key string,
	precise bool,
) (int64, error) {
	ttl, found, expiring, err := store.PTTL(namespace, key)
	if err != nil {
		return 0, kvStatusError(err)
	}
	if !found {
		return -2, nil
	}
	if !expiring {
		return -1, nil
	}
	if precise {
		return ttl.Milliseconds(), nil
	}
	return ttl.Milliseconds() / 1000, nil
}

func daemonKVEntry(entry hotkv.Entry) *daemonpb.KVEntry {
	pttlMs := int64(-1)
	expiresUnixNano := int64(0)
	if !entry.ExpiresAt.IsZero() {
		expiresUnixNano = entry.ExpiresAt.UnixNano()
		ttl := entry.ExpiresAt.Sub(auditNow())
		if ttl < 0 {
			pttlMs = 0
		} else {
			pttlMs = ttl.Milliseconds()
		}
	}
	return &daemonpb.KVEntry{
		Namespace:       entry.Namespace,
		Key:             entry.Key,
		Value:           append([]byte(nil), entry.Value...),
		Version:         entry.Version,
		CreatedUnixNano: unixNanoOrZero(entry.CreatedAt),
		UpdatedUnixNano: unixNanoOrZero(entry.UpdatedAt),
		ExpiresUnixNano: expiresUnixNano,
		PttlMs:          pttlMs,
	}
}

func unixNanoOrZero(value time.Time) int64 {
	if value.IsZero() {
		return 0
	}
	return value.UnixNano()
}
