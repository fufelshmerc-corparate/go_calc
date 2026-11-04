package app

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log"
	"math"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/mattn/go-sqlite3"
	"golang.org/x/crypto/bcrypt"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/fufelshmerc-corparate/go_calc/api/calc"
)

type taskResult struct {
	value float64
	err   error
}

type Orchestrator struct {
	pb.UnimplementedOrchestratorServer
	mu          sync.Mutex
	tasks       []*pb.Task
	processing  map[string]bool
	results     map[string]taskResult
	taskCounter uint64
	db          *sql.DB
	jwtSecret   []byte
	opTimes     map[string]int
	ctx         context.Context
	jobs        sync.WaitGroup
	slots       chan struct{}
}

func NewOrchestrator(db *sql.DB, jwtSecret []byte) *Orchestrator {
	return &Orchestrator{
		ctx:        context.Background(),
		slots:      make(chan struct{}, 128),
		tasks:      make([]*pb.Task, 0),
		processing: make(map[string]bool),
		results:    make(map[string]taskResult),
		db:         db,
		jwtSecret:  jwtSecret,
		opTimes: map[string]int{
			"+": 100,
			"-": 100,
			"*": 100,
			"/": 100,
		},
	}
}

func (o *Orchestrator) handleRegister(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Login    string `json:"login"`
		Password string `json:"password"`
	}
	if err := decodeRequest(w, r, &req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	if req.Login == "" || req.Password == "" {
		http.Error(w, "Login and password are required", http.StatusBadRequest)
		return
	}

	hashedPassword, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		http.Error(w, "Password must be at most 72 bytes", http.StatusBadRequest)
		return
	}

	_, err = o.db.Exec("INSERT INTO users (login, password) VALUES (?, ?)", req.Login, string(hashedPassword))
	if err != nil {
		var sqliteErr sqlite3.Error
		if errors.As(err, &sqliteErr) && sqliteErr.ExtendedCode == sqlite3.ErrConstraintUnique {
			http.Error(w, "User already exists", http.StatusConflict)
		} else {
			http.Error(w, "Database error", http.StatusInternalServerError)
		}
		return
	}

	w.WriteHeader(http.StatusOK)
	w.Write([]byte("OK"))
}

func (o *Orchestrator) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Login    string `json:"login"`
		Password string `json:"password"`
	}
	if err := decodeRequest(w, r, &req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	var user struct {
		ID       int
		Password string
	}
	err := o.db.QueryRow("SELECT id, password FROM users WHERE login = ?", req.Login).Scan(&user.ID, &user.Password)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "Database error", http.StatusInternalServerError)
			return
		}
		http.Error(w, "Invalid login or password", http.StatusUnauthorized)
		return
	}

	if err := bcrypt.CompareHashAndPassword([]byte(user.Password), []byte(req.Password)); err != nil {
		http.Error(w, "Invalid login or password", http.StatusUnauthorized)
		return
	}

	expirationTime := time.Now().Add(24 * time.Hour)
	claims := &struct {
		UserID int `json:"user_id"`
		jwt.RegisteredClaims
	}{
		UserID: user.ID,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(expirationTime),
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	tokenString, err := token.SignedString(o.jwtSecret)
	if err != nil {
		http.Error(w, "Failed to generate token", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"token": "Bearer " + tokenString,
	})
}

func (o *Orchestrator) authMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		authHeader := r.Header.Get("Authorization")
		if authHeader == "" {
			http.Error(w, "Authorization header required", http.StatusUnauthorized)
			return
		}

		const prefix = "Bearer "
		if !strings.HasPrefix(authHeader, prefix) {
			http.Error(w, "Authorization header format must be 'Bearer <token>'", http.StatusUnauthorized)
			return
		}

		tokenString := authHeader[len(prefix):]

		claims := &struct {
			UserID int `json:"user_id"`
			jwt.RegisteredClaims
		}{}

		token, err := jwt.ParseWithClaims(tokenString, claims, func(token *jwt.Token) (interface{}, error) {
			return o.jwtSecret, nil
		}, jwt.WithValidMethods([]string{"HS256"}), jwt.WithExpirationRequired())

		if err != nil || !token.Valid || claims.UserID <= 0 {
			http.Error(w, "Invalid token", http.StatusUnauthorized)
			return
		}

		ctx := context.WithValue(r.Context(), userIDKey{}, claims.UserID)
		next.ServeHTTP(w, r.WithContext(ctx))
	}
}

