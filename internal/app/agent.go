package app

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"time"

	pb "github.com/IvanSolomatin/calc_v2/api/calc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// getCallerInfo возвращает информацию о вызывающем коде (файл:строка)
func getCallerInfo() string {
	_, file, line, ok := runtime.Caller(2) // 2 - глубина стека вызовов
	if !ok {
		return "unknown:0"
	}
	return fmt.Sprintf("%s:%d", filepath.Base(file), line)
}

// logWithContext добавляет к логам информацию о месте вызова
func logWithContext(format string, v ...interface{}) {
	caller := getCallerInfo()
	msg := fmt.Sprintf(format, v...)
	log.Printf("[%s] %s", caller, msg)
}

type agentClient struct {
	conn   *grpc.ClientConn
	client pb.OrchestratorClient
}

func newAgentClient(addr string) (*agentClient, error) {
	logWithContext("Connecting to orchestrator at %s...", addr)
	conn, err := grpc.Dial(addr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithBlock(),
		grpc.WithTimeout(5*time.Second))
	if err != nil {
		logWithContext("Failed to connect to orchestrator: %v", err)
		return nil, err
	}
	logWithContext("Successfully connected to orchestrator")

	return &agentClient{
		conn:   conn,
		client: pb.NewOrchestratorClient(conn),
	}, nil
}

func (ac *agentClient) close() {
	logWithContext("Closing gRPC connection...")
	err := ac.conn.Close()
	if err != nil {
		logWithContext("Error closing connection: %v", err)
	}
}

func (ac *agentClient) getTask() (*pb.Task, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	logWithContext("Requesting new task from orchestrator...")
	task, err := ac.client.GetTask(ctx, &pb.TaskRequest{AgentId: "agent"})
	if err != nil {
		logWithContext("Error getting task: %v", err)
		return nil, fmt.Errorf("could not get task: %v", err)
	}

	if task != nil {
		logWithContext("Received task ID %s: %f %s %f", task.Id, task.Arg1, task.Operation, task.Arg2)
	} else {
		logWithContext("Received empty task")
	}

	return task, nil
}

func (ac *agentClient) submitResult(result *pb.Result) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	logWithContext("Submitting result for task %s: %f", result.Id, result.Result)
	_, err := ac.client.SubmitResult(ctx, result)
	if err != nil {
		logWithContext("Error submitting result: %v", err)
		return fmt.Errorf("could not submit result: %v", err)
	}
	logWithContext("Result for task %s successfully submitted", result.Id)
	return nil
}

func getEnvDuration(envVar string, defaultValue time.Duration) time.Duration {
	valueStr := os.Getenv(envVar)
	if valueStr == "" {
		logWithContext("Using default timeout: %v", defaultValue)
		return defaultValue
	}

	value, err := strconv.Atoi(valueStr)
	if err != nil {
		logWithContext("Invalid value for %s: %v. Using default: %v", envVar, err, defaultValue)
		return defaultValue
	}

	duration := time.Duration(value) * time.Millisecond
	logWithContext("Using timeout from env: %v", duration)
	return duration
}

func clientWorkerGRPC(wg *sync.WaitGroup, ac *agentClient) {
	defer wg.Done()
	timeout := getEnvDuration("TIME_TIMEOUT_MS", 1000*time.Millisecond)

	logWithContext("Starting worker...")
	for {
		task, err := ac.getTask()
		if err != nil {
			logWithContext("Worker waiting %v before retry...", timeout)
			time.Sleep(timeout)
			continue
		}

		if task == nil {
			logWithContext("No tasks available, waiting %v...", timeout)
			time.Sleep(timeout)
			continue
		}

		logWithContext("Processing task %s...", task.Id)
		result, err := performOperation(task)
		if err != nil {
			logWithContext("Error processing task %s: %v", task.Id, err)
			continue
		}

		logWithContext("Task %s result: %f", task.Id, result)
		err = ac.submitResult(&pb.Result{
			Id:     task.Id,
			Result: result,
		})

		if err != nil {
			logWithContext("Failed to submit result for task %s: %v", task.Id, err)
		}
	}
}

func RunAgentGRPC() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	logWithContext("Starting gRPC agent...")

	computingPower, err := strconv.Atoi(os.Getenv("COMPUTING_POWER"))
	if err != nil {
		logWithContext("Error reading COMPUTING_POWER env var. Using default 1 client.")
		computingPower = 1
	}

	serverAddr := os.Getenv("ORCHESTRATOR_GRPC_ADDR")
	if serverAddr == "" {
		serverAddr = "localhost:50051"
	}
	logWithContext("Orchestrator address: %s", serverAddr)

	ac, err := newAgentClient(serverAddr)
	if err != nil {
		logWithContext("Failed to create agent client: %v", err)
		return
	}
	defer ac.close()

	logWithContext("Starting %d gRPC workers...", computingPower)

	var wg sync.WaitGroup
	for i := 0; i < computingPower; i++ {
		wg.Add(1)
		go func(workerID int) {
			logWithContext("Worker %d started", workerID)
			clientWorkerGRPC(&wg, ac)
		}(i)
	}

	wg.Wait()
	logWithContext("All workers finished")
}

func performOperation(task *pb.Task) (float64, error) {
	logWithContext("Executing operation: %f %s %f", task.Arg1, task.Operation, task.Arg2)

	var result float64
	var err error

	switch task.Operation {
	case "+":
		time.Sleep(time.Duration(task.OperationTime) * time.Millisecond)
		result = task.Arg1 + task.Arg2
	case "-":
		time.Sleep(time.Duration(task.OperationTime) * time.Millisecond)
		result = task.Arg1 - task.Arg2
	case "*":
		time.Sleep(time.Duration(task.OperationTime) * time.Millisecond)
		result = task.Arg1 * task.Arg2
	case "/":
		time.Sleep(time.Duration(task.OperationTime) * time.Millisecond)
		if task.Arg2 == 0 {
			result = 0
			err = fmt.Errorf("division by zero")
			logWithContext("Division by zero in task %s", task.Id)
		} else {
			result = task.Arg1 / task.Arg2
		}
	default:
		err = fmt.Errorf("unknown operation: %s", task.Operation)
		logWithContext("Unknown operation in task %s: %s", task.Id, task.Operation)
	}

	logWithContext("Operation result: %f (error: %v)", result, err)
	return result, err
}
