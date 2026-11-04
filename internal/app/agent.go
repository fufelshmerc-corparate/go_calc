package app

import (
	"context"
	"fmt"
	"math"
	"os"
	"strconv"
	"sync"
	"time"

	pb "github.com/fufelshmerc-corparate/go_calc/api/calc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

func RunAgentGRPC(ctx context.Context, addr string, workers int, poll time.Duration) error {
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return err
	}
	defer conn.Close()
	client := pb.NewOrchestratorClient(conn)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ctx.Err() == nil {
				rpcCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
				task, err := client.GetTask(rpcCtx, &pb.TaskRequest{AgentId: "local-agent"})
				cancel()
				if err == nil {
					timer := time.NewTimer(time.Duration(task.OperationTime) * time.Millisecond)
					select {
					case <-ctx.Done():
						timer.Stop()
						return
					case <-timer.C:
					}
					task.OperationTime = 0
					result, calcErr := performOperation(task)
					response := &pb.Result{Id: task.Id, Result: result}
					if calcErr != nil {
						response.Error = calcErr.Error()
					}
					deadline := time.Now().Add(30 * time.Second)
					for ctx.Err() == nil && time.Now().Before(deadline) {
						rpcCtx, cancel = context.WithTimeout(ctx, 5*time.Second)
						_, err = client.SubmitResult(rpcCtx, response)
						cancel()
						if err == nil || status.Code(err) == codes.NotFound || status.Code(err) == codes.InvalidArgument {
							break
						}
						if !wait(ctx, poll) {
							return
						}
					}
				}
				if !wait(ctx, poll) {
					return
				}
			}
		}()
	}
	wg.Wait()
	return nil
}

func wait(ctx context.Context, duration time.Duration) bool {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func getEnvDuration(key string, fallback time.Duration) time.Duration {
	value, err := strconv.ParseInt(os.Getenv(key), 10, 64)
	if err != nil || value <= 0 || value > 60000 {
		return fallback
	}
	return time.Duration(value) * time.Millisecond
}

func performOperation(task *pb.Task) (float64, error) {
	if task == nil {
		return 0, fmt.Errorf("missing task")
	}
	if math.IsNaN(task.Arg1) || math.IsNaN(task.Arg2) || math.IsInf(task.Arg1, 0) || math.IsInf(task.Arg2, 0) {
		return 0, fmt.Errorf("non-finite operand")
	}
	var result float64
	switch task.Operation {
	case "+":
		result = task.Arg1 + task.Arg2
	case "-":
		result = task.Arg1 - task.Arg2
	case "*":
		result = task.Arg1 * task.Arg2
	case "/":
		if task.Arg2 == 0 {
			return 0, fmt.Errorf("division by zero")
		}
		result = task.Arg1 / task.Arg2
	default:
		return 0, fmt.Errorf("unknown operation: %s", task.Operation)
	}
	if math.IsInf(result, 0) || math.IsNaN(result) {
		return 0, fmt.Errorf("non-finite result")
	}
	return result, nil
}
