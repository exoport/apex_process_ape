package main

import (
	"context"
	"fmt"

	"github.com/exoport/apex_process_ape/apescript"
)

// Main runs the skill named by the first arg through apescript.RunTask and
// prints the resulting status. It works in both sandbox and unrestricted
// modes — RunTask is the intended, guard-railed side-effect channel.
func Main(ctx context.Context) error {
	args := apescript.Args()
	if len(args) == 0 {
		return fmt.Errorf("usage: run_task.go -- <skill>")
	}
	res, err := apescript.RunTask(ctx, apescript.TaskOpts{Skill: args[0]})
	if err != nil {
		return err
	}
	fmt.Printf("task run_id=%s status=%s\n", res.RunID, res.Status)
	return nil
}
