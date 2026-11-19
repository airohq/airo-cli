package ui

import (
	"bufio"
	"context"
	"strings"
)

// ReadLine reads one answer, including a last line without a newline.
// If the command is canceled, it can exit while the terminal read unblocks.
func ReadLine(ctx context.Context, r *bufio.Reader) (string, error) {
	type result struct {
		line string
		err  error
	}
	done := make(chan result, 1)
	go func() {
		line, err := r.ReadString('\n')
		done <- result{line, err}
	}()
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case r := <-done:
		if r.line != "" {
			// The last line may end without a newline.
			return strings.TrimRight(r.line, "\r\n"), nil
		}
		return "", r.err
	}
}
