package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"log"
	"net"
	"net/http"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/dgrijalva/jwt-go"
	_ "github.com/mattn/go-sqlite3"
	"golang.org/x/crypto/bcrypt"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/IvanSolomatin/calc_v2/api/calc"
)

type Orchestrator struct {
	pb.UnimplementedOrchestratorServer
	mu          sync.Mutex
	tasks       []*pb.Task
	processing  map[string]bool
	results     map[string]float64
	taskCounter uint64
	db          *sql.DB
	jwtSecret   []byte
	opTimes     map[string]int
}

func NewOrchestrator(db *sql.DB, jwtSecret []byte) *Orchestrator {
	return &Orchestrator{
		tasks:      make([]*pb.Task, 0),
		processing: make(map[string]bool),
		results:    make(map[string]float64),
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

// Регистрация нового пользователя
func (o *Orchestrator) handleRegister(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Login    string `json:"login"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	if req.Login == "" || req.Password == "" {
		http.Error(w, "Login and password are required", http.StatusBadRequest)
		return
	}

	hashedPassword, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		http.Error(w, "Failed to hash password", http.StatusInternalServerError)
		return
	}

	_, err = o.db.Exec("INSERT INTO users (login, password) VALUES (?, ?)", req.Login, string(hashedPassword))
	if err != nil {
		http.Error(w, "User already exists", http.StatusConflict)
		return
	}

	w.WriteHeader(http.StatusOK)
	w.Write([]byte("OK"))
}

// Аутентификация пользователя
func (o *Orchestrator) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Login    string `json:"login"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	var user struct {
		ID       int
		Password string
	}
	err := o.db.QueryRow("SELECT id, password FROM users WHERE login = ?", req.Login).Scan(&user.ID, &user.Password)
	if err != nil {
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
		jwt.StandardClaims
	}{
		UserID: user.ID,
		StandardClaims: jwt.StandardClaims{
			ExpiresAt: expirationTime.Unix(),
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	tokenString, err := token.SignedString(o.jwtSecret)
	if err != nil {
		http.Error(w, "Failed to generate token", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"token": tokenString})
}

// Middleware аутентификации
func (o *Orchestrator) authMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tokenString := r.Header.Get("Authorization")
		if tokenString == "" {
			http.Error(w, "Authorization header required", http.StatusUnauthorized)
			return
		}

		claims := &struct {
			UserID int `json:"user_id"`
			jwt.StandardClaims
		}{}

		token, err := jwt.ParseWithClaims(tokenString, claims, func(token *jwt.Token) (interface{}, error) {
			return o.jwtSecret, nil
		})

		if err != nil || !token.Valid {
			http.Error(w, "Invalid token", http.StatusUnauthorized)
			return
		}

		ctx := context.WithValue(r.Context(), "user_id", claims.UserID)
		next.ServeHTTP(w, r.WithContext(ctx))
	}
}

// Получение выражения по ID
func (o *Orchestrator) expressionByIDHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, `{"error":"Wrong Method"}`, http.StatusMethodNotAllowed)
		return
	}

	userID := r.Context().Value("user_id").(int)
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

// Middleware для CORS
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
// Получение списка всех выражений пользователя
func (o *Orchestrator) handleExpressions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	userID := r.Context().Value("user_id").(int)

	rows, err := o.db.Query(
		"SELECT id, expression, status, result, created_at FROM expressions WHERE user_id = ?",
		userID,
	)
	if err != nil {
		http.Error(w, "Database error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	var expressions []struct {
		ID         string   `json:"id"`
		Expression string   `json:"expression"`
		Status     string   `json:"status"`
		Result     *float64 `json:"result,omitempty"`
		CreatedAt  string   `json:"created_at"`
	}

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

func (o *Orchestrator) Start(grpcPort, httpPort string) error {
	// Start gRPC server
	lis, err := net.Listen("tcp", ":"+grpcPort)
	if err != nil {
		return fmt.Errorf("failed to listen: %v", err)
	}

	grpcServer := grpc.NewServer()
	pb.RegisterOrchestratorServer(grpcServer, o)

	go func() {
		log.Printf("gRPC server listening on :%s", grpcPort)
		if err := grpcServer.Serve(lis); err != nil {
			log.Printf("gRPC server failed: %v", err)
		}
	}()

	// Start HTTP server
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/register", o.handleRegister)
	mux.HandleFunc("/api/v1/login", o.handleLogin)
	mux.HandleFunc("/api/v1/calculate", o.authMiddleware(o.handleCalculate))
	mux.HandleFunc("/api/v1/expressions", o.authMiddleware(o.handleExpressions))
	mux.HandleFunc("/api/v1/expressions/", o.authMiddleware(o.expressionByIDHandler))

	server := &http.Server{
		Addr:    ":" + httpPort,
		Handler: o.enableCORS(mux),
	}

	go func() {
		log.Printf("HTTP server listening on :%s", httpPort)
		if err := server.ListenAndServe(); err != http.ErrServerClosed {
			log.Printf("HTTP server failed: %v", err)
		}
	}()

	return nil
}

func (o *Orchestrator) SetOperationTime(op string, timeMs int) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.opTimes[op] = timeMs
}

// gRPC methods
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

	if !o.processing[res.Id] {
		return nil, status.Error(codes.NotFound, "task not found")
	}

	o.results[res.Id] = res.Result
	delete(o.processing, res.Id)

	_, err := o.db.Exec("UPDATE expressions SET status = 'completed', result = ? WHERE id = ?", res.Result, res.Id)
	if err != nil {
		log.Printf("Failed to update DB: %v", err)
	}

	log.Printf("Result received for task %s: %f", res.Id, res.Result)
	return &pb.ResultResponse{Success: true}, nil
}

