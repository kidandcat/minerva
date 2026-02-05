package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/dop251/goja"
)

type RunCodeArgs struct {
	Code string `json:"code"`
}

type CodeResult struct {
	Success bool   `json:"success"`
	Output  string `json:"output,omitempty"`
	Result  string `json:"result,omitempty"`
	Error   string `json:"error,omitempty"`
}

func RunCode(arguments string) (string, error) {
	var args RunCodeArgs
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}

	if args.Code == "" {
		return "", fmt.Errorf("code cannot be empty")
	}

	// Create JS runtime
	vm := goja.New()

	// Capture console.log output
	var logs []string
	console := vm.NewObject()
	console.Set("log", func(call goja.FunctionCall) goja.Value {
		parts := make([]string, len(call.Arguments))
		for i, arg := range call.Arguments {
			parts[i] = arg.String()
		}
		logs = append(logs, strings.Join(parts, " "))
		return goja.Undefined()
	})
	console.Set("error", func(call goja.FunctionCall) goja.Value {
		parts := make([]string, len(call.Arguments))
		for i, arg := range call.Arguments {
			parts[i] = arg.String()
		}
		logs = append(logs, "[ERROR] "+strings.Join(parts, " "))
		return goja.Undefined()
	})
	vm.Set("console", console)

	// Add some safe utilities
	vm.Set("JSON", vm.NewObject())
	vm.RunString(`
		JSON.stringify = function(obj, replacer, space) {
			return JSON.stringify(obj, replacer, space);
		};
		JSON.parse = function(str) {
			return JSON.parse(str);
		};
	`)

	// Run with timeout
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Channel to receive result
	resultChan := make(chan CodeResult, 1)

	go func() {
		// Set interrupt handler
		vm.SetFieldNameMapper(goja.TagFieldNameMapper("json", true))

		// Execute code
		val, err := vm.RunString(args.Code)

		result := CodeResult{Success: true}
		if err != nil {
			result.Success = false
			result.Error = err.Error()
		} else if val != nil && !goja.IsUndefined(val) && !goja.IsNull(val) {
			result.Result = val.String()
		}

		if len(logs) > 0 {
			result.Output = strings.Join(logs, "\n")
		}

		resultChan <- result
	}()

	// Wait for result or timeout
	select {
	case result := <-resultChan:
		jsonResponse, _ := json.Marshal(result)
		return string(jsonResponse), nil
	case <-ctx.Done():
		vm.Interrupt("execution timeout")
		return "", fmt.Errorf("code execution timed out (5s limit)")
	}
}
