package daemon

import (
	"fmt"
	"io"
)

// ReadHookInput rejects oversized stdin without returning truncated input.
func ReadHookInput(reader io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(reader, RPCMaxMessageBytes+1))
	if err != nil {
		return nil, wrapServerError("read hook input", err)
	}

	if len(data) > RPCMaxMessageBytes {
		return nil, fmt.Errorf("hook input exceeds %d-byte RPC message limit", RPCMaxMessageBytes)
	}

	return data, nil
}
