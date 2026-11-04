package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	pb "github.com/fufelshmerc-corparate/go_calc/api/calc"
	"github.com/golang-jwt/jwt/v5"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestParseExpression(t *testing.T) {
	for _, tt := range []struct {
		input string
		valid bool
	}{
		{"2+2*2", true}, {"-(2+.5)/+2", true}, {"1e2 + 08", true},
		{"", false}, {"2+", false}, {"2%1", false}, {"1<<2", false},
		{"foo", false}, {"f(2)", false}, {"'a'", false}, {`"12"`, false},
		{"0x10", false}, {"1_000", false}, {"1e999", false}, {"!1", false},
		{strings.Repeat("(", 65) + "1" + strings.Repeat(")", 65), false},
		{strings.Repeat("1+", 2100) + "1", false},
	} {
		t.Run(tt.input[:min(len(tt.input), 40)], func(t *testing.T) {
			_, err := parseExpression(tt.input)
			if (err == nil) != tt.valid {
				t.Fatalf("valid=%v, error=%v", tt.valid, err)
			}
		})
	}
}

func TestArithmeticInvalidInputs(t *testing.T) {
	for _, task := range []*pb.Task{nil, {Arg1: math.Inf(1), Operation: "+"}, {Arg1: math.NaN(), Operation: "+"}, {Arg1: 1e308, Arg2: 1e308, Operation: "*"}, {Arg1: 1, Arg2: math.Copysign(0, -1), Operation: "/"}} {
		if _, err := performOperation(task); err == nil {
			t.Fatal("expected arithmetic error")
		}
	}
}

func TestWaitCancellationCleansQueue(t *testing.T) {
	for _, dispatched := range []bool{false, true} {
		t.Run(fmt.Sprint(dispatched), func(t *testing.T) {
			o := NewOrchestrator(nil, nil)
			o.addTask(&pb.Task{Id: "expired"})
			if dispatched {
				if _, err := o.GetTask(context.Background(), &pb.TaskRequest{}); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
			defer cancel()
			if _, err := o.waitForResult(ctx, "expired"); !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("%v", err)
			}
			if len(o.tasks)+len(o.processing)+len(o.results) != 0 {
				t.Fatal("expired task leaked")
			}
			if _, err := o.SubmitResult(context.Background(), &pb.Result{Id: "expired"}); status.Code(err) != codes.NotFound {
				t.Fatal(err)
			}
		})
	}
}

func TestSubmitResultRejectsNonFinite(t *testing.T) {
	o := NewOrchestrator(nil, nil)
	o.processing["task"] = true
	for _, res := range []*pb.Result{nil, {Id: "task", Result: math.NaN()}, {Id: "task", Result: math.Inf(1)}} {
		if _, err := o.SubmitResult(context.Background(), res); status.Code(err) != codes.InvalidArgument {
			t.Fatalf("%v", err)
		}
	}
}

func TestAuthRejectsInvalidClaims(t *testing.T) {
	secret := []byte(strings.Repeat("s", 32))
	o := NewOrchestrator(nil, secret)
	for _, tt := range []struct {
		name   string
		method jwt.SigningMethod
		uid    int
		expiry *jwt.NumericDate
	}{
		{"valid", jwt.SigningMethodHS256, 1, jwt.NewNumericDate(time.Now().Add(time.Hour))},
		{"wrong algorithm", jwt.SigningMethodHS384, 1, jwt.NewNumericDate(time.Now().Add(time.Hour))},
		{"expired", jwt.SigningMethodHS256, 1, jwt.NewNumericDate(time.Now().Add(-time.Hour))},
		{"no expiration", jwt.SigningMethodHS256, 1, nil},
		{"no user", jwt.SigningMethodHS256, 0, jwt.NewNumericDate(time.Now().Add(time.Hour))},
	} {
		t.Run(tt.name, func(t *testing.T) {
			claims := struct {
				UserID int `json:"user_id"`
				jwt.RegisteredClaims
			}{tt.uid, jwt.RegisteredClaims{ExpiresAt: tt.expiry}}
			token, err := jwt.NewWithClaims(tt.method, claims).SignedString(secret)
			if err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.Header.Set("Authorization", "Bearer "+token)
			w := httptest.NewRecorder()
			o.authMiddleware(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })(w, req)
			want := 401
			if tt.name == "valid" {
				want = 204
			}
			if w.Code != want {
				t.Fatalf("got %d, want %d", w.Code, want)
			}
		})
	}
}

func TestJSONRequestValidation(t *testing.T) {
	for _, body := range []string{`{"expression":"1"} {}`, `{"expression":"1","unknown":true}`, `{"expression":"` + strings.Repeat("1", 9000) + `"}`} {
		req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
		w := httptest.NewRecorder()
		var input struct {
			Expression string `json:"expression"`
		}
		if err := decodeRequest(w, req, &input); err == nil {
			t.Fatal("invalid body accepted")
		}
	}
}