// HTTP handlers
func (o *Orchestrator) handleCalculate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	userID := r.Context().Value("user_id").(int)

	var input struct {
		Expression string `json:"expression"`
	}
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	taskID := fmt.Sprintf("expr_%d_%d", userID, atomic.AddUint64(&o.taskCounter, 1))
	
	_, err := o.db.Exec(
		"INSERT INTO expressions (id, user_id, expression, status) VALUES (?, ?, ?, ?)",
		taskID, userID, input.Expression, "processing",
	)
	if err != nil {
		http.Error(w, "Failed to save expression", http.StatusInternalServerError)
		return
	}

	go o.processExpression(input.Expression, userID, taskID)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"id": taskID})
}

func (o *Orchestrator) processExpression(exprStr string, userID int, taskID string) {
	expr, err := parser.ParseExpr(exprStr)
	if err != nil {
		o.db.Exec("UPDATE expressions SET status = ? WHERE id = ?", "error", taskID)
		return
	}

	result, err := o.eval(expr)
	if err != nil {
		o.db.Exec("UPDATE expressions SET status = ? WHERE id = ?", "error", taskID)
		return
	}

	_, err = o.db.Exec("UPDATE expressions SET status = ?, result = ? WHERE id = ?", 
		"completed", result, taskID)
	if err != nil {
		log.Printf("Failed to update expression status: %v", err)
	}
}

func (o *Orchestrator) eval(expr ast.Expr) (float64, error) {
	switch e := expr.(type) {
	case *ast.BasicLit:
		return strconv.ParseFloat(e.Value, 64)

	case *ast.ParenExpr:
		return o.eval(e.X)

	case *ast.BinaryExpr:
		left, err := o.eval(e.X)
		if err != nil {
			return 0, err
		}

		right, err := o.eval(e.Y)
		if err != nil {
			return 0, err
		}

		if right == 0 && e.Op.String() == "/" {
			return 0, fmt.Errorf("division by zero")
		}

		taskID := fmt.Sprintf("task_%d", atomic.AddUint64(&o.taskCounter, 1))
		opTime := o.opTimes[e.Op.String()]

		task := &pb.Task{
			Id:           taskID,
			Arg1:         left,
			Arg2:         right,
			Operation:    e.Op.String(),
			OperationTime: int32(opTime),
		}

		o.addTask(task)
		return o.waitForResult(taskID)
	}

	return 0, errors.New("unsupported expression")
}

func (o *Orchestrator) addTask(task *pb.Task) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.tasks = append(o.tasks, task)
	o.processing[task.Id] = false
	log.Printf("Task added: %s %f %s %f", task.Id, task.Arg1, task.Operation, task.Arg2)
}

func (o *Orchestrator) waitForResult(taskID string) (float64, error) {
	for {
		time.Sleep(100 * time.Millisecond)
		
		o.mu.Lock()
		if result, exists := o.results[taskID]; exists {
			delete(o.results, taskID)
			o.mu.Unlock()
			return result, nil
		}
		o.mu.Unlock()
	}
}

// Other HTTP handlers and helper methods...
// (handleRegister, handleLogin, handleExpressions, expressionByIDHandler, 
// authMiddleware, enableCORS etc. remain the same as in previous examples)

func InitDB(dsn string) (*sql.DB, error) {
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, err
	}

	_, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS users (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			login TEXT UNIQUE NOT NULL,
			password TEXT NOT NULL
		);
		CREATE TABLE IF NOT EXISTS expressions (
			id TEXT PRIMARY KEY,
			user_id INTEGER NOT NULL,
			expression TEXT NOT NULL,
			status TEXT NOT NULL,
			result REAL,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			FOREIGN KEY(user_id) REFERENCES users(id)
		);
	`)
	if err != nil {
		return nil, err
	}

	return db, nil
}

func RunOrchestrator() error {
	db, err := InitDB("./expressions.db")
	if err != nil {
		return fmt.Errorf("failed to init DB: %v", err)
	}

	jwtSecret := []byte(os.Getenv("JWT_SECRET"))
	if len(jwtSecret) == 0 {
		jwtSecret = []byte("default-secret-key")
	}
	orch := NewOrchestrator(db, jwtSecret)

	if t, err := strconv.Atoi(os.Getenv("TIME_ADDITION_MS")); err == nil {
		orch.SetOperationTime("+", t)
	}
	if t, err := strconv.Atoi(os.Getenv("TIME_SUBTRACTION_MS")); err == nil {
		orch.SetOperationTime("-", t)
	}
	if t, err := strconv.Atoi(os.Getenv("TIME_MULTIPLICATION_MS")); err == nil {
		orch.SetOperationTime("*", t)
	}
	if t, err := strconv.Atoi(os.Getenv("TIME_DIVISION_MS")); err == nil {
		orch.SetOperationTime("/", t)
	}

	// Start servers
	grpcPort := os.Getenv("GRPC_PORT")
	if grpcPort == "" {
		grpcPort = "50051"
	}

	httpPort := os.Getenv("HTTP_PORT")
	if httpPort == "" {
		httpPort = "8080"
	}

	if err := orch.Start(grpcPort, httpPort); err != nil {
		return fmt.Errorf("failed to start servers: %v", err)
	}

	return nil
}