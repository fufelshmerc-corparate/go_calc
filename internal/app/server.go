package app

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strconv"
	"time"

	pb "github.com/fufelshmerc-corparate/go_calc/api/calc"
	"google.golang.org/grpc"
)

type Config struct {
	HTTPPort       string
	GRPCPort       string
	DatabasePath   string
	JWTSecret      string
	Workers        int
	PollInterval   time.Duration
	OperationTimes map[string]int
}

func ConfigFromEnv() (Config, error) {
	value := func(key, fallback string) string {
		if v := os.Getenv(key); v != "" {
			return v
		}
		return fallback
	}
	c := Config{HTTPPort: value("HTTP_PORT", "8080"), GRPCPort: value("GRPC_PORT", "50051"), DatabasePath: value("DATABASE_PATH", "data.db"), JWTSecret: os.Getenv("JWT_SECRET"), Workers: 1, PollInterval: 100 * time.Millisecond, OperationTimes: make(map[string]int)}
	if len(c.JWTSecret) < 32 {
		return c, errors.New("JWT_SECRET must contain at least 32 bytes")
	}
	for _, port := range []string{c.HTTPPort, c.GRPCPort} {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return c, fmt.Errorf("invalid port: %q", port)
		}
	}
	if v := os.Getenv("COMPUTING_POWER"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 128 {
			return c, errors.New("COMPUTING_POWER must be 1..128")
		}
		c.Workers = n
	}
	if v := os.Getenv("TIME_TIMEOUT_MS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 60000 {
			return c, errors.New("TIME_TIMEOUT_MS must be 1..60000")
		}
		c.PollInterval = time.Duration(n) * time.Millisecond
	}
	for op, key := range map[string]string{"+": "TIME_ADDITION_MS", "-": "TIME_SUBTRACTION_MS", "*": "TIME_MULTIPLICATION_MS", "/": "TIME_DIVISION_MS"} {
		n, err := strconv.Atoi(value(key, "100"))
		if err != nil || n < 0 || n > 1000 {
			return c, fmt.Errorf("%s must be 0..1000", key)
		}
		c.OperationTimes[op] = n
	}
	return c, nil
}

// Run starts both listeners before launching agents and owns their complete lifecycle.
func Run(ctx context.Context, c Config) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	db, err := InitDB(c.DatabasePath)
	if err != nil {
		return fmt.Errorf("initialize database: %w", err)
	}
	defer db.Close()
	// The task queue is in memory: interrupted expressions cannot be resumed after restart.
	if _, err := db.Exec("UPDATE expressions SET status = 'error' WHERE status = 'processing'"); err != nil {
		return err
	}
	orch := NewOrchestrator(db, []byte(c.JWTSecret))
	orch.ctx = ctx
	for op, ms := range c.OperationTimes {
		orch.SetOperationTime(op, ms)
	}
	httpListener, err := net.Listen("tcp", ":"+c.HTTPPort)
	if err != nil {
		return err
	}
	defer httpListener.Close()
	// gRPC is an internal trusted interface, available only on loopback.
	grpcListener, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", c.GRPCPort))
	if err != nil {
		return err
	}
	defer grpcListener.Close()
	grpcServer := grpc.NewServer()
	pb.RegisterOrchestratorServer(grpcServer, orch)
	server := &http.Server{Handler: orch.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
	failures := make(chan error, 3)
	go func() { failures <- grpcServer.Serve(grpcListener) }()
	go func() { failures <- server.Serve(httpListener) }()
	agentDone := make(chan struct{})
	go func() {
		defer close(agentDone)
		failures <- RunAgentGRPC(ctx, grpcListener.Addr().String(), c.Workers, c.PollInterval)
	}()
	log.Printf("HTTP :%s; internal gRPC %s; workers=%d", c.HTTPPort, grpcListener.Addr(), c.Workers)
	select {
	case <-ctx.Done():
	case err = <-failures:
	}
	cancel()
	orch.mu.Lock()
	orch.mu.Unlock() // Wait for any in-flight job admission.
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	if shutdownErr := server.Shutdown(shutdownCtx); shutdownErr != nil {
		server.Close()
		log.Printf("HTTP shutdown: %v", shutdownErr)
	}
	grpcServer.Stop()
	<-agentDone
	orch.jobs.Wait()
	if errors.Is(err, http.ErrServerClosed) || errors.Is(err, grpc.ErrServerStopped) {
		return nil
	}
	return err
}
