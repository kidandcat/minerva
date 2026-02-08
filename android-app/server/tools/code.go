package tools

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/dop251/goja"
)

type RunCodeArgs struct {
	Code string `json:"code"`
}

func RunCode(arguments string) (string, error) {
	var args RunCodeArgs
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if args.Code == "" {
		return "", fmt.Errorf("code is required")
	}

	vm := goja.New()
	var logs []string

	// Add console.log
	console := map[string]any{
		"log": func(call goja.FunctionCall) goja.Value {
			parts := make([]string, len(call.Arguments))
			for i, arg := range call.Arguments {
				parts[i] = arg.String()
			}
			logs = append(logs, strings.Join(parts, " "))
			return goja.Undefined()
		},
	}
	vm.Set("console", console)

	// Run with timeout
	done := make(chan struct{})
	var result goja.Value
	var runErr error

	go func() {
		result, runErr = vm.RunString(args.Code)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		vm.Interrupt("timeout: execution exceeded 5 seconds")
		<-done
		return "", fmt.Errorf("code execution timed out (5s limit)")
	}

	if runErr != nil {
		return "", fmt.Errorf("execution error: %w", runErr)
	}

	output := strings.Join(logs, "\n")
	resultStr := ""
	if result != nil && !goja.IsUndefined(result) && !goja.IsNull(result) {
		resultStr = result.String()
	}

	response := map[string]any{
		"success": true,
		"output":  output,
		"result":  resultStr,
	}
	jsonResponse, _ := json.Marshal(response)
	return string(jsonResponse), nil
}
