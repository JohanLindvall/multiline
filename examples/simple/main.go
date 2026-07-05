package main

import (
	"context"
	"fmt"

	"github.com/JohanLindvall/multiline"
)

func main() {
	// The emitter is called once per completed entry.
	ml := multiline.New(func(_ context.Context, e multiline.Entry[any]) error {
		if e.Match != "" {
			fmt.Printf("[stacktrace %s, %d lines]\n%s\n\n", e.Match, e.Lines, e.Text)
		} else {
			fmt.Printf("[plain] %s\n", e.Text)
		}
		return nil
	})

	log := []string{
		"server started",
		"panic: runtime error: invalid memory address or nil pointer dereference",
		"[signal SIGSEGV: segmentation violation code=0x1 addr=0x0 pc=0x123456]",
		"",
		"goroutine 1 [running]:",
		"main.handler(0x0)",
		"\t/app/main.go:42 +0x1d",
		"main.main()",
		"\t/app/main.go:17 +0x2b",
		"shutting down",
	}

	ctx := context.Background()
	// The key groups related lines together; use e.g. a container id in real use.
	for _, line := range log {
		if err := ml.Add(ctx, "key", line, nil); err != nil {
			panic(err)
		}
	}

	// Flush anything still buffered.
	if err := ml.Stop(ctx); err != nil {
		panic(err)
	}
}
