package multiline_test

import (
	"context"
	"fmt"

	"github.com/JohanLindvall/multiline"
)

func ExampleNew() {
	ml := multiline.New(func(_ context.Context, e multiline.Entry[struct{}]) error {
		fmt.Printf("match=%q lines=%d text=%q\n", e.Match, e.Lines, e.Text)
		return nil
	})

	ctx := context.Background()
	for _, line := range []string{
		"GET /healthz 200",
		"java.lang.NullPointerException: boom",
		"\tat com.example.Foo.bar(Foo.java:12)",
		"\tat com.example.Main.main(Main.java:5)",
		"shutting down",
	} {
		if err := ml.Add(ctx, "container-1", line, struct{}{}); err != nil {
			panic(err)
		}
	}
	if err := ml.Stop(ctx); err != nil {
		panic(err)
	}

	// Output:
	// match="" lines=1 text="GET /healthz 200"
	// match="java" lines=3 text="java.lang.NullPointerException: boom\n\tat com.example.Foo.bar(Foo.java:12)\n\tat com.example.Main.main(Main.java:5)"
	// match="" lines=1 text="shutting down"
}
