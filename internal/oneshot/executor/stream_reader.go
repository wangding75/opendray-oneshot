package executor

import (
	"context"
	"io"

	"github.com/opendray/opendray-v2/internal/oneshot/domain"
)

// streamWriter bounds each persistence operation and applies backpressure to
// the child pipe. It never buffers complete process output.
type streamWriter struct {
	collector *OutputCollector
	stream    domain.StreamKind
	ctx       context.Context
}

func (w *streamWriter) Write(input []byte) (int, error) {
	if w == nil || w.collector == nil {
		return 0, io.ErrClosedPipe
	}
	ctx := w.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	written := 0
	for len(input) > 0 {
		chunkSize := w.collector.chunkSize
		if chunkSize > len(input) {
			chunkSize = len(input)
		}
		receivedAt := w.collector.now().UTC()
		if err := w.collector.appendChunk(ctx, w.stream, input[:chunkSize], receivedAt); err != nil {
			return written, err
		}
		written += chunkSize
		input = input[chunkSize:]
	}
	return written, nil
}