func (o *Orchestrator) expressionByIDHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, `{"error":"Wrong Method"}`, http.StatusMethodNotAllowed)
		return
	}

	userID := r.Context().Value(userIDKey{}).(int)
	id := r.URL.Path[len("/api/v1/expressions/"):]

	var expr struct {
		ID         string
		Expression string
		Status     string
		Result     sql.NullFloat64
		CreatedAt  string
	}
	err := o.db.QueryRow(
		"SELECT id, expression, status, result, created_at FROM expressions WHERE id = ? AND user_id = ?",
		id, userID,
	).Scan(&expr.ID, &expr.Expression, &expr.Status, &expr.Result, &expr.CreatedAt)

	if err != nil {
		if err == sql.ErrNoRows {
			http.Error(w, `{"error":"Expression not found"}`, http.StatusNotFound)
		} else {
			http.Error(w, `{"error":"Database error"}`, http.StatusInternalServerError)
		}
		return
	}

	response := struct {
		ID         string   `json:"id"`
		Expression string   `json:"expression"`
		Status     string   `json:"status"`
		Result     *float64 `json:"result,omitempty"`
		CreatedAt  string   `json:"created_at"`
	}{
		ID:         expr.ID,
		Expression: expr.Expression,
		Status:     expr.Status,
		CreatedAt:  expr.CreatedAt,
	}

	if expr.Result.Valid {
		response.Result = &expr.Result.Float64
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

func (o *Orchestrator) enableCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "POST, GET, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")

		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusOK)
			return
		}

		next.ServeHTTP(w, r)
	})
}

func (o *Orchestrator) handleExpressions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	userID := r.Context().Value(userIDKey{}).(int)

	rows, err := o.db.Query(
		"SELECT id, expression, status, result, created_at FROM expressions WHERE user_id = ?",
		userID,
	)
	if err != nil {
		http.Error(w, "Database error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	expressions := make([]struct {
		ID         string   `json:"id"`
		Expression string   `json:"expression"`
		Status     string   `json:"status"`
		Result     *float64 `json:"result,omitempty"`
		CreatedAt  string   `json:"created_at"`
	}, 0)
	for rows.Next() {
		var expr struct {
			ID         string
			Expression string
			Status     string
			Result     sql.NullFloat64
			CreatedAt  string
		}

		if err := rows.Scan(&expr.ID, &expr.Expression, &expr.Status, &expr.Result, &expr.CreatedAt); err != nil {
			http.Error(w, "Database error", http.StatusInternalServerError)
			return
		}

		responseItem := struct {
			ID         string   `json:"id"`
			Expression string   `json:"expression"`
			Status     string   `json:"status"`
			Result     *float64 `json:"result,omitempty"`
			CreatedAt  string   `json:"created_at"`
		}{
			ID:         expr.ID,
			Expression: expr.Expression,
			Status:     expr.Status,
			CreatedAt:  expr.CreatedAt,
		}

		if expr.Result.Valid {
			responseItem.Result = &expr.Result.Float64
		}

		expressions = append(expressions, responseItem)
	}

	if err := rows.Err(); err != nil {
		http.Error(w, "Database error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(expressions)
}

func (o *Orchestrator) SetOperationTime(op string, timeMs int) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if timeMs >= 0 && timeMs <= 1000 {
		o.opTimes[op] = timeMs
	}
}

func (o *Orchestrator) GetTask(ctx context.Context, req *pb.TaskRequest) (*pb.Task, error) {
	o.mu.Lock()
	defer o.mu.Unlock()

	if len(o.tasks) == 0 {
		return nil, status.Error(codes.NotFound, "no tasks available")
	}

	task := o.tasks[0]
	o.tasks = o.tasks[1:]
	o.processing[task.Id] = true

	log.Printf("Task dispatched: %s %f %s %f", task.Id, task.Arg1, task.Operation, task.Arg2)
	return task, nil
}

