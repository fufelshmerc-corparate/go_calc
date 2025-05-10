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

	pb "github.com/IvanSolomatin/calc_v2/api/calc" // замените на ваш модуль
	"google.golang.org/grpc"
)

var (
	TIME_A           = 100
	TIME_S           = 100
	TIME_M           = 100
	TIME_D           = 100
	tasks            = make([]*Task, 0)
	results          = make(map[string]float64)
	mu               sync.Mutex
	taskIDCounter    uint64
	genTaskIDCounter uint64
	jwtSecret        = []byte("your-secret-key") // В реальном приложении используйте более сложный ключ
	db               *sql.DB
)

type orchestratorServer struct {
	pb.UnimplementedOrchestratorServer
	mu          sync.Mutex
	tasks       []*pb.Task
	processing  map[string]bool
	results     map[string]float64
}

func (s *orchestratorServer) GetTask(ctx context.Context, req *pb.TaskRequest) (*pb.Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if len(s.tasks) == 0 {
		return nil, fmt.Errorf("no tasks available")
	}

	task := s.tasks[0]
	s.tasks = s.tasks[1:]
	s.processing[task.Id] = true

	return task, nil
}

func (s *orchestratorServer) SubmitResult(ctx context.Context, res *pb.Result) (*pb.ResultResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if exists := s.processing[res.Id]; !exists {
		return &pb.ResultResponse{Success: false, Message: "no such task"}, nil
	}

	s.results[res.Id] = res.Result
	s.processing[res.Id] = false

	return &pb.ResultResponse{Success: true, Message: "result accepted"}, nil
}

func RunOrchestratorGRPC() {
	port := os.Getenv("GRPC_PORT")
	if port == "" {
		port = "50051"
	}

	lis, err := net.Listen("tcp", ":"+port)
	if err != nil {
		log.Fatalf("failed to listen: %v", err)
	}

	server := grpc.NewServer()
	orchServer := &orchestratorServer{
		tasks:      make([]*pb.Task, 0),
		processing: make(map[string]bool),
		results:    make(map[string]float64),
	}

	pb.RegisterOrchestratorServer(server, orchServer)

	// Инициализация HTTP сервера для API пользователей
	go Runorchestrator()

	log.Printf("gRPC server listening at %v", lis.Addr())
	if err := server.Serve(lis); err != nil {
		log.Fatalf("failed to serve: %v", err)
	}
}

type Expression struct {
	ID         string   `json:"id"`
	UserID     int      `json:"user_id"`
	Expression string   `json:"expression"`
	Status     string   `json:"status"`
	Result     *float64 `json:"result,omitempty"`
	CreatedAt  string   `json:"created_at"`
}

type Task struct {
	ID            string  `json:"id"`
	Arg1          float64 `json:"arg1"`
	Arg2          float64 `json:"arg2"`
	Operation     string  `json:"operation"`
	Operationtime int     `json:"operationtime"`
}

type Result struct {
	ID     string  `json:"id"`
	Result float64 `json:"result"`
}

type User struct {
	ID       int    `json:"id"`
	Login    string `json:"login"`
	Password string `json:"password"`
}

type AuthRequest struct {
	Login    string `json:"login"`
	Password string `json:"password"`
}

type Claims struct {
	UserID int `json:"user_id"`
	jwt.StandardClaims
}

func initDB() error {
	var err error
	db, err = sql.Open("sqlite3", "./expressions.db")
	if err != nil {
		return err
	}

	// Создаем таблицу пользователей
	_, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS users (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			login TEXT UNIQUE NOT NULL,
			password TEXT NOT NULL
		)
	`)
	if err != nil {
		return err
	}

	// Создаем таблицу выражений
	_, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS expressions (
			id TEXT PRIMARY KEY,
			user_id INTEGER NOT NULL,
			expression TEXT NOT NULL,
			status TEXT NOT NULL,
			result REAL,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			FOREIGN KEY(user_id) REFERENCES users(id)
		)
	`)
	return err
}

func handleRegister(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req AuthRequest
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

	_, err = db.Exec("INSERT INTO users (login, password) VALUES (?, ?)", req.Login, string(hashedPassword))
	if err != nil {
		http.Error(w, "User already exists", http.StatusConflict)
		return
	}

	w.WriteHeader(http.StatusOK)
	w.Write([]byte("OK"))
}

func handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req AuthRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	var user User
	err := db.QueryRow("SELECT id, password FROM users WHERE login = ?", req.Login).Scan(&user.ID, &user.Password)
	if err != nil {
		http.Error(w, "Invalid login or password", http.StatusUnauthorized)
		return
	}

	if err := bcrypt.CompareHashAndPassword([]byte(user.Password), []byte(req.Password)); err != nil {
		http.Error(w, "Invalid login or password", http.StatusUnauthorized)
		return
	}

	expirationTime := time.Now().Add(24 * time.Hour)
	claims := &Claims{
		UserID: user.ID,
		StandardClaims: jwt.StandardClaims{
			ExpiresAt: expirationTime.Unix(),
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	tokenString, err := token.SignedString(jwtSecret)
	if err != nil {
		http.Error(w, "Failed to generate token", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"token": tokenString})
}

func authMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tokenString := r.Header.Get("Authorization")
		if tokenString == "" {
			http.Error(w, "Authorization header required", http.StatusUnauthorized)
			return
		}

		claims := &Claims{}
		token, err := jwt.ParseWithClaims(tokenString, claims, func(token *jwt.Token) (interface{}, error) {
			return jwtSecret, nil
		})

		if err != nil || !token.Valid {
			http.Error(w, "Invalid token", http.StatusUnauthorized)
			return
		}

		ctx := r.Context()
		ctx = context.WithValue(ctx, "user_id", claims.UserID)
		next.ServeHTTP(w, r.WithContext(ctx))
	}
}

func eval(expr ast.Expr) (float64, error) {
	switch e := expr.(type) {
	case *ast.BasicLit:
		return strconv.ParseFloat(e.Value, 64)

	case *ast.ParenExpr:
		return eval(e.X)

	case *ast.BinaryExpr:
		leftCh := make(chan float64)
		rightCh := make(chan float64)
		errCh := make(chan error, 2)

		go func() {
			left, err := eval(e.X)
			if err != nil {
				errCh <- err
			} else {
				leftCh <- left
			}
		}()

		go func() {
			right, err := eval(e.Y)
			if err != nil {
				errCh <- err
			} else {
				rightCh <- right
			}
		}()

		var left, right float64
		select {
		case left = <-leftCh:
		case err := <-errCh:
			return 0, err
		}

		select {
		case right = <-rightCh:
		case err := <-errCh:
			return 0, err
		}
		if right == 0 {
			return 0, fmt.Errorf("деление на 0")
		}
		taskID := fmt.Sprintf("%d", atomic.AddUint64(&taskIDCounter, 1))
		envVar := 100
		switch e.Op.String() {
		case "+":
			envVar = TIME_A
		case "-":
			envVar = TIME_S
		case "*":
			envVar = TIME_M
		case "/":
			envVar = TIME_D
		default:
			return 0, fmt.Errorf("неизвестная операция: %s", e.Op.String())
		}
		task := &Task{
			ID:            taskID,
			Arg1:          left,
			Arg2:          right,
			Operation:     e.Op.String(),
			Operationtime: envVar,
		}

		mu.Lock()
		tasks = append(tasks, task)
		mu.Unlock()

		resultCh := make(chan float64)

		go func() {
			for {
				mu.Lock()
				if result, found := results[taskID]; found {
					delete(results, taskID)
					resultCh <- result
					mu.Unlock()
					return
				}
				mu.Unlock()
				time.Sleep(100 * time.Millisecond)
			}
		}()

		return <-resultCh, nil
	}

	return 0, errors.New("неподдерживаемое выражение")
}

func handleCalculate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Метод не разрешён", http.StatusMethodNotAllowed)
		return
	}

	userID := r.Context().Value("user_id").(int)

	var input struct {
		Expression string `json:"expression"`
	}
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		http.Error(w, "Ошибка декодирования", http.StatusBadRequest)
		return
	}
	if input.Expression == "" {
		http.Error(w, "StatusUnprocessableEntity", http.StatusUnprocessableEntity)
		return
	}

	genTaskID := fmt.Sprintf("%d", atomic.AddUint64(&genTaskIDCounter, 1))
	_, err := db.Exec(
		"INSERT INTO expressions (id, user_id, expression, status) VALUES (?, ?, ?, ?)",
		genTaskID, userID, input.Expression, "processing",
	)
	if err != nil {
		http.Error(w, "Failed to save expression", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]string{"id": genTaskID})

	go func(genTaskID string, userID int) {
		expr, err := parser.ParseExpr(input.Expression)
		if err != nil {
			db.Exec("UPDATE expressions SET status = ? WHERE id = ?", "error", genTaskID)
			return
		}

		finalResult, err := eval(expr)

		if err != nil {
			db.Exec("UPDATE expressions SET status = ? WHERE id = ?", "error", genTaskID)
		} else {
			db.Exec("UPDATE expressions SET status = ?, result = ? WHERE id = ?", "completed", finalResult, genTaskID)
		}
	}(genTaskID, userID)
}