func TestAPIIntegration(t *testing.T) {
	db, err := InitDB(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	o := NewOrchestrator(db, []byte(strings.Repeat("s", 32)))
	o.ctx = ctx
	for _, op := range []string{"+", "-", "*", "/"} {
		o.SetOperationTime(op, 0)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
	pb.RegisterOrchestratorServer(server, o)
	serverDone := make(chan error, 1)
	go func() { serverDone <- server.Serve(listener) }()
	agentDone := make(chan error, 1)
	go func() { agentDone <- RunAgentGRPC(ctx, listener.Addr().String(), 4, time.Millisecond) }()
	defer func() {
		cancel()
		server.Stop()
		<-serverDone
		<-agentDone
		o.jobs.Wait()
		db.Close()
	}()
	request := func(method, path, body, token string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		if token != "" {
			req.Header.Set("Authorization", token)
		}
		w := httptest.NewRecorder()
		o.Handler().ServeHTTP(w, req)
		return w
	}
	var tokens []string
	for _, login := range []string{"alice", "bob"} {
		body := fmt.Sprintf(`{"login":%q,"password":"test-password"}`, login)
		if w := request("POST", "/api/v1/register", body, ""); w.Code != 200 {
			t.Fatalf("register: %d %s", w.Code, w.Body)
		}
		if w := request("POST", "/api/v1/register", body, ""); w.Code != 409 {
			t.Fatalf("duplicate: %d", w.Code)
		}
		w := request("POST", "/api/v1/login", body, "")
		if w.Code != 200 {
			t.Fatalf("login: %d %s", w.Code, w.Body)
		}
		var auth struct {
			Token string `json:"token"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &auth); err != nil {
			t.Fatal(err)
		}
		tokens = append(tokens, auth.Token)
	}
	if w := request("GET", "/api/v1/expressions", "", tokens[0]); w.Body.String() != "[]\n" {
		t.Fatalf("empty list: %s", w.Body)
	}
	var configWG sync.WaitGroup
	configWG.Add(1)
	go func() {
		defer configWG.Done()
		for i := 0; i < 100; i++ {
			o.SetOperationTime("+", 0)
		}
	}()
	defer configWG.Wait()
	for _, tt := range []struct {
		input  string
		result float64
		state  string
	}{
		{"2+2*2", 6, "completed"}, {"(2+2)*2", 8, "completed"}, {"-(2+.5)/+2", -1.25, "completed"},
		{"1e2+08", 108, "completed"}, {"42", 42, "completed"}, {"3-2-1", 0, "completed"},
		{"2/0", 0, "error"}, {"1/(2-2)", 0, "error"}, {"1e308*1e308", 0, "error"},
	} {
		t.Run(tt.input, func(t *testing.T) {
			body, _ := json.Marshal(map[string]string{"expression": tt.input})
			w := request("POST", "/api/v1/calculate", string(body), tokens[0])
			if w.Code != 200 {
				t.Fatalf("calculate: %d %s", w.Code, w.Body)
			}
			var accepted struct {
				ID string `json:"id"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &accepted); err != nil {
				t.Fatal(err)
			}
			path := "/api/v1/expressions/" + accepted.ID
			deadline := time.Now().Add(3 * time.Second)
			for {
				w = request("GET", path, "", tokens[0])
				if w.Code != 200 {
					t.Fatalf("lookup: %d %s", w.Code, w.Body)
				}
				var result struct {
					Status string   `json:"status"`
					Result *float64 `json:"result"`
				}
				if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
					t.Fatal(err)
				}
				if result.Status != "processing" {
					if result.Status != tt.state {
						t.Fatalf("state: %s", w.Body)
					}
					if tt.state == "completed" && (result.Result == nil || *result.Result != tt.result) {
						t.Fatalf("result: %s", w.Body)
					}
					if tt.state == "error" && result.Result != nil {
						t.Fatalf("error has result: %s", w.Body)
					}
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("expression stuck")
				}
				time.Sleep(5 * time.Millisecond)
			}
			if w = request("GET", path, "", tokens[1]); w.Code != 404 {
				t.Fatalf("other user read result: %d", w.Code)
			}
		})
	}
	for _, input := range []string{"2%1", "", "2+", "x", "1<<2"} {
		body, _ := json.Marshal(map[string]string{"expression": input})
		if w := request("POST", "/api/v1/calculate", string(body), tokens[0]); w.Code != 422 {
			t.Fatalf("invalid expression: %d", w.Code)
		}
	}
	if w := request("GET", "/api/v1/expressions", "", ""); w.Code != 401 {
		t.Fatalf("no auth: %d", w.Code)
	}
	if w := request("GET", "/api/v1/calculate", "", tokens[0]); w.Code != 405 {
		t.Fatalf("wrong method: %d", w.Code)
	}
}

