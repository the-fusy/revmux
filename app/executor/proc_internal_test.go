package executor

import (
	"context"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProc_readLines_handlesTokenReadBeforeCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	r := &cancelingReader{cancel: cancel, data: []byte("session id: provider-session\n")}
	var lines []string

	err := (&proc{bin: "test"}).readLines(ctx, r, func(line string) { lines = append(lines, line) })

	require.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, []string{"session id: provider-session"}, lines,
		"a token the scanner already owns must reach session attribution before cancellation returns")
}

type cancelingReader struct {
	cancel context.CancelFunc
	data   []byte
	read   bool
}

func (r *cancelingReader) Read(p []byte) (int, error) {
	if r.read {
		return 0, io.EOF
	}
	r.read = true
	r.cancel()
	return copy(p, r.data), nil
}