func handleExpressions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Метод не разрешён", http.StatusMethodNotAllowed)
		return
	}

	userID := r.Context().Value("user_id").(int)

	rows, err := db.Query("SELECT id, expression, status, result, created_at FROM expressions WHERE user_id = ?", userID)
	if err != nil {
		http.Error(w, "Failed to fetch expressions", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	var expressions []Expression
	for rows.Next() {
		var expr Expression
		var result sql.NullFloat64
		err := rows.Scan(&expr.ID, &expr.Expression, &expr.Status, &result, &expr.CreatedAt)
		if err != nil {
			http.Error(w, "Failed to scan expression", http.StatusInternalServerError)
			return
		}
		if result.Valid {
			expr.Result = &result.Float64
		}
		expressions = append(expressions, expr)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"expressions": expressions})
}

func expressionByIDHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, `{"error":"Wrong Method"}`, http.StatusMethodNotAllowed)
		return
	}

	userID := r.Context().Value("user_id").(int)
	id := r.URL.Path[len("/api/v1/expressions/"):]

	var expr Expression
	var result sql.NullFloat64
	err := db.QueryRow(
		"SELECT id, expression, status, result, created_at FROM expressions WHERE id = ? AND user_id = ?",
		id, userID,
	).Scan(&expr.ID, &expr.Expression, &expr.Status, &result, &expr.CreatedAt)

	if err != nil {
		if err == sql.ErrNoRows {
			http.Error(w, `{"error":"Expression not found"}`, http.StatusNotFound)
		} else {
			http.Error(w, `{"error":"Database error"}`, http.StatusInternalServerError)
		}
		return
	}

	if result.Valid {
		expr.Result = &result.Float64
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(expr)
}

func enableCORS(next http.Handler) http.Handler {
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

func Runorchestrator() {
	if err := initDB(); err != nil {
		fmt.Println("Failed to initialize database:", err)
		return
	}
	defer db.Close()

	port := os.Getenv("PORT")
	if port == "" {
		port = "8082"
	}

	var err error
	if os.Getenv("TIME_ADDITION_MS") != "" {
		TIME_A, err = strconv.Atoi(os.Getenv("TIME_ADDITION_MS"))
		if err != nil {
			fmt.Println(err)
			return
		}
	}
	if os.Getenv("TIME_SUBTRACTION_MS") != "" {
		TIME_S, err = strconv.Atoi(os.Getenv("TIME_SUBTRACTION_MS"))
		if err != nil {
			fmt.Println(err)
			return
		}
	}
	if os.Getenv("TIME_MULTIPLICATIONS_MS") != "" {
		TIME_M, err = strconv.Atoi(os.Getenv("TIME_MULTIPLICATIONS_MS"))
		if err != nil {
			fmt.Println(err)
			return
		}
	}
	if os.Getenv("TIME_DIVISIONS_MS") != "" {
		TIME_D, err = strconv.Atoi(os.Getenv("TIME_DIVISIONS_MS"))
		if err != nil {
			fmt.Println(err)
			return
		}
	}

	mux := http.NewServeMux()
	handler := enableCORS(mux)

	// Public routes
	mux.HandleFunc("/api/v1/register", handleRegister)
	mux.HandleFunc("/api/v1/login", handleLogin)

	// Protected routes
	mux.HandleFunc("/api/v1/calculate", authMiddleware(handleCalculate))
	mux.HandleFunc("/api/v1/expressions", authMiddleware(handleExpressions))
	mux.HandleFunc("/api/v1/expressions/", authMiddleware(expressionByIDHandler))

	fmt.Println("Сервер запущен на порту", port)
	http.ListenAndServe(":"+port, handler)
}
