package main

import (
	"context"
	"fmt"

	"github.com/JohanLindvall/multiline"
)

func main() {
	// The emitter is called once per aggregated entry. multiline joins the
	// lines of a detected stack trace into a single "line" (separated by "\n").
	ml := multiline.New(func(_ context.Context, line, match string, _ any) error {
		if match != "" {
			fmt.Printf("[stacktrace %s]\n%s\n\n", match, line)
		} else {
			fmt.Printf("[plain] %s\n", line)
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
	// "key" groups related lines together; use e.g. a container id in real use.
	for _, line := range log {
		if err := ml.Add(ctx, line, "key", any(nil)); err != nil {
			panic(err)
		}
	}

	// Flush anything still buffered.
	if err := ml.Stop(ctx); err != nil {
		panic(err)
	}
}
