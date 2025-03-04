package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"net/http"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

type Expression struct {
	ID         string   `json:"id"`
	Expression string   `json:"expression"`
	Status     string   `json:"status"`
	Result     *float64 `json:"result"`
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

var (
	TIME_A           = 100
	TIME_S           = 100
	TIME_M           = 100
	TIME_D           = 100
	processing       = make(map[string]bool)
	expressions      = make(map[string]*Expression)
	tasks            = make([]*Task, 0)         
	results          = make(map[string]float64) 
	mu               sync.Mutex
	taskIDCounter    uint64 
	genTaskIDCounter uint64
)


func handleTask(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodGet:
		mu.Lock()
		defer mu.Unlock()

		if len(tasks) == 0 {
			http.Error(w, "Нет доступных задач", http.StatusNotFound)
			return
		}
		task := tasks[0]
		tasks = tasks[1:]

		processing[task.ID] = true

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(task)

	case r.Method == http.MethodPost:
		var res Result
		if err := json.NewDecoder(r.Body).Decode(&res); err != nil {
			http.Error(w, "Ошибка декодирования", http.StatusUnprocessableEntity)
			return
		}

		mu.Lock()
		defer mu.Unlock()

		if exists := processing[res.ID]; !exists {
			http.Error(w, "Нет такой задачи", http.StatusNotFound)
			return
		}

		results[res.ID] = res.Result
		processing[res.ID] = false
		w.WriteHeader(http.StatusOK)

	default:
		http.Error(w, "что то пошло не так", http.StatusInternalServerError)
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
		if right == 0{
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

	var input struct {
		Expression string `json:"expression"`
	}
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		http.Error(w, "Ошибка декодирования", http.StatusBadRequest)
		return
	}
	if input.Expression == ""{
		http.Error(w, "StatusUnprocessableEntity", http.StatusUnprocessableEntity)
		return
	}
	genTaskID := fmt.Sprintf("%d", atomic.AddUint64(&genTaskIDCounter, 1))
	expressions[genTaskID] = &Expression{ID: genTaskID, Expression: input.Expression, Status: "processing"}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]string{"id": genTaskID})

	go func(genTaskID string) {
		expr, err := parser.ParseExpr(input.Expression)
		if err != nil {
			http.Error(w, "Ошибка парсинга выражения", http.StatusBadRequest)
			return
		}

		finalResult, err := eval(expr)

		mu.Lock()
		if err != nil {
			expressions[genTaskID].Status = "error"
		} else {
			expressions[genTaskID].Status = "completed"
			expressions[genTaskID].Result = &finalResult
		}
		mu.Unlock()
	}(genTaskID)
}

func handleExpressions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Метод не разрешён", http.StatusMethodNotAllowed)
		return
	}

	w.Header().Set("Content-Type", "application/json")

	mu.Lock()
	json.NewEncoder(w).Encode(map[string]any{"expressions": expressions})
	mu.Unlock()

}
func expressionByIDHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, `{"error":"Wrong Method"}`, http.StatusMethodNotAllowed)
		return
	}
	id := r.URL.Path[len("/api/v1/expressions/"):]
	mu.Lock()
	expr, ok := expressions[id]
	mu.Unlock()
	if !ok {
		http.Error(w, `{"error":"Expression not found"}`, http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(expr)
}

func Runorchestrator() {

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

	http.HandleFunc("/api/v1/calculate", handleCalculate)
	http.HandleFunc("/internal/task", handleTask)
	http.HandleFunc("/api/v1/expressions", handleExpressions)
	http.HandleFunc("/api/v1/expressions/", expressionByIDHandler)
	fmt.Println("Сервер запущен на порту", port)
	http.ListenAndServe(":"+port, nil)
}