func (o *Orchestrator) SubmitResult(ctx context.Context, res *pb.Result) (*pb.ResultResponse, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if res == nil {
		return nil, status.Error(codes.InvalidArgument, "missing result")
	}
	if !o.processing[res.Id] {
		return nil, status.Error(codes.NotFound, "task not found")
	}

	if math.IsNaN(res.Result) || math.IsInf(res.Result, 0) {
		return nil, status.Error(codes.InvalidArgument, "non-finite result")
	}
	result := taskResult{value: res.Result}
	if res.Error != "" {
		result.value = 0
		result.err = errors.New(res.Error)
	}
	o.results[res.Id] = result
	delete(o.processing, res.Id)

	log.Printf("Result received for task %s: %f", res.Id, res.Result)
	return &pb.ResultResponse{Success: true}, nil
}

func (o *Orchestrator) handleCalculate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	userID := r.Context().Value(userIDKey{}).(int)

	var input struct {
		Expression string `json:"expression"`
	}
	if err := decodeRequest(w, r, &input); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	expr, err := parseExpression(input.Expression)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	// Admission and shutdown are synchronized so Wait cannot race with Add.
	o.mu.Lock()
	if o.ctx.Err() != nil || len(o.slots) == cap(o.slots) {
		o.mu.Unlock()
		http.Error(w, "Calculator is busy or shutting down", http.StatusServiceUnavailable)
		return
	}
	o.slots <- struct{}{}
	o.jobs.Add(1)
	o.mu.Unlock()
	scheduled := false
	defer func() {
		if !scheduled {
			<-o.slots
			o.jobs.Done()
		}
	}()
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		http.Error(w, "Failed to generate ID", 500)
		return
	}
	taskID := hex.EncodeToString(id[:])

	_, err = o.db.Exec(
		"INSERT INTO expressions (id, user_id, expression, status) VALUES (?, ?, ?, ?)",
		taskID, userID, input.Expression, "processing",
	)
	if err != nil {
		http.Error(w, "Failed to save expression", http.StatusInternalServerError)
		return
	}

	scheduled = true
	go o.processExpression(expr, taskID)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"id": taskID})
}

func (o *Orchestrator) addTask(task *pb.Task) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.tasks = append(o.tasks, task)
	o.processing[task.Id] = false
	log.Printf("Task added: %s %f %s %f", task.Id, task.Arg1, task.Operation, task.Arg2)
}

func (o *Orchestrator) waitForResult(ctx context.Context, taskID string) (float64, error) {
	defer func() {
		o.mu.Lock()
		defer o.mu.Unlock()
		delete(o.processing, taskID)
		delete(o.results, taskID)
		for i, task := range o.tasks {
			if task.Id == taskID {
				o.tasks = append(o.tasks[:i], o.tasks[i+1:]...)
				break
			}
		}
	}()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-ticker.C:
		}
		o.mu.Lock()
		result, exists := o.results[taskID]
		o.mu.Unlock()
		if exists {
			return result.value, result.err
		}
	}
}

type userIDKey struct{}

func decodeRequest(w http.ResponseWriter, r *http.Request, target interface{}) error {
	r.Body = http.MaxBytesReader(w, r.Body, 8192)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(new(interface{})); err != io.EOF {
		return errors.New("expected one JSON object")
	}
	return nil
}

func (o *Orchestrator) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/register", o.handleRegister)
	mux.HandleFunc("/api/v1/login", o.handleLogin)
	mux.HandleFunc("/api/v1/calculate", o.authMiddleware(o.handleCalculate))
	mux.HandleFunc("/api/v1/expressions", o.authMiddleware(o.handleExpressions))
	mux.HandleFunc("/api/v1/expressions/", o.authMiddleware(o.expressionByIDHandler))
	return o.enableCORS(mux)
}