func TestConfigValidation(t *testing.T) {
	for _, key := range []string{"JWT_SECRET", "HTTP_PORT", "GRPC_PORT", "DATABASE_PATH", "COMPUTING_POWER", "TIME_TIMEOUT_MS", "TIME_ADDITION_MS", "TIME_SUBTRACTION_MS", "TIME_MULTIPLICATION_MS", "TIME_DIVISION_MS"} {
		t.Setenv(key, "")
	}
	t.Setenv("JWT_SECRET", strings.Repeat("s", 32))
	if _, err := ConfigFromEnv(); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct{ key, value string }{
		{"JWT_SECRET", ""}, {"HTTP_PORT", "0"}, {"GRPC_PORT", "localhost:50051"},
		{"COMPUTING_POWER", "0"}, {"COMPUTING_POWER", "129"}, {"TIME_TIMEOUT_MS", "-1"},
		{"TIME_ADDITION_MS", "-1"}, {"TIME_DIVISION_MS", "1001"}, {"HTTP_PORT", "abc"},
	} {
		t.Run(tt.key+tt.value, func(t *testing.T) {
			t.Setenv(tt.key, tt.value)
			if _, err := ConfigFromEnv(); err == nil {
				t.Fatal("invalid config accepted")
			}
		})
	}
}

func TestRunLifecycle(t *testing.T) {
	for _, cancelled := range []bool{true, false} {
		t.Run(fmt.Sprint(cancelled), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if cancelled {
				cancel()
			}
			port := "0"
			var occupied net.Listener
			if !cancelled {
				var err error
				occupied, err = net.Listen("tcp", ":0")
				if err != nil {
					t.Fatal(err)
				}
				defer occupied.Close()
				port = fmt.Sprint(occupied.Addr().(*net.TCPAddr).Port)
			}
			err := Run(ctx, Config{HTTPPort: port, GRPCPort: "0", DatabasePath: filepath.Join(t.TempDir(), "test.db"), JWTSecret: strings.Repeat("s", 32), Workers: 1, PollInterval: time.Millisecond})
			if (err == nil) != cancelled {
				t.Fatalf("unexpected Run result: %v", err)
			}
		})
	}
}

func TestCalculationAdmission(t *testing.T) {
	for _, tt := range []string{"busy", "stopping", "database failure"} {
		t.Run(tt, func(t *testing.T) {
			o, db, mock := setupTestOrchestrator(t)
			defer db.Close()
			want := http.StatusServiceUnavailable
			switch tt {
			case "busy":
				for i := 0; i < cap(o.slots); i++ {
					o.slots <- struct{}{}
				}
			case "stopping":
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				o.ctx = ctx
			case "database failure":
				want = 500
				mock.ExpectExec("INSERT INTO expressions").WillReturnError(errors.New("disk full"))
			}
			req := httptest.NewRequest("POST", "/api/v1/calculate", strings.NewReader(`{"expression":"1"}`))
			req = req.WithContext(context.WithValue(req.Context(), userIDKey{}, 1))
			w := httptest.NewRecorder()
			o.handleCalculate(w, req)
			if w.Code != want {
				t.Fatalf("got %d, want %d", w.Code, want)
			}
			o.jobs.Wait()
			if tt != "busy" && len(o.slots) != 0 {
				t.Fatal("admission slot leaked")
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestRestartMarksInterruptedExpressions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "restart.db")
	db, err := InitDB(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("INSERT INTO users (id, login, password) VALUES (1, 'demo', 'hash')"); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("INSERT INTO expressions (id,user_id,expression,status) VALUES ('old',1,'2+2','processing')"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Run(ctx, Config{HTTPPort: "0", GRPCPort: "0", DatabasePath: path, JWTSecret: strings.Repeat("s", 32), Workers: 1, PollInterval: time.Millisecond}); err != nil {
		t.Fatal(err)
	}
	db, err = InitDB(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var state string
	if err := db.QueryRow("SELECT status FROM expressions WHERE id='old'").Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "error" {
		t.Fatalf("interrupted expression is %s", state)
	}
}

func TestEvalPropagatesAgentError(t *testing.T) {
	for _, tt := range []struct{ input, operation, message string }{
		{"1/0", "/", "division by zero"},
		{"1e308*1e308", "*", "non-finite result"},
		{"(1/0)+(2*3)", "/", "division by zero"},
	} {
		t.Run(tt.input, func(t *testing.T) {
			o := NewOrchestrator(nil, nil)
			expr, err := parseExpression(tt.input)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			done := make(chan error, 1)
			go func() { _, err := o.eval(ctx, expr); done <- err }()
			var task *pb.Task
			for task == nil {
				select {
				case err := <-done:
					t.Fatalf("evaluation finished before agent received a task: %v", err)
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				default:
				}
				task, err = o.GetTask(ctx, &pb.TaskRequest{})
				if err != nil && status.Code(err) != codes.NotFound {
					t.Fatal(err)
				}
				if task == nil {
					time.Sleep(time.Millisecond)
				}
			}
			if task.Operation != tt.operation {
				t.Fatalf("operation=%s", task.Operation)
			}
			response, err := o.SubmitResult(ctx, &pb.Result{Id: task.Id, Error: tt.message})
			if err != nil || !response.Success {
				t.Fatalf("submit: %v, %v", response, err)
			}
			select {
			case err := <-done:
				if err == nil || err.Error() != tt.message {
					t.Fatalf("error=%v", err)
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			o.mu.Lock()
			defer o.mu.Unlock()
			if len(o.tasks)+len(o.processing)+len(o.results) != 0 {
				t.Fatal("failed expression left tasks behind")
			}
		})
	}
}
