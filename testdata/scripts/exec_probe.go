package main

import (
	"context"
	"fmt"
	"os/exec"
)

// Main shells out with os/exec. Under --sandbox the os/exec import is not in
// the interpreter's symbol set, so evaluation fails with a symbol-not-allowed
// error before Main ever runs; unrestricted, it runs the command.
func Main(ctx context.Context) error {
	out, err := exec.CommandContext(ctx, "echo", "exec-ran").Output()
	if err != nil {
		return err
	}
	fmt.Printf("exec output: %s", out)
	return nil
}
